package session

// sessionsSchema is the migration list for sessions.db, owned exclusively by the
// Session store (single-writer, ADR-0016/0026; data-model.md §1.4).
//
// A (document_id, anchor_block_id) pair maps to at most one session
// (create-or-resume); many sessions may share one document_id.
var sessionsSchema = []string{
	`CREATE TABLE sessions (
		id              TEXT PRIMARY KEY,
		document_id     TEXT NOT NULL,
		anchor_block_id TEXT,
		mode_type       TEXT NOT NULL,
		title           TEXT NOT NULL DEFAULT '',
		token_budget    INTEGER,
		created_at      INTEGER NOT NULL,
		updated_at      INTEGER NOT NULL
	)`,
	`CREATE INDEX sessions_document_idx ON sessions(document_id)`,
	// messages — conversation history (many per session)
	`CREATE TABLE messages (
		id         INTEGER PRIMARY KEY,
		session_id TEXT NOT NULL REFERENCES sessions(id),
		role       TEXT NOT NULL,
		content    TEXT NOT NULL,
		ts         INTEGER NOT NULL
	)`,
	`CREATE INDEX messages_session_idx ON messages(session_id)`,
}
