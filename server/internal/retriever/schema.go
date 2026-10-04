package retriever

import "fmt"

// indexSchema returns the migration list for a workspace shard's index.db,
// owned exclusively by the Retriever (single-writer, ADR-0016; data-model.md
// §1.2; per-workspace shards per ADR-0049 §5).
//
// index.db is a derived, rebuildable projection of documents and corpus files:
// the Chunker produces chunks and Index/IndexPath rebuilds it. It may
// denormalize chunk text. Identity:
//
//   - file_key keys indexed_files: a documentID for versioned documents, the
//     case-folded canonical path for path-keyed corpus files (ADR-0049 §4).
//   - chunk_key keys chunks/blocks_ft/vec_chunks: documentID#index for
//     versioned documents, path#index for corpus files (ADR-0049 §4).
//
// The vec0 embedding dimension is a property of the embedding model (e.g. 768
// for nomic-embed-text); it is learned on the first embed and appended as the
// final migration statement, recorded in index_meta. A dimension of 0 omits the
// vec statement, so a fresh shard can migrate its base schema (status/evict)
// before any embedding has happened.
func indexSchema(embedDim int) []string {
	stmts := []string{
		// index_meta — learned properties (embedding dimension), so a restart
		// validates the model against the existing vec table.
		`CREATE TABLE index_meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		// indexed_files — one status row per indexed file (per-document status:
		// indexed/stale detection, chunk count; ADR-0049 §4).
		`CREATE TABLE indexed_files (
			file_key     TEXT PRIMARY KEY,
			path_key     TEXT NOT NULL,
			path         TEXT NOT NULL,
			document_id  TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL,
			chunk_count  INTEGER NOT NULL,
			indexed_at   INTEGER NOT NULL
		)`,
		`CREATE INDEX indexed_files_path_idx ON indexed_files(path_key)`,
		// chunks — the chunk corpus (text + provenance). file_key is the
		// owning identity: documentID for versioned documents, the case-folded
		// canonical path for corpus files (ADR-0049 §4).
		`CREATE TABLE chunks (
			chunk_key TEXT PRIMARY KEY,
			file_key  TEXT NOT NULL,
			path_key  TEXT NOT NULL,
			path      TEXT NOT NULL,
			heading   TEXT NOT NULL DEFAULT '',
			block_id  TEXT NOT NULL DEFAULT '',
			content   TEXT NOT NULL
		)`,
		`CREATE INDEX chunks_file_idx ON chunks(file_key)`,
		`CREATE INDEX chunks_path_idx ON chunks(path_key)`,
		// blocks_ft — FTS5 full-text index (lexical retrieval, bm25).
		`CREATE VIRTUAL TABLE blocks_ft USING fts5(chunk_key UNINDEXED, content)`,
	}
	if embedDim > 0 {
		// vec_chunks — embeddings (sqlite-vec vec0 table, KNN-indexed). No
		// explicit id: vec0 assigns rowids, so per-document index counters
		// cannot collide across files (fixes the Phase C id-collision bug).
		stmts = append(stmts, fmt.Sprintf(`CREATE VIRTUAL TABLE vec_chunks USING vec0(
			chunk_key TEXT,
			embedding float[%d]
		)`, embedDim))
	}
	return stmts
}
