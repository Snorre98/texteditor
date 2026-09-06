package session

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"texteditor/internal/sqlmigrate"
)

func TestSessionsSchemaMigrates(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := sqlmigrate.Migrate(context.Background(), db, sessionsSchema); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO sessions (id, document_id, anchor_block_id, mode_type, title, token_budget, created_at, updated_at)
		 VALUES ('s1', 'd1', NULL, 'proofreader', 'title', NULL, 1, 1)`,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO messages (session_id, role, content, ts) VALUES ('s1', 'user', 'hello', 1)`,
	); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	var role string
	if err := db.QueryRow(`SELECT role FROM messages WHERE session_id = 's1'`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "user" {
		t.Fatalf("role = %q, want user", role)
	}
}
