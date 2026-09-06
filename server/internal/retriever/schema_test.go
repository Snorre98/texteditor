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
	defer db.Close()

	if err := sqlmigrate.Migrate(context.Background(), db, indexSchema(4)); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// FTS5 present.
	if _, err := db.Exec(`SELECT count(*) FROM blocks_ft`); err != nil {
		t.Fatalf("blocks_ft query: %v", err)
	}

	// vec0 KNN answers.
	if _, err := db.Exec(
		`INSERT INTO vec_chunks (id, document_id, block_id, embedding) VALUES (1, 'd1', 'b1', '[1.0, 0.0, 0.0, 0.0]')`,
	); err != nil {
		t.Fatalf("vec insert: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO vec_chunks (id, document_id, block_id, embedding) VALUES (2, 'd1', 'b2', '[0.0, 1.0, 0.0, 0.0]')`,
	); err != nil {
		t.Fatalf("vec insert: %v", err)
	}

	rows, err := db.Query(
		`SELECT id, block_id, distance FROM vec_chunks WHERE embedding MATCH ? AND k = 1 ORDER BY distance`,
		"[1.0, 0.0, 0.0, 0.0]",
	)
	if err != nil {
		t.Fatalf("knn: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("knn returned no rows")
	}
	var id int
	var blockID string
	var dist float64
	if err := rows.Scan(&id, &blockID, &dist); err != nil {
		t.Fatal(err)
	}
	if id != 1 || blockID != "b1" || dist != 0 {
		t.Fatalf("unexpected nearest: id=%d block=%s dist=%f", id, blockID, dist)
	}
}
