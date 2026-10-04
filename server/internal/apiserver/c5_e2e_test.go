package apiserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"texteditor/internal/assembler"
	"texteditor/internal/loop"
	"texteditor/internal/meter"
	"texteditor/internal/session"
	"texteditor/internal/shard"
	"texteditor/internal/workspace"
	"texteditor/shared/dto"
)

// replayBus is an EventSource that buffers every emitted event and replays the
// buffered events to a late subscriber. It closes the Run/subscribe race that a
// real in-process loop otherwise has (the loop starts async; StartTurn
// subscribes after Run returns).
type replayBus struct {
	mu     sync.Mutex
	events []dto.Event
	subs   []chan dto.Event
}

func (b *replayBus) Emit(ev dto.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (b *replayBus) Subscribe(filter func(dto.Event) bool) <-chan dto.Event {
	ch := make(chan dto.Event, 512)
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ev := range b.events {
		if filter == nil || filter(ev) {
			select {
			case ch <- ev:
			default:
			}
		}
	}
	b.subs = append(b.subs, ch)
	return ch
}

// ------------------------- C5 stubs -------------------------

type c5Modes struct{}

func (c5Modes) List() []dto.Mode { return nil }
func (c5Modes) Get(name string) (dto.Mode, error) {
	return dto.Mode{Name: name, SystemPrompt: "You are helpful.", DefaultModel: "gemma4-12b"}, nil
}

type c5Tools struct{}

func (c5Tools) Register(dto.ToolDef) error { return nil }
func (c5Tools) List() []dto.ToolDef        { return nil }

type c5Executor struct{}

func (c5Executor) Invoke(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

type c5Provider struct {
	stream func(context.Context, func(dto.RawEvent)) error
}

func (c5Provider) Chat(context.Context, dto.Target, dto.Request) (dto.Completion, error) {
	return dto.Completion{}, nil
}
func (p c5Provider) Stream(ctx context.Context, _ dto.Target, _ dto.Request, emit func(dto.RawEvent)) error {
	return p.stream(ctx, emit)
}
func (c5Provider) Embed(context.Context, dto.Target, string) ([]float32, error) { return nil, nil }

type c5Fleet struct{ res dto.Resolution }

func (c5Fleet) ListModels() ([]dto.Model, error)                          { return nil, nil }
func (f c5Fleet) Resolve(string, dto.ResolveOpts) (dto.Resolution, error) { return f.res, nil }
func (c5Fleet) Status(string) (dto.LiveState, error)                      { return dto.LiveUp, nil }
func (c5Fleet) ListStatus() ([]dto.ModelState, error)                     { return nil, nil }
func (c5Fleet) Start(string) error                                        { return nil }
func (c5Fleet) Stop(string) error                                         { return nil }
func (c5Fleet) Provision(context.Context, string) (string, error)         { return "", nil }
func (c5Fleet) Fingerprint(string) (string, error)                        { return "", nil }

type c5Pipeline struct{ policy dto.PipelinePolicy }

func (p c5Pipeline) Policy() dto.PipelinePolicy { return p.policy }

type c5Retriever struct{}

func (c5Retriever) Query(context.Context, string, int) ([]dto.Chunk, error) { return nil, nil }
func (c5Retriever) Index(context.Context, string) error                     { return nil }
func (c5Retriever) SearchText(context.Context, string, int) ([]dto.Chunk, error) {
	return nil, nil
}
func (c5Retriever) IndexPath(context.Context, string, string) error { return nil }
func (c5Retriever) Evict(context.Context, string) error             { return nil }
func (c5Retriever) Status() ([]dto.IndexedDocument, error)          { return nil, nil }
func (c5Retriever) Get(context.Context, []dto.ChunkRef) ([]dto.Chunk, error) {
	return nil, nil
}

type c5Resolver struct{ svc *shard.Services }

func (r c5Resolver) Services(context.Context, string) (*shard.Lease, error) {
	return shard.NewLease(*r.svc), nil
}

// budgetSessionStore wraps a real session store, forcing a TokenBudget on Resume
// so the over-budget refusal path can be exercised over HTTP.
type budgetSessionStore struct {
	session.Interface
	budget int
}

func (b budgetSessionStore) Resume(id string) (dto.Session, error) {
	s, _ := b.Interface.Resume(id)
	s.TokenBudget = &b.budget
	return s, nil
}

// newC5E2EServer wires a real loop + real session/meter stores (temp dirs) behind
// the HTTP surface, with a stub provider.
func newC5E2EServer(t *testing.T, policy dto.PipelinePolicy, res dto.Resolution, stream func(context.Context, func(dto.RawEvent)) error, budget *int) (*Server, *replayBus) {
	t.Helper()
	dataDir := t.TempDir()

	wsDB, err := sql.Open("sqlite", filepath.Join(dataDir, "workspaces.db"))
	if err != nil {
		t.Fatal(err)
	}
	wsDB.SetMaxOpenConns(1)
	t.Cleanup(func() { wsDB.Close() })
	if err := workspace.Migrate(context.Background(), wsDB); err != nil {
		t.Fatal(err)
	}
	wsStore := workspace.New(wsDB)

	sessDB, err := sql.Open("sqlite", filepath.Join(dataDir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	sessDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sessDB.Close() })
	if err := session.Migrate(context.Background(), sessDB); err != nil {
		t.Fatal(err)
	}
	meterDB, err := sql.Open("sqlite", filepath.Join(dataDir, "meter.db"))
	if err != nil {
		t.Fatal(err)
	}
	meterDB.SetMaxOpenConns(1)
	t.Cleanup(func() { meterDB.Close() })
	bus := &replayBus{}
	if err := meter.Migrate(context.Background(), meterDB); err != nil {
		t.Fatal(err)
	}

	var sessStore session.Interface = session.New(sessDB)
	if budget != nil {
		sessStore = budgetSessionStore{Interface: sessStore, budget: *budget}
	}
	svc := &shard.Services{Retriever: c5Retriever{}, Sessions: sessStore, Meter: meter.New(meterDB, bus)}
	shards := c5Resolver{svc: svc}

	loopGW := loop.New(loop.Deps{
		Modes:      c5Modes{},
		Tools:      c5Tools{},
		Executor:   c5Executor{},
		Assembler:  assembler.New(),
		Provider:   c5Provider{stream: stream},
		Fleet:      c5Fleet{res: res},
		Doc:        stubDoc{},
		Shards:     shards,
		Workspaces: wsStore,
		Bus:        bus,
		Pipeline:   c5Pipeline{policy: policy},
		Filesystem: &stubFilesystem{},
	})

	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Loop:       loopGW,
		Filesystem: &stubFilesystem{},
		Workspaces: wsStore,
		Shards:     shards,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	return srv, bus
}

// postTurnSSE posts a turn and returns the raw SSE body.
func postTurnSSE(t *testing.T, srv *Server, body map[string]interface{}) string {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/turn", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Body.String()
}

func sseEvent(body, name string) (string, bool) {
	for _, block := range strings.Split(body, "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) >= 2 && strings.TrimPrefix(lines[0], "event: ") == name {
			return strings.TrimPrefix(lines[1], "data: "), true
		}
	}
	return "", false
}

func baseC5Policy() dto.PipelinePolicy {
	return dto.PipelinePolicy{
		MaxSteps: 6, MaxHistoryTokens: 100000, MaxRagTokens: 100000, MaxMentionTokens: 100000,
		AutoRagTopK: 3, Thinking: dto.ThinkingOn, ReserveOutputTokens: 0,
	}
}

// TestC5ThinkingEventHTTPE2E: a streaming turn exposes the `thinking` event over
// the hand-framed SSE surface (ADR-0051 §4).
func TestC5ThinkingEventHTTPE2E(t *testing.T) {
	stream := func(_ context.Context, emit func(dto.RawEvent)) error {
		emit(dto.RawEvent{Type: "reasoning", Data: json.RawMessage(`{"text":"reasoning..."}`)})
		emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"answer"}`)})
		emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
		emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":9,"outputTokens":7,"thinkingTokens":3}`)})
		return nil
	}
	res := dto.Resolution{
		Model:           dto.Model{Name: "gemma4-12b", BaseURL: "http://stub/v1", ModelID: "gemma4-12b", Runner: "mlx-lm", Capabilities: dto.Capabilities{ContextLength: 131072}},
		EffectiveParams: dto.SamplingParams{Temperature: 0.3, MaxTokens: 100},
		UsedName:        "gemma4-12b",
	}
	srv, _ := newC5E2EServer(t, baseC5Policy(), res, stream, nil)

	body := postTurnSSE(t, srv, map[string]interface{}{
		"sessionId": "s1", "modeName": "proofreader", "documentId": "d1", "userInput": "fix",
	})
	if _, ok := sseEvent(body, "thinking"); !ok {
		t.Fatalf("no thinking event in stream:\n%s", body)
	}
	if _, ok := sseEvent(body, "done"); !ok {
		t.Fatalf("no done event in stream:\n%s", body)
	}
}

// TestC5ContextWindowRefusalHTTPE2E: fixed + pinned + user over the window is a
// typed refusal before any provider call (ADR-0051 §6).
func TestC5ContextWindowRefusalHTTPE2E(t *testing.T) {
	called := false
	stream := func(_ context.Context, emit func(dto.RawEvent)) error {
		called = true
		emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"answer"}`)})
		emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
		return nil
	}
	res := dto.Resolution{
		Model:           dto.Model{Name: "gemma4-12b", BaseURL: "http://stub/v1", ModelID: "gemma4-12b", Runner: "mlx-lm", Capabilities: dto.Capabilities{ContextLength: 5}},
		EffectiveParams: dto.SamplingParams{Temperature: 0.3, MaxTokens: 100},
		UsedName:        "gemma4-12b",
	}
	srv, _ := newC5E2EServer(t, baseC5Policy(), res, stream, nil)

	body := postTurnSSE(t, srv, map[string]interface{}{
		"sessionId": "s1", "modeName": "proofreader", "documentId": "d1",
		"userInput": strings.Repeat("u", 400),
	})
	data, ok := sseEvent(body, "error")
	if !ok {
		t.Fatalf("no error event:\n%s", body)
	}
	var ed struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(data), &ed)
	if ed.Code != "context-window-exceeded" {
		t.Fatalf("error code = %q, want context-window-exceeded", ed.Code)
	}
	if called {
		t.Fatalf("provider was called despite the window refusal")
	}
}

// TestC5OverBudgetRefusalHTTPE2E: the hard session budget refuses the turn with
// a typed error (ADR-0051 §7).
func TestC5OverBudgetRefusalHTTPE2E(t *testing.T) {
	stream := func(_ context.Context, emit func(dto.RawEvent)) error {
		emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"answer"}`)})
		emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
		return nil
	}
	res := dto.Resolution{
		Model:           dto.Model{Name: "gemma4-12b", BaseURL: "http://stub/v1", ModelID: "gemma4-12b", Runner: "mlx-lm", Capabilities: dto.Capabilities{ContextLength: 131072}},
		EffectiveParams: dto.SamplingParams{Temperature: 0.3, MaxTokens: 100},
		UsedName:        "gemma4-12b",
	}
	budget := 1
	srv, _ := newC5E2EServer(t, baseC5Policy(), res, stream, &budget)

	body := postTurnSSE(t, srv, map[string]interface{}{
		"sessionId": "s1", "modeName": "proofreader", "documentId": "d1",
		"userInput": strings.Repeat("u", 400),
	})
	data, ok := sseEvent(body, "error")
	if !ok {
		t.Fatalf("no error event:\n%s", body)
	}
	var ed struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(data), &ed)
	if ed.Code != "session-budget-exceeded" {
		t.Fatalf("error code = %q, want session-budget-exceeded", ed.Code)
	}
}
