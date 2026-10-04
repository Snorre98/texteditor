// Package workspace holds the Workspace store — the sealed owner of the global
// workspaces.db registry (ADR-0049 §2/§3/§5). It persists workspace records,
// corpus roots/scope, index-job progress, eviction tombstones, and the
// turn/session routing index. It is a pure data leaf over its single file:
// no filesystem traversal, no indexing, no provider calls.
//
// The stateless filesystem reach previously named Workspace is now the
// Filesystem leaf (ADR-0049 §5); this package is the persistent Workspace
// entity.
package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"texteditor/internal/pathutil"
	"texteditor/internal/sqlmigrate"
	"texteditor/shared/dto"
)

// Interface is the Workspace store public API (interface.md §9c, ADR-0049 §2).
type Interface interface {
	// ResolveOrCreate resolves a directory to the most specific existing
	// workspace whose root contains it, or creates one (ADR-0049 §2). The root
	// is canonicalized (EvalSymlinks + case-fold), so aliases resolve to one
	// workspace.
	ResolveOrCreate(root string) (dto.Workspace, error)
	Get(id string) (dto.Workspace, error)
	List() ([]dto.Workspace, error)
	// FindContaining returns the most specific workspace whose root contains
	// path, without creating one.
	FindContaining(path string) (dto.Workspace, bool, error)

	// Scope returns a workspace's corpus scope (roots + include/exclude).
	Scope(workspaceID string) (dto.CorpusScope, error)
	// SetScope replaces a workspace's corpus scope, canonicalizing and
	// deduping roots. It is idempotent and index-only (ADR-0049 §4).
	SetScope(workspaceID string, scope dto.CorpusScope) (dto.CorpusScope, error)

	// StartJob records a running corpus job; UpdateJob advances it.
	StartJob(workspaceID, kind string, total int) (dto.CorpusJob, error)
	UpdateJob(id string, completed int, state, errMsg string) error
	// LatestJob returns the workspace's most recent job, if any.
	LatestJob(workspaceID string) (dto.CorpusJob, bool, error)

	// MarkEvicted/ClearEvicted maintain explicit-eviction tombstones (status
	// "evicted" until re-indexed).
	MarkEvicted(workspaceID, path string) error
	ClearEvicted(workspaceID, path string) error
	EvictedPaths(workspaceID string) ([]string, error)
	// MarkError/ClearError maintain per-path index errors (status "error").
	MarkError(workspaceID, path, message string) error
	ClearError(workspaceID, path string) error
	Errors(workspaceID string) (map[string]string, error)

	// RouteSession/SessionWorkspace route a session id to its shard; the
	// registry is the only global index (ADR-0049 §5).
	RouteSession(sessionID, workspaceID string) error
	SessionWorkspace(sessionID string) (string, bool, error)
	// RouteTurn/TurnRoute route a turn id to its workspace shard so
	// GET /turns/{id}/context can resolve after the turn ends.
	RouteTurn(turnID, workspaceID, sessionID string) error
	TurnRoute(turnID string) (workspaceID, sessionID string, ok bool, err error)
}

// Typed errors.
var (
	ErrNotFound        = errors.New("workspace not found")
	ErrRootUnavailable = errors.New("workspace-root-unavailable: the root does not exist or is not a directory")
	ErrNotDirectory    = errors.New("workspace-root-not-a-directory")
)

// store is the concrete Workspace store. workspaces.db is its single-writer
// file (ADR-0016/0049 §5).
type store struct {
	db *sql.DB
	mu sync.Mutex // serializes create-or-resume (unique root_key race)
}

// New returns a Workspace store over an already-migrated workspaces.db.
func New(db *sql.DB) Interface { return &store{db: db} }

// Migrate applies the workspaces.db schema (exposed for the composition root).
func Migrate(ctx context.Context, db *sql.DB) error {
	return sqlmigrate.Migrate(ctx, db, workspacesSchema)
}

// ResolveOrCreate resolves or creates a workspace by canonical root.
func (s *store) ResolveOrCreate(root string) (dto.Workspace, error) {
	if strings.TrimSpace(root) == "" {
		return dto.Workspace{}, ErrRootUnavailable
	}
	canonical, key := pathutil.Canonical(root)
	info, err := os.Stat(canonical)
	if err != nil {
		return dto.Workspace{}, ErrRootUnavailable
	}
	if !info.IsDir() {
		return dto.Workspace{}, ErrNotDirectory
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Exact root already registered.
	if ws, err := s.byRootKey(key); err == nil {
		s.touch(ws.ID)
		return ws, nil
	} else if !errors.Is(err, ErrNotFound) {
		return dto.Workspace{}, err
	}
	// Most specific existing workspace whose root contains the directory.
	if ws, ok, err := s.findContainingLocked(canonical); err != nil {
		return dto.Workspace{}, err
	} else if ok {
		s.touch(ws.ID)
		return ws, nil
	}
	return s.createLocked(canonical, key)
}

// createLocked inserts a workspace plus its default corpus scope (root +
// **/*.md, ADR-0049 §3/§15).
func (s *store) createLocked(canonical, key string) (dto.Workspace, error) {
	now := time.Now().Unix()
	ws := dto.Workspace{
		ID:        uuid.NewString(),
		Root:      canonical,
		Name:      filepath.Base(canonical),
		CreatedAt: now,
		UpdatedAt: now,
	}
	tx, err := s.db.Begin()
	if err != nil {
		return dto.Workspace{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO workspaces (id, root, root_key, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ws.ID, ws.Root, key, ws.Name, ws.CreatedAt, ws.UpdatedAt,
	); err != nil {
		return dto.Workspace{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO corpus_roots (workspace_id, path, path_key, position) VALUES (?, ?, ?, 0)`,
		ws.ID, canonical, key,
	); err != nil {
		return dto.Workspace{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO corpus_scope (workspace_id, kind, pattern) VALUES (?, 'include', ?)`,
		ws.ID, dto.DefaultCorpusInclude,
	); err != nil {
		return dto.Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return dto.Workspace{}, err
	}
	return ws, nil
}

// Get returns a workspace by id.
func (s *store) Get(id string) (dto.Workspace, error) {
	return scanWorkspace(s.db.QueryRow(
		`SELECT id, root, name, created_at, updated_at FROM workspaces WHERE id = ?`, id,
	))
}

// List returns every workspace, newest-created first.
func (s *store) List() ([]dto.Workspace, error) {
	rows, err := s.db.Query(
		`SELECT id, root, name, created_at, updated_at FROM workspaces ORDER BY created_at DESC, id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dto.Workspace
	for rows.Next() {
		var ws dto.Workspace
		if err := rows.Scan(&ws.ID, &ws.Root, &ws.Name, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

// FindContaining returns the most specific workspace whose root contains path.
func (s *store) FindContaining(path string) (dto.Workspace, bool, error) {
	if strings.TrimSpace(path) == "" {
		return dto.Workspace{}, false, nil
	}
	canonical, _ := pathutil.Canonical(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findContainingLocked(canonical)
}

// findContainingLocked picks the longest root that contains canonical.
func (s *store) findContainingLocked(canonical string) (dto.Workspace, bool, error) {
	all, err := s.List()
	if err != nil {
		return dto.Workspace{}, false, err
	}
	var best dto.Workspace
	found := false
	for _, ws := range all {
		if !pathutil.Within(canonical, ws.Root) {
			continue
		}
		if !found || len(ws.Root) > len(best.Root) {
			best = ws
			found = true
		}
	}
	return best, found, nil
}

// byRootKey looks a workspace up by its case-folded canonical root key.
func (s *store) byRootKey(key string) (dto.Workspace, error) {
	return scanWorkspace(s.db.QueryRow(
		`SELECT id, root, name, created_at, updated_at FROM workspaces WHERE root_key = ?`, key,
	))
}

func (s *store) touch(id string) {
	_, _ = s.db.Exec(`UPDATE workspaces SET updated_at = ? WHERE id = ?`, time.Now().Unix(), id)
}

// Scope returns the workspace's corpus roots + include/exclude globs.
func (s *store) Scope(workspaceID string) (dto.CorpusScope, error) {
	if _, err := s.Get(workspaceID); err != nil {
		return dto.CorpusScope{}, err
	}
	roots, err := s.patterns(`SELECT path FROM corpus_roots WHERE workspace_id = ? ORDER BY position`, workspaceID)
	if err != nil {
		return dto.CorpusScope{}, err
	}
	include, err := s.patterns(`SELECT pattern FROM corpus_scope WHERE workspace_id = ? AND kind = 'include' ORDER BY pattern`, workspaceID)
	if err != nil {
		return dto.CorpusScope{}, err
	}
	exclude, err := s.patterns(`SELECT pattern FROM corpus_scope WHERE workspace_id = ? AND kind = 'exclude' ORDER BY pattern`, workspaceID)
	if err != nil {
		return dto.CorpusScope{}, err
	}
	return dto.CorpusScope{Roots: roots, Include: include, Exclude: exclude}, nil
}

func (s *store) patterns(query, workspaceID string) ([]string, error) {
	rows, err := s.db.Query(query, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetScope replaces the workspace's corpus scope. Roots are canonicalized and
// deduped; an empty scope falls back to the workspace root + default include
// (never an accidentally empty corpus).
func (s *store) SetScope(workspaceID string, scope dto.CorpusScope) (dto.CorpusScope, error) {
	ws, err := s.Get(workspaceID)
	if err != nil {
		return dto.CorpusScope{}, err
	}
	normalized := dto.CorpusScope{
		Roots:   canonicalRoots(scope.Roots),
		Include: dedupeNonEmpty(scope.Include),
		Exclude: dedupeNonEmpty(scope.Exclude),
	}
	if len(normalized.Roots) == 0 {
		normalized.Roots = []string{ws.Root}
	}
	if len(normalized.Include) == 0 {
		normalized.Include = []string{dto.DefaultCorpusInclude}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return dto.CorpusScope{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM corpus_roots WHERE workspace_id = ?`, workspaceID); err != nil {
		return dto.CorpusScope{}, err
	}
	if _, err := tx.Exec(`DELETE FROM corpus_scope WHERE workspace_id = ?`, workspaceID); err != nil {
		return dto.CorpusScope{}, err
	}
	for i, root := range normalized.Roots {
		_, key := pathutil.Canonical(root)
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO corpus_roots (workspace_id, path, path_key, position) VALUES (?, ?, ?, ?)`,
			workspaceID, root, key, i,
		); err != nil {
			return dto.CorpusScope{}, err
		}
	}
	for _, pattern := range normalized.Include {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO corpus_scope (workspace_id, kind, pattern) VALUES (?, 'include', ?)`,
			workspaceID, pattern,
		); err != nil {
			return dto.CorpusScope{}, err
		}
	}
	for _, pattern := range normalized.Exclude {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO corpus_scope (workspace_id, kind, pattern) VALUES (?, 'exclude', ?)`,
			workspaceID, pattern,
		); err != nil {
			return dto.CorpusScope{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE workspaces SET updated_at = ? WHERE id = ?`, time.Now().Unix(), workspaceID); err != nil {
		return dto.CorpusScope{}, err
	}
	if err := tx.Commit(); err != nil {
		return dto.CorpusScope{}, err
	}
	return normalized, nil
}

// canonicalRoots canonicalizes and dedupes (by case-folded key) preserving order.
func canonicalRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		canonical, key := pathutil.Canonical(root)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, canonical)
	}
	return out
}

// dedupeNonEmpty trims and dedupes globs preserving order.
func dedupeNonEmpty(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	seen := map[string]bool{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// StartJob records a running corpus job.
func (s *store) StartJob(workspaceID, kind string, total int) (dto.CorpusJob, error) {
	job := dto.CorpusJob{
		ID:          uuid.NewString(),
		WorkspaceID: workspaceID,
		Kind:        kind,
		State:       "running",
		Total:       total,
		StartedAt:   time.Now().Unix(),
	}
	_, err := s.db.Exec(
		`INSERT INTO corpus_jobs (id, workspace_id, kind, state, total, completed, error, started_at)
		 VALUES (?, ?, ?, ?, ?, 0, '', ?)`,
		job.ID, job.WorkspaceID, job.Kind, job.State, job.Total, job.StartedAt,
	)
	if err != nil {
		return dto.CorpusJob{}, err
	}
	return job, nil
}

// UpdateJob advances a job; terminal states stamp finished_at.
func (s *store) UpdateJob(id string, completed int, state, errMsg string) error {
	var finished interface{}
	if state == "done" || state == "error" {
		finished = time.Now().Unix()
	}
	_, err := s.db.Exec(
		`UPDATE corpus_jobs SET completed = ?, state = ?, error = ?, finished_at = COALESCE(?, finished_at) WHERE id = ?`,
		completed, state, errMsg, finished, id,
	)
	return err
}

// LatestJob returns the workspace's most recent job.
func (s *store) LatestJob(workspaceID string) (dto.CorpusJob, bool, error) {
	// rowid DESC (insertion order) is the tie-breaker: jobs created within the
	// same second must still order newest-first.
	row := s.db.QueryRow(
		`SELECT id, workspace_id, kind, state, total, completed, error, started_at, finished_at
		 FROM corpus_jobs WHERE workspace_id = ? ORDER BY rowid DESC LIMIT 1`, workspaceID,
	)
	var j dto.CorpusJob
	err := row.Scan(&j.ID, &j.WorkspaceID, &j.Kind, &j.State, &j.Total, &j.Completed, &j.Error, &j.StartedAt, &j.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return dto.CorpusJob{}, false, nil
	}
	if err != nil {
		return dto.CorpusJob{}, false, err
	}
	return j, true, nil
}

// MarkEvicted records an explicit eviction tombstone.
func (s *store) MarkEvicted(workspaceID, path string) error {
	canonical, key := pathutil.Canonical(path)
	_, err := s.db.Exec(
		`INSERT INTO corpus_evicted (workspace_id, path_key, path, evicted_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(workspace_id, path_key) DO UPDATE SET path = excluded.path, evicted_at = excluded.evicted_at`,
		workspaceID, key, canonical, time.Now().Unix(),
	)
	return err
}

// ClearEvicted removes an eviction tombstone.
func (s *store) ClearEvicted(workspaceID, path string) error {
	_, key := pathutil.Canonical(path)
	_, err := s.db.Exec(`DELETE FROM corpus_evicted WHERE workspace_id = ? AND path_key = ?`, workspaceID, key)
	return err
}

// EvictedPaths returns the workspace's tombstoned canonical paths.
func (s *store) EvictedPaths(workspaceID string) ([]string, error) {
	return s.patterns(`SELECT path FROM corpus_evicted WHERE workspace_id = ? ORDER BY path`, workspaceID)
}

// MarkError records a per-path index error.
func (s *store) MarkError(workspaceID, path, message string) error {
	canonical, key := pathutil.Canonical(path)
	_, err := s.db.Exec(
		`INSERT INTO corpus_errors (workspace_id, path_key, path, message, ts) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(workspace_id, path_key) DO UPDATE SET path = excluded.path, message = excluded.message, ts = excluded.ts`,
		workspaceID, key, canonical, message, time.Now().Unix(),
	)
	return err
}

// ClearError removes a per-path index error.
func (s *store) ClearError(workspaceID, path string) error {
	_, key := pathutil.Canonical(path)
	_, err := s.db.Exec(`DELETE FROM corpus_errors WHERE workspace_id = ? AND path_key = ?`, workspaceID, key)
	return err
}

// Errors returns the workspace's per-path error messages (keyed by path_key).
func (s *store) Errors(workspaceID string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT path_key, message FROM corpus_errors WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, msg string
		if err := rows.Scan(&key, &msg); err != nil {
			return nil, err
		}
		out[key] = msg
	}
	return out, rows.Err()
}

// RouteSession records the session → workspace shard mapping.
func (s *store) RouteSession(sessionID, workspaceID string) error {
	_, err := s.db.Exec(
		`INSERT INTO session_routes (session_id, workspace_id, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET workspace_id = excluded.workspace_id`,
		sessionID, workspaceID, time.Now().Unix(),
	)
	return err
}

// SessionWorkspace resolves a session's workspace shard.
func (s *store) SessionWorkspace(sessionID string) (string, bool, error) {
	var ws string
	err := s.db.QueryRow(`SELECT workspace_id FROM session_routes WHERE session_id = ?`, sessionID).Scan(&ws)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return ws, true, nil
}

// TurnRoute records the turn → workspace/session mapping so a finished turn's
// context snapshot is resolvable (retention: rows older than 30 days pruned).
func (s *store) RouteTurn(turnID, workspaceID, sessionID string) error {
	now := time.Now().Unix()
	if _, err := s.db.Exec(
		`INSERT INTO turn_routes (turn_id, workspace_id, session_id, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(turn_id) DO UPDATE SET workspace_id = excluded.workspace_id, session_id = excluded.session_id`,
		turnID, workspaceID, sessionID, now,
	); err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM turn_routes WHERE created_at < ?`, now-30*24*3600)
	return nil
}

// TurnRoute resolves a turn's workspace and session ids.
func (s *store) TurnRoute(turnID string) (string, string, bool, error) {
	var ws, session string
	err := s.db.QueryRow(
		`SELECT workspace_id, session_id FROM turn_routes WHERE turn_id = ?`, turnID,
	).Scan(&ws, &session)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return ws, session, true, nil
}

// scanWorkspace maps one workspace row, translating sql.ErrNoRows.
func scanWorkspace(row *sql.Row) (dto.Workspace, error) {
	var ws dto.Workspace
	err := row.Scan(&ws.ID, &ws.Root, &ws.Name, &ws.CreatedAt, &ws.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return dto.Workspace{}, ErrNotFound
	}
	if err != nil {
		return dto.Workspace{}, fmt.Errorf("workspace scan: %w", err)
	}
	return ws, nil
}
