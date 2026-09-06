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

	"texteditor/internal/workspace"
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
func (s stubFleet) Fingerprint(string) (string, error)                { return "", nil }

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
func (stubDoc) SaveTree(string, []dto.BlockWrite) (dto.Revision, error) {
	return dto.Revision{ID: "r1", Message: "autosave @ 1", Timestamp: 1}, nil
}
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

// stubWorkspace implements the apiserver's workspace.Interface.
type stubWorkspace struct {
	entries []workspace.Entry
	err     error
}

func (s *stubWorkspace) List(context.Context, string) ([]workspace.Entry, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.entries, nil
}
func (s *stubWorkspace) Read(context.Context, string, int) ([]byte, error) { return nil, nil }

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

func TestHealthAdvertisesBaseURL(t *testing.T) {
	// /health carries the engine's actual base URL for dynamic-port discovery
	// (ADR-0021 §1); when unset it is omitted (fixed/legacy mode).
	bus := &fakeBus{}
	srv, err := New(Deps{
		Fleet:    stubFleet{models: []dto.Model{{Name: "gemma4-12b", BaseURL: "http://x/v1", Capabilities: dto.Capabilities{ContextLength: 131072}}}},
		Modes:    stubModes{},
		Tools:    stubTools{},
		Doc:      stubDoc{},
		Sessions: stubSessions{},
		Loop:     &stubLoopEmitter{bus: bus},
		BaseURL:  "http://127.0.0.1:41234",
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"baseUrl":"http://127.0.0.1:41234"`) {
		t.Fatalf("health body missing baseUrl: %s", rec.Body.String())
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

func TestSaveDocument(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	body := `{"blocks":[{"kind":"paragraph","text":"human typed"},{"id":"b1","kind":"paragraph","text":"kept"}]}`
	req := httptest.NewRequest(http.MethodPut, "/documents/d1/tree", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "autosave") {
		t.Fatalf("save body = %s, want autosave revision", rec.Body.String())
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

func TestListDirectory(t *testing.T) {
	bus := &fakeBus{}
	ws := &stubWorkspace{entries: []workspace.Entry{
		{Name: "a.md", Path: "/vault/a.md", IsDir: false},
		{Name: "notes", Path: "/vault/notes", IsDir: true},
	}}
	srv, err := New(Deps{
		Fleet:     stubFleet{},
		Modes:     stubModes{},
		Tools:     stubTools{},
		Doc:       stubDoc{},
		Sessions:  stubSessions{},
		Loop:      &stubLoopEmitter{bus: bus},
		Workspace: ws,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/directories?path=/vault", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("directories = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "a.md") || !strings.Contains(rec.Body.String(), "notes") || !strings.Contains(rec.Body.String(), "isDir") {
		t.Fatalf("directories body = %s", rec.Body.String())
	}
}

func TestListDirectoryNotFound(t *testing.T) {
	bus := &fakeBus{}
	srv, err := New(Deps{
		Fleet:     stubFleet{},
		Modes:     stubModes{},
		Tools:     stubTools{},
		Doc:       stubDoc{},
		Sessions:  stubSessions{},
		Loop:      &stubLoopEmitter{bus: bus},
		Workspace: &stubWorkspace{err: workspace.ErrNotFound},
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/directories?path=/nope", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("directories = %d, want 500 (oproject not-found as error)", rec.Code)
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

// captureLoop captures the dto.Task it is handed, then emits a done event, so
// StartTurn's mention decoding can be asserted end-to-end.
type captureLoop struct {
	bus      *fakeBus
	mu       sync.Mutex
	mentions []string
}

func (c *captureLoop) Run(_ context.Context, task dto.Task) (string, error) {
	c.mu.Lock()
	for _, m := range task.Mentions {
		c.mentions = append(c.mentions, m.Path)
	}
	c.mu.Unlock()
	id := "t1"
	go func() {
		c.bus.Emit(dto.Event{TurnID: id, Type: "done", Data: json.RawMessage(`{"degraded":false}`)})
	}()
	return id, nil
}

func TestStartTurnDecodesMentions(t *testing.T) {
	bus := &fakeBus{}
	loop := &captureLoop{bus: bus}
	srv, err := New(Deps{
		Fleet:    stubFleet{},
		Modes:    stubModes{},
		Tools:    stubTools{},
		Doc:      stubDoc{},
		Sessions: stubSessions{},
		Loop:     loop,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"sessionId":"s1","modeName":"proofreader","documentId":"d1","userInput":"fix","mentions":[{"path":"/notes/a.md"},{"path":"/notes/b.md"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/turn", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("turn = %d body %s", rec.Code, rec.Body.String())
	}

	loop.mu.Lock()
	defer loop.mu.Unlock()
	if len(loop.mentions) != 2 || loop.mentions[0] != "/notes/a.md" || loop.mentions[1] != "/notes/b.md" {
		t.Fatalf("decoded mentions = %v, want [/notes/a.md /notes/b.md]", loop.mentions)
	}
}

func newCORSServer(t *testing.T, origins []string) *Server {
	t.Helper()
	bus := &fakeBus{}
	srv, err := New(Deps{
		Fleet:       stubFleet{},
		Modes:       stubModes{},
		Tools:       stubTools{},
		Doc:         stubDoc{},
		Sessions:    stubSessions{},
		Loop:        &stubLoopEmitter{bus: bus},
		CORSOrigins: origins,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestCORSDisabledByDefault(t *testing.T) {
	// No allowlist = no CORS headers, exactly the standalone-daemon/TUI behavior.
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "tauri://localhost")
	srv.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unexpected allow-origin header when CORS is disabled: %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSAllowsListedOrigin(t *testing.T) {
	srv := newCORSServer(t, []string{"tauri://localhost", "http://localhost:5173"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "tauri://localhost")
	srv.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "tauri://localhost" {
		t.Fatalf("allow-origin = %q, want tauri://localhost", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary = %q, want to include Origin", got)
	}
}

func TestCORSRejectsUnlistedOrigin(t *testing.T) {
	srv := newCORSServer(t, []string{"tauri://localhost"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "http://evil.example")
	srv.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unlisted origin must get no allow-origin, got %q", got)
	}
}

func TestCORSPreflightShortCircuits(t *testing.T) {
	srv := newCORSServer(t, []string{"tauri://localhost"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/turn", nil)
	req.Header.Set("Origin", "tauri://localhost")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type, accept")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "tauri://localhost" {
		t.Fatalf("preflight allow-origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "content-type, accept" {
		t.Fatalf("preflight allow-headers = %q, want content-type, accept", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") || !strings.Contains(got, "PUT") {
		t.Fatalf("preflight allow-methods = %q, want POST + PUT", got)
	}
}

func TestCORSPreflightRejectsUnlistedOrigin(t *testing.T) {
	srv := newCORSServer(t, []string{"tauri://localhost"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/turn", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d, want 204 (fail-closed)", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unlisted preflight must not be answered, got %q", got)
	}
}
