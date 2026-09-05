// Package session holds the Session store — a pure data leaf owning a dedicated
// sessions.db (ADR-0026 §2). It is single-writer over that file.
package session

import (
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"texteditor/shared/dto"
)

// SessionStore is the Session store public API (interface.md §10).
type SessionStore interface {
	ListByDocument(documentID string) ([]dto.Session, error)
	Create(documentID string, anchorBlockID *string, modeType string) (dto.Session, error)
	Resume(id string) (dto.Session, error)
	Append(sessionID string, msg dto.Message) error
	History(sessionID string) ([]dto.Message, error)
}

// Interface is an alias for SessionStore (the contracted name, interface.md §10).
type Interface = SessionStore

// ErrNotFound is returned when a look-up finds no matching row.
var ErrNotFound = errors.New("session not found")

// store is the concrete Session store. It is a leaf: no out-edges (ADR-0026).
type store struct {
	db *sql.DB
}

// New returns a Session store over an already-migrated sessions.db.
func New(db *sql.DB) SessionStore {
	return &store{db: db}
}

// Create is create-or-resume: a (document_id, anchor_block_id) pair maps to at
// most one session; re-anchoring to the same block reopens the same session
// (ADR-0026 §1/§2, interface.md §10).
func (s *store) Create(documentID string, anchorBlockID *string, modeType string) (dto.Session, error) {
	if existing, err := s.findByAnchor(documentID, anchorBlockID); err == nil {
		return existing, nil
	}

	now := time.Now().Unix()
	sess := dto.Session{
		ID:            uuid.NewString(),
		DocumentID:    documentID,
		AnchorBlockID: anchorBlockID,
		ModeType:      modeType,
		Title:         "",
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
	var sess dto.Session
	var query string
	var args []interface{}
	if anchorBlockID == nil {
		query = `SELECT id, document_id, anchor_block_id, mode_type, title, token_budget, created_at, updated_at
		         FROM sessions WHERE document_id = ? AND anchor_block_id IS NULL`
		args = []interface{}{documentID}
	} else {
		query = `SELECT id, document_id, anchor_block_id, mode_type, title, token_budget, created_at, updated_at
		         FROM sessions WHERE document_id = ? AND anchor_block_id = ?`
		args = []interface{}{documentID, *anchorBlockID}
	}
	err := s.db.QueryRow(query, args...).Scan(
		&sess.ID, &sess.DocumentID, &sess.AnchorBlockID, &sess.ModeType, &sess.Title, &sess.TokenBudget, &sess.CreatedAt, &sess.UpdatedAt,
	)
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
	var sess dto.Session
	err := s.db.QueryRow(
		`SELECT id, document_id, anchor_block_id, mode_type, title, token_budget, created_at, updated_at
		 FROM sessions WHERE id = ?`, id,
	).Scan(
		&sess.ID, &sess.DocumentID, &sess.AnchorBlockID, &sess.ModeType, &sess.Title, &sess.TokenBudget, &sess.CreatedAt, &sess.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return dto.Session{}, ErrNotFound
	}
	if err != nil {
		return dto.Session{}, err
	}
	return sess, nil
}

// ListByDocument returns every session sharing a document id, newest first.
func (s *store) ListByDocument(documentID string) ([]dto.Session, error) {
	rows, err := s.db.Query(
		`SELECT id, document_id, anchor_block_id, mode_type, title, token_budget, created_at, updated_at
		 FROM sessions WHERE document_id = ? ORDER BY updated_at DESC`, documentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dto.Session
	for rows.Next() {
		var sess dto.Session
		if err := rows.Scan(
			&sess.ID, &sess.DocumentID, &sess.AnchorBlockID, &sess.ModeType, &sess.Title, &sess.TokenBudget, &sess.CreatedAt, &sess.UpdatedAt,
		); err != nil {
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
