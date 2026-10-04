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
	// meter_measurements — one per-turn measurement row (ADR-0051 §11): prompt/
	// thinking/completion tokens, wall-clock latency, model, and window
	// utilization, so the hardware map accumulates per model/quant. Append-only;
	// new migrations go at the end.
	`CREATE TABLE meter_measurements (
		turn_id            TEXT PRIMARY KEY,
		session_id         TEXT NOT NULL,
		model              TEXT NOT NULL,
		prompt_tokens      INTEGER NOT NULL,
		thinking_tokens    INTEGER NOT NULL,
		completion_tokens  INTEGER NOT NULL,
		latency_ms         INTEGER NOT NULL,
		window_utilization REAL NOT NULL,
		ts                 INTEGER NOT NULL
	)`,
	`CREATE INDEX meter_measurements_session_idx ON meter_measurements(session_id)`,
}
