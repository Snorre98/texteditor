// Package retriever holds the Retriever — hybrid (semantic + lexical) retrieval
// over a workspace shard's index.db (sqlite-vec vec0 + FTS5), fused with
// reciprocal rank fusion (ADR-0044 §3, ADR-0049 §4/§5).
//
// The Retriever is NOT a leaf (ADR-0016 §8): it depends on Fleet (to resolve the
// embedding model), Provider (to embed), the Chunker (to produce chunks), a
// document source (block tree + path for versioned documents), and the
// Filesystem (bounded corpus reads). At the A2 layer those are sealed local
// seams, stubbed in tests and wired to the real modules at the composition root.
//
// Identity: versioned documents are indexed by documentID and keep the existing
// Index(documentID) path; corpus files are indexed path-keyed by IndexPath and
// never become documents (ADR-0049 §4). Eviction deletes both vec0 and FTS rows
// and is idempotent; re-indexing rebuilds by chunk key so no stale FTS row or
// vec0 id collision survives.
package retriever

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"texteditor/internal/chunker"
	"texteditor/internal/pathutil"
	"texteditor/internal/sqlmigrate"
	"texteditor/shared/dto"
)

// Retriever is the Retriever public API (interface.md §3, extended by
// ADR-0049 §4).
type Retriever interface {
	Query(ctx context.Context, text string, topK int) ([]dto.Chunk, error)
	// Index chunks a versioned document's block tree (ADR-0049 §4).
	Index(ctx context.Context, documentID string) error
	// IndexPath chunks and indexes one corpus file path-keyed (ADR-0049 §4).
	// It is idempotent per unchanged content: when the file's hash and chunk
	// count match the indexed row, no embedding or write happens. The
	// contentHash argument is advisory (the Retriever recomputes from its own
	// bounded read); the authoritative hash is the one persisted.
	IndexPath(ctx context.Context, path, contentHash string) error
	// Evict removes one chunk (chunkKey), one path's chunks, or one document's
	// chunks from both the vec0 and FTS indexes. It is idempotent: evicting an
	// already-evicted target succeeds (ADR-0049 §4).
	Evict(ctx context.Context, chunkKeyOrPath string) error
	// Status returns one row per indexed file (path, hash, chunk count) so the
	// corpus layer can report indexed/stale (ADR-0049 §4).
	Status() ([]dto.IndexedDocument, error)
	// Get resolves stable ChunkRefs to their indexed chunks (context-tray pins,
	// ADR-0049 §8). Missing refs are omitted; the caller labels them.
	Get(ctx context.Context, refs []dto.ChunkRef) ([]dto.Chunk, error)
}

// Interface is an alias for Retriever (the contracted name, interface.md §3).
type Interface = Retriever

// Resolver is the sealed subset of the Fleet gateway the Retriever needs: it
// resolves the embedding model to a served Target (interface.md §1).
type Resolver interface {
	Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error)
}

// Embedder is the sealed subset of the Provider gateway the Retriever needs:
// embedding a text into a float vector (interface.md §2).
type Embedder interface {
	Embed(ctx context.Context, target dto.Target, text string) ([]float32, error)
}

// DocumentReader is the sealed versioned-document source: the block tree to
// chunk plus the canonical path for provenance (satisfied by the Document
// store; ADR-0049 §2).
type DocumentReader interface {
	Blocks(documentID string) ([]dto.Block, error)
	Path(documentID string) (string, error)
}

// FileReader is the sealed bounded corpus reader (satisfied by the Filesystem
// leaf, so ALLOWED_ROOTS bounds corpus indexing; ADR-0049 §6).
type FileReader interface {
	Read(ctx context.Context, path string, maxBytes int) ([]byte, error)
}

// EmbedModelName is the embedding model the Retriever resolves via Fleet
// (interface.md §3).
const EmbedModelName = "nomic-embed"

// RRF constant (ADR-0044 §3): score = Σ 1/(k + rank), k = 60.
const rrfK = 60

// corpusReadCap bounds one corpus file read (4 MiB).
const corpusReadCap = 4 << 20

// maxFTSTerms bounds the generated FTS query.
const maxFTSTerms = 32

// retriever is the concrete Retriever. A workspace shard's index.db is its
// single-writer file (ADR-0049 §5).
type retriever struct {
	db          *sql.DB
	resolver    Resolver
	embedder    Embedder
	docs        DocumentReader
	files       FileReader
	chunk       chunker.Interface
	chunkTokens int

	mu        sync.Mutex
	dim       int  // learned/cached embedding dimension
	baseReady bool // base schema migrated at least once
	vecReady  bool // vec table known present with r.dim
}

// New returns a Retriever over a workspace shard's index.db. chunkTokens is the
// RAG token lever (ADR-0020 §5): the per-chunk size bound handed to the Chunker.
func New(db *sql.DB, resolver Resolver, embedder Embedder, docs DocumentReader, files FileReader, ch chunker.Interface, chunkTokens int) Retriever {
	return &retriever{
		db:          db,
		resolver:    resolver,
		embedder:    embedder,
		docs:        docs,
		files:       files,
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

// ensureBaseSchema migrates the non-vector schema (status/evict paths).
func (r *retriever) ensureBaseSchema(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.baseReady {
		return nil
	}
	if err := sqlmigrate.Migrate(ctx, r.db, indexSchema(0)); err != nil {
		return err
	}
	r.baseReady = true
	return nil
}

// ensureVecSchema migrates the vec0 table once the embedding dimension is known
// and records it in index_meta so a restart validates the model against the
// existing table. Re-migrating with a different dim is a typed error.
func (r *retriever) ensureVecSchema(ctx context.Context, dim int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.vecReady && r.dim == dim {
		return nil
	}
	var recorded string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM index_meta WHERE key = 'dim'`).Scan(&recorded)
	if err == nil {
		if recorded != strconv.Itoa(dim) {
			return fmt.Errorf("embedding dimension changed (%s -> %d)", recorded, dim)
		}
		r.dim = dim
		r.baseReady = true
		r.vecReady = true
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := sqlmigrate.Migrate(ctx, r.db, indexSchema(dim)); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO index_meta (key, value) VALUES ('dim', ?)`, strconv.Itoa(dim),
	); err != nil {
		return err
	}
	r.dim = dim
	r.baseReady = true
	r.vecReady = true
	return nil
}

// Index chunks a versioned document's block tree and rebuilds its rows (vec0 +
// FTS5 + chunks). The document keeps its surrogate identity: chunkKey =
// documentID#index (ADR-0049 §4).
func (r *retriever) Index(ctx context.Context, documentID string) error {
	if err := r.ensureBaseSchema(ctx); err != nil {
		return err
	}
	tree, err := r.docs.Blocks(documentID)
	if err != nil {
		return err
	}
	chunks, err := r.chunk.Chunk(tree, r.chunkTokens)
	if err != nil {
		return err
	}
	path, err := r.docs.Path(documentID)
	if err != nil {
		return err
	}
	canonical, pathKey := pathutil.Canonical(path)

	if len(chunks) == 0 {
		// Empty document: rebuild to empty (evict any prior rows).
		return r.deleteByFileKey(ctx, documentID)
	}

	rows, dim, err := r.embedChunks(ctx, chunks, func(i int) string {
		return documentID + "#" + strconv.Itoa(i)
	})
	if err != nil {
		return err
	}
	if err := r.ensureVecSchema(ctx, dim); err != nil {
		return err
	}
	hash := pathutil.Hash(joinTexts(chunks))
	return r.rebuild(ctx, documentID, pathKey, canonical, documentID, hash, rows)
}

// IndexPath chunks and indexes one corpus file path-keyed. Unchanged content is
// a no-op (no re-embedding): idempotent per ADR-0049 §4.
func (r *retriever) IndexPath(ctx context.Context, path, contentHash string) error {
	if err := r.ensureBaseSchema(ctx); err != nil {
		return err
	}
	canonical, pathKey := pathutil.Canonical(path)
	raw, err := r.files.Read(ctx, canonical, corpusReadCap)
	if err != nil {
		return err
	}
	hash := pathutil.Hash(string(raw))
	chunks, err := chunker.ChunkMarkdown(string(raw), r.chunkTokens)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return r.deleteByFileKey(ctx, pathKey)
	}

	var haveHash string
	var haveCount int
	err = r.db.QueryRowContext(ctx,
		`SELECT content_hash, chunk_count FROM indexed_files WHERE file_key = ?`, pathKey,
	).Scan(&haveHash, &haveCount)
	if err == nil && haveHash == hash && haveCount == len(chunks) {
		return nil // unchanged content: idempotent no-op
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	rows, dim, err := r.embedChunks(ctx, chunks, func(i int) string {
		return canonical + "#" + strconv.Itoa(i)
	})
	if err != nil {
		return err
	}
	if err := r.ensureVecSchema(ctx, dim); err != nil {
		return err
	}
	return r.rebuild(ctx, pathKey, pathKey, canonical, "", hash, rows)
}

// Evict removes a chunk (by chunkKey) or every chunk of a path/document from
// both the vector and FTS indexes. Idempotent (ADR-0049 §4).
func (r *retriever) Evict(ctx context.Context, chunkKeyOrPath string) error {
	ref := strings.TrimSpace(chunkKeyOrPath)
	if ref == "" {
		return nil
	}
	if err := r.ensureBaseSchema(ctx); err != nil {
		return err
	}
	// A ref that names an existing chunk key is evicted exactly; anything else
	// is treated as a path (or document id) and evicted by file key.
	var one int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM chunks WHERE chunk_key = ? LIMIT 1`, ref).Scan(&one)
	if err == nil {
		return r.deleteByChunkKeys(ctx, []string{ref}, "")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, pathKey := pathutil.Canonical(ref); pathKey != "" {
		if err := r.deleteByPathKey(ctx, pathKey); err != nil {
			return err
		}
	}
	// Also allow eviction by document id (chunks.file_key = documentID).
	return r.deleteByFileKey(ctx, ref)
}

// Status returns one row per indexed file (versioned documents and corpus
// files), newest-index first is not needed; path order is stable.
func (r *retriever) Status() ([]dto.IndexedDocument, error) {
	ctx := context.Background()
	if err := r.ensureBaseSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT path, document_id, content_hash, chunk_count, indexed_at
		 FROM indexed_files ORDER BY path, document_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dto.IndexedDocument
	for rows.Next() {
		var d dto.IndexedDocument
		if err := rows.Scan(&d.Path, &d.DocumentID, &d.ContentHash, &d.ChunkCount, &d.IndexedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Get resolves ChunkRefs to indexed chunks (pins). Refs with a chunkKey match
// exactly; refs with only a path return that path's chunks in key order.
// Missing refs are omitted (the caller labels them, never silent).
func (r *retriever) Get(ctx context.Context, refs []dto.ChunkRef) ([]dto.Chunk, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if err := r.ensureBaseSchema(ctx); err != nil {
		return nil, err
	}
	var out []dto.Chunk
	for _, ref := range refs {
		if ref.ChunkKey != "" {
			c, ok, err := r.chunkByKey(ctx, ref.ChunkKey)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, c)
			}
			continue
		}
		if ref.Path == "" {
			continue
		}
		_, pathKey := pathutil.Canonical(ref.Path)
		rows, err := r.db.QueryContext(ctx,
			`SELECT chunk_key, path, heading, block_id, content FROM chunks
			 WHERE path_key = ? ORDER BY chunk_key`, pathKey)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c dto.Chunk
			if err := rows.Scan(&c.ChunkKey, &c.Path, &c.Heading, &c.BlockID, &c.Text); err != nil {
				rows.Close()
				return nil, err
			}
			c.Source = sourceOf(c.Path, c.BlockID)
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// Query embeds the text, runs vec0 KNN (semantic) and FTS5 bm25 (lexical), and
// fuses the two rankings with reciprocal rank fusion (k=60) plus dedupe
// (ADR-0044 §3). Zero chunks is not an error (failure-semantics §3).
func (r *retriever) Query(ctx context.Context, text string, topK int) ([]dto.Chunk, error) {
	if topK <= 0 {
		return nil, nil
	}
	v, err := r.embedTarget(ctx, EmbedModelName, text)
	if err != nil {
		return nil, err
	}
	if err := r.ensureVecSchema(ctx, len(v)); err != nil {
		return nil, err
	}
	k := topK * 2
	if k < 10 {
		k = 10
	}

	vecKeys, err := r.knnKeys(ctx, v, k)
	if err != nil {
		return nil, err
	}
	ftsKeys, err := r.ftsKeys(ctx, text, k)
	if err != nil {
		return nil, err
	}
	fused := rrfFuse(vecKeys, ftsKeys)
	if len(fused) > topK {
		fused = fused[:topK]
	}
	return r.chunksByKeys(ctx, fused)
}

// knnKeys returns the vec0 KNN chunk keys in distance order.
func (r *retriever) knnKeys(ctx context.Context, v []float32, k int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT chunk_key FROM vec_chunks WHERE embedding MATCH ? AND k = ? ORDER BY distance`,
		encodeVec(v), k,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// ftsKeys returns the FTS5 bm25 chunk keys, best first. The query is sanitized
// into a quoted OR of terms so user punctuation cannot break FTS syntax.
func (r *retriever) ftsKeys(ctx context.Context, text string, k int) ([]string, error) {
	q := ftsQuery(text)
	if q == "" {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT chunk_key FROM blocks_ft WHERE blocks_ft MATCH ? ORDER BY bm25(blocks_ft) LIMIT ?`,
		q, k,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// scored is one fused chunk key.
type scored struct {
	key   string
	score float64
}

// rrfFuse merges two ranked key lists with reciprocal rank fusion (k=60),
// deduping by key; ties break by key for determinism.
func rrfFuse(rankings ...[]string) []scored {
	scores := map[string]float64{}
	for _, ranking := range rankings {
		for i, key := range ranking {
			scores[key] += 1.0 / float64(rrfK+i+1)
		}
	}
	out := make([]scored, 0, len(scores))
	for key, score := range scores {
		out = append(out, scored{key: key, score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].key < out[j].key
	})
	return out
}

// chunksByKeys loads the fused keys' chunks in rank order.
func (r *retriever) chunksByKeys(ctx context.Context, keys []scored) ([]dto.Chunk, error) {
	out := make([]dto.Chunk, 0, len(keys))
	for _, s := range keys {
		c, ok, err := r.chunkByKey(ctx, s.key)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // chunk vanished mid-query (concurrent eviction); skip
		}
		c.Score = float32(s.score)
		out = append(out, c)
	}
	return out, nil
}

// chunkByKey loads one chunk row by chunk key.
func (r *retriever) chunkByKey(ctx context.Context, key string) (dto.Chunk, bool, error) {
	var c dto.Chunk
	err := r.db.QueryRowContext(ctx,
		`SELECT chunk_key, path, heading, block_id, content FROM chunks WHERE chunk_key = ?`, key,
	).Scan(&c.ChunkKey, &c.Path, &c.Heading, &c.BlockID, &c.Text)
	if errors.Is(err, sql.ErrNoRows) {
		return dto.Chunk{}, false, nil
	}
	if err != nil {
		return dto.Chunk{}, false, err
	}
	c.Source = sourceOf(c.Path, c.BlockID)
	return c, true, nil
}

// chunkRow is one embedded chunk ready to insert.
type chunkRow struct {
	key     string
	blockID string
	heading string
	text    string
	vec     []float32
}

// embedChunks embeds every chunk, keyed by keyFn. It resolves the embedding
// model once and returns the learned dimension.
func (r *retriever) embedChunks(ctx context.Context, chunks []dto.Chunk, keyFn func(i int) string) ([]chunkRow, int, error) {
	res, err := r.resolver.Resolve(EmbedModelName, dto.ResolveOpts{ModeTag: EmbedModelName})
	if err != nil {
		return nil, 0, err
	}
	target := dto.Target{BaseURL: res.Model.BaseURL, Capabilities: res.Model.Capabilities}
	rows := make([]chunkRow, 0, len(chunks))
	dim := 0
	for i, c := range chunks {
		v, err := r.embedder.Embed(ctx, target, c.Text)
		if err != nil {
			return nil, 0, err
		}
		if dim == 0 {
			dim = len(v)
		}
		rows = append(rows, chunkRow{key: keyFn(i), blockID: c.BlockID, heading: c.Heading, text: c.Text, vec: v})
	}
	return rows, dim, nil
}

// rebuild atomically replaces one file's rows (delete by file key, then insert)
// so re-indexing never leaves stale FTS rows or colliding vec ids.
func (r *retriever) rebuild(ctx context.Context, fileKey, pathKey, path, documentID, contentHash string, rows []chunkRow) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := deleteFileRows(ctx, tx, fileKey); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chunks (chunk_key, file_key, path_key, path, heading, block_id, content)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			row.key, fileKey, pathKey, path, row.heading, row.blockID, row.text,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO blocks_ft (chunk_key, content) VALUES (?, ?)`,
			row.key, row.text,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vec_chunks (chunk_key, embedding) VALUES (?, ?)`,
			row.key, encodeVec(row.vec),
		); err != nil {
			return err
		}
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO indexed_files (file_key, path_key, path, document_id, content_hash, chunk_count, indexed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(file_key) DO UPDATE SET
		   path_key = excluded.path_key,
		   path = excluded.path,
		   document_id = excluded.document_id,
		   content_hash = excluded.content_hash,
		   chunk_count = excluded.chunk_count,
		   indexed_at = excluded.indexed_at`,
		fileKey, pathKey, path, documentID, contentHash, len(rows), now,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteByFileKey removes a file's chunks and status row (idempotent).
func (r *retriever) deleteByFileKey(ctx context.Context, fileKey string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteFileRows(ctx, tx, fileKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM indexed_files WHERE file_key = ?`, fileKey); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteByPathKey removes every chunk indexed under one canonical path (corpus
// or versioned) and its status rows (idempotent).
func (r *retriever) deleteByPathKey(ctx context.Context, pathKey string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keys, err := chunkKeys(ctx, tx, `path_key = ?`, pathKey)
	if err != nil {
		return err
	}
	if err := deleteChunkKeys(ctx, tx, keys); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE path_key = ?`, pathKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM indexed_files WHERE path_key = ?`, pathKey); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteByChunkKeys removes specific chunk keys plus any status row that
// becomes empty (idempotent).
func (r *retriever) deleteByChunkKeys(ctx context.Context, keys []string, pathKey string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteChunkKeys(ctx, tx, keys); err != nil {
		return err
	}
	if pathKey != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM indexed_files WHERE path_key = ?`, pathKey); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// deleteFileRows deletes all index rows for a file key (vec, FTS, chunks).
func deleteFileRows(ctx context.Context, tx *sql.Tx, fileKey string) error {
	keys, err := chunkKeys(ctx, tx, `file_key = ?`, fileKey)
	if err != nil {
		return err
	}
	if err := deleteChunkKeys(ctx, tx, keys); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM chunks WHERE file_key = ?`, fileKey)
	return err
}

// deleteChunkKeys removes vec + FTS rows for the given chunk keys. The vec
// table is skipped when it does not exist yet (no embedding has happened).
func deleteChunkKeys(ctx context.Context, tx *sql.Tx, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if hasVecTable(ctx, tx) {
		for _, key := range keys {
			if _, err := tx.ExecContext(ctx, `DELETE FROM vec_chunks WHERE chunk_key = ?`, key); err != nil {
				return err
			}
		}
	}
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM blocks_ft WHERE chunk_key = ?`, key); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE chunk_key = ?`, key); err != nil {
			return err
		}
	}
	return nil
}

// chunkKeys selects chunk keys matching one WHERE clause.
func chunkKeys(ctx context.Context, q queryRower, where string, arg interface{}) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT chunk_key FROM chunks WHERE `+where, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// hasVecTable reports whether the vec0 table exists.
func hasVecTable(ctx context.Context, q queryRower) bool {
	var name string
	err := q.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'vec_chunks'`).Scan(&name)
	return err == nil
}

// queryRower is satisfied by *sql.DB and *sql.Tx.
type queryRower interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// ftsQuery sanitizes free text into a quoted OR of terms for FTS5 MATCH.
func ftsQuery(text string) string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(fields) > maxFTSTerms {
		fields = fields[:maxFTSTerms]
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " OR ")
}

// joinTexts concatenates chunk texts for a deterministic versioned-document
// content hash.
func joinTexts(chunks []dto.Chunk) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// sourceOf is the citation marker: the canonical path when known, else the
// block id (versioned chunks always carry a path, so this is the corpus form).
func sourceOf(path, blockID string) string {
	if path != "" {
		return path
	}
	return blockID
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
