package meter

// meterSchema is the migration list for meter.db, owned exclusively by the Token
// metering module (single-writer, ADR-0016; data-model.md §1.3).
//
// meter_events is append-only. Each row is attributable to exactly one component;
// a component with approx=1 is a labeled approximation, never silent (ADR-0024).
var meterSchema = []string{
	`CREATE TABLE meter_events (
		id                INTEGER PRIMARY KEY,
		ts                INTEGER NOT NULL,
		session_id        TEXT NOT NULL,
		turn_id           TEXT NOT NULL,
		component         TEXT NOT NULL,
		prompt_tokens     INTEGER NOT NULL,
		completion_tokens INTEGER NOT NULL,
		approx            INTEGER NOT NULL DEFAULT 0,
		model             TEXT NOT NULL
	)`,
	`CREATE INDEX meter_turn_idx ON meter_events(turn_id)`,
	`CREATE INDEX meter_session_idx ON meter_events(session_id)`,
}
