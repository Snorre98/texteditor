// Package retriever holds the Retriever — hybrid (semantic + lexical) retrieval
// over a document's chunks, backed by index.db (sqlite-vec vec0 + FTS5).
//
// The Retriever is NOT a leaf (ADR-0016 §8): it depends on Fleet (to resolve the
// embedding model) and Provider (to embed), and on the Chunker (to produce
// chunks) plus a block source (to read a document's block tree). At the A2 layer
// the Fleet/Provider dependencies are sealed local seams, stubbed in tests and
// wired to the real Fleet + Provider gateways at the composition root (A3).
package retriever

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"texteditor/internal/chunker"
	"texteditor/internal/sqlmigrate"
	"texteditor/shared/dto"
)

// Retriever is the Retriever public API (interface.md §3).
type Retriever interface {
	Query(ctx context.Context, text string, topK int) ([]dto.Chunk, error)
	Index(ctx context.Context, documentID string) error
}

// Interface is an alias for Retriever (the contracted name, interface.md §3).
type Interface = Retriever

// Resolver is the sealed subset of the Fleet gateway the Retriever needs: it
// resolves the embedding model to a served Target (interface.md §1). Wired to the
// real Fleet gateway at the composition root (A3).
type Resolver interface {
	Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error)
}

// Embedder is the sealed subset of the Provider gateway the Retriever needs:
// embedding a text into a float vector (interface.md §2). Wired to the real
// Provider gateway at the composition root (A3).
type Embedder interface {
	Embed(ctx context.Context, target dto.Target, text string) ([]float32, error)
}

// BlockReader is the sealed document-block source: given a document id, returns
// the block tree the Retriever chunks. Satisfied by the Document store. (Judgment
// call: interface.md §3 fixes Index(ctx, documentID); the Retriever must obtain
// the tree to chunk, so it takes a narrow block-source seam at construction.)
type BlockReader interface {
	Blocks(documentID string) ([]dto.Block, error)
}

// EmbedModelName is the embedding model the Retriever resolves via Fleet
// (interface.md §3).
const EmbedModelName = "nomic-embed"

// retriever is the concrete Retriever. index.db is its single-writer file.
type retriever struct {
	db          *sql.DB
	resolver    Resolver
	embedder    Embedder
	blocks      BlockReader
	chunk       chunker.Interface
	chunkTokens int

	mu      sync.Mutex
	dim     int  // learned/cached embedding dimension
	schemaN bool // schema migrated at least once
}

// New returns a Retriever over an index.db. chunkTokens is the RAG token lever
// (ADR-0020 §5): the per-chunk size bound handed to the Chunker.
func New(db *sql.DB, resolver Resolver, embedder Embedder, blocks BlockReader, ch chunker.Interface, chunkTokens int) Retriever {
	return &retriever{
		db:          db,
		resolver:    resolver,
		embedder:    embedder,
		blocks:      blocks,
		chunk:       ch,
		chunkTokens: chunkTokens,
	}
}

// embedTarget resolves the embedding model and embeds text into a vector.
func (r *retriever) embedTarget(ctx context.Context, name, text string) ([]float32, error) {
	res, err := r.resolver.Resolve(name, dto.ResolveOpts{ModeTag: name})
	if err != nil {
		return nil, err
	}
	target := dto.Target{BaseURL: res.Model.BaseURL, Capabilities: res.Model.Capabilities}
	return r.embedder.Embed(ctx, target, text)
}

// ensureSchema migrates index.db once the embedding dimension is known. The vec0
// table's `embedding float[dim]` needs the dim at CREATE time (schema.go), so it
// is derived from the embedding model's output at Index time (first embed).
func (r *retriever) ensureSchema(ctx context.Context, dim int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.schemaN && r.dim == dim {
		return nil
	}
	if err := sqlmigrate.Migrate(ctx, r.db, indexSchema(dim)); err != nil {
		return err
	}
	// Re-migrating with a different dim is unsupported: the dim is fixed by the
	// embedding model. Guard against a mid-flight change.
	if r.schemaN && r.dim != dim {
		return fmt.Errorf("embedding dimension changed (%d -> %d)", r.dim, dim)
	}
	r.dim = dim
	r.schemaN = true
	return nil
}

// Index is the write side (interface.md §3): chunk the document and write index.db
// (vec0 + FTS5). It derives the embedding dimension from the resolved embed model.
func (r *retriever) Index(ctx context.Context, documentID string) error {
	tree, err := r.blocks.Blocks(documentID)
	if err != nil {
		return err
	}
	chunks, err := r.chunk.Chunk(tree, r.chunkTokens)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}

	// Embed each chunk (resolve nomic-embed via Fleet once, reuse the target).
	res, err := r.resolver.Resolve(EmbedModelName, dto.ResolveOpts{ModeTag: EmbedModelName})
	if err != nil {
		return err
	}
	target := dto.Target{BaseURL: res.Model.BaseURL, Capabilities: res.Model.Capabilities}

	type chunkRow struct {
		vec  []float32
		text string
		id   string
	}
	rows := make([]chunkRow, 0, len(chunks))
	for _, c := range chunks {
		v, err := r.embedder.Embed(ctx, target, c.Text)
		if err != nil {
			return err
		}
		rows = append(rows, chunkRow{vec: v, text: c.Text, id: c.BlockID})
	}

	if err := r.ensureSchema(ctx, len(rows[0].vec)); err != nil {
		return err
	}

	// Rebuild: delete this document's rows, then insert. index.db is a derived,
	// rebuildable projection (data-model.md §1.2).
	if _, err := r.db.ExecContext(ctx, `DELETE FROM vec_chunks WHERE document_id = ?`, documentID); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM blocks_ft WHERE block_id IN (SELECT block_id FROM vec_chunks WHERE document_id = ?)`, documentID); err != nil {
		return err
	}

	for i, row := range rows {
		if _, err := r.db.ExecContext(ctx,
			`INSERT INTO vec_chunks (id, document_id, block_id, embedding) VALUES (?, ?, ?, ?)`,
			i+1, documentID, row.id, encodeVec(row.vec),
		); err != nil {
			return err
		}
		if _, err := r.db.ExecContext(ctx,
			`INSERT INTO blocks_ft (block_id, content) VALUES (?, ?)`,
			row.id, row.text,
		); err != nil {
			return err
		}
	}
	return nil
}

// Query takes raw text, embeds it, runs vec0 KNN (semantic) plus FTS5 (lexical),
// and returns ranked chunks (interface.md §3). Zero chunks is not an error
// (failure-semantics §3).
func (r *retriever) Query(ctx context.Context, text string, topK int) ([]dto.Chunk, error) {
	if topK <= 0 {
		return nil, nil
	}
	v, err := r.embedTarget(ctx, EmbedModelName, text)
	if err != nil {
		return nil, err
	}

	// Semantic: KNN over vec_chunks.
	vec := encodeVec(v)
	semRows, err := r.db.QueryContext(ctx,
		`SELECT block_id, distance FROM vec_chunks WHERE embedding MATCH ? AND k = ? ORDER BY distance`,
		vec, topK,
	)
	if err != nil {
		return nil, err
	}
	type hit struct {
		blockID string
		score   float32
	}
	order := []string{}
	scores := map[string]float32{}
	for semRows.Next() {
		var blockID string
		var dist float64
		if err := semRows.Scan(&blockID, &dist); err != nil {
			semRows.Close()
			return nil, err
		}
		order = append(order, blockID)
		scores[blockID] = 1.0 / (1.0 + float32(dist))
	}
	semRows.Close()

	// Read chunk text from blocks_ft; build results in KNN order.
	var out []dto.Chunk
	for _, blockID := range order {
		var content string
		err := r.db.QueryRowContext(ctx,
			`SELECT content FROM blocks_ft WHERE block_id = ?`, blockID,
		).Scan(&content)
		if err != nil {
			continue // chunk not in FTS (shouldn't happen); skip
		}
		out = append(out, dto.Chunk{
			BlockID: blockID,
			Text:    content,
			Score:   scores[blockID],
			Source:  blockID,
		})
	}
	return out, nil
}

// encodeVec serializes a float vector to the "[a, b, c]" string literal form the
// vec0 `MATCH` predicate and embedding column expect (schema_test.go).
func encodeVec(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = fmt.Sprintf("%g", f)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
