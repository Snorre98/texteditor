package document

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"texteditor/internal/pathutil"
	"texteditor/internal/sqlmigrate"
	"texteditor/internal/textformatter"
	"texteditor/shared/dto"
)

// DocumentStore is the Document store public API (interface.md §9). It owns
// app.db (documents/blocks/candidates), the git history repo, and the engine
// working tree (ADR-0020 §2).
type DocumentStore interface {
	Open(path string) (dto.OpenResult, error)
	// Path returns a document's canonical absolute path by surrogate id. The
	// Retriever uses it for chunk provenance and the Agent loop for the
	// workspace fallback (ADR-0049 §2).
	Path(documentID string) (string, error)
	SaveTree(documentID string, tree []dto.BlockWrite, opts dto.SaveOptions) (dto.WriteResult, error)
	Blocks(documentID string) ([]dto.Block, error)
	ApplyEdit(ctx context.Context, documentID string, edit dto.BlockEdit) (dto.Revision, error)
	Commit(documentID string, opts dto.CommitOptions) (dto.WriteResult, error)
	Diff(documentID string, baseRev, rev string) ([]dto.WordEdit, error)
	History(documentID string) ([]dto.Revision, error)
	Candidates(documentID string, blockID string) ([]dto.Candidate, error)
}

// Interface is an alias for DocumentStore (the contracted name, interface.md §9).
type Interface = DocumentStore

// Typed errors (ADR-0029 §4, §5; ADR-0047 §3; failure-semantics §3).
var (
	ErrGuardFailed      = errors.New("guard-failed: a guarded block's content changed")
	ErrInvalidStructure = errors.New("invalid-structure: text failed structural validation")
	ErrBlockNotFound    = errors.New("block not found")
	ErrDocumentNotFound = errors.New("document not found")
	ErrUnknownRevision  = errors.New("unknown revision")
	// ErrFileChangedExternally is the sentinel for the write-through conflict
	// (ADR-0047 §3): the on-disk hash differs from the engine's last-known hash.
	ErrFileChangedExternally = errors.New("file-changed-externally: the file changed on disk since the engine last read it")
)

// FileChangedExternallyError carries the current on-disk state so the API layer
// can return the typed 409 body (ADR-0047 §3). CurrentHash is empty when the
// file was deleted externally.
type FileChangedExternallyError struct {
	Path        string
	CurrentHash string
}

func (e *FileChangedExternallyError) Error() string {
	if e.CurrentHash == "" {
		return fmt.Sprintf("%s: %s (missing on disk)", ErrFileChangedExternally, e.Path)
	}
	return fmt.Sprintf("%s: %s (current hash %s)", ErrFileChangedExternally, e.Path, e.CurrentHash)
}

// Unwrap makes errors.Is(err, ErrFileChangedExternally) true.
func (e *FileChangedExternallyError) Unwrap() error { return ErrFileChangedExternally }

// GuardFailure names one changed block caught by a guard (ADR-0029 §4).
type GuardFailure struct {
	BlockID string
	Reason  string
}

// store is the concrete Document store. Single-writer over app.db + the git repo
// (ADR-0016 §9). It depends on the textformatter leaf (ADR-0029 §6) and is NOT a
// pure leaf.
type store struct {
	db           *sql.DB
	tf           textformatter.Interface
	gitRoot      string // parent dir for per-document git repos
	worktreeRoot string // single dir holding each doc's canonical file
	workfileName string // the fixed filename inside each worktree (per doc file is named by id)

	mu    sync.Mutex
	hists map[string]*historyStore // per-document git history (ADR-0020 §2)
	locks map[string]*sync.Mutex   // per-document edit serialization (ADR-0026 §4)
}

// Migrate applies the app.db schema (exposed for the composition root).
func Migrate(ctx context.Context, db *sql.DB) error {
	return sqlmigrate.Migrate(ctx, db, appSchema)
}

// NewStore opens a Document store over the supplied app.db. gitDir is the
// parent directory for per-document git repos; worktreeDir the directory holding
// each document's canonical markdown file (ADR-0020 §2). The textformatter is
// injected so the canonical-content invariant (ADR-0029) is enforced at the single
// write boundary.
func NewStore(db *sql.DB, gitDir, worktreeDir string, tf textformatter.Interface) (DocumentStore, error) {
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		return nil, fmt.Errorf("document store: %w", err)
	}
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		return nil, fmt.Errorf("document store: %w", err)
	}
	return &store{
		db:           db,
		tf:           tf,
		gitRoot:      gitDir,
		worktreeRoot: worktreeDir,
		workfileName: "content.md",
		hists:        map[string]*historyStore{},
		locks:        map[string]*sync.Mutex{},
	}, nil
}

// histFor returns (opening if needed) the per-document history and working tree.
// The worktree directory is the single owner of a document's canonical bytes
// (ADR-0020 §2), and git holds that document's append-only delta history.
func (s *store) histFor(docID string) (*historyStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.hists[docID]; ok {
		return h, nil
	}
	h, err := initHistory(filepath.Join(s.gitRoot, docID+".git"), filepath.Join(s.worktreeRoot, docID))
	if err != nil {
		return nil, err
	}
	s.hists[docID] = h
	return h, nil
}

func (s *store) lockFor(docID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.locks[docID]
	if !ok {
		m = &sync.Mutex{}
		s.locks[docID] = m
	}
	return m
}

// Path returns a document's canonical absolute path by surrogate id (the
// Retriever's provenance seam and the loop's workspace fallback).
func (s *store) Path(documentID string) (string, error) {
	var path string
	err := s.db.QueryRow(`SELECT path FROM documents WHERE id = ?`, documentID).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrDocumentNotFound
	}
	if err != nil {
		return "", err
	}
	return path, nil
}

// Open resolves a document by canonical path (ADR-0047 §2): an existing row
// re-opens and revalidates the disk file against the engine's last-known hash —
// a mismatch re-reads the file into the worktree and reports ExternalChange;
// otherwise the file is parsed into a block tree, stable UUIDs are minted
// (ADR-0020 §3), the canonical markdown is written to the worktree, and rows are
// inserted. Symlink and case aliases of the same file resolve to one row.
func (s *store) Open(path string) (dto.OpenResult, error) {
	canonical, key := canonicalPath(path)

	var id, storedPath, storedHash string
	err := s.db.QueryRow(
		`SELECT id, path, content_hash FROM documents WHERE path_key = ?`, key,
	).Scan(&id, &storedPath, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		// Legacy rows may predate the path_key backfill (non-ASCII case folds);
		// fall back to the raw path so an alias still resolves to one row.
		err = s.db.QueryRow(
			`SELECT id, path, content_hash FROM documents WHERE path = ?`, canonical,
		).Scan(&id, &storedPath, &storedHash)
	}
	if err == nil {
		return s.reopen(id, canonical, key, storedPath, storedHash)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return dto.OpenResult{}, err
	}

	var src string
	b, err := os.ReadFile(canonical)
	if err != nil {
		if os.IsNotExist(err) {
			src = ""
		} else {
			return dto.OpenResult{}, err
		}
	} else {
		src = string(b)
	}

	docID := uuid.NewString()
	rootID := uuid.NewString()
	now := time.Now().Unix()

	if _, err := s.db.Exec(
		`INSERT INTO documents (id, path, path_key, root_block_id, updated_at, content_hash) VALUES (?, ?, ?, ?, ?, ?)`,
		docID, canonical, key, rootID, now, contentHash([]byte(src)),
	); err != nil {
		return dto.OpenResult{}, err
	}

	blocks := parseBlocks(src)
	if len(blocks) == 0 {
		// An empty document still has a root block (a document is a tree).
		if err := s.insertBlock(docID, rootID, nil, "", dto.BlockKindParagraph, 0); err != nil {
			return dto.OpenResult{}, err
		}
	} else {
		for i, pb := range blocks {
			id := rootID
			if i > 0 {
				id = uuid.NewString()
			}
			if err := s.insertBlock(docID, id, nil, "", pb.Kind, i); err != nil {
				return dto.OpenResult{}, err
			}
		}
	}

	// Write canonical markdown into the worktree file (the doc's single owner).
	h, err := s.histFor(docID)
	if err != nil {
		return dto.OpenResult{}, err
	}
	if err := h.writeFile(s.workfileName, []byte(src)); err != nil {
		return dto.OpenResult{}, err
	}
	return dto.OpenResult{DocumentID: docID, Path: canonical}, nil
}

// reopen revalidates an existing document row (ADR-0047 §2). It normalizes the
// stored canonical path, initializes the last-known disk hash for legacy rows,
// and on a disk mismatch re-reads the file into the worktree.
func (s *store) reopen(id, canonical, key, storedPath, storedHash string) (dto.OpenResult, error) {
	s.lockFor(id).Lock()
	defer s.lockFor(id).Unlock()

	h, err := s.histFor(id)
	if err != nil {
		return dto.OpenResult{}, err
	}

	if storedPath != canonical {
		if _, err := s.db.Exec(
			`UPDATE documents SET path = ?, path_key = ? WHERE id = ?`, canonical, key, id,
		); err != nil {
			return dto.OpenResult{}, err
		}
	}

	worktreeText, err := h.readFile(s.workfileName)
	if err != nil {
		return dto.OpenResult{}, err
	}
	lastKnown := storedHash
	if lastKnown == "" {
		// Legacy row: the worktree is the engine's copy of the last write, so it
		// is the honest last-known baseline for the first revalidation.
		lastKnown = contentHash([]byte(worktreeText))
		if _, err := s.db.Exec(`UPDATE documents SET content_hash = ? WHERE id = ?`, lastKnown, id); err != nil {
			return dto.OpenResult{}, err
		}
	}

	disk, err := os.ReadFile(canonical)
	if err != nil {
		if os.IsNotExist(err) {
			return dto.OpenResult{DocumentID: id, Path: canonical}, nil
		}
		return dto.OpenResult{}, err
	}
	diskHash := contentHash(disk)
	if diskHash == lastKnown {
		return dto.OpenResult{DocumentID: id, Path: canonical}, nil
	}

	// External change: re-read the file into the worktree (ADR-0047 §2). The
	// structure is preserved when the block count still matches (stable IDs and
	// candidates survive; Commit re-validates their base hash), and rebuilt with
	// fresh IDs when it does not.
	if err := s.resyncExternal(id, h, disk); err != nil {
		return dto.OpenResult{}, err
	}
	if _, err := s.db.Exec(
		`UPDATE documents SET content_hash = ?, updated_at = ? WHERE id = ?`,
		diskHash, time.Now().Unix(), id,
	); err != nil {
		return dto.OpenResult{}, err
	}
	return dto.OpenResult{DocumentID: id, Path: canonical, ExternalChange: true}, nil
}

// resyncExternal replaces the worktree bytes with the externally changed disk
// content (the caller has already written them). Same block count preserves the
// stable IDs and staged candidates; a changed count rebuilds the structure.
func (s *store) resyncExternal(docID string, h *historyStore, content []byte) error {
	if err := h.writeFile(s.workfileName, content); err != nil {
		return err
	}

	parsed := parseBlocks(string(content))
	rowIDs, err := s.blockIDs(docID)
	if err != nil {
		return err
	}
	if len(parsed) == len(rowIDs) {
		for i, pb := range parsed {
			if _, err := s.db.Exec(`UPDATE blocks SET kind = ? WHERE id = ?`, string(pb.Kind), rowIDs[i]); err != nil {
				return err
			}
		}
		return nil
	}

	rootID := uuid.NewString()
	if _, err := s.db.Exec(
		`DELETE FROM candidates WHERE block_id IN (SELECT id FROM blocks WHERE document_id = ?)`, docID,
	); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM blocks WHERE document_id = ?`, docID); err != nil {
		return err
	}
	if len(parsed) == 0 {
		if err := s.insertBlock(docID, rootID, nil, "", dto.BlockKindParagraph, 0); err != nil {
			return err
		}
		return s.setRootBlock(docID, rootID)
	}
	for i, pb := range parsed {
		bid := rootID
		if i > 0 {
			bid = uuid.NewString()
		}
		if err := s.insertBlock(docID, bid, nil, "", pb.Kind, i); err != nil {
			return err
		}
	}
	return s.setRootBlock(docID, rootID)
}

func (s *store) setRootBlock(docID, rootID string) error {
	_, err := s.db.Exec(`UPDATE documents SET root_block_id = ? WHERE id = ?`, rootID, docID)
	return err
}

func (s *store) insertBlock(docID, id string, parent *string, pKind string, kind dto.BlockKind, pos int) error {
	_, err := s.db.Exec(
		`INSERT INTO blocks (id, document_id, parent_id, kind, position) VALUES (?, ?, ?, ?, ?)`,
		id, docID, parent, string(kind), pos,
	)
	return err
}

// SaveTree reconciles a manual whole-tree snapshot (ADR-0038): array order =
// position, `id` absent → mint a UUID (ADR-0020 §3), a block in the current tree
// but absent from the request is dropped, a changed kind/parent retypes/moves. It
// normalizes on write and formats on commit (ADR-0029 §6), drops every block's
// open candidates (human keystrokes supersede AI proposals), and commits an
// `autosave @ <ts>` snapshot iff anything changed. When WriteThrough is true
// (explicit Save / Cmd+S, not the periodic autosave), the canonical markdown is
// also mirrored back to the opened file path (ADR-0039). A no-op save is no
// longer silent: per ADR-0047 §4 it mirrors a stale disk (explicit save only) and
// refuses an externally changed file with a typed conflict.
func (s *store) SaveTree(documentID string, tree []dto.BlockWrite, opts dto.SaveOptions) (dto.WriteResult, error) {
	s.lockFor(documentID).Lock()
	defer s.lockFor(documentID).Unlock()

	type curRow struct {
		id     string
		parent *string
		kind   dto.BlockKind
	}
	rows, err := s.db.Query(
		`SELECT id, parent_id, kind FROM blocks WHERE document_id = ? ORDER BY position`,
		documentID,
	)
	if err != nil {
		return dto.WriteResult{}, err
	}
	defer rows.Close()
	var cur []curRow
	curByID := map[string]bool{}
	for rows.Next() {
		var r curRow
		var parent sql.NullString
		var kind string
		if err := rows.Scan(&r.id, &parent, &kind); err != nil {
			return dto.WriteResult{}, err
		}
		if parent.Valid {
			r.parent = &parent.String
		}
		r.kind = dto.BlockKind(kind)
		cur = append(cur, r)
		curByID[r.id] = true
	}
	if err := rows.Err(); err != nil {
		return dto.WriteResult{}, err
	}

	type block struct {
		id     string
		parent *string
		kind   dto.BlockKind
		text   string
	}
	out := make([]block, 0, len(tree))
	texts := make([]string, 0, len(tree))
	for _, bw := range tree {
		id := ""
		if bw.ID != nil {
			id = *bw.ID
		}
		// Existing IDs stay stable; a nil or stale ID mints a fresh UUID.
		if id == "" || !curByID[id] {
			id = uuid.NewString()
		}
		text, _ := s.tf.Format(bw.Kind, bw.Text)
		out = append(out, block{id: id, parent: bw.ParentID, kind: bw.Kind, text: text})
		texts = append(texts, text)
	}

	canonical := serializeBlocks(texts)

	// changed = structure or content differs.
	changed := len(cur) != len(out)
	if !changed {
		for i := range cur {
			if cur[i].id != out[i].id || cur[i].kind != out[i].kind || !strPtrEq(cur[i].parent, out[i].parent) {
				changed = true
				break
			}
		}
	}

	h, err := s.histFor(documentID)
	if err != nil {
		return dto.WriteResult{}, err
	}
	currentText, err := h.readFile(s.workfileName)
	if err != nil {
		return dto.WriteResult{}, err
	}
	if canonical != currentText {
		changed = true
	}
	if !changed {
		// ADR-0047 §4: the tree matches the worktree, but the disk may not.
		rev, err := s.currentRevision(documentID)
		if err != nil {
			return dto.WriteResult{}, err
		}
		res, err := s.noopResync(documentID, canonical, opts)
		if err != nil {
			return dto.WriteResult{}, err
		}
		res.Revision = rev
		return res, nil
	}

	// Write-through (ADR-0039 §3): mirror to the opened file *before* the
	// worktree write / DB rewrite / git commit. A failure aborts the save —
	// nothing is committed, the tree stays dirty, and a retry re-attempts.
	res := dto.WriteResult{WrittenThrough: opts.WriteThrough}
	if opts.WriteThrough {
		path, err := s.writeBack(documentID, canonical, opts.Overwrite)
		if err != nil {
			return dto.WriteResult{}, err
		}
		res.Path = path
	}

	// Drop open candidates (human keystrokes supersede the AI proposal), rewrite
	// the structure rows in position order, write the worktree, and commit.
	if _, err := s.db.Exec(
		`DELETE FROM candidates WHERE block_id IN (SELECT id FROM blocks WHERE document_id = ?)`,
		documentID,
	); err != nil {
		return dto.WriteResult{}, err
	}
	if _, err := s.db.Exec(`DELETE FROM blocks WHERE document_id = ?`, documentID); err != nil {
		return dto.WriteResult{}, err
	}
	for i, b := range out {
		if err := s.insertBlock(documentID, b.id, b.parent, "", b.kind, i); err != nil {
			return dto.WriteResult{}, err
		}
	}
	if err := h.writeFile(s.workfileName, []byte(canonical)); err != nil {
		return dto.WriteResult{}, err
	}
	now := time.Now().Unix()
	msg := fmt.Sprintf("autosave @ %d", now)
	hash, err := h.commit(msg)
	if err != nil {
		return dto.WriteResult{}, err
	}
	if _, err := s.db.Exec(`UPDATE documents SET updated_at = ? WHERE id = ?`, now, documentID); err != nil {
		return dto.WriteResult{}, err
	}
	res.Revision = dto.Revision{ID: hash, Message: msg, Timestamp: now}
	res.Committed = true
	return res, nil
}

// noopResync implements ADR-0047 §4 for a save whose tree already matches the
// worktree. disk == canonical is a true no-op; a stale disk (unchanged since the
// engine last read it) is mirrored only on an explicit write-through; an
// external change is refused with a typed conflict regardless of writeThrough.
func (s *store) noopResync(documentID, canonical string, opts dto.SaveOptions) (dto.WriteResult, error) {
	var path, lastKnown string
	if err := s.db.QueryRow(`SELECT path, content_hash FROM documents WHERE id = ?`, documentID).
		Scan(&path, &lastKnown); err != nil {
		return dto.WriteResult{}, err
	}

	b, readErr := os.ReadFile(path)
	missing := os.IsNotExist(readErr)
	if readErr != nil && !missing {
		return dto.WriteResult{}, readErr
	}
	if missing {
		if !opts.Overwrite {
			return dto.WriteResult{}, &FileChangedExternallyError{Path: path}
		}
	} else if contentHash(b) == contentHash([]byte(canonical)) {
		return dto.WriteResult{}, nil // already in sync
	} else if contentHash(b) != lastKnown && !opts.Overwrite {
		return dto.WriteResult{}, &FileChangedExternallyError{Path: path, CurrentHash: contentHash(b)}
	}

	// A safe (or explicitly overwriting) stale disk only re-syncs on an
	// explicit write-through; the periodic autosave never touches the disk.
	if !opts.WriteThrough {
		return dto.WriteResult{}, nil
	}
	writtenPath, err := s.writeBack(documentID, canonical, opts.Overwrite)
	if err != nil {
		return dto.WriteResult{}, err
	}
	return dto.WriteResult{WrittenThrough: true, Path: writtenPath}, nil
}

// currentRevision returns the newest revision (or a zero revision with no commits).
func (s *store) currentRevision(documentID string) (dto.Revision, error) {
	hist, err := s.History(documentID)
	if err != nil {
		return dto.Revision{}, err
	}
	if len(hist) == 0 {
		return dto.Revision{}, nil
	}
	return hist[0], nil
}

// strPtrEq compares two optional strings for pointer equality.
func strPtrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Blocks reconstructs the block tree from structure rows + the worktree file,
// surfacing each block's canonical Text and its content Hash (the guard anchor).
func (s *store) Blocks(documentID string) ([]dto.Block, error) {
	rows, err := s.db.Query(
		`SELECT id, parent_id, kind, position FROM blocks WHERE document_id = ? ORDER BY position`,
		documentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type bRow struct {
		id     string
		parent *string
		kind   dto.BlockKind
		pos    int
	}
	var structure []bRow
	for rows.Next() {
		var r bRow
		var kind string
		var parent sql.NullString
		if err := rows.Scan(&r.id, &parent, &kind, &r.pos); err != nil {
			return nil, err
		}
		if parent.Valid {
			r.parent = &parent.String
		}
		r.kind = dto.BlockKind(kind)
		structure = append(structure, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Text comes from the worktree file (no text column — ADR-0020 §2).
	h, err := s.histFor(documentID)
	if err != nil {
		return nil, err
	}
	fileText, err := h.readFile(s.workfileName)
	if err != nil {
		return nil, err
	}
	parsed := parseBlocks(fileText)

	out := make([]dto.Block, 0, len(structure))
	for i, r := range structure {
		text := ""
		if i < len(parsed) {
			text = parsed[i].Text
		}
		blk := dto.Block{
			ID:       r.id,
			ParentID: r.parent,
			Kind:     r.kind,
			Position: r.pos,
			Text:     text,
			Hash:     shortHash(text),
		}
		out = append(out, blk)
	}
	return out, nil
}

// ApplyEdit stages a whole-block replacement candidate (ADR-0029 §1/§3/§4):
// normalize the text, verify guards atomically, and stage a candidates row.
func (s *store) ApplyEdit(ctx context.Context, documentID string, edit dto.BlockEdit) (dto.Revision, error) {
	s.lockFor(documentID).Lock()
	defer s.lockFor(documentID).Unlock()

	blocks, err := s.Blocks(documentID)
	if err != nil {
		return dto.Revision{}, err
	}
	byID := map[string]dto.Block{}
	for _, b := range blocks {
		byID[b.ID] = b
	}

	target, ok := byID[edit.BlockID]
	if !ok {
		return dto.Revision{}, ErrBlockNotFound
	}

	// 1. Normalize to canonical form (always, ADR-0029 §2).
	canonical, changed := s.tf.Normalize(target.Kind, edit.Text)

	// 2. Verify guards atomically (before staging).
	var failures []GuardFailure
	for _, g := range edit.Guards {
		gb, ok := byID[g.BlockID]
		if !ok {
			failures = append(failures, GuardFailure{BlockID: g.BlockID, Reason: "block not found"})
			continue
		}
		if gb.Hash != g.Hash {
			failures = append(failures, GuardFailure{BlockID: g.BlockID, Reason: "content changed"})
		}
	}
	if len(failures) > 0 {
		return dto.Revision{}, fmt.Errorf("%w: %v", ErrGuardFailed, failures)
	}

	// 3. Stage the candidate (unaccepted edit side-table, ADR-0020 §4).
	base, err := s.headRev(documentID)
	if err != nil {
		return dto.Revision{}, err
	}
	// The preset that produced the edit drives the derived commit message
	// (ADR-0020 §1). A client re-stage (accept path) carries no preset; inherit
	// the newest staged candidate's mode so it survives to Commit.
	mode := edit.Mode
	if mode == "" {
		_ = s.db.QueryRow(
			`SELECT mode FROM candidates WHERE block_id = ? ORDER BY ts DESC, rowid DESC LIMIT 1`,
			edit.BlockID,
		).Scan(&mode)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO candidates (block_id, base_rev, base_hash, text, mode, ts) VALUES (?, ?, ?, ?, ?, ?)`,
		edit.BlockID, base, target.Hash, canonical, mode, time.Now().Unix(),
	); err != nil {
		return dto.Revision{}, err
	}

	_ = changed
	return dto.Revision{ID: base, Message: "candidate staged", Timestamp: time.Now().Unix()}, nil
}

// Commit accepts staged candidates: re-validates every candidate's base content
// hash (ADR-0047 §5), formats the accepted blocks (ADR-0029 §2), derives the
// commit message `mode · blockID · diff` (ADR-0020 §1, ADR-0047 §8), makes one
// git commit, and mirrors the canonical markdown to the opened file. An empty
// accept creates no commit and touches nothing.
func (s *store) Commit(documentID string, opts dto.CommitOptions) (dto.WriteResult, error) {
	s.lockFor(documentID).Lock()
	defer s.lockFor(documentID).Unlock()

	blocks, err := s.Blocks(documentID)
	if err != nil {
		return dto.WriteResult{}, err
	}
	byID := map[string]dto.Block{}
	for _, b := range blocks {
		byID[b.ID] = b
	}

	// Collect the latest candidate per block in deterministic newest-first order
	// (ts DESC, rowid DESC — ADR-0047 §5).
	type staged struct {
		text, baseHash, mode string
	}
	rows, err := s.db.Query(
		`SELECT block_id, text, base_hash, mode FROM candidates c
		 WHERE c.block_id IN (SELECT id FROM blocks WHERE document_id = ?)
		 ORDER BY c.ts DESC, c.rowid DESC`, documentID)
	if err != nil {
		return dto.WriteResult{}, err
	}
	var order []string
	applied := map[string]staged{}
	for rows.Next() {
		var blockID string
		var st staged
		if err := rows.Scan(&blockID, &st.text, &st.baseHash, &st.mode); err != nil {
			rows.Close()
			return dto.WriteResult{}, err
		}
		if _, seen := applied[blockID]; seen {
			continue
		}
		applied[blockID] = st
		order = append(order, blockID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return dto.WriteResult{}, err
	}

	// Re-validate each candidate's base hash before touching any bytes: a stale
	// candidate fails the whole accept with guard-failed (ADR-0047 §5).
	var failures []GuardFailure
	for _, blockID := range order {
		st := applied[blockID]
		if st.baseHash == "" {
			continue // legacy candidate staged before base_hash existed
		}
		b, ok := byID[blockID]
		if !ok {
			failures = append(failures, GuardFailure{BlockID: blockID, Reason: "block not found"})
			continue
		}
		if b.Hash != st.baseHash {
			failures = append(failures, GuardFailure{BlockID: blockID, Reason: "content changed"})
		}
	}
	if len(failures) > 0 {
		return dto.WriteResult{}, fmt.Errorf("%w: %v", ErrGuardFailed, failures)
	}

	// Empty accept: no commit, no write (ADR-0047 §8).
	if len(applied) == 0 {
		rev, err := s.currentRevision(documentID)
		if err != nil {
			return dto.WriteResult{}, err
		}
		return dto.WriteResult{Revision: rev}, nil
	}

	// Rebuild the canonical markdown: apply candidates, format every block, and
	// derive one message segment per accepted block.
	var texts []string
	var segments []string
	for _, b := range blocks {
		text := b.Text
		if st, ok := applied[b.ID]; ok {
			normalized, _ := s.tf.Normalize(b.Kind, st.text) // candidate already normalized
			segments = append(segments, commitSegment(st.mode, b.ID, text, normalized))
			text = normalized
		}
		formatted, _ := s.tf.Format(b.Kind, text)
		texts = append(texts, formatted)
	}
	canonical := serializeBlocks(texts)
	msg := strings.Join(segments, "; ")

	h, err := s.histFor(documentID)
	if err != nil {
		return dto.WriteResult{}, err
	}
	// Write-through (ADR-0039): an accepted AI edit mirrors to the opened file,
	// before the worktree write + commit. A conflict aborts the whole accept and
	// leaves the candidates staged (ADR-0047 §3).
	path, err := s.writeBack(documentID, canonical, opts.Overwrite)
	if err != nil {
		return dto.WriteResult{}, err
	}
	if err := h.writeFile(s.workfileName, []byte(canonical)); err != nil {
		return dto.WriteResult{}, err
	}
	hash, err := h.commit(msg)
	if err != nil {
		return dto.WriteResult{}, err
	}

	// Clear accepted candidates.
	if _, err := s.db.Exec(
		`DELETE FROM candidates WHERE block_id IN (SELECT id FROM blocks WHERE document_id = ?)`,
		documentID,
	); err != nil {
		return dto.WriteResult{}, err
	}
	now := time.Now().Unix()
	if _, err := s.db.Exec(`UPDATE documents SET updated_at = ? WHERE id = ?`, now, documentID); err != nil {
		return dto.WriteResult{}, err
	}
	return dto.WriteResult{
		Revision:       dto.Revision{ID: hash, Message: msg, Timestamp: now},
		WrittenThrough: true,
		Path:           path,
		Committed:      true,
	}, nil
}

// commitSegment derives one `mode · blockID · one-line-diff-summary` message
// segment (ADR-0020 §1, ADR-0047 §8). The mode is empty for HTTP re-stages that
// could not inherit a preset; "accepted" is the neutral label.
func commitSegment(mode, blockID, oldText, newText string) string {
	if mode == "" {
		mode = "accepted"
	}
	insertions, deletions := wordDiff(oldText, newText)
	parts := make([]string, 0, len(insertions)+len(deletions))
	for _, w := range insertions {
		parts = append(parts, "+"+w)
	}
	for _, w := range deletions {
		parts = append(parts, "-"+w)
	}
	summary := "no change"
	if len(parts) > 0 {
		summary = strings.Join(parts, " ")
		if len(summary) > 80 {
			summary = summary[:80] + "…"
		}
	}
	return fmt.Sprintf("%s · %s · %s", mode, blockID, summary)
}

// writeBack mirrors canonical markdown to the document's opened file path
// (ADR-0039): the file the user actually reads (Obsidian, the vault) is a
// write-through mirror of the engine's canonical bytes, written atomically and
// only when it differs. Immediately before the rename it compares the on-disk
// hash to the engine's last-known hash; a mismatch aborts with a typed
// file-changed-externally error unless overwrite is set (ADR-0047 §3). It must
// be called under the document lock, before the worktree write + commit, so a
// failure aborts the whole save. It returns the mirrored path.
func (s *store) writeBack(documentID string, canonical string, overwrite bool) (string, error) {
	var path, lastKnown string
	if err := s.db.QueryRow(
		`SELECT path, content_hash FROM documents WHERE id = ?`, documentID,
	).Scan(&path, &lastKnown); err != nil {
		return "", err
	}

	b, readErr := os.ReadFile(path)
	missing := os.IsNotExist(readErr)
	if readErr != nil && !missing {
		return "", readErr
	}
	canonicalHash := contentHash([]byte(canonical))

	if missing {
		// An externally deleted file is an external change, not a clobber: it
		// requires an explicit overwrite to be recreated (ADR-0047 §3).
		if !overwrite {
			return "", &FileChangedExternallyError{Path: path}
		}
	} else if string(b) == canonical {
		// Already holds the canonical bytes (no-op saves / empty accepts never
		// rewrite an unchanged file, ADR-0039 §4).
		return path, s.markSynced(documentID, canonicalHash)
	} else if !overwrite && lastKnown != "" && contentHash(b) != lastKnown {
		return "", &FileChangedExternallyError{Path: path, CurrentHash: contentHash(b)}
	}

	if err := atomicWriteFile(path, []byte(canonical)); err != nil {
		return "", err
	}
	if err := s.markSynced(documentID, canonicalHash); err != nil {
		return "", err
	}
	return path, nil
}

// markSynced records the engine's last-known disk hash after a write-through.
func (s *store) markSynced(documentID, hash string) error {
	_, err := s.db.Exec(`UPDATE documents SET content_hash = ? WHERE id = ?`, hash, documentID)
	return err
}

// atomicWriteFile writes data to the write target via a temp file + rename in
// the target's directory, preserving the existing file's mode (0o644 when the
// file is new). A symlink at path is written through — the link is preserved,
// its target is replaced (ADR-0047 §7). A partial or interleaved write can never
// be observed at the target.
func atomicWriteFile(path string, data []byte) error {
	target := resolveWriteTarget(path)
	mode := os.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".texteditor-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

// resolveWriteTarget follows symlinks (bounded depth) to the path that should
// actually receive the bytes. A dangling symlink resolves to its target so the
// link itself survives the write.
func resolveWriteTarget(path string) string {
	p := path
	for i := 0; i < 32; i++ {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return p
		}
		dst, err := os.Readlink(p)
		if err != nil {
			return p
		}
		if !filepath.IsAbs(dst) {
			dst = filepath.Join(filepath.Dir(p), dst)
		}
		p = dst
	}
	return p
}

// Diff returns word-level insertions/deletions per block between two revisions
// (ADR-0004 §2, ADR-0016 §9). go-diff is the hidden internal.
func (s *store) Diff(documentID string, baseRev, rev string) ([]dto.WordEdit, error) {
	h, err := s.histFor(documentID)
	if err != nil {
		return nil, err
	}
	baseText, err := h.fileAt(s.workfileName, baseRev)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownRevision, baseRev)
	}
	revText, err := h.fileAt(s.workfileName, rev)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownRevision, rev)
	}

	baseBlocks := parseBlocks(baseText)
	revBlocks := parseBlocks(revText)

	// Map by position/index; block IDs are stable across edits (ADR-0020 §3).
	structure, err := s.blockIDs(documentID)
	if err != nil {
		return nil, err
	}

	var out []dto.WordEdit
	for i := 0; i < len(baseBlocks) || i < len(revBlocks); i++ {
		var base, revV string
		if i < len(baseBlocks) {
			base = baseBlocks[i].Text
		}
		if i < len(revBlocks) {
			revV = revBlocks[i].Text
		}
		if base == revV {
			continue
		}
		we := dto.WordEdit{BlockID: strconv.Itoa(i)}
		if i < len(structure) {
			we.BlockID = structure[i]
		}
		we.Insertions, we.Deletions = wordDiff(base, revV)
		out = append(out, we)
	}
	return out, nil
}

// History walks the git commit log, newest first (ADR-0020 §1).
func (s *store) History(documentID string) ([]dto.Revision, error) {
	h, err := s.histFor(documentID)
	if err != nil {
		return nil, err
	}
	commits, err := h.log()
	if err != nil {
		return nil, err
	}
	out := make([]dto.Revision, 0, len(commits))
	for _, c := range commits {
		out = append(out, dto.Revision{
			ID:        c.hash,
			Message:   c.msg,
			Timestamp: c.ts,
		})
	}
	return out, nil
}

// Candidates lists open (unaccepted) proposals for one block (ADR-0020 §4).
func (s *store) Candidates(documentID string, blockID string) ([]dto.Candidate, error) {
	rows, err := s.db.Query(
		`SELECT block_id, text, base_rev FROM candidates
		 WHERE block_id = ? AND block_id IN (SELECT id FROM blocks WHERE document_id = ?)
		 ORDER BY ts DESC, rowid DESC`, blockID, documentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dto.Candidate
	for rows.Next() {
		var c dto.Candidate
		if err := rows.Scan(&c.BlockID, &c.Text, &c.BaseID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *store) headRev(documentID string) (string, error) {
	h, err := s.histFor(documentID)
	if err != nil {
		return "", err
	}
	commits, err := h.log()
	if err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "", nil // no base yet — candidate diffs against the empty initial state
	}
	return commits[0].hash, nil
}

func (s *store) blockIDs(documentID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM blocks WHERE document_id = ? ORDER BY position`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// canonicalPath resolves a path to its canonical absolute form and case-folded
// identity key through the shared pathutil discipline (ADR-0047 §2), so
// document, workspace, corpus, and filesystem identity agree.
func canonicalPath(path string) (canonical, key string) {
	return pathutil.Canonical(path)
}

// contentHash returns the full SHA-256 content hash of the disk bytes — the
// external-change / write-through comparison anchor (ADR-0047 §2/§3).
func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// shortHash returns a short content hash for the guard anchor (ADR-0029 §4).
func shortHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:4])
}

// wordDiff computes insertions/deletions at word granularity via a simple
// sequence diff. Kept independent of go-diff so the store has no extra dep;
// ADR-0016 §9 pins the behavior (word-level), not the library.
func wordDiff(a, b string) (insertions, deletions []string) {
	aw := strings.Fields(a)
	bw := strings.Fields(b)
	lcs := lcsWords(aw, bw)
	i, j := 0, 0
	for _, w := range lcs {
		for i < len(aw) && aw[i] != w {
			deletions = append(deletions, aw[i])
			i++
		}
		for j < len(bw) && bw[j] != w {
			insertions = append(insertions, bw[j])
			j++
		}
		i++
		j++
	}
	for ; i < len(aw); i++ {
		deletions = append(deletions, aw[i])
	}
	for ; j < len(bw); j++ {
		insertions = append(insertions, bw[j])
	}
	sort.Strings(insertions)
	sort.Strings(deletions)
	return insertions, deletions
}

func lcsWords(a, b []string) []string {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return nil
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	out := make([]string, 0, dp[0][0])
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			out = append(out, a[i])
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return out
}
