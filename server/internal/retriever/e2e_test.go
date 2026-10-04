package retriever

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"texteditor/internal/chunker"
	"texteditor/internal/filesystem"
)

// TestTempVaultE2ETwoRoots is the Phase C retrieval gate over real stores: a
// temp vault with two corpus roots (a multi-root corpus), path-keyed indexing
// through the bounded Filesystem, a fused query, and idempotent eviction.
func TestTempVaultE2ETwoRoots(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	fileA := filepath.Join(rootA, "alpha.md")
	fileB := filepath.Join(rootB, "beta.md")
	if err := os.WriteFile(fileA, []byte("# Alpha\n\nalpha beta gamma delta epsilon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("# Beta\n\nomega psi chi phi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fsGW, err := filesystem.New([]string{rootA, rootB})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	r := New(db, stubResolver{}, &stubEmbedder{dim: 8}, &stubDocs{}, fsGW, chunker.New(), 512)
	ctx := context.Background()

	if err := r.IndexPath(ctx, fileA, ""); err != nil {
		t.Fatalf("index A: %v", err)
	}
	if err := r.IndexPath(ctx, fileB, ""); err != nil {
		t.Fatalf("index B: %v", err)
	}
	status, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 2 {
		t.Fatalf("status = %+v, want 2 indexed files", status)
	}

	chunks, err := r.Query(ctx, "alpha beta", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Fatal("query returned no chunks across the two roots")
	}
	if chunks[0].Path != mustCanonical(t, fileA) {
		t.Fatalf("first chunk path = %q, want %q", chunks[0].Path, mustCanonical(t, fileA))
	}
	if chunks[0].Heading == "" {
		t.Fatalf("chunk lost its markdown heading provenance: %+v", chunks[0])
	}

	// Eviction removes the file from both indexes and is idempotent.
	for i := 0; i < 2; i++ {
		if err := r.Evict(ctx, fileA); err != nil {
			t.Fatalf("evict #%d: %v", i+1, err)
		}
	}
	status, err = r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 1 || status[0].Path != mustCanonical(t, fileB) {
		t.Fatalf("status after evict = %+v, want only B", status)
	}
	chunks, err = r.Query(ctx, "alpha beta", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if c.Path == mustCanonical(t, fileA) {
			t.Fatalf("evicted file still retrievable: %+v", c)
		}
	}
}

func mustCanonical(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
