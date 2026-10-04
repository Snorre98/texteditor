package retriever

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"texteditor/internal/sqlmigrate"
)

func TestIndexSchemaMigrates(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	if err := sqlmigrate.Migrate(context.Background(), db, indexSchema(4)); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// FTS5 present.
	if _, err := db.Exec(`SELECT count(*) FROM blocks_ft`); err != nil {
		t.Fatalf("blocks_ft query: %v", err)
	}

	// vec0 KNN answers; chunk keys are unique (no explicit colliding ids).
	for _, key := range []string{"d1#0", "d1#1"} {
		if _, err := db.Exec(
			`INSERT INTO vec_chunks (chunk_key, embedding) VALUES (?, ?)`,
			key, "[1.0, 0.0, 0.0, 0.0]",
		); err != nil {
			t.Fatalf("vec insert: %v", err)
		}
	}

	rows, err := db.Query(
		`SELECT chunk_key, distance FROM vec_chunks WHERE embedding MATCH ? AND k = 1 ORDER BY distance`,
		"[1.0, 0.0, 0.0, 0.0]",
	)
	if err != nil {
		t.Fatalf("knn: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("knn returned no rows")
	}
	var key string
	var dist float64
	if err := rows.Scan(&key, &dist); err != nil {
		t.Fatal(err)
	}
	if dist != 0 {
		t.Fatalf("unexpected distance: key=%s dist=%f", key, dist)
	}
}

// TestChunksProvenanceColumns: the chunks table carries the provenance needed
// by the rag event and the corpus status surface.
func TestChunksProvenanceColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := sqlmigrate.Migrate(context.Background(), db, indexSchema(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO chunks (chunk_key, file_key, path_key, path, heading, block_id, content)
		 VALUES ('/vault/a.md#0', '/vault/a.md', '/vault/a.md', '/vault/a.md', 'Intro > A', '', 'alpha')`,
	); err != nil {
		t.Fatal(err)
	}
	var heading, path string
	if err := db.QueryRow(`SELECT heading, path FROM chunks WHERE chunk_key = '/vault/a.md#0'`).Scan(&heading, &path); err != nil {
		t.Fatal(err)
	}
	if heading != "Intro > A" || path != "/vault/a.md" {
		t.Fatalf("provenance = %q %q", heading, path)
	}
}
