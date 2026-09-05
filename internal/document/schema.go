package document

// appSchema is the migration list for app.db, owned exclusively by the Document
// store (single-writer, ADR-0016; data-model.md §1.1).
//
// Block identity (ADR-0020 §3): stable UUIDs, minted at creation. Content hashes
// are used only as transient guard anchors, never as identity. The canonical
// block text lives in the git worktree file (ADR-0020 §2), so the blocks table
// stores structure only — no text column.
var appSchema = []string{
	// documents — document metadata
	`CREATE TABLE documents (
		id            TEXT PRIMARY KEY,
		path          TEXT NOT NULL UNIQUE,
		root_block_id TEXT NOT NULL,
		updated_at    INTEGER NOT NULL
	)`,
	// blocks — stable block IDs (paragraphs/headings/tables, UUID)
	`CREATE TABLE blocks (
		id          TEXT PRIMARY KEY,
		document_id TEXT NOT NULL REFERENCES documents(id),
		parent_id   TEXT,
		kind        TEXT NOT NULL,
		position    INTEGER NOT NULL
	)`,
	`CREATE INDEX blocks_document_idx ON blocks(document_id)`,
	// candidates — block rewrites (unaccepted AI edits, ADR-0020 §4)
	`CREATE TABLE candidates (
		id       INTEGER PRIMARY KEY,
		block_id TEXT NOT NULL REFERENCES blocks(id),
		base_rev TEXT NOT NULL,
		text     TEXT NOT NULL,
		mode     TEXT NOT NULL,
		ts       INTEGER NOT NULL
	)`,
	`CREATE INDEX candidates_block_idx ON candidates(block_id)`,
}
