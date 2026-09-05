package apiserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"texteditor/shared/dto"
)

// ------------------------- minimal stubs -------------------------

type stubFleet struct{ models []dto.Model }

func (s stubFleet) ListModels() ([]dto.Model, error) { return s.models, nil }
func (s stubFleet) Resolve(string, dto.ResolveOpts) (dto.Resolution, error) {
	return dto.Resolution{}, nil
}
func (s stubFleet) Status(name string) (dto.LiveState, error) {
	for _, m := range s.models {
		if m.Name == name {
			return dto.LiveUp, nil
		}
	}
	return dto.LiveUnknown, nil
}
func (s stubFleet) Start(string) error                                { return nil }
func (s stubFleet) Stop(string) error                                 { return nil }
func (s stubFleet) Provision(context.Context, string) (string, error) { return "p1", nil }

type stubModes struct{ modes []dto.Mode }

func (s stubModes) List() []dto.Mode { return s.modes }
func (s stubModes) Get(name string) (dto.Mode, error) {
	for _, m := range s.modes {
		if m.Name == name {
			return m, nil
		}
	}
	return dto.Mode{}, nil
}

type stubTools struct{}

func (stubTools) Register(dto.ToolDef) error          { return nil }
func (stubTools) List() []dto.ToolDef                 { return nil }
func (stubTools) AllowlistFor(dto.Mode) []dto.ToolDef { return nil }

type stubDoc struct{}

func (stubDoc) Open(string) (string, error) { return "d1", nil }
func (stubDoc) Save(dto.Document) error     { return nil }
func (stubDoc) Blocks(string) ([]dto.Block, error) {
	return []dto.Block{{ID: "b1", Kind: dto.BlockKindParagraph, Text: "hello"}}, nil
}
func (stubDoc) ApplyEdit(context.Context, string, dto.BlockEdit) (dto.Revision, error) {
	return dto.Revision{ID: "r1", Message: "candidate", Timestamp: 1}, nil
}
func (stubDoc) Commit(string, string) error { return nil }
func (stubDoc) Diff(string, string, string) ([]dto.WordEdit, error) {
	return []dto.WordEdit{{BlockID: "b1", Insertions: []string{"x"}, Deletions: []string{"y"}}}, nil
}
func (stubDoc) History(string) ([]dto.Revision, error) {
	return []dto.Revision{{ID: "r1", Message: "m", Timestamp: 1}}, nil
}
func (stubDoc) Candidates(string, string) ([]dto.Candidate, error) { return nil, nil }

type stubSessions struct{}

func (stubSessions) ListByDocument(string) ([]dto.Session, error) {
	return []dto.Session{{ID: "s1", DocumentID: "d1", ModeType: "proofreader"}}, nil
}
func (stubSessions) Create(string, *string, string) (dto.Session, error) {
	return dto.Session{ID: "s1", DocumentID: "d1"}, nil
}
func (stubSessions) Resume(string) (dto.Session, error) { return dto.Session{}, nil }
func (stubSessions) Append(string, dto.Message) error   { return nil }
func (stubSessions) History(string) ([]dto.Message, error) {
	return []dto.Message{{Role: "user", Content: "hi"}}, nil
}

// stubLoop is superseded by stubLoopEmitter; kept removed below.

// fakeBus is an in-memory EventSource that both records Emit and fans to subscribers.
type fakeBus struct {
	mu   sync.Mutex
	subs []chan dto.Event
}

func (b *fakeBus) Emit(ev dto.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (b *fakeBus) Subscribe(filter func(dto.Event) bool) <-chan dto.Event {
	ch := make(chan dto.Event, 256)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

func newTestServer(t *testing.T) (*Server, *fakeBus) {
	bus := &fakeBus{}
	// A loop that emits into the bus after Run, carrying the real turnID.
	loop := &stubLoopEmitter{bus: bus}
	srv, err := New(Deps{
		Fleet:    stubFleet{models: []dto.Model{{Name: "gemma4-12b", BaseURL: "http://x/v1", Capabilities: dto.Capabilities{ContextLength: 131072}}}},
		Modes:    stubModes{},
		Tools:    stubTools{},
		Doc:      stubDoc{},
		Sessions: stubSessions{},
		Loop:     loop,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	return srv, bus
}

// stubLoopEmitter implements loop.Interface and pushes events into the bus keyed
// by the returned turnID, so /turn's correlation can be asserted end-to-end.
type stubLoopEmitter struct{ bus EventSource }

func (s *stubLoopEmitter) Run(ctx context.Context, task dto.Task) (string, error) {
	id := "t1"
	bus, _ := s.bus.(*fakeBus)
	go func() {
		time.Sleep(20 * time.Millisecond)
		bus.Emit(dto.Event{TurnID: id, Type: "token", Data: json.RawMessage(`{"text":"hi"}`)})
		bus.Emit(dto.Event{TurnID: id, Type: "done", Data: json.RawMessage(`{"degraded":false}`)})
	}()
	return id, nil
}

// ------------------------- tests -------------------------

func TestHealth(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("health body = %s", rec.Body.String())
	}
}

func TestListModels(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("models = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "gemma4-12b") {
		t.Fatalf("models body = %s", rec.Body.String())
	}
}

func TestGetBlocks(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/documents/d1/blocks", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("blocks = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "b1") {
		t.Fatalf("blocks body = %s", rec.Body.String())
	}
}

func TestOpenDocument(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(`{"path":"/tmp/x.md"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("open = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestApplyEdit(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	body := `{"blockId":"b1","text":"hi","guards":[{"blockId":"b2","hash":"abc"}]}`
	req := httptest.NewRequest(http.MethodPost, "/documents/d1/edits", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestSessions(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sessions?documentId=d1", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "s1") {
		t.Fatalf("sessions body = %s", rec.Body.String())
	}
}

func TestTurnSSE(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	defer ts.Close()

	body := `{"sessionId":"s1","modeName":"proofreader","documentId":"d1","userInput":"fix"}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/turn", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	out := string(b)
	if !strings.Contains(out, "event: token") || !strings.Contains(out, "event: done") {
		t.Fatalf("SSE stream = %q", out)
	}
}
