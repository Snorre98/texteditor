package document

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"texteditor/internal/sqlmigrate"
	"texteditor/internal/textformatter"
	"texteditor/shared/dto"
)

// newTestStore opens a migrated in-memory app.db plus temp git/worktree dirs.
func newTestStore(t *testing.T) Interface {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlmigrate.Migrate(context.Background(), db, appSchema); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := NewStore(db, filepath.Join(dir, "git"), filepath.Join(dir, "worktree"), textformatter.New())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func openDoc(t *testing.T, s Interface, content string) string {
	t.Helper()
	id, _ := openDocPath(t, s, content)
	return id
}

func openDocPath(t *testing.T, s Interface, content string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return res.DocumentID, path
}

func TestOpenBlocksRoundTrip(t *testing.T) {
	s := newTestStore(t)

	id := openDoc(t, s, "# Title\n\nhello world\n\n- item one\n- item two\n")

	blocks, err := s.Blocks(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(blocks))
	}
	if blocks[0].Kind != dto.BlockKindHeading || blocks[0].Text != "# Title" {
		t.Fatalf("block0 = %+v", blocks[0])
	}
	if blocks[1].Kind != dto.BlockKindParagraph || blocks[1].Text != "hello world" {
		t.Fatalf("block1 = %+v", blocks[1])
	}
	if blocks[1].Hash == "" {
		t.Fatal("hash must be populated (guard anchor)")
	}
}

func TestOpenRevalidatesExternalChange(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "first version")

	if err := os.WriteFile(path, []byte("external version"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.DocumentID != id {
		t.Fatalf("reopen returned a new document id %q, want %q", res.DocumentID, id)
	}
	if !res.ExternalChange {
		t.Fatal("external change must be reported (ADR-0047 §2)")
	}
	blocks, _ := s.Blocks(id)
	if blocks[0].Text != "external version" {
		t.Fatalf("worktree not re-read: %q", blocks[0].Text)
	}

	// A second open with no disk change is quiet.
	res, err = s.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalChange {
		t.Fatal("unchanged disk must not report an external change")
	}
}

func TestOpenAliasResolvesToOneRow(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.md")
	if err := os.WriteFile(target, []byte("aliased"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	viaLink, err := s.Open(link)
	if err != nil {
		t.Fatal(err)
	}
	viaTarget, err := s.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	if viaLink.DocumentID != viaTarget.DocumentID {
		t.Fatalf("alias ids differ: %q vs %q", viaLink.DocumentID, viaTarget.DocumentID)
	}
	if viaTarget.ExternalChange {
		t.Fatal("opening an alias of an unchanged file is not an external change")
	}
}

func TestOpenExternalChangeRebuildsWhenCountChanges(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "one")
	oldBlocks, _ := s.Blocks(id)

	if err := os.WriteFile(path, []byte("one\n\ntwo\n\nthree"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ExternalChange {
		t.Fatal("external change not reported")
	}
	blocks, _ := s.Blocks(id)
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want 3 after re-read", len(blocks))
	}
	if blocks[0].ID == oldBlocks[0].ID {
		t.Fatal("changed block count must rebuild structure with fresh IDs")
	}
}

func TestApplyEditNormalizesAndStages(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "a paragraph")

	blocks, _ := s.Blocks(id)
	_, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{
		BlockID: blocks[0].ID,
		Text:    "new  line\r\nwith leading spaces   \r\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	cands, err := s.Candidates(id, blocks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1", len(cands))
	}
	if cands[0].Text != "new  line\nwith leading spaces" {
		t.Fatalf("candidate text not normalized: %q", cands[0].Text)
	}
}

func TestGuardFailed(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "block A\n\nblock B")

	blocks, _ := s.Blocks(id)
	target := blocks[0]
	guardBlock := blocks[1]

	_, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{
		BlockID: target.ID,
		Text:    "changed",
		Guards:  []dto.Guard{{BlockID: guardBlock.ID, Hash: "deadbeef"}},
	})
	if !errors.Is(err, ErrGuardFailed) {
		t.Fatalf("want ErrGuardFailed, got %v", err)
	}

	_, err = s.ApplyEdit(context.Background(), id, dto.BlockEdit{
		BlockID: target.ID,
		Text:    "changed",
		Guards:  []dto.Guard{{BlockID: guardBlock.ID, Hash: guardBlock.Hash}},
	})
	if err != nil {
		t.Fatalf("valid guard should pass: %v", err)
	}
}

func TestCommitClearsCandidatesAndCommits(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "before edit")

	blocks, _ := s.Blocks(id)
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{
		BlockID: blocks[0].ID, Text: "after edit", Mode: "proofreader",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Commit(id, dto.CommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Committed || res.Revision.ID == "" {
		t.Fatalf("commit result = %+v, want a populated revision", res)
	}
	if !strings.Contains(res.Revision.Message, "proofreader") || !strings.Contains(res.Revision.Message, blocks[0].ID) {
		t.Fatalf("derived message %q must name the preset and block", res.Revision.Message)
	}

	cands, _ := s.Candidates(id, blocks[0].ID)
	if len(cands) != 0 {
		t.Fatalf("candidates = %d, want 0 after commit", len(cands))
	}
	after, _ := s.Blocks(id)
	if after[0].Text != "after edit" {
		t.Fatalf("committed text = %q", after[0].Text)
	}
	hist, err := s.History(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("history = %d, want 1", len(hist))
	}
}

func TestCommitWithoutCandidatesCreatesNoCommit(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "no candidates")

	res, err := s.Commit(id, dto.CommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Committed {
		t.Fatal("an empty accept must create no commit (ADR-0047 §8)")
	}
	if res.WrittenThrough {
		t.Fatal("an empty accept must not write through")
	}
	hist, _ := s.History(id)
	if len(hist) != 0 {
		t.Fatalf("history = %d, want 0", len(hist))
	}
	if got := readFile(t, path); got != "no candidates" {
		t.Fatalf("file after empty accept = %q", got)
	}
}

func TestDiffIsWordLevel(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "the quick brown fox")

	// Commit 1 = base (stage the unchanged text, then accept).
	blocks, _ := s.Blocks(id)
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: blocks[0].ID, Text: "the quick brown fox"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(id, dto.CommitOptions{}); err != nil {
		t.Fatal(err)
	}
	baseRev := mustHead(t, s, id)

	// Edit + commit 2.
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: blocks[0].ID, Text: "the quick red fox"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(id, dto.CommitOptions{}); err != nil {
		t.Fatal(err)
	}
	rev := mustHead(t, s, id)

	we, err := s.Diff(id, baseRev, rev)
	if err != nil {
		t.Fatal(err)
	}
	if len(we) != 1 {
		t.Fatalf("wordEdits = %d, want 1", len(we))
	}
	if len(we[0].Insertions) != 1 || we[0].Insertions[0] != "red" {
		t.Fatalf("insertions = %v, want [red]", we[0].Insertions)
	}
	if len(we[0].Deletions) != 1 || we[0].Deletions[0] != "brown" {
		t.Fatalf("deletions = %v, want [brown]", we[0].Deletions)
	}
}

func TestCommitMessageCarriesDiff(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "old words here")

	blocks, _ := s.Blocks(id)
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{
		BlockID: blocks[0].ID, Text: "new words here", Mode: "editor",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Commit(id, dto.CommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	msg := res.Revision.Message
	if !strings.Contains(msg, "editor") || !strings.Contains(msg, "+new") || !strings.Contains(msg, "-old") {
		t.Fatalf("derived message = %q, want preset + word diff", msg)
	}
}

func TestBlockNotFound(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "hello")
	_, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: "nope", Text: "x"})
	if !errors.Is(err, ErrBlockNotFound) {
		t.Fatalf("want ErrBlockNotFound, got %v", err)
	}
}

func TestSaveTreeAutosaves(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "a paragraph")

	blocks, _ := s.Blocks(id)
	res, err := s.SaveTree(id, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "a changed paragraph"},
	}, dto.SaveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision.ID == "" || res.Revision.Message == "" || !res.Committed {
		t.Fatalf("saveTree result = %+v, want a populated revision", res)
	}

	// SaveTree is the autosave path: one snapshot commit exists.
	hist, err := s.History(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("history = %d, want 1 autosave commit", len(hist))
	}
	after, _ := s.Blocks(id)
	if after[0].Text != "a changed paragraph" {
		t.Fatalf("saved text = %q", after[0].Text)
	}
}

func TestSaveTreeNoopWhenUnchanged(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "a paragraph")

	blocks, _ := s.Blocks(id)
	if _, err := s.SaveTree(id, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "a paragraph"},
	}, dto.SaveOptions{WriteThrough: true}); err != nil {
		t.Fatal(err)
	}
	// An unchanged tree is a no-op: no new commit.
	hist, _ := s.History(id)
	if len(hist) != 0 {
		t.Fatalf("history = %d, want 0 (no-op save must not commit)", len(hist))
	}
}

func TestSaveTreeMintsAndDrops(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "block one\n\nblock two")

	blocks, _ := s.Blocks(id)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	// Reorder + mint a new block + drop the second. New block has no ID; the
	// engine mints it (ADR-0038 §2).
	tree := []dto.BlockWrite{
		{Kind: dto.BlockKindParagraph, Text: "fresh block"},                  // new (no id)
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "block one"}, // kept
	}
	if _, err := s.SaveTree(id, tree, dto.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Blocks(id)
	if len(after) != 2 {
		t.Fatalf("after blocks = %d, want 2", len(after))
	}
	if after[0].ID == blocks[0].ID || after[0].ID == blocks[1].ID {
		t.Fatalf("new block must mint a fresh ID, got %q", after[0].ID)
	}
	if after[0].Text != "fresh block" || after[1].Text != "block one" {
		t.Fatalf("order/text wrong: %+v", after)
	}
	if after[1].ID != blocks[0].ID {
		t.Fatalf("kept block must retain its stable ID")
	}
}

func TestSaveTreeDropsOpenCandidates(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "before edit")

	blocks, _ := s.Blocks(id)
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: blocks[0].ID, Text: "candidate"}); err != nil {
		t.Fatal(err)
	}
	if cands, _ := s.Candidates(id, blocks[0].ID); len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1 staged", len(cands))
	}

	// A manual save of the block drops its open candidates (ADR-0038 §5).
	if _, err := s.SaveTree(id, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "human typed"},
	}, dto.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if cands, _ := s.Candidates(id, blocks[0].ID); len(cands) != 0 {
		t.Fatalf("candidates = %d, want 0 after manual save", len(cands))
	}
}

// mustHead returns the current HEAD revision id (there must be a commit).
func mustHead(t *testing.T, s Interface, id string) string {
	t.Helper()
	hist, err := s.History(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) == 0 {
		t.Fatal("no commits")
	}
	return hist[0].ID
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSaveTreeWriteThrough(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "a paragraph")

	blocks, _ := s.Blocks(id)
	res, err := s.SaveTree(id, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "a changed paragraph"},
	}, dto.SaveOptions{WriteThrough: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.WrittenThrough || res.Path == "" {
		t.Fatalf("write-through not reported: %+v", res)
	}
	// Explicit save mirrors the canonical markdown back to the opened file.
	if got := readFile(t, path); got != "a changed paragraph" {
		t.Fatalf("file after write-through = %q", got)
	}
}

func TestSaveTreeAutosaveDoesNotWriteBack(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "a paragraph")

	blocks, _ := s.Blocks(id)
	if _, err := s.SaveTree(id, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "autosaved change"},
	}, dto.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	// The periodic autosave snapshots the engine only; the disk file is untouched.
	if got := readFile(t, path); got != "a paragraph" {
		t.Fatalf("file after autosave = %q, want untouched", got)
	}
}

func TestSaveTreeNoopDoesNotWriteBack(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "a paragraph")

	blocks, _ := s.Blocks(id)
	// Unchanged tree + writeThrough: no commit, no file write (ADR-0039 §4).
	if _, err := s.SaveTree(id, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "a paragraph"},
	}, dto.SaveOptions{WriteThrough: true}); err != nil {
		t.Fatal(err)
	}
	hist, _ := s.History(id)
	if len(hist) != 0 {
		t.Fatalf("history = %d, want 0 (no-op save must not commit)", len(hist))
	}
	if got := readFile(t, path); got != "a paragraph" {
		t.Fatalf("file after no-op save = %q", got)
	}
}

// TestSaveTreeNoopMirrorsStaleDisk covers ADR-0047 §4: an engine-only autosave
// leaves the disk stale; a later no-op explicit save mirrors it with no commit.
func TestSaveTreeNoopMirrorsStaleDisk(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "a paragraph")
	blocks, _ := s.Blocks(id)

	tree := []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "engine change"},
	}
	if _, err := s.SaveTree(id, tree, dto.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "a paragraph" {
		t.Fatalf("autosave must not touch the disk, got %q", got)
	}

	// Same tree (no engine change) + explicit save: mirror the stale disk.
	res, err := s.SaveTree(id, tree, dto.SaveOptions{WriteThrough: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.WrittenThrough || res.Committed {
		t.Fatalf("no-op mirror result = %+v, want writtenThrough without a commit", res)
	}
	if got := readFile(t, path); got != "engine change" {
		t.Fatalf("file after no-op mirror = %q", got)
	}
	hist, _ := s.History(id)
	if len(hist) != 1 {
		t.Fatalf("history = %d, want 1 (no new commit)", len(hist))
	}
}

// TestSaveTreeNoopExternalConflict covers ADR-0047 §4: a no-op save over an
// externally changed disk is a typed conflict, never a clobber.
func TestSaveTreeNoopExternalConflict(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "a paragraph")
	blocks, _ := s.Blocks(id)

	if err := os.WriteFile(path, []byte("external edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree := []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "a paragraph"},
	}
	_, err := s.SaveTree(id, tree, dto.SaveOptions{WriteThrough: true})
	var fce *FileChangedExternallyError
	if !errors.As(err, &fce) {
		t.Fatalf("want FileChangedExternallyError, got %v", err)
	}
	if fce.CurrentHash != contentHash([]byte("external edit")) {
		t.Fatalf("current hash = %q, want the on-disk hash", fce.CurrentHash)
	}
	if got := readFile(t, path); got != "external edit" {
		t.Fatalf("conflict must not clobber, got %q", got)
	}

	// The periodic autosave also refuses to paper over an external change.
	_, err = s.SaveTree(id, tree, dto.SaveOptions{})
	if !errors.Is(err, ErrFileChangedExternally) {
		t.Fatalf("autosave over an external change = %v, want conflict", err)
	}
}

func TestCommitWritesThrough(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "before edit")

	blocks, _ := s.Blocks(id)
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: blocks[0].ID, Text: "after edit"}); err != nil {
		t.Fatal(err)
	}
	// Accepting a candidate is a commit: the opened file must change too.
	res, err := s.Commit(id, dto.CommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.WrittenThrough || !res.Committed || res.Path == "" {
		t.Fatalf("commit result = %+v, want committed write-through", res)
	}
	if got := readFile(t, path); got != "after edit" {
		t.Fatalf("file after commit = %q", got)
	}
}

// TestCommitConflictLeavesEverythingIntact covers ADR-0047 §3: an external
// change between stage and accept refuses the write and leaves the candidate.
func TestCommitConflictLeavesEverythingIntact(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "before edit")

	blocks, _ := s.Blocks(id)
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: blocks[0].ID, Text: "after edit"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := s.Commit(id, dto.CommitOptions{})
	var fce *FileChangedExternallyError
	if !errors.As(err, &fce) {
		t.Fatalf("want FileChangedExternallyError, got %v", err)
	}
	if fce.CurrentHash != contentHash([]byte("external edit")) {
		t.Fatalf("current hash = %q", fce.CurrentHash)
	}
	if got := readFile(t, path); got != "external edit" {
		t.Fatalf("conflict clobbered the disk: %q", got)
	}
	hist, _ := s.History(id)
	if len(hist) != 0 {
		t.Fatalf("history = %d, want 0 (no commit on conflict)", len(hist))
	}
	after, _ := s.Blocks(id)
	if after[0].Text != "before edit" {
		t.Fatalf("worktree changed on conflict: %q", after[0].Text)
	}
	if cands, _ := s.Candidates(id, blocks[0].ID); len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1 left staged", len(cands))
	}
}

// TestCommitOverwriteWins covers the explicit opt-in overwrite (ADR-0047 §3).
func TestCommitOverwriteWins(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "before edit")

	blocks, _ := s.Blocks(id)
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: blocks[0].ID, Text: "after edit"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := s.Commit(id, dto.CommitOptions{Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Committed || !res.WrittenThrough {
		t.Fatalf("overwrite result = %+v", res)
	}
	if got := readFile(t, path); got != "after edit" {
		t.Fatalf("file after overwrite = %q", got)
	}
}

func TestCommitStaleCandidateGuardFailed(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "block A")

	blocks, _ := s.Blocks(id)
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{
		BlockID: blocks[0].ID, Text: "proposed", Mode: "editor",
	}); err != nil {
		t.Fatal(err)
	}

	// The file changes externally with the same block count; the re-read keeps
	// stable IDs, so the staged candidate is now stale.
	if err := os.WriteFile(path, []byte("externally changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ExternalChange {
		t.Fatal("expected an external change on reopen")
	}

	_, err = s.Commit(id, dto.CommitOptions{})
	if !errors.Is(err, ErrGuardFailed) {
		t.Fatalf("stale candidate commit = %v, want ErrGuardFailed", err)
	}
	// No bytes written and no commit created.
	if got := readFile(t, path); got != "externally changed" {
		t.Fatalf("stale commit wrote bytes: %q", got)
	}
	hist, _ := s.History(id)
	if len(hist) != 0 {
		t.Fatalf("history = %d, want 0", len(hist))
	}
	after, _ := s.Blocks(id)
	if after[0].Text != "externally changed" {
		t.Fatalf("worktree = %q", after[0].Text)
	}
}

func TestCandidateOrderNewestFirst(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "block")

	blocks, _ := s.Blocks(id)
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: blocks[0].ID, Text: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: blocks[0].ID, Text: "second"}); err != nil {
		t.Fatal(err)
	}
	cands, err := s.Candidates(id, blocks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	if cands[0].Text != "second" {
		t.Fatalf("candidates[0] = %q, want the newest (ADR-0047 §5)", cands[0].Text)
	}
	if _, err := s.Commit(id, dto.CommitOptions{}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Blocks(id)
	if after[0].Text != "second" {
		t.Fatalf("commit applied %q, want the newest candidate", after[0].Text)
	}
}

func TestWriteBackPreservesFileMode(t *testing.T) {
	s := newTestStore(t)
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte("a paragraph"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := s.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	blocks, _ := s.Blocks(res.DocumentID)
	if _, err := s.SaveTree(res.DocumentID, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "changed"},
	}, dto.SaveOptions{WriteThrough: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 600", perm)
	}
}

// TestWriteBackSymlinkPreserved covers ADR-0047 §7: a symlinked vault path is
// written through; the link is never replaced by a regular file.
func TestWriteBackSymlinkPreserved(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.md")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	res, err := s.Open(link)
	if err != nil {
		t.Fatal(err)
	}
	blocks, _ := s.Blocks(res.DocumentID)
	if _, err := s.SaveTree(res.DocumentID, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "changed through"},
	}, dto.SaveOptions{WriteThrough: true}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a regular file (ADR-0047 §7)")
	}
	if got := readFile(t, target); got != "changed through" {
		t.Fatalf("symlink target = %q", got)
	}
}

// TestAtomicWriteFileWritesThroughSymlink covers ADR-0047 §7 at the leaf: a
// symlink at the write path is followed, never renamed over.
func TestAtomicWriteFileWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.md")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := atomicWriteFile(link, []byte("through the link")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("atomicWriteFile replaced the symlink with a regular file")
	}
	if got := readFile(t, target); got != "through the link" {
		t.Fatalf("target = %q", got)
	}
}

// TestWriteBackMissingFileConflicts covers the externally-deleted case: the
// write is refused until the client explicitly overwrites.
func TestWriteBackMissingFileConflicts(t *testing.T) {
	s := newTestStore(t)
	id, path := openDocPath(t, s, "a paragraph")
	blocks, _ := s.Blocks(id)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err := s.SaveTree(id, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "changed"},
	}, dto.SaveOptions{WriteThrough: true})
	var fce *FileChangedExternallyError
	if !errors.As(err, &fce) {
		t.Fatalf("want FileChangedExternallyError, got %v", err)
	}
	if fce.CurrentHash != "" {
		t.Fatalf("deleted file current hash = %q, want empty", fce.CurrentHash)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("conflict must not recreate the file")
	}

	// Explicit overwrite recreates it.
	if _, err := s.SaveTree(id, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "changed"},
	}, dto.SaveOptions{WriteThrough: true, Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "changed" {
		t.Fatalf("file after overwrite = %q", got)
	}
}
