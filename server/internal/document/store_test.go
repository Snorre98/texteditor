package document

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
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
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := s.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return id
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
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: blocks[0].ID, Text: "after edit"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(id, "proofreader · "+blocks[0].ID+" · edited"); err != nil {
		t.Fatal(err)
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

func TestDiffIsWordLevel(t *testing.T) {
	s := newTestStore(t)
	id := openDoc(t, s, "the quick brown fox")

	// Commit 1 = base.
	if err := s.Commit(id, "initial"); err != nil {
		t.Fatal(err)
	}
	baseRev := mustHead(t, s, id)

	// Edit + commit 2.
	blocks, _ := s.Blocks(id)
	if _, err := s.ApplyEdit(context.Background(), id, dto.BlockEdit{BlockID: blocks[0].ID, Text: "the quick red fox"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(id, "edit two"); err != nil {
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
	rev, err := s.SaveTree(id, []dto.BlockWrite{
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "a changed paragraph"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rev.ID == "" || rev.Message == "" {
		t.Fatalf("saveTree revision = %+v, want populated", rev)
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
	}); err != nil {
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
		{Kind: dto.BlockKindParagraph, Text: "fresh block"},     // new (no id)
		{ID: &blocks[0].ID, Kind: dto.BlockKindParagraph, Text: "block one"}, // kept
	}
	if _, err := s.SaveTree(id, tree); err != nil {
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
	}); err != nil {
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
