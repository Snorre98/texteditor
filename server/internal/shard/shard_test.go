package shard

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"texteditor/internal/retriever"
	"texteditor/shared/dto"
)

// stubRetriever satisfies retriever.Interface for shard-lifecycle tests.
type stubRetriever struct{}

func (stubRetriever) Query(context.Context, string, int) ([]dto.Chunk, error) { return nil, nil }
func (stubRetriever) SearchText(context.Context, string, int) ([]dto.Chunk, error) {
	return nil, nil
}
func (stubRetriever) Index(context.Context, string) error             { return nil }
func (stubRetriever) IndexPath(context.Context, string, string) error { return nil }
func (stubRetriever) Evict(context.Context, string) error             { return nil }
func (stubRetriever) Status() ([]dto.IndexedDocument, error)          { return nil, nil }
func (stubRetriever) Get(context.Context, []dto.ChunkRef) ([]dto.Chunk, error) {
	return nil, nil
}

var _ retriever.Interface = stubRetriever{}

func newTestManager(t *testing.T, cap int) (*Manager, string) {
	t.Helper()
	dataDir := t.TempDir()
	m := New(Options{
		DataDir:      dataDir,
		NewRetriever: func(*sql.DB) retriever.Interface { return stubRetriever{} },
		Cap:          cap,
	})
	t.Cleanup(func() { m.Close() })
	return m, dataDir
}

func TestLazyOpenAndMigrate(t *testing.T) {
	m, dataDir := newTestManager(t, 4)
	ctx := context.Background()

	if m.IsOpen("ws1") {
		t.Fatal("shard open before first use")
	}
	lease, err := m.Services(ctx, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	for _, name := range []string{"index.db", "sessions.db", "meter.db"} {
		p := filepath.Join(dataDir, "workspaces", "ws1", name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("shard file %s: %v", name, err)
		}
	}
	// The session store is migrated and usable.
	sess, err := lease.Sessions.Create("doc1", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Sessions.Resume(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Sessions.History(sess.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLRUEvictionAndReopen(t *testing.T) {
	m, _ := newTestManager(t, 2)
	ctx := context.Background()

	for _, id := range []string{"ws1", "ws2", "ws3"} {
		lease, err := m.Services(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		lease.Release()
	}
	if m.IsOpen("ws1") {
		t.Fatal("least-recently-used shard ws1 should have been evicted")
	}
	if !m.IsOpen("ws2") || !m.IsOpen("ws3") {
		t.Fatalf("recent shards closed: ws2=%v ws3=%v", m.IsOpen("ws2"), m.IsOpen("ws3"))
	}
	if m.OpenCount() != 2 {
		t.Fatalf("open shards = %d, want 2", m.OpenCount())
	}

	// Reopening a closed shard works (lazy reopen).
	lease, err := m.Services(ctx, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if !m.IsOpen("ws1") {
		t.Fatal("ws1 did not reopen")
	}
}

func TestLeasePreventsEviction(t *testing.T) {
	m, _ := newTestManager(t, 2)
	ctx := context.Background()

	held, err := m.Services(ctx, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	lease2, err := m.Services(ctx, "ws2")
	if err != nil {
		t.Fatal(err)
	}
	lease2.Release()
	lease3, err := m.Services(ctx, "ws3")
	if err != nil {
		t.Fatal(err)
	}
	lease3.Release()

	if !m.IsOpen("ws1") {
		t.Fatal("leased shard ws1 was evicted")
	}
	if m.IsOpen("ws2") {
		t.Fatal("idle shard ws2 should have been evicted to stay at cap")
	}

	// Releasing the lease lets LRU reclaim it.
	held.Release()
	lease4, err := m.Services(ctx, "ws4")
	if err != nil {
		t.Fatal(err)
	}
	lease4.Release()
	if m.OpenCount() > 2 {
		t.Fatalf("open shards = %d, want <= 2 after release", m.OpenCount())
	}
}

func TestServicesRequiresWorkspaceID(t *testing.T) {
	m, _ := newTestManager(t, 2)
	if _, err := m.Services(context.Background(), ""); err == nil {
		t.Fatal("empty workspace id must fail")
	}
}

func TestCloseAll(t *testing.T) {
	m, _ := newTestManager(t, 4)
	ctx := context.Background()
	lease, err := m.Services(ctx, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if m.OpenCount() != 0 {
		t.Fatalf("open shards after close = %d", m.OpenCount())
	}
	if _, err := m.Services(ctx, "ws1"); err == nil {
		t.Fatal("Services after Close must fail")
	}
}
