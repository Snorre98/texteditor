package apiserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"texteditor/internal/document"
	"texteditor/internal/filesystem"
	"texteditor/internal/fleet"
	"texteditor/internal/genapi"
	"texteditor/internal/loop"
	"texteditor/internal/session"
	"texteditor/internal/shard"
	"texteditor/internal/textformatter"
	"texteditor/internal/workspace"
	"texteditor/shared/dto"
)

// ------------------------- minimal stubs -------------------------

type stubFleet struct {
	models      []dto.Model
	states      map[string]dto.LiveState // nil → every model up
	unreachable bool
}

func (s stubFleet) ListModels() ([]dto.Model, error) {
	if s.unreachable {
		return s.models, fleet.ErrDaemonUnreachable
	}
	return s.models, nil
}
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
func (s stubFleet) ListStatus() ([]dto.ModelState, error) {
	states := make([]dto.ModelState, 0, len(s.models))
	for _, m := range s.models {
		st := dto.LiveUp
		if s.states != nil {
			if v, ok := s.states[m.Name]; ok {
				st = v
			}
		}
		if s.unreachable {
			st = dto.LiveUnknown
		}
		states = append(states, dto.ModelState{Name: m.Name, State: st})
	}
	if s.unreachable {
		return states, fleet.ErrDaemonUnreachable
	}
	return states, nil
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

func (stubTools) Register(dto.ToolDef) error { return nil }
func (stubTools) List() []dto.ToolDef        { return nil }

type stubDoc struct{}

func (stubDoc) Open(string) (dto.OpenResult, error) {
	return dto.OpenResult{DocumentID: "d1", Path: "/tmp/x.md"}, nil
}
func (stubDoc) SaveTree(string, []dto.BlockWrite, dto.SaveOptions) (dto.WriteResult, error) {
	return dto.WriteResult{
		Revision:  dto.Revision{ID: "r1", Message: "autosave @ 1", Timestamp: 1},
		Committed: true,
	}, nil
}
func (stubDoc) Blocks(string) ([]dto.Block, error) {
	return []dto.Block{{ID: "b1", Kind: dto.BlockKindParagraph, Text: "hello"}}, nil
}
func (stubDoc) ApplyEdit(context.Context, string, dto.BlockEdit) (dto.Revision, error) {
	return dto.Revision{ID: "r1", Message: "candidate", Timestamp: 1}, nil
}
func (stubDoc) Commit(string, dto.CommitOptions) (dto.WriteResult, error) {
	return dto.WriteResult{
		Revision:       dto.Revision{ID: "r1", Message: "accepted", Timestamp: 1},
		WrittenThrough: true,
		Path:           "/tmp/x.md",
		Committed:      true,
	}, nil
}
func (stubDoc) Diff(string, string, string) ([]dto.WordEdit, error) {
	return []dto.WordEdit{{BlockID: "b1", Insertions: []string{"x"}, Deletions: []string{"y"}}}, nil
}
func (stubDoc) History(string) ([]dto.Revision, error) {
	return []dto.Revision{{ID: "r1", Message: "m", Timestamp: 1}}, nil
}
func (stubDoc) Path(string) (string, error)                        { return "/tmp/x.md", nil }
func (stubDoc) Candidates(string, string) ([]dto.Candidate, error) { return nil, nil }

type stubSessions struct{}

func (stubSessions) ListByDocument(string) ([]dto.Session, error) {
	return []dto.Session{{ID: "s1", DocumentID: "d1", ModeType: "proofreader"}}, nil
}
func (stubSessions) ListByWorkspace() ([]dto.Session, error) {
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
func (stubSessions) SaveContext(string, string, json.RawMessage) error { return nil }
func (stubSessions) TurnContext(string) (json.RawMessage, error) {
	return json.RawMessage(`{"turnId":"t1","sessionId":"s1","workspaceId":"ws1","retrievalQuery":"q","autoRag":true,"messages":[{"role":"system","component":"system","tokens":1,"pinned":false}],"chunks":[],"drops":[],"budget":[],"createdAt":1}`), nil
}
func (stubSessions) SetContextPolicy(string, json.RawMessage) error { return nil }
func (stubSessions) ContextPolicy(string) (json.RawMessage, error)  { return nil, nil }
func (stubSessions) CompactHistory(string, string, int) (int64, int64, int, error) {
	return 0, 0, 0, nil
}

// stubMeter implements meter.Interface for the /sessions/{id}/meter route.
type stubMeter struct{}

func (stubMeter) Attribute(context.Context, string, string, string, dto.Breakdown, dto.ProviderCounts, dto.TurnMeasurement) (dto.AttributedBreakdown, error) {
	return dto.AttributedBreakdown{}, nil
}
func (stubMeter) AttributeCompaction(context.Context, string, string, string, dto.ProviderCounts) error {
	return nil
}
func (stubMeter) SessionUsage(context.Context, string) (int, error) { return 5, nil }
func (stubMeter) SessionBreakdown(_ context.Context, sessionID string) (dto.SessionMeter, error) {
	return dto.SessionMeter{
		SessionID:  sessionID,
		Components: []dto.SessionMeterComponent{{Component: "system", PromptTokens: 3, CompletionTokens: 2}},
		Total:      5,
	}, nil
}

// stubFilesystem implements the apiserver's filesystem.Interface.
type stubFilesystem struct {
	entries []filesystem.Entry
	err     error
}

func (s *stubFilesystem) List(context.Context, string) ([]filesystem.Entry, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.entries, nil
}
func (s *stubFilesystem) Read(context.Context, string, int) ([]byte, error) { return nil, nil }
func (s *stubFilesystem) AllowedRoots() []string                            { return []string{"/"} }
func (s *stubFilesystem) Check(string) error                                { return s.err }

// stubWorkspaces implements workspace.Interface with permissive defaults.
type stubWorkspaces struct{}

func (stubWorkspaces) ResolveOrCreate(root string) (dto.Workspace, error) {
	return dto.Workspace{ID: "ws1", Root: root}, nil
}
func (stubWorkspaces) Get(id string) (dto.Workspace, error) {
	return dto.Workspace{ID: id, Root: "/vault"}, nil
}
func (stubWorkspaces) List() ([]dto.Workspace, error) { return nil, nil }
func (stubWorkspaces) FindContaining(string) (dto.Workspace, bool, error) {
	return dto.Workspace{ID: "ws1", Root: "/vault"}, true, nil
}
func (stubWorkspaces) Scope(string) (dto.CorpusScope, error) { return dto.CorpusScope{}, nil }
func (stubWorkspaces) SetScope(string, dto.CorpusScope) (dto.CorpusScope, error) {
	return dto.CorpusScope{}, nil
}
func (stubWorkspaces) StartJob(string, string, int) (dto.CorpusJob, error) {
	return dto.CorpusJob{}, nil
}
func (stubWorkspaces) UpdateJob(string, int, string, string) error { return nil }
func (stubWorkspaces) LatestJob(string) (dto.CorpusJob, bool, error) {
	return dto.CorpusJob{}, false, nil
}
func (stubWorkspaces) MarkEvicted(string, string) error         { return nil }
func (stubWorkspaces) ClearEvicted(string, string) error        { return nil }
func (stubWorkspaces) EvictedPaths(string) ([]string, error)    { return nil, nil }
func (stubWorkspaces) MarkError(string, string, string) error   { return nil }
func (stubWorkspaces) ClearError(string, string) error          { return nil }
func (stubWorkspaces) Errors(string) (map[string]string, error) { return nil, nil }
func (stubWorkspaces) RouteSession(string, string) error        { return nil }
func (stubWorkspaces) SessionWorkspace(string) (string, bool, error) {
	return "ws1", true, nil
}
func (stubWorkspaces) RouteTurn(string, string, string) error { return nil }
func (stubWorkspaces) TurnRoute(string) (string, string, bool, error) {
	return "ws1", "s1", true, nil
}

var _ workspace.Interface = stubWorkspaces{}

// emptyWorkspaces resolves no turn/session routing — the typed-404 path.
type emptyWorkspaces struct{ stubWorkspaces }

func (emptyWorkspaces) TurnRoute(string) (string, string, bool, error) { return "", "", false, nil }
func (emptyWorkspaces) SessionWorkspace(string) (string, bool, error)  { return "", false, nil }

// stubShards returns one fixed shard lease carrying the stub session store.
type stubShards struct{ svc *shard.Services }

func (s stubShards) Services(context.Context, string) (*shard.Lease, error) {
	return shard.NewLease(*s.svc), nil
}

// shardDeps builds the shard/registry deps every test server needs.
func shardDeps() (workspace.Interface, shard.Resolver) {
	return stubWorkspaces{}, stubShards{svc: &shard.Services{Sessions: stubSessions{}}}
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
		Fleet:      stubFleet{models: []dto.Model{{Name: "gemma4-12b", BaseURL: "http://x/v1", Capabilities: dto.Capabilities{ContextLength: 131072}}}},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       loop,
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

// ResolveLocate satisfies loop.Interface; these stubs never open a picker.
func (*stubLoopEmitter) ResolveLocate(string, dto.LocateChoice) error { return nil }

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
		Fleet:      stubFleet{models: []dto.Model{{Name: "gemma4-12b", BaseURL: "http://x/v1", Capabilities: dto.Capabilities{ContextLength: 131072}}}},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       &stubLoopEmitter{bus: bus},
		BaseURL:    "http://127.0.0.1:41234",
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

func TestGetFleet(t *testing.T) {
	bus := &fakeBus{}
	srv, err := New(Deps{
		Fleet: stubFleet{
			models: []dto.Model{
				{Name: "gemma4-12b", BaseURL: "http://x/v1", Capabilities: dto.Capabilities{ContextLength: 131072}, ModeTags: []string{"editor"}},
				{Name: "gemma4-26b", BaseURL: "http://y/v1"},
			},
			states: map[string]dto.LiveState{"gemma4-12b": dto.LiveUp, "gemma4-26b": dto.LiveDown},
		},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       &stubLoopEmitter{bus: bus},
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/fleet", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fleet = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Control string `json:"control"`
		Models  []struct {
			Name      string `json:"name"`
			LiveState string `json:"liveState"`
			BaseURL   string `json:"baseUrl"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Control != "up" {
		t.Fatalf("control = %q, want up", body.Control)
	}
	if len(body.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(body.Models))
	}
	if body.Models[0].LiveState != "up" || body.Models[0].BaseURL != "http://x/v1" {
		t.Fatalf("models[0] = %+v, want gemma4-12b up", body.Models[0])
	}
	if body.Models[1].LiveState != "down" {
		t.Fatalf("models[1] = %+v, want gemma4-26b down", body.Models[1])
	}
}

func TestGetFleetDaemonUnreachable(t *testing.T) {
	// A control-plane outage is data, not an error (ADR-0040 §3): 200 with
	// control: unreachable and the last-known projection labeled unknown.
	bus := &fakeBus{}
	srv, err := New(Deps{
		Fleet: stubFleet{
			models:      []dto.Model{{Name: "gemma4-12b", BaseURL: "http://x/v1"}},
			unreachable: true,
		},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       &stubLoopEmitter{bus: bus},
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/fleet", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fleet = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Control string `json:"control"`
		Models  []struct {
			Name      string `json:"name"`
			LiveState string `json:"liveState"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Control != "unreachable" {
		t.Fatalf("control = %q, want unreachable", body.Control)
	}
	if len(body.Models) != 1 || body.Models[0].LiveState != "unknown" {
		t.Fatalf("models = %+v, want the last-good projection with unknown states", body.Models)
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
	ws := &stubFilesystem{entries: []filesystem.Entry{
		{Name: "a.md", Path: "/vault/a.md", IsDir: false},
		{Name: "notes", Path: "/vault/notes", IsDir: true},
	}}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: ws,
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       &stubLoopEmitter{bus: bus},
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
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{err: filesystem.ErrNotFound},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       &stubLoopEmitter{bus: bus},
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

// ResolveLocate satisfies loop.Interface.
func (c *captureLoop) ResolveLocate(string, dto.LocateChoice) error { return nil }

func TestStartTurnDecodesMentions(t *testing.T) {
	bus := &fakeBus{}
	loop := &captureLoop{bus: bus}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       loop,
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
		Filesystem:  &stubFilesystem{},
		Workspaces:  stubWorkspaces{},
		Shards:      stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
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

// ------------------------- write-through E2E (ADR-0047) -------------------------

// stagingLoop simulates the model path: a stubbed turn stages an edit_markdown
// candidate through the real document store (the same seam the executor handler
// uses, including the echoed base-hash guard), then emits done.
type stagingLoop struct {
	doc  document.Interface
	bus  *fakeBus
	text string

	mu  sync.Mutex
	err error
}

func (s *stagingLoop) Run(ctx context.Context, task dto.Task) (string, error) {
	id := "t1"
	blocks, err := s.doc.Blocks(task.DocumentID)
	if err != nil {
		s.setErr(err)
	} else if len(blocks) > 0 {
		if _, err := s.doc.ApplyEdit(ctx, task.DocumentID, dto.BlockEdit{
			BlockID: blocks[0].ID,
			Text:    s.text,
			Mode:    task.ModeName,
			Guards:  []dto.Guard{{BlockID: blocks[0].ID, Hash: blocks[0].Hash}},
		}); err != nil {
			s.setErr(err)
		}
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.bus.Emit(dto.Event{TurnID: id, Type: "done", Data: json.RawMessage(`{"degraded":false}`)})
	}()
	return id, nil
}

func (s *stagingLoop) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// ResolveLocate satisfies loop.Interface.
func (s *stagingLoop) ResolveLocate(string, dto.LocateChoice) error { return nil }

func (s *stagingLoop) runErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// newRealDocServer wires the API server to a real document store over a temp
// app.db, git repo, and worktree — the HTTP-level acceptance surface.
func newRealDocServer(t *testing.T, notePath string, loop *stagingLoop) (*Server, *fakeBus) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := document.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ds, err := document.NewStore(db, filepath.Join(dir, "git"), filepath.Join(dir, "worktree"), textformatter.New())
	if err != nil {
		t.Fatal(err)
	}
	loop.doc = ds
	bus := &fakeBus{}
	loop.bus = bus
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        ds,
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       loop,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	return srv, bus
}

// openNote opens the note over HTTP and returns the document id.
func openNote(t *testing.T, srv *Server, notePath string) string {
	t.Helper()
	openBody, _ := json.Marshal(map[string]string{"path": notePath})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(string(openBody)))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("open = %d body %s", rec.Code, rec.Body.String())
	}
	var docBody struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &docBody); err != nil {
		t.Fatal(err)
	}
	return docBody.ID
}

// openAndTurn opens the note over HTTP, runs one stubbed turn that stages an
// edit, and returns the document id.
func openAndTurn(t *testing.T, srv *Server, loop *stagingLoop, notePath string) string {
	t.Helper()
	docID := openNote(t, srv, notePath)

	turnBody, _ := json.Marshal(map[string]string{
		"sessionId": "s1", "modeName": "proofreader", "documentId": docID, "userInput": "rewrite",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/turn", strings.NewReader(string(turnBody)))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("turn = %d body %s", rec.Code, rec.Body.String())
	}
	if err := loop.runErr(); err != nil {
		t.Fatalf("staging turn failed: %v", err)
	}
	return docID
}

func TestWriteThroughE2E(t *testing.T) {
	notePath := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(notePath, []byte("original paragraph"), 0o644); err != nil {
		t.Fatal(err)
	}
	loop := &stagingLoop{text: "rewritten by the model"}
	srv, _ := newRealDocServer(t, notePath, loop)
	id := openAndTurn(t, srv, loop, notePath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/documents/"+id+"/commits", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit = %d body %s", rec.Code, rec.Body.String())
	}
	var rev struct {
		ID             string `json:"id"`
		Message        string `json:"message"`
		WrittenThrough bool   `json:"writtenThrough"`
		Path           string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rev); err != nil {
		t.Fatal(err)
	}
	if rev.ID == "" {
		t.Fatalf("commit revision not populated: %s", rec.Body.String())
	}
	if !rev.WrittenThrough || rev.Path == "" {
		t.Fatalf("commit response must report write-through + path: %s", rec.Body.String())
	}
	if !strings.Contains(rev.Message, "proofreader") {
		t.Fatalf("derived message = %q, want the preset", rev.Message)
	}
	if got := readFile(t, notePath); got != "rewritten by the model" {
		t.Fatalf("bytes on disk = %q, want the accepted rewrite", got)
	}
}

func TestWriteThroughConflictE2E(t *testing.T) {
	notePath := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(notePath, []byte("original paragraph"), 0o644); err != nil {
		t.Fatal(err)
	}
	loop := &stagingLoop{text: "rewritten by the model"}
	srv, _ := newRealDocServer(t, notePath, loop)
	id := openAndTurn(t, srv, loop, notePath)

	// An external editor changes the file between the turn and the accept.
	if err := os.WriteFile(notePath, []byte("external edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/documents/"+id+"/commits", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("commit = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var conflict struct {
		Error       string `json:"error"`
		Path        string `json:"path"`
		CurrentHash string `json:"currentHash"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Error != "file-changed-externally" {
		t.Fatalf("error = %q", conflict.Error)
	}
	sum := sha256.Sum256([]byte("external edit"))
	if want := hex.EncodeToString(sum[:]); conflict.CurrentHash != want {
		t.Fatalf("currentHash = %q, want %q", conflict.CurrentHash, want)
	}
	if got := readFile(t, notePath); got != "external edit" {
		t.Fatalf("conflict clobbered the disk: %q", got)
	}

	// The explicit overwrite escape hatch accepts the write.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/documents/"+id+"/commits", strings.NewReader(`{"overwrite":true}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("overwrite commit = %d body %s", rec.Code, rec.Body.String())
	}
	if got := readFile(t, notePath); got != "rewritten by the model" {
		t.Fatalf("file after overwrite = %q", got)
	}
}

// TestSaveDocumentConflictAndReopenE2E covers the ADR-0047 §4 no-op save over an
// external change (409, no write) and the Open revalidation notice.
func TestSaveDocumentConflictAndReopenE2E(t *testing.T) {
	notePath := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(notePath, []byte("original paragraph"), 0o644); err != nil {
		t.Fatal(err)
	}
	loop := &stagingLoop{text: "unused"}
	srv, _ := newRealDocServer(t, notePath, loop)
	id := openNote(t, srv, notePath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/documents/"+id+"/blocks", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("blocks = %d body %s", rec.Code, rec.Body.String())
	}
	var blocks []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &blocks); err != nil || len(blocks) == 0 {
		t.Fatalf("blocks body = %s (%v)", rec.Body.String(), err)
	}

	// External edit, then an explicit save of the unchanged tree: 409, no clobber.
	if err := os.WriteFile(notePath, []byte("external edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	treeBody, _ := json.Marshal(map[string]interface{}{
		"blocks":       []map[string]string{{"id": blocks[0].ID, "kind": "paragraph", "text": "original paragraph"}},
		"writeThrough": true,
	})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/documents/"+id+"/tree", strings.NewReader(string(treeBody)))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("save = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if got := readFile(t, notePath); got != "external edit" {
		t.Fatalf("save conflict clobbered the disk: %q", got)
	}

	// Re-open revalidates: the external change is re-read and reported.
	reopenBody, _ := json.Marshal(map[string]string{"path": notePath})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(string(reopenBody)))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reopen = %d body %s", rec.Code, rec.Body.String())
	}
	var docBody struct {
		ExternalChange bool `json:"externalChange"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &docBody); err != nil {
		t.Fatal(err)
	}
	if !docBody.ExternalChange {
		t.Fatalf("reopen must report externalChange: %s", rec.Body.String())
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ------------------------- context + meter routes (ADR-0044 §4) -------------------------

// newRouteServer builds a server over the supplied workspace resolver and shard
// services for the context/meter route tests.
func newRouteServer(t *testing.T, ws workspace.Interface, svc *shard.Services) *Server {
	t.Helper()
	bus := &fakeBus{}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: ws,
		Shards:     stubShards{svc: svc},
		Loop:       &stubLoopEmitter{bus: bus},
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestGetTurnContext(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/turns/t1/context", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("turn context = %d body %s", rec.Code, rec.Body.String())
	}
	var snap struct {
		TurnID      string `json:"turnId"`
		SessionID   string `json:"sessionId"`
		WorkspaceID string `json:"workspaceId"`
		AutoRag     bool   `json:"autoRag"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.TurnID != "t1" || snap.SessionID != "s1" || snap.WorkspaceID != "ws1" || !snap.AutoRag {
		t.Fatalf("snapshot = %+v, want the persisted t1/s1/ws1 snapshot", snap)
	}
}

func TestGetTurnContextUnknownIsTyped404(t *testing.T) {
	srv := newRouteServer(t, emptyWorkspaces{}, &shard.Services{Sessions: stubSessions{}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/turns/nope/context", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("turn context = %d body %s, want 404", rec.Code, rec.Body.String())
	}
	var nf struct {
		Error    string `json:"error"`
		Resource string `json:"resource"`
		ID       string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &nf); err != nil {
		t.Fatal(err)
	}
	if nf.Error != "not-found" || nf.Resource != "turn" || nf.ID != "nope" {
		t.Fatalf("not-found body = %+v", nf)
	}
}

func TestGetSessionMeter(t *testing.T) {
	srv := newRouteServer(t, stubWorkspaces{}, &shard.Services{Sessions: stubSessions{}, Meter: stubMeter{}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sessions/s1/meter", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session meter = %d body %s", rec.Code, rec.Body.String())
	}
	var m struct {
		SessionID  string `json:"sessionId"`
		Total      int    `json:"total"`
		Components []struct {
			Component    string `json:"component"`
			PromptTokens int    `json:"promptTokens"`
		} `json:"components"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.SessionID != "s1" || m.Total != 5 || len(m.Components) != 1 || m.Components[0].Component != "system" {
		t.Fatalf("session meter = %+v", m)
	}
}

func TestGetSessionMeterUnknownIsTyped404(t *testing.T) {
	srv := newRouteServer(t, emptyWorkspaces{}, &shard.Services{Sessions: stubSessions{}, Meter: stubMeter{}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sessions/nope/meter", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("session meter = %d body %s, want 404", rec.Code, rec.Body.String())
	}
	var nf struct {
		Error    string `json:"error"`
		Resource string `json:"resource"`
		ID       string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &nf); err != nil {
		t.Fatal(err)
	}
	if nf.Error != "not-found" || nf.Resource != "session" || nf.ID != "nope" {
		t.Fatalf("not-found body = %+v", nf)
	}
}

// testSnapshotE2E is a full ContextSnapshot payload (messages/chunks/drops/
// budget) used to prove the route returns exactly the persisted engine data.
const testSnapshotE2E = `{"turnId":"t1","sessionId":"s1","workspaceId":"ws1","retrievalQuery":"fix","autoRag":true,"messages":[{"role":"system","component":"system","tokens":3,"pinned":false}],"chunks":[{"blockId":"b1","chunkKey":"/v/a.md#0","text":"hi","path":"/v/a.md","heading":"Intro"}],"drops":[{"component":"history","reason":"history-budget","count":2}],"budget":[{"component":"history","used":1,"limit":10}],"createdAt":2}`

// snapshotLoop persists a snapshot through the real session store and emits it
// as the `context` event, so the E2E can compare the route's response with the
// event payload.
type snapshotLoop struct {
	sessions session.Interface
	bus      *fakeBus
	snapshot []byte
}

func (s *snapshotLoop) Run(_ context.Context, task dto.Task) (string, error) {
	const id = "t1"
	if err := s.sessions.SaveContext(id, task.SessionID, s.snapshot); err != nil {
		return "", err
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.bus.Emit(dto.Event{TurnID: id, Type: "context", Data: s.snapshot})
		s.bus.Emit(dto.Event{TurnID: id, Type: "done", Data: json.RawMessage(`{}`)})
	}()
	return id, nil
}

// ResolveLocate satisfies loop.Interface.
func (s *snapshotLoop) ResolveLocate(string, dto.LocateChoice) error { return nil }

// TestContextSnapshotE2EMatchesEvent covers context-inspector "A completed turn
// persists a context snapshot": the snapshot persisted through the real session
// store is returned by GET /turns/{id}/context and is the same data the loop
// emitted as the `context` event.
func TestContextSnapshotE2EMatchesEvent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := session.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store := session.New(db)

	bus := &fakeBus{}
	loop := &snapshotLoop{sessions: store, bus: bus, snapshot: []byte(testSnapshotE2E)}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: store}},
		Loop:       loop,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/turn", strings.NewReader(`{"sessionId":"s1","modeName":"proofreader","documentId":"d1","userInput":"fix"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "event: context") {
		t.Fatalf("SSE stream missing context event: %s", body)
	}

	rec := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/turns/t1/context", nil)
	srv.ServeHTTP(rec, getReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("turn context = %d body %s", rec.Code, rec.Body.String())
	}

	var fromEvent, fromRoute genapi.ContextSnapshot
	if err := json.Unmarshal([]byte(testSnapshotE2E), &fromEvent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fromRoute); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromEvent, fromRoute) {
		t.Fatalf("route snapshot != context event:\n route: %+v\n event: %+v", fromRoute, fromEvent)
	}
}

// --------------------- session context policy (ADR-0049 §8) ---------------------

// captureContextLoop records the dto.Task.Context it is handed.
type captureContextLoop struct {
	bus    *fakeBus
	mu     sync.Mutex
	policy *dto.ContextPolicy
}

func (c *captureContextLoop) Run(_ context.Context, task dto.Task) (string, error) {
	c.mu.Lock()
	c.policy = task.Context
	c.mu.Unlock()
	id := "t1"
	go func() {
		c.bus.Emit(dto.Event{TurnID: id, Type: "done", Data: json.RawMessage(`{"degraded":false}`)})
	}()
	return id, nil
}

// ResolveLocate satisfies loop.Interface.
func (c *captureContextLoop) ResolveLocate(string, dto.LocateChoice) error { return nil }

func TestStartTurnDecodesContext(t *testing.T) {
	bus := &fakeBus{}
	loop := &captureContextLoop{bus: bus}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       loop,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"sessionId":"s1","modeName":"proofreader","documentId":"d1","userInput":"fix",` +
		`"context":{"pinned":[{"path":"/v/a.md","chunkKey":"/v/a.md#0"}],"autoRag":false,"retrievalQuery":"rq"}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/turn", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("turn = %d body %s", rec.Code, rec.Body.String())
	}

	loop.mu.Lock()
	defer loop.mu.Unlock()
	if loop.policy == nil {
		t.Fatal("Task.context not decoded")
	}
	if len(loop.policy.Pinned) != 1 || loop.policy.Pinned[0].Path != "/v/a.md" || loop.policy.Pinned[0].ChunkKey != "/v/a.md#0" {
		t.Fatalf("pinned = %+v", loop.policy.Pinned)
	}
	if loop.policy.AutoRag == nil || *loop.policy.AutoRag {
		t.Fatalf("autoRag = %v, want false", loop.policy.AutoRag)
	}
	if loop.policy.RetrievalQuery == nil || *loop.policy.RetrievalQuery != "rq" {
		t.Fatalf("retrievalQuery = %v", loop.policy.RetrievalQuery)
	}
}

// TestStartTurnContextEmptyListPreserved: an explicit empty pinned list must
// decode to a non-nil empty slice (replace-when-present clear), not nil.
func TestStartTurnContextEmptyListPreserved(t *testing.T) {
	bus := &fakeBus{}
	loop := &captureContextLoop{bus: bus}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       loop,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"sessionId":"s1","modeName":"proofreader","documentId":"d1","userInput":"fix","context":{"pinned":[]}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/turn", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)

	loop.mu.Lock()
	defer loop.mu.Unlock()
	if loop.policy == nil || loop.policy.Pinned == nil || len(loop.policy.Pinned) != 0 {
		t.Fatalf("explicit empty pinned list not preserved: %+v", loop.policy)
	}
}

// policySessions is a stateful session.Interface for the PUT context route.
type policySessions struct {
	stubSessions
	mu     sync.Mutex
	policy json.RawMessage
}

func (p *policySessions) Resume(id string) (dto.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return dto.Session{ID: id, DocumentID: "d1", ContextPolicy: p.policy}, nil
}

func (p *policySessions) SetContextPolicy(_ string, policy json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.policy = append(json.RawMessage(nil), policy...)
	return nil
}

func (p *policySessions) ContextPolicy(string) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.policy, nil
}

func TestPutSessionContextPersistsAndReadsBack(t *testing.T) {
	bus := &fakeBus{}
	ps := &policySessions{}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: ps}},
		Loop:       &stubLoopEmitter{bus: bus},
	}, bus)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"pinned":[{"path":"/v/a.md","chunkKey":"/v/a.md#0"}],"excluded":[{"path":"/v/b.md"}],"autoRag":false,"retrievalQuery":"rq"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/sessions/s1/context", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("put context = %d body %s", rec.Code, rec.Body.String())
	}
	var got genapi.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.ContextPolicy.IsSet() {
		t.Fatalf("response session lacks contextPolicy: %s", rec.Body.String())
	}
	cp := got.ContextPolicy.Value
	if len(cp.Pinned) != 1 || cp.Pinned[0].Path != "/v/a.md" {
		t.Fatalf("read-back pinned = %+v", cp.Pinned)
	}
	if v, ok := cp.AutoRag.Get(); !ok || v {
		t.Fatalf("read-back autoRag = %v, want false", cp.AutoRag)
	}
	raw, err := ps.ContextPolicy("s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"autoRag":false`) || !strings.Contains(string(raw), `"chunkKey":"/v/a.md#0"`) {
		t.Fatalf("persisted policy = %s", raw)
	}
}

func TestPutSessionContextUnknownIsTyped404(t *testing.T) {
	bus := &fakeBus{}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: emptyWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       &stubLoopEmitter{bus: bus},
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/sessions/nope/context", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not-found") || !strings.Contains(rec.Body.String(), "session") {
		t.Fatalf("404 body = %s", rec.Body.String())
	}
}

// --------------------- /locate picker route (ADR-0048 §4) ---------------------

// locateLoop records ResolveLocate calls and returns a configured error.
type locateLoop struct {
	mu      sync.Mutex
	choices []dto.LocateChoice
	err     error
}

func (*locateLoop) Run(context.Context, dto.Task) (string, error) { return "t1", nil }

func (l *locateLoop) ResolveLocate(_ string, c dto.LocateChoice) error {
	l.mu.Lock()
	l.choices = append(l.choices, c)
	l.mu.Unlock()
	return l.err
}

func (l *locateLoop) recorded() []dto.LocateChoice {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]dto.LocateChoice(nil), l.choices...)
}

func TestResolveLocateRoute204(t *testing.T) {
	bus := &fakeBus{}
	ll := &locateLoop{}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       ll,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/turns/t1/locate", strings.NewReader(`{"chunkKey":"b1"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("resolve locate = %d, body %s", rec.Code, rec.Body.String())
	}
	got := ll.recorded()
	if len(got) != 1 || got[0].ChunkKey != "b1" || got[0].Cancel {
		t.Fatalf("choice = %+v, want chunkKey b1", got)
	}
}

func TestResolveLocateUnknownTurn404(t *testing.T) {
	bus := &fakeBus{}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: emptyWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       &locateLoop{},
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/turns/nope/locate", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown turn = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not-found") || !strings.Contains(rec.Body.String(), "turn") {
		t.Fatalf("404 body = %s", rec.Body.String())
	}
}

func TestResolveLocateNoPending409(t *testing.T) {
	bus := &fakeBus{}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Filesystem: &stubFilesystem{},
		Workspaces: stubWorkspaces{},
		Shards:     stubShards{svc: &shard.Services{Sessions: stubSessions{}}},
		Loop:       &locateLoop{err: loop.ErrNoPendingLocate},
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/turns/t1/locate", strings.NewReader(`{"cancel":true}`))
	req.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("no pending locate = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no-pending-locate") {
		t.Fatalf("409 body = %s", rec.Body.String())
	}
}
