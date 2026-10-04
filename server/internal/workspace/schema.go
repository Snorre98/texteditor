package workspace

// workspacesSchema is the migration list for workspaces.db, owned exclusively by
// the Workspace store (single-writer, ADR-0016/0049 §5).
//
// workspaces.db is the GLOBAL registry: workspace records, corpus roots/scope,
// index-job progress, eviction tombstones, and the turn/session routing index
// that lets the API resolve a shard by turn/session id. It never stores
// document identity (that stays in app.db) or per-workspace context state
// (that lives in the workspace shard).
var workspacesSchema = []string{
	`CREATE TABLE workspaces (
		id         TEXT PRIMARY KEY,
		root       TEXT NOT NULL,
		root_key   TEXT NOT NULL UNIQUE,
		name       TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE TABLE corpus_roots (
		workspace_id TEXT NOT NULL REFERENCES workspaces(id),
		path         TEXT NOT NULL,
		path_key     TEXT NOT NULL,
		position     INTEGER NOT NULL,
		PRIMARY KEY (workspace_id, path_key)
	)`,
	`CREATE TABLE corpus_scope (
		workspace_id TEXT NOT NULL REFERENCES workspaces(id),
		kind         TEXT NOT NULL,
		pattern      TEXT NOT NULL,
		PRIMARY KEY (workspace_id, kind, pattern)
	)`,
	`CREATE TABLE corpus_jobs (
		id           TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		kind         TEXT NOT NULL,
		state        TEXT NOT NULL,
		total        INTEGER NOT NULL DEFAULT 0,
		completed    INTEGER NOT NULL DEFAULT 0,
		error        TEXT NOT NULL DEFAULT '',
		started_at   INTEGER NOT NULL,
		finished_at  INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX corpus_jobs_workspace_idx ON corpus_jobs(workspace_id, started_at DESC)`,
	`CREATE TABLE corpus_evicted (
		workspace_id TEXT NOT NULL,
		path_key     TEXT NOT NULL,
		path         TEXT NOT NULL,
		evicted_at   INTEGER NOT NULL,
		PRIMARY KEY (workspace_id, path_key)
	)`,
	`CREATE TABLE corpus_errors (
		workspace_id TEXT NOT NULL,
		path_key     TEXT NOT NULL,
		path         TEXT NOT NULL,
		message      TEXT NOT NULL,
		ts           INTEGER NOT NULL,
		PRIMARY KEY (workspace_id, path_key)
	)`,
	// Routing index: no cross-workspace lookup exists otherwise (ADR-0049 §5:
	// the registry is the only global index). Rows are pruned with retention.
	`CREATE TABLE session_routes (
		session_id   TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		created_at   INTEGER NOT NULL
	)`,
	`CREATE TABLE turn_routes (
		turn_id      TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		session_id   TEXT NOT NULL,
		created_at   INTEGER NOT NULL
	)`,
	`CREATE INDEX turn_routes_session_idx ON turn_routes(session_id)`,
}
