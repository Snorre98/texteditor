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

	"texteditor/internal/sqlmigrate"
	"texteditor/internal/textformatter"
	"texteditor/shared/dto"
)

// DocumentStore is the Document store public API (interface.md §9). It owns
// app.db (documents/blocks/candidates), the git history repo, and the engine
// working tree (ADR-0020 §2).
type DocumentStore interface {
	Open(path string) (documentID string, err error)
	SaveTree(documentID string, tree []dto.BlockWrite) (dto.Revision, error)
	Blocks(documentID string) ([]dto.Block, error)
	ApplyEdit(ctx context.Context, documentID string, edit dto.BlockEdit) (dto.Revision, error)
	Commit(documentID string, msg string) error
	Diff(documentID string, baseRev, rev string) ([]dto.WordEdit, error)
	History(documentID string) ([]dto.Revision, error)
	Candidates(documentID string, blockID string) ([]dto.Candidate, error)
}

// Interface is an alias for DocumentStore (the contracted name, interface.md §9).
type Interface = DocumentStore

// Typed errors (ADR-0029 §4, §5; failure-semantics §3).
var (
	ErrGuardFailed      = errors.New("guard-failed: a guarded block's content changed")
	ErrInvalidStructure = errors.New("invalid-structure: text failed structural validation")
	ErrBlockNotFound    = errors.New("block not found")
	ErrDocumentNotFound = errors.New("document not found")
	ErrUnknownRevision  = errors.New("unknown revision")
)

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

// Open resolves a document by absolute path: an existing row re-opens; otherwise
// the file is parsed into a block tree, stable UUIDs are minted (ADR-0020 §3),
// the canonical markdown is written to the worktree, and rows are inserted.
func (s *store) Open(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	var id string
	err = s.db.QueryRow(`SELECT id FROM documents WHERE path = ?`, abs).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	var src string
	b, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			src = ""
		} else {
			return "", err
		}
	} else {
		src = string(b)
	}

	docID := uuid.NewString()
	rootID := uuid.NewString()
	now := time.Now().Unix()

	if _, err := s.db.Exec(
		`INSERT INTO documents (id, path, root_block_id, updated_at) VALUES (?, ?, ?, ?)`,
		docID, abs, rootID, now,
	); err != nil {
		return "", err
	}

	blocks := parseBlocks(src)
	if len(blocks) == 0 {
		// An empty document still has a root block (a document is a tree).
		if err := s.insertBlock(docID, rootID, nil, "", dto.BlockKindParagraph, 0); err != nil {
			return "", err
		}
	} else {
		for i, pb := range blocks {
			id := rootID
			if i > 0 {
				id = uuid.NewString()
			}
			if err := s.insertBlock(docID, id, nil, "", pb.Kind, i); err != nil {
				return "", err
			}
		}
	}

	// Write canonical markdown into the worktree file (the doc's single owner).
	h, err := s.histFor(docID)
	if err != nil {
		return "", err
	}
	if err := h.writeFile(s.workfileName, []byte(src)); err != nil {
		return "", err
	}
	return docID, nil
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
// `autosave @ <ts>` snapshot iff anything changed — an unchanged tree returns the
// current HEAD with no new commit.
func (s *store) SaveTree(documentID string, tree []dto.BlockWrite) (dto.Revision, error) {
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
		return dto.Revision{}, err
	}
	defer rows.Close()
	var cur []curRow
	curByID := map[string]bool{}
	for rows.Next() {
		var r curRow
		var parent sql.NullString
		var kind string
		if err := rows.Scan(&r.id, &parent, &kind); err != nil {
			return dto.Revision{}, err
		}
		if parent.Valid {
			r.parent = &parent.String
		}
		r.kind = dto.BlockKind(kind)
		cur = append(cur, r)
		curByID[r.id] = true
	}
	if err := rows.Err(); err != nil {
		return dto.Revision{}, err
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

	// changed = structure or content differs (a no-op returns the current HEAD).
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
		return dto.Revision{}, err
	}
	currentText, err := h.readFile(s.workfileName)
	if err != nil {
		return dto.Revision{}, err
	}
	if canonical != currentText {
		changed = true
	}
	if !changed {
		return s.currentRevision(documentID)
	}

	// Drop open candidates (human keystrokes supersede the AI proposal), rewrite
	// the structure rows in position order, write the worktree, and commit.
	if _, err := s.db.Exec(
		`DELETE FROM candidates WHERE block_id IN (SELECT id FROM blocks WHERE document_id = ?)`,
		documentID,
	); err != nil {
		return dto.Revision{}, err
	}
	if _, err := s.db.Exec(`DELETE FROM blocks WHERE document_id = ?`, documentID); err != nil {
		return dto.Revision{}, err
	}
	for i, b := range out {
		if err := s.insertBlock(documentID, b.id, b.parent, "", b.kind, i); err != nil {
			return dto.Revision{}, err
		}
	}
	if err := h.writeFile(s.workfileName, []byte(canonical)); err != nil {
		return dto.Revision{}, err
	}
	now := time.Now().Unix()
	msg := fmt.Sprintf("autosave @ %d", now)
	hash, err := h.commit(msg)
	if err != nil {
		return dto.Revision{}, err
	}
	if _, err := s.db.Exec(`UPDATE documents SET updated_at = ? WHERE id = ?`, now, documentID); err != nil {
		return dto.Revision{}, err
	}
	return dto.Revision{ID: hash, Message: msg, Timestamp: now}, nil
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
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO candidates (block_id, base_rev, text, mode, ts) VALUES (?, ?, ?, '', ?)`,
		edit.BlockID, base, canonical, time.Now().Unix(),
	); err != nil {
		return dto.Revision{}, err
	}

	_ = changed
	return dto.Revision{ID: base, Message: "candidate staged", Timestamp: time.Now().Unix()}, nil
}

// Commit accepts staged candidates: formats the accepted blocks (ADR-0029 §2)
// and makes one git commit (ADR-0020 §1).
func (s *store) Commit(documentID string, msg string) error {
	s.lockFor(documentID).Lock()
	defer s.lockFor(documentID).Unlock()

	blocks, err := s.Blocks(documentID)
	if err != nil {
		return err
	}
	byID := map[string]dto.Block{}
	for _, b := range blocks {
		byID[b.ID] = b
	}

	// Collect the latest candidate per block and apply it.
	rows, err := s.db.Query(
		`SELECT block_id, text FROM candidates c
		 WHERE c.block_id IN (SELECT id FROM blocks WHERE document_id = ?)
		 ORDER BY c.ts DESC`, documentID)
	if err != nil {
		return err
	}
	applied := map[string]string{}
	for rows.Next() {
		var blockID, text string
		if err := rows.Scan(&blockID, &text); err != nil {
			rows.Close()
			return err
		}
		if _, seen := applied[blockID]; !seen {
			applied[blockID] = text
		}
	}
	rows.Close()

	// Rebuild the canonical markdown: apply candidates, format every block.
	var texts []string
	for _, b := range blocks {
		text := b.Text
		if t, ok := applied[b.ID]; ok {
			normalized, _ := s.tf.Normalize(b.Kind, t) // candidate already normalized
			text = normalized
		}
		formatted, _ := s.tf.Format(b.Kind, text)
		texts = append(texts, formatted)
	}
	canonical := serializeBlocks(texts)

	h, err := s.histFor(documentID)
	if err != nil {
		return err
	}
	if err := h.writeFile(s.workfileName, []byte(canonical)); err != nil {
		return err
	}
	if _, err := h.commit(msg); err != nil {
		return err
	}

	// Clear accepted candidates.
	if _, err := s.db.Exec(
		`DELETE FROM candidates WHERE block_id IN (SELECT id FROM blocks WHERE document_id = ?)`,
		documentID,
	); err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE documents SET updated_at = ? WHERE id = ?`, time.Now().Unix(), documentID)
	if err != nil {
		return err
	}
	return nil
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
		 ORDER BY ts DESC`, blockID, documentID,
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
