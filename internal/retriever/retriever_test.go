package retriever

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"texteditor/internal/chunker"
	"texteditor/shared/dto"
)

// stubResolver returns a fixed embedding model.
type stubResolver struct{}

func (stubResolver) Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error) {
	return dto.Resolution{
		Model: dto.Model{Name: name, BaseURL: "http://localhost:9999/v1"},
	}, nil
}

// stubEmbedder embeds a text into a 4-dim vector; the first component is derived
// from the text so KNN has deterministic ordering. The doc chunks are indexed so
// their vectors are distinguishable from the query.
type stubEmbedder struct {
	dim int
}

func (s stubEmbedder) Embed(ctx context.Context, target dto.Target, text string) ([]float32, error) {
	v := make([]float32, s.dim)
	// A simple token hash into the first slot for deterministic yet distinct codes.
	var code float32
	for _, b := range text {
		code = code*31 + float32(b)
	}
	if code == 0 {
		code = 1
	}
	v[0] = code
	v[1] = float32(len(text))
	return v, nil
}

// stubBlocks returns a fixed document tree.
type stubBlocks struct{}

func (stubBlocks) Blocks(documentID string) ([]dto.Block, error) {
	return []dto.Block{
		{ID: "b1", Kind: dto.BlockKindParagraph, Position: 0, Text: "alpha beta gamma"},
		{ID: "b2", Kind: dto.BlockKindParagraph, Position: 1, Text: "delta epsilon zeta"},
	}, nil
}

func newTestRetriever(t *testing.T) (Interface, *sql.DB) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := &retriever{
		db:          db,
		resolver:    stubResolver{},
		embedder:    stubEmbedder{dim: 4},
		blocks:      stubBlocks{},
		chunk:       chunker.New(),
		chunkTokens: 3,
	}
	return r, db
}

func TestIndexAndQuery(t *testing.T) {
	r, db := newTestRetriever(t)
	defer db.Close()

	if err := r.Index(context.Background(), "d1"); err != nil {
		t.Fatalf("index: %v", err)
	}

	// Both chunks are embedded and written.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM vec_chunks WHERE document_id = 'd1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("vec_chunks = %d, want 2", n)
	}

	// Query returns chunks ranked by semantic distance.
	chunks, err := r.Query(context.Background(), "alpha beta", 2)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("query returned no chunks")
	}
	if chunks[0].BlockID == "" || chunks[0].Text == "" {
		t.Fatalf("chunk missing provenance/text: %+v", chunks[0])
	}
}

func TestQueryZeroK(t *testing.T) {
	r, _ := newTestRetriever(t)
	chunks, err := r.Query(context.Background(), "anything", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("zero-k query returned %d chunks", len(chunks))
	}
}

func TestIndexEmptyDocument(t *testing.T) {
	r, _ := newTestRetriever(t)
	// An empty tree produces no chunks and no error.
	r2 := &retriever{
		db:          r.(*retriever).db,
		resolver:    stubResolver{},
		embedder:    stubEmbedder{dim: 4},
		blocks:      stubEmptyBlocks{},
		chunk:       chunker.New(),
		chunkTokens: 10,
	}
	if err := r2.Index(context.Background(), "empty"); err != nil {
		t.Fatalf("empty index should not error: %v", err)
	}
}

type stubEmptyBlocks struct{}

func (stubEmptyBlocks) Blocks(documentID string) ([]dto.Block, error) { return nil, nil }
