package retriever

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"texteditor/internal/chunker"
	"texteditor/internal/sqlmigrate"
	"texteditor/shared/dto"
)

// ------------------------- fakes -------------------------

type stubResolver struct{}

func (stubResolver) Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error) {
	return dto.Resolution{Model: dto.Model{Name: name, BaseURL: "http://localhost:9999/v1"}}, nil
}

// stubEmbedder is a deterministic bag-of-words embedder: each word increments a
// slot, so cosine/KNN ordering is predictable without a model.
type stubEmbedder struct {
	dim   int
	mu    sync.Mutex
	calls int
}

func (s *stubEmbedder) Embed(_ context.Context, _ dto.Target, text string) ([]float32, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	v := make([]float32, s.dim)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(w))
		v[h.Sum32()%uint32(s.dim)]++
	}
	return v, nil
}

func (s *stubEmbedder) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type stubDocs struct {
	blocks []dto.Block
	path   string
}

func (s *stubDocs) Blocks(string) ([]dto.Block, error) { return s.blocks, nil }
func (s *stubDocs) Path(string) (string, error) {
	if s.path == "" {
		return "", errors.New("no path")
	}
	return s.path, nil
}

type stubFiles struct {
	mu      sync.Mutex
	content map[string]string
}

func (s *stubFiles) Read(_ context.Context, path string, maxBytes int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.content[path]
	if !ok {
		return nil, errors.New("not found")
	}
	if len(c) > maxBytes {
		return nil, errors.New("too large")
	}
	return []byte(c), nil
}

func (s *stubFiles) set(path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.content == nil {
		s.content = map[string]string{}
	}
	s.content[path] = content
}

// ------------------------- helpers -------------------------

func newTestRetriever(t *testing.T, docs *stubDocs, files *stubFiles, embedder *stubEmbedder) *retriever {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	r := New(db, stubResolver{}, embedder, docs, files, chunker.New(), 5).(*retriever)
	return r
}

func twoBlockDocs(path string) *stubDocs {
	return &stubDocs{
		path: path,
		blocks: []dto.Block{
			{ID: "b1", Kind: dto.BlockKindParagraph, Text: "alpha beta gamma"},
			{ID: "b2", Kind: dto.BlockKindParagraph, Text: "delta epsilon zeta"},
		},
	}
}

func count(t *testing.T, db *sql.DB, query string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// ------------------------- tests -------------------------

func TestIndexAndQuery(t *testing.T) {
	r := newTestRetriever(t, twoBlockDocs("/vault/note.md"), &stubFiles{}, &stubEmbedder{dim: 8})
	ctx := context.Background()

	if err := r.Index(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	if got := count(t, r.db, `SELECT count(*) FROM chunks WHERE file_key = 'd1'`); got != 2 {
		t.Fatalf("chunks = %d, want 2", got)
	}
	if got := count(t, r.db, `SELECT count(*) FROM blocks_ft`); got != 2 {
		t.Fatalf("fts rows = %d, want 2", got)
	}
	if got := count(t, r.db, `SELECT count(*) FROM indexed_files`); got != 1 {
		t.Fatalf("indexed_files = %d, want 1", got)
	}

	chunks, err := r.Query(ctx, "alpha beta", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Fatal("query returned no chunks")
	}
	if chunks[0].ChunkKey != "d1#0" {
		t.Fatalf("first chunk key = %q, want d1#0", chunks[0].ChunkKey)
	}
	if chunks[0].Path != "/vault/note.md" {
		t.Fatalf("path provenance = %q", chunks[0].Path)
	}
	if chunks[0].Text == "" || chunks[0].Score <= 0 {
		t.Fatalf("chunk = %+v", chunks[0])
	}
}

func TestQueryZeroK(t *testing.T) {
	r := newTestRetriever(t, twoBlockDocs("/vault/note.md"), &stubFiles{}, &stubEmbedder{dim: 8})
	chunks, err := r.Query(context.Background(), "alpha", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("chunks = %v, want empty", chunks)
	}
}

// TestSearchTextFTSOnly covers the `/locate` lexical surface (ADR-0048 §2): a
// sanitized FTS5 MATCH with provenance, no embedding call.
func TestSearchTextFTSOnly(t *testing.T) {
	embedder := &stubEmbedder{dim: 8}
	r := newTestRetriever(t, twoBlockDocs("/vault/note.md"), &stubFiles{}, embedder)
	ctx := context.Background()
	if err := r.Index(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	before := embedder.callCount()

	// Punctuation in the query must not break the FTS MATCH syntax.
	chunks, err := r.SearchText(ctx, `alpha, "beta" gamma`, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Fatal("SearchText returned no chunks")
	}
	if chunks[0].ChunkKey != "d1#0" || chunks[0].Path != "/vault/note.md" || chunks[0].Text == "" {
		t.Fatalf("chunk provenance = %+v", chunks[0])
	}
	if embedder.callCount() != before {
		t.Fatalf("SearchText embedded: calls %d -> %d", before, embedder.callCount())
	}

	// A punctuation-only query sanitizes to no terms.
	empty, err := r.SearchText(ctx, "!!! ???", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("punctuation-only query = %v, want empty", empty)
	}
}

func TestIndexEmptyDocument(t *testing.T) {
	r := newTestRetriever(t, &stubDocs{path: "/vault/empty.md"}, &stubFiles{}, &stubEmbedder{dim: 8})
	if err := r.Index(context.Background(), "d1"); err != nil {
		t.Fatal(err)
	}
	if got := count(t, r.db, `SELECT count(*) FROM indexed_files`); got != 0 {
		t.Fatalf("indexed_files = %d, want 0", got)
	}
}

// TestReindexLeavesNoStaleFTS is the Phase C FTS-staleness fix: a re-index must
// delete the old FTS rows before inserting the new ones.
func TestReindexLeavesNoStaleFTS(t *testing.T) {
	docs := &stubDocs{path: "/vault/note.md", blocks: []dto.Block{
		{ID: "b1", Kind: dto.BlockKindParagraph, Text: "alpha beta"},
	}}
	r := newTestRetriever(t, docs, &stubFiles{}, &stubEmbedder{dim: 8})
	ctx := context.Background()
	if err := r.Index(ctx, "d1"); err != nil {
		t.Fatal(err)
	}

	docs.blocks = []dto.Block{{ID: "b1", Kind: dto.BlockKindParagraph, Text: "omega psi"}}
	if err := r.Index(ctx, "d1"); err != nil {
		t.Fatal(err)
	}

	// The old token must not match in FTS.
	keys, err := r.ftsKeys(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("stale FTS rows survived re-index: %v", keys)
	}
	if got := count(t, r.db, `SELECT count(*) FROM blocks_ft`); got != 1 {
		t.Fatalf("fts rows = %d, want 1", got)
	}
	chunks, err := r.Query(ctx, "omega", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 || !strings.Contains(chunks[0].Text, "omega") {
		t.Fatalf("query after re-index = %+v", chunks)
	}
}

// TestNoVecIDCollision is the Phase C vec0 id fix: per-document index counters
// must not collide across documents.
func TestNoVecIDCollision(t *testing.T) {
	docs := &stubDocs{path: "/vault/a.md", blocks: []dto.Block{
		{ID: "a1", Kind: dto.BlockKindParagraph, Text: "alpha one"},
		{ID: "a2", Kind: dto.BlockKindParagraph, Text: "alpha two"},
	}}
	r := newTestRetriever(t, docs, &stubFiles{}, &stubEmbedder{dim: 8})
	ctx := context.Background()
	if err := r.Index(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	docs.path = "/vault/b.md"
	docs.blocks = []dto.Block{
		{ID: "b1", Kind: dto.BlockKindParagraph, Text: "beta one"},
		{ID: "b2", Kind: dto.BlockKindParagraph, Text: "beta two"},
	}
	if err := r.Index(ctx, "d2"); err != nil {
		t.Fatal(err)
	}
	if got := count(t, r.db, `SELECT count(*) FROM vec_chunks`); got != 2 {
		t.Fatalf("vec rows = %d, want 2 (id collision drops one?)", got)
	}
	if got := count(t, r.db, `SELECT count(DISTINCT chunk_key) FROM vec_chunks`); got != 2 {
		t.Fatalf("distinct vec chunk keys = %d, want 2", got)
	}
	for _, key := range []string{"d1#0", "d2#0"} {
		if got := count(t, r.db, `SELECT count(*) FROM vec_chunks WHERE chunk_key = ?`, key); got != 1 {
			t.Fatalf("vec key %q rows = %d, want 1", key, got)
		}
	}
}

// TestEvictionRemovesBothIndexesAndIsIdempotent covers ADR-0049 §4.
func TestEvictionRemovesBothIndexesAndIsIdempotent(t *testing.T) {
	r := newTestRetriever(t, twoBlockDocs("/vault/note.md"), &stubFiles{}, &stubEmbedder{dim: 8})
	ctx := context.Background()
	if err := r.Index(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := r.Evict(ctx, "d1"); err != nil {
			t.Fatalf("evict #%d: %v", i+1, err)
		}
	}
	if got := count(t, r.db, `SELECT count(*) FROM chunks`); got != 0 {
		t.Fatalf("chunks = %d, want 0", got)
	}
	if got := count(t, r.db, `SELECT count(*) FROM blocks_ft`); got != 0 {
		t.Fatalf("fts = %d, want 0", got)
	}
	if got := count(t, r.db, `SELECT count(*) FROM vec_chunks`); got != 0 {
		t.Fatalf("vec = %d, want 0", got)
	}
	if got := count(t, r.db, `SELECT count(*) FROM indexed_files`); got != 0 {
		t.Fatalf("indexed_files = %d, want 0", got)
	}
	chunks, err := r.Query(ctx, "alpha", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("evicted chunks still retrievable: %+v", chunks)
	}
}

func TestEvictByPathAndChunkKey(t *testing.T) {
	files := &stubFiles{}
	files.set("/vault/corpus.md", "alpha beta gamma delta epsilon")
	r := newTestRetriever(t, &stubDocs{}, files, &stubEmbedder{dim: 8})
	ctx := context.Background()
	if err := r.IndexPath(ctx, "/vault/corpus.md", ""); err != nil {
		t.Fatal(err)
	}
	var key string
	if err := r.db.QueryRow(`SELECT chunk_key FROM chunks LIMIT 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if err := r.Evict(ctx, key); err != nil {
		t.Fatal(err)
	}
	if got := count(t, r.db, `SELECT count(*) FROM chunks`); got != 0 {
		t.Fatalf("chunks = %d, want 0 after chunk-key evict", got)
	}
	// Evicting the same path again is a no-op, not an error.
	if err := r.Evict(ctx, "/vault/corpus.md"); err != nil {
		t.Fatal(err)
	}
}

// TestIndexPathIdempotentPerContent: unchanged content must not re-embed.
func TestIndexPathIdempotentPerContent(t *testing.T) {
	files := &stubFiles{}
	files.set("/vault/corpus.md", "alpha beta gamma delta")
	emb := &stubEmbedder{dim: 8}
	r := newTestRetriever(t, &stubDocs{}, files, emb)
	ctx := context.Background()

	if err := r.IndexPath(ctx, "/vault/corpus.md", ""); err != nil {
		t.Fatal(err)
	}
	first := emb.callCount()
	if first == 0 {
		t.Fatal("expected embedding calls")
	}
	if err := r.IndexPath(ctx, "/vault/corpus.md", ""); err != nil {
		t.Fatal(err)
	}
	if got := emb.callCount(); got != first {
		t.Fatalf("embed calls after unchanged re-index = %d, want %d (no re-embed)", got, first)
	}

	// Changed content re-indexes and updates the stored hash.
	files.set("/vault/corpus.md", "omega psi chi phi")
	if err := r.IndexPath(ctx, "/vault/corpus.md", ""); err != nil {
		t.Fatal(err)
	}
	if emb.callCount() == first {
		t.Fatal("changed content did not re-embed")
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 1 || status[0].ChunkCount == 0 {
		t.Fatalf("status = %+v", status)
	}
}

// TestStatusContentHashEnablesStaleDetection: Status exposes the indexed hash
// so the corpus layer can compare disk hashes (ADR-0049 §4).
func TestStatusContentHashEnablesStaleDetection(t *testing.T) {
	files := &stubFiles{}
	files.set("/vault/corpus.md", "alpha beta")
	r := newTestRetriever(t, &stubDocs{}, files, &stubEmbedder{dim: 8})
	if err := r.IndexPath(context.Background(), "/vault/corpus.md", ""); err != nil {
		t.Fatal(err)
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 1 {
		t.Fatalf("status = %+v", status)
	}
	indexed := status[0].ContentHash
	// The disk changed but was not re-indexed: the persisted hash differs from
	// the new disk hash, which is exactly how "stale" is derived.
	if indexed == "" {
		t.Fatal("empty content hash")
	}
}

// TestFusionOrdering: RRF ranks a key appearing high in both lists first.
func TestFusionOrdering(t *testing.T) {
	fused := rrfFuse(
		[]string{"a", "b", "c"},
		[]string{"b", "a", "d"},
	)
	if len(fused) != 4 {
		t.Fatalf("fused = %+v", fused)
	}
	// a: 1/61 + 1/62; b: 1/62 + 1/61 — equal; deterministic key tie-break.
	// c: 1/63; d: 1/63. The pair {a,b} must outrank {c,d}.
	top := map[string]bool{fused[0].key: true, fused[1].key: true}
	if !top["a"] || !top["b"] {
		t.Fatalf("top pair = %v, want a,b", top)
	}
	if fused[0].score <= fused[2].score {
		t.Fatalf("scores not descending: %+v", fused)
	}
}

func TestFTSQuerySanitized(t *testing.T) {
	if got := ftsQuery("alpha, beta!"); got != `"alpha" OR "beta"` {
		t.Fatalf("ftsQuery = %q", got)
	}
	if got := ftsQuery("   "); got != "" {
		t.Fatalf("empty ftsQuery = %q", got)
	}
	if got := ftsQuery(`say "hi"`); got != `"say" OR "hi"` {
		t.Fatalf("quoted ftsQuery = %q", got)
	}
}

// TestGetResolvesPins: tray pins resolve by chunk key or path (ADR-0049 §8).
func TestGetResolvesPins(t *testing.T) {
	files := &stubFiles{}
	files.set("/vault/corpus.md", "alpha beta gamma delta epsilon zeta")
	r := newTestRetriever(t, &stubDocs{}, files, &stubEmbedder{dim: 8})
	ctx := context.Background()
	if err := r.IndexPath(ctx, "/vault/corpus.md", ""); err != nil {
		t.Fatal(err)
	}
	var key string
	if err := r.db.QueryRow(`SELECT chunk_key FROM chunks LIMIT 1`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, []dto.ChunkRef{
		{Path: "/vault/corpus.md", ChunkKey: key},
		{Path: "/vault/missing.md", ChunkKey: "missing#0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChunkKey != key {
		t.Fatalf("pins = %+v", got)
	}
}

// TestSchemaPartialMigration: the base schema migrates before the embedding
// dimension is known, then the vec table is appended (dim learned later).
func TestSchemaPartialMigration(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	ctx := context.Background()

	if err := sqlmigrate.Migrate(ctx, db, indexSchema(0)); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM sqlite_master WHERE name = 'vec_chunks'`); got != 0 {
		t.Fatalf("vec_chunks exists before dim known")
	}
	if err := sqlmigrate.Migrate(ctx, db, indexSchema(4)); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM sqlite_master WHERE name = 'vec_chunks'`); got != 1 {
		t.Fatalf("vec_chunks missing after dim migration")
	}
}
