package retriever

import "fmt"

// indexSchema returns the migration list for index.db, owned exclusively by the
// Retriever (single-writer, ADR-0016; data-model.md §1.2).
//
// index.db is a derived, rebuildable projection of documents: the Chunker
// produces it and Index rebuilds it. It may denormalize block text.
//
// The vec0 embedding dimension is a property of the embedding model (e.g. 768 for
// nomic-embed-text); it is supplied at open time, not hardcoded in the schema.
func indexSchema(embedDim int) []string {
	return []string{
		// blocks_ft — FTS5 full-text index (lexical retrieval)
		`CREATE VIRTUAL TABLE blocks_ft USING fts5(block_id UNINDEXED, content)`,
		// vec_chunks — embeddings (sqlite-vec vec0 table, KNN-indexed)
		fmt.Sprintf(`CREATE VIRTUAL TABLE vec_chunks USING vec0(
			id          INTEGER PRIMARY KEY,
			document_id TEXT,
			block_id    TEXT,
			embedding   float[%d]
		)`, embedDim),
	}
}
