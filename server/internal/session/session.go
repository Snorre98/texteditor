// Package session holds the Session store — a pure data leaf owning a dedicated
// sessions.db (ADR-0026 §2). It is single-writer over that file.
package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"texteditor/internal/sqlmigrate"
	"texteditor/shared/dto"
)

// SessionStore is the Session store public API (interface.md §10).
type SessionStore interface {
	ListByDocument(documentID string) ([]dto.Session, error)
	// ListByWorkspace returns every session in this workspace shard, newest
	// first (ADR-0049 §5: sessions are workspace-scoped by shard).
	ListByWorkspace() ([]dto.Session, error)
	Create(documentID string, anchorBlockID *string, modeType, title string) (dto.Session, error)
	Resume(id string) (dto.Session, error)
	// Rename sets a session's human title (data-model §1.4). An explicit empty
	// title clears it. An unknown session is ErrNotFound.
	Rename(id, title string) error
	Append(sessionID string, msg dto.Message) error
	History(sessionID string) ([]dto.Message, error)
	// SaveContext persists one turn's context snapshot in this shard, keeping
	// only the newest 100 rows per session (ADR-0044 §4). The snapshot is an
	// opaque JSON passthrough — the session leaf never parses it.
	SaveContext(turnID, sessionID string, snapshot json.RawMessage) error
	// TurnContext returns a persisted turn's context snapshot, or ErrNotFound.
	TurnContext(turnID string) (json.RawMessage, error)
	// SetContextPolicy persists a session's context policy (ADR-0049 §8) as
	// opaque JSON, validating that it decodes into a dto.ContextPolicy. An
	// invalid policy is ErrInvalidContextPolicy; an unknown session is
	// ErrNotFound.
	SetContextPolicy(sessionID string, policy json.RawMessage) error
	// ContextPolicy returns a session's persisted context policy JSON, or nil
	// when none. An unknown session is ErrNotFound.
	ContextPolicy(sessionID string) (json.RawMessage, error)
	// CompactHistory replaces the oldest turns with one labeled summary message
	// (ADR-0051 §8): it keeps the most recent keepRecentTurns turns (a turn
	// starts at a user message) and returns the summarized timestamp range and
	// the number of turns summarized. Pins and recent turns survive; the
	// replacement is a labeled record, never a silent drop.
	CompactHistory(sessionID, summary string, keepRecentTurns int) (fromTs, toTs int64, turns int, err error)
}

// Interface is an alias for SessionStore (the contracted name, interface.md §10).
type Interface = SessionStore

// ErrNotFound is returned when a look-up finds no matching row.
var ErrNotFound = errors.New("session not found")

// ErrInvalidContextPolicy is the typed refusal for a context policy that does
// not decode (ADR-0049 §8).
var ErrInvalidContextPolicy = errors.New("invalid-context-policy")

// store is the concrete Session store. It is a leaf: no out-edges (ADR-0026).
type store struct {
	db *sql.DB
}

// New returns a Session store over an already-migrated sessions.db.
func New(db *sql.DB) SessionStore {
	return &store{db: db}
}

// scanSession scans one sessions row. The SELECT column order is fixed:
// id, document_id, anchor_block_id, mode_type, title, token_budget,
// context_policy, created_at, updated_at.
func scanSession(row interface{ Scan(...any) error }) (dto.Session, error) {
	var sess dto.Session
	var rawPolicy string
	if err := row.Scan(
		&sess.ID, &sess.DocumentID, &sess.AnchorBlockID, &sess.ModeType, &sess.Title, &sess.TokenBudget,
		&rawPolicy, &sess.CreatedAt, &sess.UpdatedAt,
	); err != nil {
		return dto.Session{}, err
	}
	if rawPolicy != "" {
		sess.ContextPolicy = json.RawMessage(rawPolicy)
	}
	return sess, nil
}

// sessionColumns is the stable SELECT list shared by every session read.
const sessionColumns = `id, document_id, anchor_block_id, mode_type, title, token_budget, context_policy, created_at, updated_at`

// Migrate applies the sessions.db schema (exposed for the composition root).
func Migrate(ctx context.Context, db *sql.DB) error {
	return sqlmigrate.Migrate(ctx, db, sessionsSchema)
}

// Create is create-or-resume: a (document_id, anchor_block_id) pair maps to at
// most one session; re-anchoring to the same block reopens the same session
// (ADR-0026 §1/§2, interface.md §10).
func (s *store) Create(documentID string, anchorBlockID *string, modeType, title string) (dto.Session, error) {
	if existing, err := s.findByAnchor(documentID, anchorBlockID); err == nil {
		return existing, nil
	}

	now := time.Now().Unix()
	sess := dto.Session{
		ID:            uuid.NewString(),
		DocumentID:    documentID,
		AnchorBlockID: anchorBlockID,
		ModeType:      modeType,
		Title:         title,
		TokenBudget:   nil,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, document_id, anchor_block_id, mode_type, title, token_budget, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.DocumentID, sess.AnchorBlockID, sess.ModeType, sess.Title, sess.TokenBudget, sess.CreatedAt, sess.UpdatedAt,
	)
	if err != nil {
		return dto.Session{}, err
	}
	return sess, nil
}

// findByAnchor returns a session matching (document_id, anchor_block_id), or
// ErrNotFound. NULL anchor matching is handled so doc-level chats (nil anchor)
// and block-anchored chats are distinct.
func (s *store) findByAnchor(documentID string, anchorBlockID *string) (dto.Session, error) {
	var query string
	var args []interface{}
	if anchorBlockID == nil {
		query = `SELECT ` + sessionColumns + ` FROM sessions WHERE document_id = ? AND anchor_block_id IS NULL`
		args = []interface{}{documentID}
	} else {
		query = `SELECT ` + sessionColumns + ` FROM sessions WHERE document_id = ? AND anchor_block_id = ?`
		args = []interface{}{documentID, *anchorBlockID}
	}
	sess, err := scanSession(s.db.QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return dto.Session{}, ErrNotFound
	}
	if err != nil {
		return dto.Session{}, err
	}
	return sess, nil
}

// Resume returns a session by id (find-or-open an anchored session).
func (s *store) Resume(id string) (dto.Session, error) {
	sess, err := scanSession(s.db.QueryRow(
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return dto.Session{}, ErrNotFound
	}
	if err != nil {
		return dto.Session{}, err
	}
	return sess, nil
}

// Rename sets a session's human title. It bumps updated_at so a rename is
// reflected in the workspace's newest-first session listing. An unknown session
// is ErrNotFound.
func (s *store) Rename(id, title string) error {
	res, err := s.db.Exec(
		`UPDATE sessions SET title = ?, updated_at = ? WHERE id = ?`,
		title, time.Now().Unix(), id,
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListByWorkspace returns every session in the shard, newest first.
func (s *store) ListByWorkspace() ([]dto.Session, error) {
	rows, err := s.db.Query(
		`SELECT ` + sessionColumns + ` FROM sessions ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dto.Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// ListByDocument returns every session sharing a document id, newest first.
func (s *store) ListByDocument(documentID string) ([]dto.Session, error) {
	rows, err := s.db.Query(
		`SELECT `+sessionColumns+` FROM sessions WHERE document_id = ? ORDER BY updated_at DESC`, documentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dto.Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Append appends one message to a session's history and bumps updated_at.
func (s *store) Append(sessionID string, msg dto.Message) error {
	ts := msg.Timestamp
	if ts == 0 {
		ts = time.Now().Unix()
	}
	if _, err := s.db.Exec(
		`INSERT INTO messages (session_id, role, content, ts) VALUES (?, ?, ?, ?)`,
		sessionID, msg.Role, msg.Content, ts,
	); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, time.Now().Unix(), sessionID)
	return err
}

// History returns a session's messages in insertion order.
func (s *store) History(sessionID string) ([]dto.Message, error) {
	rows, err := s.db.Query(
		`SELECT role, content, ts FROM messages WHERE session_id = ? ORDER BY id ASC`, sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dto.Message
	for rows.Next() {
		var m dto.Message
		if err := rows.Scan(&m.Role, &m.Content, &m.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// retentionPerSession bounds persisted turn-context snapshots per session
// (ADR-0044 §4: snapshots grow with usage; retention is required).
const retentionPerSession = 100

// SaveContext persists one turn's context snapshot (opaque JSON) and prunes the
// session's snapshots to the newest retentionPerSession rows. Insert + prune run
// in one transaction so the bound always holds.
func (s *store) SaveContext(turnID, sessionID string, snapshot json.RawMessage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO turn_context (turn_id, session_id, snapshot, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(turn_id) DO UPDATE SET session_id = excluded.session_id, snapshot = excluded.snapshot, created_at = excluded.created_at`,
		turnID, sessionID, string(snapshot), time.Now().Unix(),
	); err != nil {
		return err
	}
	// Keep only the newest N rows per session; rowid breaks same-second ties.
	if _, err := tx.Exec(
		`DELETE FROM turn_context WHERE session_id = ? AND turn_id NOT IN (
			SELECT turn_id FROM turn_context WHERE session_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?
		)`,
		sessionID, sessionID, retentionPerSession,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// TurnContext returns a persisted turn's context snapshot, or ErrNotFound.
func (s *store) TurnContext(turnID string) (json.RawMessage, error) {
	var snap string
	err := s.db.QueryRow(`SELECT snapshot FROM turn_context WHERE turn_id = ?`, turnID).Scan(&snap)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(snap), nil
}

// SetContextPolicy persists a session's context policy (ADR-0049 §8). The JSON
// is validated against dto.ContextPolicy; an unknown session is ErrNotFound.
func (s *store) SetContextPolicy(sessionID string, policy json.RawMessage) error {
	if len(policy) > 0 {
		var probe dto.ContextPolicy
		if err := json.Unmarshal(policy, &probe); err != nil {
			return ErrInvalidContextPolicy
		}
	}
	res, err := s.db.Exec(
		`UPDATE sessions SET context_policy = ?, updated_at = ? WHERE id = ?`,
		string(policy), time.Now().Unix(), sessionID,
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// ContextPolicy returns a session's persisted context policy JSON, or nil when
// none. An unknown session is ErrNotFound.
func (s *store) ContextPolicy(sessionID string) (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRow(`SELECT context_policy FROM sessions WHERE id = ?`, sessionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	return json.RawMessage(raw), nil
}

// CompactHistory replaces the oldest turns with one labeled summary message
// (ADR-0051 §8). A turn starts at a user message; the most recent
// keepRecentTurns turns are preserved. The summary is inserted as a labeled
// user message so the model sees it in order; the summarized range is returned
// for the snapshot. A no-op (nothing to summarize) returns zero values.
func (s *store) CompactHistory(sessionID, summary string, keepRecentTurns int) (fromTs, toTs int64, turns int, err error) {
	rows, err := s.db.Query(`SELECT id, role, ts FROM messages WHERE session_id = ? ORDER BY id ASC`, sessionID)
	if err != nil {
		return 0, 0, 0, err
	}
	type msgRow struct {
		id   int64
		role string
		ts   int64
	}
	var all []msgRow
	for rows.Next() {
		var m msgRow
		if err := rows.Scan(&m.id, &m.role, &m.ts); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		all = append(all, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}

	// Find the start index of the last keepRecentTurns turns (user-message
	// boundaries). keepRecentTurns <= 0 summarizes everything.
	starts := []int{}
	for i, m := range all {
		if m.role == "user" {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return 0, 0, 0, nil
	}
	keep := keepRecentTurns
	if keep < 0 {
		keep = 0
	}
	cutTurn := len(starts) - keep
	if cutTurn <= 0 {
		return 0, 0, 0, nil // nothing older than the kept window
	}
	boundary := starts[cutTurn] // first message of the oldest kept turn
	if boundary == 0 {
		return 0, 0, 0, nil
	}
	summarized := all[:boundary]
	fromTs = summarized[0].ts
	toTs = summarized[len(summarized)-1].ts
	for _, m := range summarized {
		if m.role == "user" {
			turns++
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ? AND id < ?`, sessionID, all[boundary].id); err != nil {
		return 0, 0, 0, err
	}
	// Insert the summary with the lowest freed id so History (ORDER BY id ASC)
	// yields the summary first, followed by the surviving recent turns.
	now := time.Now().Unix()
	if _, err := tx.Exec(`INSERT INTO messages (id, session_id, role, content, ts) VALUES (?, ?, 'user', ?, ?)`,
		all[0].id, sessionID, "[Summary of earlier conversation]\n"+summary, fromTs); err != nil {
		return 0, 0, 0, err
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, now, sessionID); err != nil {
		return 0, 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return fromTs, toTs, turns, nil
}
