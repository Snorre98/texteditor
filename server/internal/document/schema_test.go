package document

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"texteditor/internal/sqlmigrate"
)

func TestAppSchemaMigrates(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := sqlmigrate.Migrate(context.Background(), db, appSchema); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, table := range []string{"documents", "blocks", "candidates"} {
		var n int
		if err := db.QueryRow(
			"SELECT count(*) FROM sqlite_master WHERE type='table' AND name = ?", table,
		).Scan(&n); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if n != 1 {
			t.Fatalf("table %s missing", table)
		}
	}

	// A document + block + candidate round-trips.
	if _, err := db.Exec(
		`INSERT INTO documents (id, path, root_block_id, updated_at) VALUES ('d1', '/tmp/x.md', 'r1', 1)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO blocks (id, document_id, parent_id, kind, position) VALUES ('b1', 'd1', NULL, 'paragraph', 0)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO candidates (block_id, base_rev, text, mode, ts) VALUES ('b1', 'r1', 'text', 'proofreader', 1)`,
	); err != nil {
		t.Fatal(err)
	}
}
