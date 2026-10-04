package corpus

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"texteditor/internal/document"
	"texteditor/internal/filesystem"
	"texteditor/internal/pathutil"
	"texteditor/internal/retriever"
	"texteditor/internal/shard"
	"texteditor/internal/workspace"
	"texteditor/shared/dto"
)

// fakeRetriever is an in-memory path-keyed index over the real Filesystem.
type fakeRetriever struct {
	fs filesystem.Interface

	mu   sync.Mutex
	rows map[string]dto.IndexedDocument // path_key → row
}

func newFakeRetriever(fs filesystem.Interface) *fakeRetriever {
	return &fakeRetriever{fs: fs, rows: map[string]dto.IndexedDocument{}}
}

func (f *fakeRetriever) IndexPath(ctx context.Context, path, _ string) error {
	raw, err := f.fs.Read(ctx, path, 1<<20)
	if err != nil {
		return err
	}
	canonical, key := pathutil.Canonical(path)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[key] = dto.IndexedDocument{
		Path:        canonical,
		ContentHash: pathutil.Hash(string(raw)),
		ChunkCount:  1,
		IndexedAt:   time.Now().Unix(),
	}
	return nil
}

func (f *fakeRetriever) Status() ([]dto.IndexedDocument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]dto.IndexedDocument, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeRetriever) Evict(_ context.Context, ref string) error {
	_, key := pathutil.Canonical(ref)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, key)
	return nil
}

func (f *fakeRetriever) has(path string) bool {
	_, key := pathutil.Canonical(path)
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.rows[key]
	return ok
}

func (f *fakeRetriever) Query(context.Context, string, int) ([]dto.Chunk, error) { return nil, nil }
func (f *fakeRetriever) SearchText(context.Context, string, int) ([]dto.Chunk, error) {
	return nil, nil
}
func (f *fakeRetriever) Index(context.Context, string) error { return nil }
func (f *fakeRetriever) Get(context.Context, []dto.ChunkRef) ([]dto.Chunk, error) {
	return nil, nil
}

var _ retriever.Interface = (*fakeRetriever)(nil)

// fakeShards returns one lease carrying the fake retriever.
type fakeShards struct{ rec *fakeRetriever }

func (f fakeShards) Services(context.Context, string) (*shard.Lease, error) {
	return shard.NewLease(shard.Services{Retriever: f.rec}), nil
}

func newTestService(t *testing.T, roots []string) (Interface, workspace.Interface, *fakeRetriever) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := workspace.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ws := workspace.New(db)
	fsGW, err := filesystem.New(roots)
	if err != nil {
		t.Fatal(err)
	}
	rec := newFakeRetriever(fsGW)
	svc := New(Options{Workspaces: ws, Filesystem: fsGW, Shards: fakeShards{rec: rec}})
	return svc, ws, rec
}

// waitJob polls until the workspace's latest job leaves "running".
func waitJob(t *testing.T, ws workspace.Interface, workspaceID string) dto.CorpusJob {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, ok, err := ws.LatestJob(workspaceID)
		if err != nil {
			t.Fatal(err)
		}
		if ok && job.State != "running" {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return dto.CorpusJob{}
}

func TestDefaultScopeIndexesMarkdownExcludesHidden(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.md"), "# A\n\nalpha")
	mustWrite(t, filepath.Join(root, ".obsidian", "b.md"), "# B\n\nbeta")
	mustWrite(t, filepath.Join(root, "notes", "c.md"), "# C\n\ngamma")
	mustWrite(t, filepath.Join(root, "d.txt"), "not markdown")

	svc, ws, rec := newTestService(t, []string{root})
	wsObj, err := ws.ResolveOrCreate(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Index(context.Background(), wsObj.ID); err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, ws, wsObj.ID)
	if job.State != "done" || job.Completed != 2 {
		t.Fatalf("job = %+v, want done with 2 files", job)
	}
	state, err := svc.Get(context.Background(), wsObj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Documents) != 2 {
		t.Fatalf("documents = %+v, want 2 (hidden + txt excluded)", state.Documents)
	}
	for _, d := range state.Documents {
		if d.Status != "indexed" {
			t.Fatalf("doc %s status = %s, want indexed", d.Path, d.Status)
		}
		if d.ChunkCount == 0 {
			t.Fatalf("doc %s missing chunk count", d.Path)
		}
	}
	if rec.has(filepath.Join(root, ".obsidian", "b.md")) || rec.has(filepath.Join(root, "d.txt")) {
		t.Fatal("out-of-scope files were indexed")
	}
}

func TestScopeDecidesRetrievabilityAndStaleDetection(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.md")
	mustWrite(t, file, "# A\n\nalpha")
	svc, ws, _ := newTestService(t, []string{root})
	wsObj, _ := ws.ResolveOrCreate(root)

	if _, err := svc.Index(context.Background(), wsObj.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, ws, wsObj.ID)

	// The disk changed since indexing: stale, not indexed.
	mustWrite(t, file, "# A\n\nalpha changed")
	state, err := svc.Get(context.Background(), wsObj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Documents) != 1 || state.Documents[0].Status != "stale" {
		t.Fatalf("status = %+v, want stale", state.Documents)
	}

	// Excluding the file evicts it and removes it from the scope listing.
	excluded, err := svc.SetScope(context.Background(), wsObj.ID, dto.CorpusScope{
		Roots:   []string{root},
		Include: []string{dto.DefaultCorpusInclude},
		Exclude: []string{"a.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, ws, wsObj.ID)
	if len(excluded.Documents) != 0 {
		t.Fatalf("excluded doc still listed: %+v", excluded.Documents)
	}
	state, _ = svc.Get(context.Background(), wsObj.ID)
	if len(state.Documents) != 0 {
		t.Fatalf("excluded doc still in scope: %+v", state.Documents)
	}
}

// TestExcludingEvictsIndexedRows covers the reconcile-evict path.
func TestExcludingEvictsIndexedRows(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.md")
	mustWrite(t, file, "# A\n\nalpha")
	svc, ws, rec := newTestService(t, []string{root})
	wsObj, _ := ws.ResolveOrCreate(root)
	if _, err := svc.Index(context.Background(), wsObj.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, ws, wsObj.ID)
	if !rec.has(file) {
		t.Fatal("file not indexed")
	}
	if _, err := svc.SetScope(context.Background(), wsObj.ID, dto.CorpusScope{
		Roots:   []string{root},
		Include: []string{dto.DefaultCorpusInclude},
		Exclude: []string{"**/a.md"},
	}); err != nil {
		t.Fatal(err)
	}
	waitJob(t, ws, wsObj.ID)
	if rec.has(file) {
		t.Fatal("excluded file still indexed (reconcile did not evict)")
	}
}

func TestEvictIdempotentAndReindexClearsTombstone(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.md")
	mustWrite(t, file, "# A\n\nalpha")
	svc, ws, rec := newTestService(t, []string{root})
	wsObj, _ := ws.ResolveOrCreate(root)
	if _, err := svc.Index(context.Background(), wsObj.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, ws, wsObj.ID)

	docID := pathutil.DocID(mustCanonical(t, file))
	for i := 0; i < 2; i++ {
		if err := svc.Evict(context.Background(), wsObj.ID, docID); err != nil {
			t.Fatalf("evict #%d: %v", i+1, err)
		}
	}
	if rec.has(file) {
		t.Fatal("evicted file still indexed")
	}
	state, _ := svc.Get(context.Background(), wsObj.ID)
	if len(state.Documents) != 1 || state.Documents[0].Status != "evicted" {
		t.Fatalf("status = %+v, want evicted", state.Documents)
	}

	// A rebuild re-indexes in-scope files and clears the tombstone.
	if _, err := svc.Index(context.Background(), wsObj.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, ws, wsObj.ID)
	state, _ = svc.Get(context.Background(), wsObj.ID)
	if state.Documents[0].Status != "indexed" {
		t.Fatalf("status after rebuild = %s, want indexed", state.Documents[0].Status)
	}
}

func TestUnionOfRootsAndDedupe(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	fileA := filepath.Join(rootA, "a.md")
	fileB := filepath.Join(rootB, "b.md")
	shared := filepath.Join(rootB, "shared.md")
	mustWrite(t, fileA, "# A\n\nalpha")
	mustWrite(t, fileB, "# B\n\nbeta")
	mustWrite(t, shared, "# S\n\nshared")

	svc, ws, rec := newTestService(t, []string{rootA, rootB})
	wsObj, _ := ws.ResolveOrCreate(rootA)
	// Root B plus a sub-root that overlaps it: the shared file must index once.
	subRoot := filepath.Join(rootB, ".")
	if _, err := svc.SetScope(context.Background(), wsObj.ID, dto.CorpusScope{
		Roots:   []string{rootA, rootB, subRoot},
		Include: []string{dto.DefaultCorpusInclude},
	}); err != nil {
		t.Fatal(err)
	}
	waitJob(t, ws, wsObj.ID)

	if !rec.has(fileA) || !rec.has(fileB) || !rec.has(shared) {
		t.Fatalf("union of roots not indexed: a=%v b=%v shared=%v", rec.has(fileA), rec.has(fileB), rec.has(shared))
	}
	state, _ := svc.Get(context.Background(), wsObj.ID)
	if len(state.Documents) != 3 {
		t.Fatalf("documents = %d, want 3 (deduped)", len(state.Documents))
	}
}

func TestAllowedRootsRefusal(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	svc, ws, _ := newTestService(t, []string{allowed})
	wsObj, _ := ws.ResolveOrCreate(allowed)
	_, err := svc.SetScope(context.Background(), wsObj.ID, dto.CorpusScope{
		Roots:   []string{outside},
		Include: []string{dto.DefaultCorpusInclude},
	})
	if !errors.Is(err, filesystem.ErrPathOutsideAllowedRoots) {
		t.Fatalf("err = %v, want ErrPathOutsideAllowedRoots", err)
	}
}

func TestNotifyChangedIndexesInScopeFileOnly(t *testing.T) {
	root := t.TempDir()
	inScope := filepath.Join(root, "a.md")
	mustWrite(t, inScope, "# A\n\nalpha")
	svc, ws, rec := newTestService(t, []string{root})
	if _, err := ws.ResolveOrCreate(root); err != nil {
		t.Fatal(err)
	}

	svc.NotifyChanged(inScope)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !rec.has(inScope) {
		time.Sleep(5 * time.Millisecond)
	}
	if !rec.has(inScope) {
		t.Fatal("NotifyChanged did not index the in-scope file")
	}

	// An out-of-scope path is a no-op.
	other := filepath.Join(t.TempDir(), "b.md")
	mustWrite(t, other, "# B\n\nbeta")
	svc.NotifyChanged(other)
	time.Sleep(50 * time.Millisecond)
	if rec.has(other) {
		t.Fatal("NotifyChanged indexed an out-of-scope file")
	}
}

func TestCorpusFilesAreNeverDocuments(t *testing.T) {
	// The corpus service has no Document store dependency at all: this test
	// pins that indexing cannot mint a documents row by construction.
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.md"), "# A\n\nalpha")
	svc, ws, _ := newTestService(t, []string{root})
	wsObj, _ := ws.ResolveOrCreate(root)
	if _, err := svc.Index(context.Background(), wsObj.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, ws, wsObj.ID)
	state, _ := svc.Get(context.Background(), wsObj.ID)
	if state.Documents[0].Status != "indexed" {
		t.Fatalf("status = %s", state.Documents[0].Status)
	}
}

// stubDocStore records which document boundaries fired so the DocHook's
// lifecycle notification can be asserted.
type stubDocStore struct {
	document.Interface
	opened         []string
	commits        []string
	saves          []string
	savePath       string
	writeThr       bool
	externalChange bool
}

func (s *stubDocStore) Open(path string) (dto.OpenResult, error) {
	s.opened = append(s.opened, path)
	return dto.OpenResult{DocumentID: "d1", Path: path, ExternalChange: s.externalChange}, nil
}
func (s *stubDocStore) Commit(documentID string, _ dto.CommitOptions) (dto.WriteResult, error) {
	s.commits = append(s.commits, documentID)
	return dto.WriteResult{Path: "/vault/a.md", WrittenThrough: true}, nil
}
func (s *stubDocStore) SaveTree(documentID string, _ []dto.BlockWrite, _ dto.SaveOptions) (dto.WriteResult, error) {
	s.saves = append(s.saves, documentID)
	return dto.WriteResult{Path: s.savePath, WrittenThrough: s.writeThr}, nil
}

// notifyingCorpus records NotifyChanged calls.
type notifyingCorpus struct {
	Interface
	mu    sync.Mutex
	paths []string
}

func (n *notifyingCorpus) NotifyChanged(path string) {
	n.mu.Lock()
	n.paths = append(n.paths, path)
	n.mu.Unlock()
}

// TestDocHookNotifiesOnWriteBoundaries covers the ADR-0049 §4 lifecycle: a
// successful Open/Commit/write-through SaveTree enqueues a re-index; an
// engine-only autosave does not.
func TestDocHookNotifiesOnWriteBoundaries(t *testing.T) {
	doc := &stubDocStore{}
	notif := &notifyingCorpus{}
	hook := &DocHook{Interface: doc, Corpus: notif}
	ctx := context.Background()

	if _, err := hook.Open("/vault/a.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := hook.Commit("d1", dto.CommitOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := hook.SaveTree("d1", nil, dto.SaveOptions{WriteThrough: false}); err != nil {
		t.Fatal(err)
	}
	notif.mu.Lock()
	got := len(notif.paths)
	notif.mu.Unlock()
	if got != 2 {
		t.Fatalf("notifications = %d, want 2 (open + commit; autosave is engine-only)", got)
	}

	// A write-through save notifies too.
	doc.savePath = "/vault/b.md"
	doc.writeThr = true
	if _, err := hook.SaveTree("d1", nil, dto.SaveOptions{WriteThrough: true}); err != nil {
		t.Fatal(err)
	}
	notif.mu.Lock()
	defer notif.mu.Unlock()
	if len(notif.paths) != 3 || notif.paths[2] != "/vault/b.md" {
		t.Fatalf("paths = %v, want a third notification for b.md", notif.paths)
	}
	_ = ctx
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
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
