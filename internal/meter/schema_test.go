package meter

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"texteditor/internal/sqlmigrate"
)

func TestMeterSchemaMigrates(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := sqlmigrate.Migrate(context.Background(), db, meterSchema); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO meter_events (ts, session_id, turn_id, component, prompt_tokens, completion_tokens, approx, model)
		 VALUES (1, 's1', 't1', 'thinking', 10, 20, 1, 'qwen')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var approx int
	if err := db.QueryRow(
		`SELECT approx FROM meter_events WHERE component = 'thinking'`,
	).Scan(&approx); err != nil {
		t.Fatal(err)
	}
	if approx != 1 {
		t.Fatalf("approx = %d, want 1", approx)
	}
}
