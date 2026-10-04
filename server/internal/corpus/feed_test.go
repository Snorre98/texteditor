package corpus

// Feed producer tests (ADR-0052 §4): corpus job progress/completion emits
// `corpus` events and the DocHook emits `document` events.

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"texteditor/internal/filesystem"
	"texteditor/internal/workspace"
	"texteditor/shared/dto"
)

// recordingBus captures emitted events for assertions.
type recordingBus struct {
	mu     sync.Mutex
	events []dto.Event
}

func (b *recordingBus) Emit(ev dto.Event) {
	b.mu.Lock()
	b.events = append(b.events, ev)
	b.mu.Unlock()
}

func (b *recordingBus) snapshot() []dto.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]dto.Event(nil), b.events...)
}

// newTestServiceWithBus mirrors newTestService but wires the recording bus.
func newTestServiceWithBus(t *testing.T, roots []string, bus Emitter) (Interface, workspace.Interface) {
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
	svc := New(Options{Workspaces: ws, Filesystem: fsGW, Shards: fakeShards{rec: rec}, Bus: bus})
	return svc, ws
}

func TestIndexEmitsCorpusFeedEvents(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.md"), "# A\n\nalpha")
	mustWrite(t, filepath.Join(root, "b.md"), "# B\n\nbeta")

	bus := &recordingBus{}
	svc, ws := newTestServiceWithBus(t, []string{root}, bus)
	wsObj, err := ws.ResolveOrCreate(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Index(context.Background(), wsObj.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, ws, wsObj.ID)

	events := bus.snapshot()
	if len(events) < 3 {
		t.Fatalf("corpus events = %d, want >= 3 (start + per-file + done)", len(events))
	}
	sawTerminal := false
	for _, ev := range events {
		if ev.Type != dto.EventCorpus {
			t.Fatalf("event type = %q, want corpus", ev.Type)
		}
		if ev.WorkspaceID != wsObj.ID {
			t.Fatalf("event workspaceId = %q, want %q", ev.WorkspaceID, wsObj.ID)
		}
		var payload dto.CorpusEventPayload
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if payload.WorkspaceID != wsObj.ID {
			t.Fatalf("payload workspaceId = %q", payload.WorkspaceID)
		}
		if payload.Job.State == "done" {
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatal("no terminal (done) corpus event emitted")
	}
}

func TestDocHookEmitsDocumentEvents(t *testing.T) {
	doc := &stubDocStore{externalChange: true}
	notif := &notifyingCorpus{}
	bus := &recordingBus{}
	hook := &DocHook{Interface: doc, Corpus: notif, Bus: bus}

	if _, err := hook.Open("/vault/a.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := hook.Commit("d1", dto.CommitOptions{}); err != nil {
		t.Fatal(err)
	}
	doc.savePath = "/vault/b.md"
	doc.writeThr = true
	if _, err := hook.SaveTree("d1", nil, dto.SaveOptions{WriteThrough: true}); err != nil {
		t.Fatal(err)
	}

	events := bus.snapshot()
	if len(events) != 3 {
		t.Fatalf("document events = %d, want 3 (open external-change + commit + write-through save)", len(events))
	}
	var kinds []string
	for _, ev := range events {
		if ev.Type != dto.EventDocument {
			t.Fatalf("event type = %q, want document", ev.Type)
		}
		if ev.WorkspaceID != "" {
			t.Fatalf("document events are global; got workspaceId %q", ev.WorkspaceID)
		}
		var payload dto.DocumentEventPayload
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("payload: %v", err)
		}
		kinds = append(kinds, payload.Kind)
	}
	want := []string{"external-change", "commit", "commit"}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
}
