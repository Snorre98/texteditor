package loop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"texteditor/internal/assembler"
	"texteditor/internal/document"
	"texteditor/internal/filesystem"
	"texteditor/internal/locate"
	"texteditor/internal/meter"
	"texteditor/internal/retriever"
	"texteditor/internal/session"
	"texteditor/internal/shard"
	"texteditor/internal/textformatter"
	"texteditor/internal/workspace"
	"texteditor/shared/dto"
)

// --------------------------- minimal stubs ---------------------------

type stubMode struct{ modes map[string]dto.Mode }

func (s stubMode) List() []dto.Mode {
	out := make([]dto.Mode, 0, len(s.modes))
	for _, m := range s.modes {
		out = append(out, m)
	}
	return out
}
func (s stubMode) Get(name string) (dto.Mode, error) {
	m, ok := s.modes[name]
	if ok {
		return m, nil
	}
	return m, &stubErr{msg: "mode " + name + " not found"}
}

type stubTools struct{ defs []dto.ToolDef }

func (s stubTools) Register(dto.ToolDef) error { return nil }
func (s stubTools) List() []dto.ToolDef        { return s.defs }

type stubExecutor struct{}

func (stubExecutor) Invoke(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

// recordingExecutor records invocations and returns a fixed result.
type recordingExecutor struct {
	mu      sync.Mutex
	calls   []string
	result  json.RawMessage
	errResp error
}

func (r *recordingExecutor) Invoke(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	r.mu.Lock()
	r.calls = append(r.calls, name)
	r.mu.Unlock()
	return r.result, r.errResp
}

type stubAssembler struct{}

func (stubAssembler) Assemble(_ context.Context, in dto.AssemblerInput) (dto.Payload, dto.Breakdown, error) {
	return dto.Payload{Request: dto.Request{ModelName: "gemma4-12b"}}, dto.Breakdown{SystemPrompt: 10, User: 4}, nil
}

type stubProvider struct {
	stream func(ctx context.Context, emit func(dto.RawEvent)) error
	calls  *int
	reqs   *[]dto.Request
}

func (s stubProvider) Chat(context.Context, dto.Target, dto.Request) (dto.Completion, error) {
	return dto.Completion{}, nil
}
func (s stubProvider) Stream(ctx context.Context, t dto.Target, r dto.Request, emit func(dto.RawEvent)) error {
	if s.calls != nil {
		*s.calls++
	}
	if s.reqs != nil {
		*s.reqs = append(*s.reqs, r)
	}
	return s.stream(ctx, emit)
}
func (stubProvider) Embed(context.Context, dto.Target, string) ([]float32, error) { return nil, nil }

type stubFleet struct {
	res dto.Resolution
	err error
}

func (s stubFleet) ListModels() ([]dto.Model, error)                        { return nil, nil }
func (s stubFleet) Resolve(string, dto.ResolveOpts) (dto.Resolution, error) { return s.res, s.err }
func (s stubFleet) Status(string) (dto.LiveState, error)                    { return dto.LiveUp, nil }
func (s stubFleet) ListStatus() ([]dto.ModelState, error)                   { return nil, nil }
func (s stubFleet) Start(string) error                                      { return nil }
func (s stubFleet) Stop(string) error                                       { return nil }
func (s stubFleet) Provision(context.Context, string) (string, error)       { return "", nil }
func (s stubFleet) Fingerprint(string) (string, error)                      { return "", nil }

type stubDoc struct {
	blocks []dto.Block
	path   string
}

func (stubDoc) Open(string) (dto.OpenResult, error) { return dto.OpenResult{}, nil }
func (s stubDoc) Path(string) (string, error) {
	if s.path != "" {
		return s.path, nil
	}
	return "/vault/d1.md", nil
}
func (stubDoc) SaveTree(string, []dto.BlockWrite, dto.SaveOptions) (dto.WriteResult, error) {
	return dto.WriteResult{}, nil
}
func (s stubDoc) Blocks(string) ([]dto.Block, error) { return s.blocks, nil }
func (stubDoc) ApplyEdit(context.Context, string, dto.BlockEdit) (dto.Revision, error) {
	return dto.Revision{}, nil
}
func (stubDoc) Commit(string, dto.CommitOptions) (dto.WriteResult, error) {
	return dto.WriteResult{}, nil
}
func (stubDoc) Diff(string, string, string) ([]dto.WordEdit, error) { return nil, nil }
func (stubDoc) History(string) ([]dto.Revision, error)              { return nil, nil }
func (stubDoc) Candidates(string, string) ([]dto.Candidate, error)  { return nil, nil }

type stubRetriever struct {
	mu        sync.Mutex
	chunks    []dto.Chunk
	calls     []int // topK per Query
	texts     []string
	getChunks []dto.Chunk
	getRefs   [][]dto.ChunkRef
	getCalls  int

	searchChunks []dto.Chunk
	status       []dto.IndexedDocument
}

func (s *stubRetriever) Query(_ context.Context, text string, topK int) ([]dto.Chunk, error) {
	s.mu.Lock()
	s.calls = append(s.calls, topK)
	s.texts = append(s.texts, text)
	s.mu.Unlock()
	return s.chunks, nil
}
func (*stubRetriever) Index(context.Context, string) error { return nil }
func (s *stubRetriever) SearchText(_ context.Context, text string, limit int) ([]dto.Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, text)
	return s.searchChunks, nil
}
func (*stubRetriever) IndexPath(context.Context, string, string) error { return nil }
func (*stubRetriever) Evict(context.Context, string) error             { return nil }
func (s *stubRetriever) Status() ([]dto.IndexedDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, nil
}
func (s *stubRetriever) Get(_ context.Context, refs []dto.ChunkRef) ([]dto.Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	s.getRefs = append(s.getRefs, refs)
	return s.getChunks, nil
}

func (s *stubRetriever) queries() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.calls...)
}

func (s *stubRetriever) queryTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.texts...)
}

func (s *stubRetriever) getCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCalls
}

// stubPipeline supplies the one global policy (ADR-0045 §3) to the loop.
type stubPipeline struct{ policy dto.PipelinePolicy }

func (s stubPipeline) Policy() dto.PipelinePolicy { return s.policy }

// stubLocate is a configurable `/locate` resolver for loop tests.
type stubLocate struct {
	mu     sync.Mutex
	result dto.LocateResult
	err    error
	calls  int
	chunks []string
}

func (s *stubLocate) Resolve(_ context.Context, req locate.Request) (dto.LocateResult, error) {
	s.mu.Lock()
	s.calls++
	s.chunks = append(s.chunks, req.Chunk)
	s.mu.Unlock()
	return s.result, s.err
}

func (s *stubLocate) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type stubSessions struct {
	hist      []dto.Message
	policy    json.RawMessage
	mu        sync.Mutex
	snapshots map[string]json.RawMessage
	appended  []dto.Message
}

func newStubSessions() *stubSessions { return &stubSessions{snapshots: map[string]json.RawMessage{}} }

func (s *stubSessions) ListByDocument(string) ([]dto.Session, error) { return nil, nil }
func (s *stubSessions) ListByWorkspace() ([]dto.Session, error)      { return nil, nil }
func (s *stubSessions) Create(string, *string, string) (dto.Session, error) {
	return dto.Session{}, nil
}
func (s *stubSessions) Resume(string) (dto.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return dto.Session{ContextPolicy: s.policy}, nil
}
func (s *stubSessions) Append(_ string, msg dto.Message) error {
	s.mu.Lock()
	s.appended = append(s.appended, msg)
	s.mu.Unlock()
	return nil
}
func (s *stubSessions) History(string) ([]dto.Message, error) { return s.hist, nil }

func (s *stubSessions) appendedMessages() []dto.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dto.Message(nil), s.appended...)
}

func (s *stubSessions) SetContextPolicy(_ string, policy json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = append(json.RawMessage(nil), policy...)
	return nil
}

func (s *stubSessions) ContextPolicy(string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy, nil
}

func (s *stubSessions) SaveContext(turnID, sessionID string, snapshot json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[turnID] = snapshot
	return nil
}

func (s *stubSessions) TurnContext(turnID string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, ok := s.snapshots[turnID]; ok {
		return snap, nil
	}
	return nil, session.ErrNotFound
}

// CompactHistory replaces the oldest turns with one summary message (ADR-0051
// §8); it mutates the stub history so a compaction test can observe the effect.
func (s *stubSessions) CompactHistory(sessionID, summary string, keepRecentTurns int) (fromTs, toTs int64, turns int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hist := s.hist
	starts := []int{}
	for i, m := range hist {
		if m.Role == "user" {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return 0, 0, 0, nil
	}
	cut := len(starts) - keepRecentTurns
	if cut <= 0 {
		return 0, 0, 0, nil
	}
	boundary := starts[cut]
	if boundary == 0 {
		return 0, 0, 0, nil
	}
	summarized := hist[:boundary]
	fromTs = summarized[0].Timestamp
	toTs = summarized[len(summarized)-1].Timestamp
	for _, m := range summarized {
		if m.Role == "user" {
			turns++
		}
	}
	kept := append([]dto.Message(nil), hist[boundary:]...)
	s.hist = append([]dto.Message{{Role: "user", Content: "[Summary]\n" + summary, Timestamp: fromTs}}, kept...)
	return fromTs, toTs, turns, nil
}

// stubFilesystem implements filesystem.Interface over an in-memory map keyed by
// path → (content, error). Used to drive mention resolution and the
// ALLOWED_ROOTS refusal path.
type stubFilesystem struct {
	files map[string]string
	err   map[string]error
}

func (s *stubFilesystem) List(context.Context, string) ([]filesystem.Entry, error) { return nil, nil }
func (s *stubFilesystem) AllowedRoots() []string                                   { return []string{"/"} }
func (s *stubFilesystem) Check(string) error                                       { return nil }
func (s *stubFilesystem) Read(_ context.Context, path string, maxBytes int) ([]byte, error) {
	if e, ok := s.err[path]; ok {
		return nil, e
	}
	b := []byte(s.files[path])
	if maxBytes > 0 && len(b) > maxBytes {
		return nil, filesystem.ErrTooLarge
	}
	return b, nil
}

// stubWorkspaces implements workspace.Interface with permissive defaults; the
// loop only calls Get (explicit workspaceId) and ResolveOrCreate (fallback).
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

// stubShards returns one fixed shard lease; tests mutate the Services pointer
// to swap in their fake retriever/sessions/meter.
type stubShards struct{ svc *shard.Services }

func (s stubShards) Services(context.Context, string) (*shard.Lease, error) {
	return shard.NewLease(*s.svc), nil
}

var _ retriever.Interface = (*stubRetriever)(nil)

type stubMeter struct {
	mu          sync.Mutex
	called      int
	compactions int
	measurement *dto.TurnMeasurement
}

func (s *stubMeter) Attribute(_ context.Context, _, _, _ string, _ dto.Breakdown, _ dto.ProviderCounts, m dto.TurnMeasurement) (dto.AttributedBreakdown, error) {
	s.mu.Lock()
	s.called++
	s.measurement = &m
	s.mu.Unlock()
	return dto.AttributedBreakdown{}, nil
}
func (s *stubMeter) AttributeCompaction(context.Context, string, string, string, dto.ProviderCounts) error {
	s.mu.Lock()
	s.compactions++
	s.mu.Unlock()
	return nil
}
func (s *stubMeter) SessionUsage(context.Context, string) (int, error) { return 0, nil }
func (s *stubMeter) SessionBreakdown(context.Context, string) (dto.SessionMeter, error) {
	return dto.SessionMeter{}, nil
}

type stubBus struct {
	mu     sync.Mutex
	events []dto.Event
	done   chan struct{}
}

func (b *stubBus) Emit(ev dto.Event) {
	b.mu.Lock()
	b.events = append(b.events, ev)
	isDone := ev.Type == "done" || ev.Type == "error"
	b.mu.Unlock()
	if isDone {
		select {
		case <-b.done:
		default:
			close(b.done)
		}
	}
}

type stubErr struct{ msg string }

func (e *stubErr) Error() string { return e.msg }

// --------------------------- tests ---------------------------

func happyPathDeps(bus *stubBus) Deps {
	svc := &shard.Services{
		Retriever: &stubRetriever{},
		Sessions:  newStubSessions(),
		Meter:     &stubMeter{},
	}
	return Deps{
		Modes: stubMode{modes: map[string]dto.Mode{
			"proofreader": {Name: "proofreader", DefaultModel: "gemma4-12b"},
		}},
		Tools:     stubTools{},
		Executor:  stubExecutor{},
		Assembler: stubAssembler{},
		Provider: stubProvider{stream: func(ctx context.Context, emit func(dto.RawEvent)) error {
			emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"hi"}`)})
			emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":14,"outputTokens":5}`)})
			return nil
		}},
		Fleet: stubFleet{res: dto.Resolution{
			Model:           dto.Model{Name: "gemma4-12b", BaseURL: "http://x/v1"},
			EffectiveParams: dto.SamplingParams{Temperature: 0.3, MaxTokens: 10},
			UsedName:        "gemma4-12b",
		}},
		Doc:        stubDoc{},
		Shards:     stubShards{svc: svc},
		Workspaces: stubWorkspaces{},
		Filesystem: &stubFilesystem{files: map[string]string{}, err: map[string]error{}},
		Bus:        bus,
		Locate:     &stubLocate{result: dto.LocateResult{Status: dto.LocateStatusNotFound}},
		Pipeline: stubPipeline{policy: dto.PipelinePolicy{
			MaxSteps:         6,
			MaxHistoryTokens: 32000,
			MaxRagTokens:     16000,
			MaxMentionTokens: 16000,
			AutoRagTopK:      3,
		}},
	}
}

// shardSvc returns the mutable services behind happyPathDeps' fake shard.
func shardSvc(deps Deps) *shard.Services { return deps.Shards.(stubShards).svc }

func TestRunHappyPath(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	l := New(happyPathDeps(bus))

	turnID, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix",
	})
	if err != nil {
		t.Fatal(err)
	}
	if turnID == "" {
		t.Fatal("empty turnID")
	}

	select {
	case <-bus.done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not complete")
	}

	bus.mu.Lock()
	defer bus.mu.Unlock()
	foundDone := false
	for _, ev := range bus.events {
		if ev.Type == "done" && ev.TurnID == turnID {
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("no done event with turnID %s: %+v", turnID, bus.events)
	}
}

func TestRunUnknownModeEmitsError(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	deps.Modes = stubMode{modes: map[string]dto.Mode{}}

	l := New(deps)
	_, err := l.Run(context.Background(), dto.Task{SessionID: "s1", ModeName: "nope"})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-bus.done:
	case <-time.After(2 * time.Second):
		t.Fatal("no terminal event")
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if len(bus.events) == 0 || bus.events[0].Type != "error" {
		t.Fatalf("expected error event, got %+v", bus.events)
	}
}

// TestAgenticToolDispatch drives a two-round agentic turn: round 1 emits a
// tool_call → finish(tool_calls); the executor runs; round 2 answers. The loop
// must dispatch the tool once and emit a candidate event for the edit result.
func TestAgenticToolDispatch(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	exec := &recordingExecutor{result: json.RawMessage(`{"ok":true,"blockId":"b1","diff":{"ok":true}}`)}

	round := 0
	provider := stubProvider{stream: func(ctx context.Context, emit func(dto.RawEvent)) error {
		round++
		if round == 1 {
			emit(dto.RawEvent{Type: "tool_call", Data: json.RawMessage(`{"id":"c1","name":"edit_markdown","arguments":"{\"blockId\":\"b1\",\"text\":\"new\"}"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"tool_calls"}`)})
		} else {
			emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"done now"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
			emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":14,"outputTokens":5}`)})
		}
		return nil
	}}

	deps := happyPathDeps(bus)
	deps.Modes = stubMode{modes: map[string]dto.Mode{
		"editor": {Name: "editor", DefaultModel: "gemma4-12b"},
	}}
	deps.Executor = exec
	deps.Provider = provider

	l := New(deps)
	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "editor", DocumentID: "d1", UserInput: "fix this",
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-bus.done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not complete")
	}

	exec.mu.Lock()
	calls := append([]string(nil), exec.calls...)
	exec.mu.Unlock()
	if len(calls) != 1 || calls[0] != "edit_markdown" {
		t.Fatalf("executor calls = %v, want [edit_markdown]", calls)
	}

	bus.mu.Lock()
	defer bus.mu.Unlock()
	var sawCandidate, sawDone bool
	for _, ev := range bus.events {
		if ev.Type == "candidate" {
			sawCandidate = true
		}
		if ev.Type == "done" {
			sawDone = true
		}
	}
	if !sawCandidate {
		t.Fatalf("no candidate event: %+v", bus.events)
	}
	if !sawDone {
		t.Fatalf("no done event: %+v", bus.events)
	}
}

// TestAgenticRetrieveEmitsRag drives a retrieve tool call and asserts the
// structured result surfaces as a `rag` event (recorded amendment, ADR-0017 §6).
func TestAgenticRetrieveEmitsRag(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	exec := &recordingExecutor{result: json.RawMessage(`{"ok":true,"chunks":[{"blockId":"b9","text":"cited"}]}`)}

	round := 0
	provider := stubProvider{stream: func(ctx context.Context, emit func(dto.RawEvent)) error {
		round++
		if round == 1 {
			emit(dto.RawEvent{Type: "tool_call", Data: json.RawMessage(`{"id":"c1","name":"retrieve","arguments":"{\"query\":\"citations\"}"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"tool_calls"}`)})
		} else {
			emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"cited"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
			emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":14,"outputTokens":5}`)})
		}
		return nil
	}}

	deps := happyPathDeps(bus)
	deps.Modes = stubMode{modes: map[string]dto.Mode{
		"drafter": {Name: "drafter", DefaultModel: "mistral-24b"},
	}}
	deps.Executor = exec
	deps.Provider = provider

	l := New(deps)
	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "drafter", DocumentID: "d1", UserInput: "add citations",
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-bus.done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not complete")
	}

	bus.mu.Lock()
	defer bus.mu.Unlock()
	sawRag, sawToolChunk := false, false
	for _, ev := range bus.events {
		if ev.Type != "rag" {
			continue
		}
		sawRag = true
		var d struct {
			Chunks []struct {
				BlockID string `json:"blockId"`
				Text    string `json:"text"`
			} `json:"chunks"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			t.Fatalf("rag data = %s: %v", ev.Data, err)
		}
		// Auto-RAG emits an (empty) rag event first; the tool retrieval emits
		// the structured chunk. Assert the tool result is among them.
		if len(d.Chunks) == 1 && d.Chunks[0].Text == "cited" {
			sawToolChunk = true
		}
	}
	if !sawRag {
		t.Fatalf("no rag event: %+v", bus.events)
	}
	if !sawToolChunk {
		t.Fatalf("no tool-retrieval rag event with the structured chunk: %+v", bus.events)
	}
}

// TestBudgetExceeded surfaces session-budget-exceeded before any model call.
func TestBudgetExceeded(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)

	budget := 10
	svc := shardSvc(deps)
	svc.Sessions = budgetSessions{budget: &budget}
	svc.Meter = &budgetMeter{used: 11}

	l := New(deps)
	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix",
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-bus.done:
	case <-time.After(2 * time.Second):
		t.Fatal("no terminal event")
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	var sawBudget bool
	for _, ev := range bus.events {
		if ev.Type == "error" {
			var d struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			if d.Code == "session-budget-exceeded" {
				sawBudget = true
			}
		}
	}
	if !sawBudget {
		t.Fatalf("no session-budget-exceeded error: %+v", bus.events)
	}
}

type budgetSessions struct{ budget *int }

func (budgetSessions) ListByDocument(string) ([]dto.Session, error) { return nil, nil }
func (budgetSessions) ListByWorkspace() ([]dto.Session, error)      { return nil, nil }
func (budgetSessions) Create(string, *string, string) (dto.Session, error) {
	return dto.Session{}, nil
}
func (b budgetSessions) Resume(string) (dto.Session, error) {
	return dto.Session{TokenBudget: b.budget}, nil
}
func (budgetSessions) Append(string, dto.Message) error      { return nil }
func (budgetSessions) History(string) ([]dto.Message, error) { return nil, nil }
func (budgetSessions) SaveContext(string, string, json.RawMessage) error {
	return nil
}
func (budgetSessions) TurnContext(string) (json.RawMessage, error) {
	return nil, session.ErrNotFound
}
func (budgetSessions) SetContextPolicy(string, json.RawMessage) error { return nil }
func (budgetSessions) ContextPolicy(string) (json.RawMessage, error)  { return nil, nil }
func (budgetSessions) CompactHistory(string, string, int) (int64, int64, int, error) {
	return 0, 0, 0, nil
}

type budgetMeter struct{ used int }

func (b budgetMeter) Attribute(context.Context, string, string, string, dto.Breakdown, dto.ProviderCounts, dto.TurnMeasurement) (dto.AttributedBreakdown, error) {
	return dto.AttributedBreakdown{}, nil
}
func (b budgetMeter) AttributeCompaction(context.Context, string, string, string, dto.ProviderCounts) error {
	return nil
}
func (b budgetMeter) SessionUsage(context.Context, string) (int, error) { return b.used, nil }
func (b budgetMeter) SessionBreakdown(context.Context, string) (dto.SessionMeter, error) {
	return dto.SessionMeter{}, nil
}

// --------------------- one pipeline, every preset (ADR-0045) ---------------------

// shippedPresets mirrors the four data presets in server/config/modes/.
var shippedPresets = []string{"drafter", "editor", "grammar", "proofreader"}

// presetDeps returns loop deps with all four shipped presets and all four
// registered tools.
func presetDeps(bus *stubBus) (Deps, []dto.ToolDef) {
	modes := map[string]dto.Mode{}
	for _, name := range shippedPresets {
		modes[name] = dto.Mode{Name: name, SystemPrompt: "preset " + name, DefaultModel: "gemma4-12b"}
	}
	defs := []dto.ToolDef{
		{Name: "diff", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "edit_markdown", Description: "e", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "read_note", Description: "r", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "retrieve", Description: "q", Parameters: json.RawMessage(`{"type":"object"}`)},
	}
	deps := happyPathDeps(bus)
	deps.Modes = stubMode{modes: modes}
	deps.Tools = stubTools{defs: defs}
	return deps, defs
}

// reqProvider records every dto.Request it streams, so tests can assert the
// spliced tool set.
type reqProvider struct {
	mu     sync.Mutex
	reqs   []dto.Request
	stream func(ctx context.Context, req dto.Request, emit func(dto.RawEvent)) error
}

func (s *reqProvider) Chat(context.Context, dto.Target, dto.Request) (dto.Completion, error) {
	return dto.Completion{}, nil
}
func (s *reqProvider) Stream(ctx context.Context, t dto.Target, r dto.Request, emit func(dto.RawEvent)) error {
	s.mu.Lock()
	s.reqs = append(s.reqs, r)
	s.mu.Unlock()
	return s.stream(ctx, r, emit)
}
func (*reqProvider) Embed(context.Context, dto.Target, string) ([]float32, error) { return nil, nil }

// editThenAnswerProvider emits an edit_markdown tool call in round 1 and a
// terminal stop round in round 2.
func editThenAnswerProvider() *reqProvider {
	round := 0
	return &reqProvider{stream: func(ctx context.Context, req dto.Request, emit func(dto.RawEvent)) error {
		round++
		if round == 1 {
			emit(dto.RawEvent{Type: "tool_call", Data: json.RawMessage(`{"id":"c1","name":"edit_markdown","arguments":"{\"blockId\":\"b1\",\"text\":\"new\"}"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"tool_calls"}`)})
		} else {
			emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"done"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
			emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":14,"outputTokens":5}`)})
		}
		return nil
	}}
}

// TestEveryPresetRunsEditTurn is the ADR-0045 gate: every preset shares the one
// pipeline, every registered tool is advertised (never request_tool), and an
// edit_markdown tool call stages a candidate — the drafter trap is gone.
func TestEveryPresetRunsEditTurn(t *testing.T) {
	for _, preset := range shippedPresets {
		t.Run(preset, func(t *testing.T) {
			bus := &stubBus{done: make(chan struct{})}
			deps, defs := presetDeps(bus)
			exec := &recordingExecutor{result: json.RawMessage(`{"ok":true,"blockId":"b1","diff":{"ok":true}}`)}
			prov := editThenAnswerProvider()
			deps.Executor = exec
			deps.Provider = prov

			l := New(deps)
			if _, err := l.Run(context.Background(), dto.Task{
				SessionID: "s1", ModeName: preset, DocumentID: "d1", UserInput: "rewrite this",
			}); err != nil {
				t.Fatal(err)
			}
			waitDone(t, bus)

			exec.mu.Lock()
			calls := append([]string(nil), exec.calls...)
			exec.mu.Unlock()
			if len(calls) != 1 || calls[0] != "edit_markdown" {
				t.Fatalf("executor calls = %v, want [edit_markdown]", calls)
			}

			events := busEvents(t, bus)
			if !hasEvent(events, "candidate") || !hasEvent(events, "done") {
				t.Fatalf("want candidate + done, got %+v", events)
			}

			prov.mu.Lock()
			reqs := append([]dto.Request(nil), prov.reqs...)
			prov.mu.Unlock()
			if len(reqs) == 0 {
				t.Fatal("provider never called")
			}
			for _, req := range reqs {
				if len(req.Tools) != len(defs) {
					t.Fatalf("tools advertised = %d, want all %d", len(req.Tools), len(defs))
				}
				for _, td := range req.Tools {
					if td.Name == "request_tool" {
						t.Fatalf("request_tool advertised in a payload: %+v", req.Tools)
					}
				}
			}
		})
	}
}

// TestAutoRagRunsForEveryPreset: retrieval always runs with the policy's top-k,
// regardless of preset, and the chunks reach the assembler.
func TestAutoRagRunsForEveryPreset(t *testing.T) {
	for _, preset := range shippedPresets {
		t.Run(preset, func(t *testing.T) {
			bus := &stubBus{done: make(chan struct{})}
			deps, _ := presetDeps(bus)
			ret := &stubRetriever{chunks: []dto.Chunk{{BlockID: "b9", Text: "retrieved"}}}
			asm := &recordingAssembler{}
			shardSvc(deps).Retriever = ret
			deps.Assembler = asm

			l := New(deps)
			if _, err := l.Run(context.Background(), dto.Task{
				SessionID: "s1", ModeName: preset, DocumentID: "d1", UserInput: "hi",
			}); err != nil {
				t.Fatal(err)
			}
			waitDone(t, bus)

			calls := ret.queries()
			if len(calls) != 1 || calls[0] != 3 {
				t.Fatalf("retriever calls = %v, want one Query with the policy top-k 3", calls)
			}
			asm.mu.Lock()
			rag := append([]dto.Chunk(nil), asm.rag...)
			asm.mu.Unlock()
			if len(rag) != 1 || rag[0].Text != "retrieved" {
				t.Fatalf("assembler RAG chunks = %+v, want the retrieved chunk", rag)
			}
		})
	}
}

// TestGlobalMaxStepsCap: the global policy bounds the dispatch/observe cycle
// for every preset (a mode has no step field to diverge).
func TestGlobalMaxStepsCap(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	deps.Pipeline = stubPipeline{policy: dto.PipelinePolicy{
		MaxSteps: 2, MaxHistoryTokens: 1, MaxRagTokens: 1, MaxMentionTokens: 1, AutoRagTopK: 1,
	}}
	exec := &recordingExecutor{result: json.RawMessage(`{"ok":true}`)}
	rounds := 0
	prov := &reqProvider{stream: func(ctx context.Context, req dto.Request, emit func(dto.RawEvent)) error {
		rounds++
		emit(dto.RawEvent{Type: "tool_call", Data: json.RawMessage(`{"id":"c1","name":"retrieve","arguments":"{}"}`)})
		emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"tool_calls"}`)})
		return nil
	}}
	deps.Executor = exec
	deps.Provider = prov

	l := New(deps)
	if _, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "loop",
	}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	exec.mu.Lock()
	n := len(exec.calls)
	exec.mu.Unlock()
	if n != 2 {
		t.Fatalf("executor calls = %d, want the global cap 2", n)
	}
	if rounds != 3 {
		t.Fatalf("provider rounds = %d, want cap+1 = 3", rounds)
	}
}

// --------------------- mention resolution (ADR-0036 §2) ---------------------

// recordingAssembler captures the MentionContent and RAG chunks handed to it,
// asserting the loop resolves mentions through the Filesystem leaf and always
// retrieves.
type recordingAssembler struct {
	mu       sync.Mutex
	mentions []dto.MentionContent
	rag      []dto.Chunk
}

func (r *recordingAssembler) Assemble(_ context.Context, in dto.AssemblerInput) (dto.Payload, dto.Breakdown, error) {
	r.mu.Lock()
	r.mentions = append([]dto.MentionContent(nil), in.Mentions...)
	r.rag = append([]dto.Chunk(nil), in.RAGChunks...)
	r.mu.Unlock()
	return dto.Payload{Request: dto.Request{ModelName: "gemma4-12b"}}, dto.Breakdown{SystemPrompt: 10, User: 4}, nil
}

// modelNameAssembler captures the ModelName the loop hands the assembler.
type modelNameAssembler struct {
	mu        sync.Mutex
	modelName string
}

func (r *modelNameAssembler) Assemble(_ context.Context, in dto.AssemblerInput) (dto.Payload, dto.Breakdown, error) {
	r.mu.Lock()
	r.modelName = in.ModelName
	r.mu.Unlock()
	return dto.Payload{Request: dto.Request{ModelName: in.ModelName}}, dto.Breakdown{SystemPrompt: 10, User: 4}, nil
}

// TestWireModelIDReachesAssembler: the provider's `model` field uses the daemon-
// projected wire id (Model.ModelID), not the manifest name (UsedName), while the
// done event still labels the manifest name for the user.
func TestWireModelIDReachesAssembler(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	assembler := &modelNameAssembler{}
	deps := happyPathDeps(bus)
	deps.Assembler = assembler
	deps.Fleet = stubFleet{res: dto.Resolution{
		Model:           dto.Model{Name: "gemma4-12b", BaseURL: "http://x/v1", ModelID: "mlx-community/gemma-4-26B-A4B-it-OptiQ-4bit"},
		EffectiveParams: dto.SamplingParams{Temperature: 0.3, MaxTokens: 10},
		UsedName:        "gemma4-12b",
	}}
	l := New(deps)

	if _, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bus.done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not complete")
	}

	assembler.mu.Lock()
	got := assembler.modelName
	assembler.mu.Unlock()
	if got != "mlx-community/gemma-4-26B-A4B-it-OptiQ-4bit" {
		t.Fatalf("assembler ModelName = %q, want the wire id", got)
	}

	bus.mu.Lock()
	defer bus.mu.Unlock()
	var usedModel string
	for _, ev := range bus.events {
		if ev.Type == "done" {
			var d struct {
				UsedModel string `json:"usedModel"`
			}
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				usedModel = d.UsedModel
			}
		}
	}
	if usedModel != "gemma4-12b" {
		t.Fatalf("done usedModel = %q, want the manifest name", usedModel)
	}
}

func mentionDeps(bus *stubBus) Deps {
	d := happyPathDeps(bus)
	d.Filesystem = &stubFilesystem{
		files: map[string]string{"/notes/a.md": "mentioned content"},
		err:   map[string]error{},
	}
	return d
}

// TestMentionsResolvedAndSpliced: valid mentions resolve through the Filesystem
// leaf and reach the assembler as MentionContent.
func TestMentionsResolvedAndSpliced(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	assembler := &recordingAssembler{}
	deps := mentionDeps(bus)
	deps.Assembler = assembler

	l := New(deps)
	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix",
		Mentions: []dto.Mention{{Path: "/notes/a.md"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	assembler.mu.Lock()
	defer assembler.mu.Unlock()
	if len(assembler.mentions) != 1 || assembler.mentions[0].Path != "/notes/a.md" || assembler.mentions[0].Text != "mentioned content" {
		t.Fatalf("assembler mentions = %+v, want resolved /notes/a.md", assembler.mentions)
	}
	if !hasEvent(busEvents(t, bus), "done") {
		t.Fatalf("want done: %+v", busEvents(t, bus))
	}
}

// TestMentionNotFoundFailsFast: an unresolvable mention errors pre-streaming
// with mention-not-found and no stream starts.
func TestMentionNotFoundFailsFast(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"not-found", filesystem.ErrNotFound, "mention-not-found"},
		{"not-regular", filesystem.ErrNotRegular, "mention-not-found"},
		{"too-large", filesystem.ErrTooLarge, "mention-too-large"},
		{"read-failed", filesystem.ErrReadFailed, "mention-unreadable"},
		{"outside-roots", filesystem.ErrPathOutsideAllowedRoots, "path-outside-allowed-roots"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := &stubBus{done: make(chan struct{})}
			deps := happyPathDeps(bus)
			deps.Filesystem = &stubFilesystem{
				files: map[string]string{},
				err:   map[string]error{"/x.md": tc.err},
			}
			provCalled := false
			sp := deps.Provider.(stubProvider)
			sp.stream = func(ctx context.Context, emit func(dto.RawEvent)) error {
				provCalled = true
				emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":14,"outputTokens":5}`)})
				return nil
			}
			deps.Provider = sp

			l := New(deps)
			_, err := l.Run(context.Background(), dto.Task{
				SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix",
				Mentions: []dto.Mention{{Path: "/x.md"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			waitDone(t, bus)

			if provCalled {
				t.Fatal("provider streamed despite mention resolution failure (no fail-fast)")
			}
			events := busEvents(t, bus)
			if len(events) == 0 || events[0].Type != "error" {
				t.Fatalf("want error event first: %+v", events)
			}
			var d struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(events[0].Data, &d); err != nil || d.Code != tc.code {
				t.Fatalf("error code = %q, want %q (%s)", d.Code, tc.code, events[0].Data)
			}
		})
	}
}

// TestTooManyMentionsFailsFast: over the count cap errors pre-streaming.
func TestTooManyMentionsFailsFast(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	deps.Filesystem = &stubFilesystem{files: map[string]string{}, err: map[string]error{}}

	mentions := make([]dto.Mention, maxMentions+1)
	for i := range mentions {
		mentions[i] = dto.Mention{Path: "/x.md"}
	}

	l := New(deps)
	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix",
		Mentions: mentions,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	events := busEvents(t, bus)
	if len(events) == 0 || events[0].Type != "error" {
		t.Fatalf("want error event: %+v", events)
	}
	var d struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(events[0].Data, &d); err != nil || d.Code != "too-many-mentions" {
		t.Fatalf("code = %q, want too-many-mentions (%s)", d.Code, events[0].Data)
	}
}

// --------------------- workspace resolution (ADR-0049 §2/§5) ---------------------

// captureShards records which workspace ids were leased.
type captureShards struct {
	mu  sync.Mutex
	ids []string
	svc *shard.Services
}

func (c *captureShards) Services(_ context.Context, workspaceID string) (*shard.Lease, error) {
	c.mu.Lock()
	c.ids = append(c.ids, workspaceID)
	c.mu.Unlock()
	return shard.NewLease(*c.svc), nil
}

func (c *captureShards) leased() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ids...)
}

// captureWorkspaces records fallback ResolveOrCreate roots.
type captureWorkspaces struct {
	stubWorkspaces
	mu    sync.Mutex
	roots []string
	id    string
}

func (c *captureWorkspaces) ResolveOrCreate(root string) (dto.Workspace, error) {
	c.mu.Lock()
	c.roots = append(c.roots, root)
	c.mu.Unlock()
	return dto.Workspace{ID: c.id, Root: root}, nil
}

func (c *captureWorkspaces) resolved() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.roots...)
}

// TestWorkspaceFallbackResolvesFromDocument: an absent Task.workspaceId
// resolve-or-creates a workspace rooted at the canonical parent directory of
// the turn's document (keeps pre-workspace clients working).
func TestWorkspaceFallbackResolvesFromDocument(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	shards := &captureShards{svc: shardSvc(deps)}
	workspaces := &captureWorkspaces{id: "ws-fallback"}
	deps.Shards = shards
	deps.Workspaces = workspaces

	l := New(deps)
	if _, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix",
	}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	if got := shards.leased(); len(got) != 1 || got[0] != "ws-fallback" {
		t.Fatalf("leased workspaces = %v, want [ws-fallback]", got)
	}
	roots := workspaces.resolved()
	if len(roots) != 1 || roots[0] != "/vault" {
		t.Fatalf("fallback root = %v, want [/vault] (parent of /vault/d1.md)", roots)
	}
}

// TestExplicitWorkspaceIDWins: an explicit workspaceId is used as-is and never
// falls back to the document parent.
func TestExplicitWorkspaceIDWins(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	shards := &captureShards{svc: shardSvc(deps)}
	workspaces := &captureWorkspaces{id: "ws-fallback"}
	deps.Shards = shards
	deps.Workspaces = workspaces

	l := New(deps)
	if _, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", WorkspaceID: "ws-explicit", UserInput: "fix",
	}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	if got := shards.leased(); len(got) != 1 || got[0] != "ws-explicit" {
		t.Fatalf("leased workspaces = %v, want [ws-explicit]", got)
	}
	if got := workspaces.resolved(); len(got) != 0 {
		t.Fatalf("explicit workspace must not fall back: resolved %v", got)
	}
}

// routeWorkspaces records the turn→workspace→session routing writes so the loop
// can be asserted to write RouteTurn at turn start (ADR-0044 §4).
type routeWorkspaces struct {
	stubWorkspaces
	mu     sync.Mutex
	routes []turnRoute
}

type turnRoute struct{ turnID, workspaceID, sessionID string }

func (r *routeWorkspaces) RouteTurn(turnID, workspaceID, sessionID string) error {
	r.mu.Lock()
	r.routes = append(r.routes, turnRoute{turnID, workspaceID, sessionID})
	r.mu.Unlock()
	return nil
}

func (r *routeWorkspaces) recorded() []turnRoute {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]turnRoute(nil), r.routes...)
}

// TestAutoRagRagEventAndSnapshot covers context-inspector "Auto-retrieved chunks
// are visible with provenance" and "A completed turn persists a context
// snapshot": auto-RAG emits the same rag shape as tool retrieval, the loop
// persists a snapshot and emits it as `context`, the snapshot records the
// retrieved chunks and the tool/thinking components, and RouteTurn is written.
func TestAutoRagRagEventAndSnapshot(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	svc := shardSvc(deps)
	svc.Retriever = &stubRetriever{chunks: []dto.Chunk{{
		BlockID: "b9", ChunkKey: "/vault/a.md#0", Text: "retrieved", Score: 0.8,
		Path: "/vault/a.md", Heading: "Intro",
	}}}
	deps.Assembler = assembler.New()
	rw := &routeWorkspaces{}
	deps.Workspaces = rw

	l := New(deps)
	turnID, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "find citations",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	events := busEvents(t, bus)
	ragIdx, ctxIdx := -1, -1
	for i := range events {
		switch events[i].Type {
		case "rag":
			if ragIdx < 0 {
				ragIdx = i
			}
		case "context":
			ctxIdx = i
		}
	}
	if ragIdx < 0 {
		t.Fatalf("no rag event: %+v", events)
	}
	if ctxIdx < 0 {
		t.Fatalf("no context event: %+v", events)
	}
	if ragIdx >= ctxIdx {
		t.Fatalf("rag must precede context: rag@%d context@%d", ragIdx, ctxIdx)
	}

	// The rag event has the same {ok, chunks} shape as tool retrieval.
	var rag struct {
		Ok     bool        `json:"ok"`
		Chunks []dto.Chunk `json:"chunks"`
	}
	if err := json.Unmarshal(events[ragIdx].Data, &rag); err != nil {
		t.Fatal(err)
	}
	if !rag.Ok || len(rag.Chunks) != 1 || rag.Chunks[0].Path != "/vault/a.md" || rag.Chunks[0].Heading != "Intro" {
		t.Fatalf("rag data = %s, want structured provenance", events[ragIdx].Data)
	}

	// The context event carries the persisted snapshot.
	var snap dto.ContextSnapshot
	if err := json.Unmarshal(events[ctxIdx].Data, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.TurnID != turnID || snap.SessionID != "s1" || snap.WorkspaceID != "ws1" {
		t.Fatalf("snapshot envelope = %+v", snap)
	}
	if !snap.AutoRag || snap.RetrievalQuery != "find citations" {
		t.Fatalf("snapshot retrieval metadata = %+v", snap)
	}
	if len(snap.Chunks) != 1 || snap.Chunks[0].ChunkKey != "/vault/a.md#0" {
		t.Fatalf("snapshot chunks = %+v, want the retrieved chunk", snap.Chunks)
	}
	messageComponents := map[string]bool{}
	for _, m := range snap.Messages {
		messageComponents[m.Component] = true
	}
	if !messageComponents["system"] || !messageComponents["user"] || !messageComponents["rag"] {
		t.Fatalf("snapshot message components = %v, want system+rag+user", messageComponents)
	}
	budgetComponents := map[string]bool{}
	for _, u := range snap.Budget {
		budgetComponents[u.Component] = true
	}
	for _, want := range []string{"system", "tools", "rag", "history", "mentions", "user", "thinking"} {
		if !budgetComponents[want] {
			t.Fatalf("snapshot budget missing %q: %+v", want, snap.Budget)
		}
	}

	// The snapshot is persisted and retrievable by turnID, byte-identical to the
	// emitted context event (clients never reconstruct it).
	recorder := svc.Sessions.(*stubSessions)
	raw, err := recorder.TurnContext(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(events[ctxIdx].Data) {
		t.Fatalf("persisted snapshot != context event:\n%s\n%s", raw, events[ctxIdx].Data)
	}

	// RouteTurn was written at turn start.
	routes := rw.recorded()
	if len(routes) != 1 || routes[0].turnID != turnID || routes[0].workspaceID != "ws1" || routes[0].sessionID != "s1" {
		t.Fatalf("turn routes = %+v, want one turnID/ws1/s1", routes)
	}
}

// --------------------- small test helpers ---------------------

func waitDone(t *testing.T, bus *stubBus) {
	t.Helper()
	select {
	case <-bus.done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not complete")
	}
}

func busEvents(t *testing.T, bus *stubBus) []dto.Event {
	t.Helper()
	bus.mu.Lock()
	defer bus.mu.Unlock()
	return append([]dto.Event(nil), bus.events...)
}

func hasEvent(events []dto.Event, typ string) bool {
	for _, ev := range events {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

// TestInjectDocumentIDCarriesMode covers ADR-0020 §1: edit_markdown args get the
// turn's preset (for the derived commit message) alongside the document binding;
// other tools never see either field.
func TestInjectDocumentIDCarriesMode(t *testing.T) {
	task := dto.Task{DocumentID: "d1", ModeName: "proofreader"}

	out := injectDocumentID("edit_markdown", task, nil, json.RawMessage(`{"blockId":"b1","text":"x"}`))
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["documentId"] != "d1" || m["modeName"] != "proofreader" {
		t.Fatalf("edit args = %v, want documentId + modeName", m)
	}

	out = injectDocumentID("diff", task, nil, json.RawMessage(`{}`))
	m = map[string]interface{}{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["modeName"]; ok {
		t.Fatalf("diff args = %v, must not carry modeName", m)
	}
}

// --------------------- context tray (ADR-0049 §7/§8/§11) ---------------------

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

// runAndCollect runs one turn and returns its events, turnID, and snapshot.
func runAndCollect(t *testing.T, deps Deps, task dto.Task) ([]dto.Event, string, dto.ContextSnapshot) {
	t.Helper()
	bus := deps.Bus.(*stubBus)
	l := New(deps)
	turnID, err := l.Run(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)
	events := busEvents(t, bus)
	return events, turnID, snapshotOf(t, events)
}

func snapshotOf(t *testing.T, events []dto.Event) dto.ContextSnapshot {
	t.Helper()
	var snap dto.ContextSnapshot
	found := false
	for _, ev := range events {
		if ev.Type == "context" {
			if err := json.Unmarshal(ev.Data, &snap); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no context event: %+v", events)
	}
	return snap
}

func ragChunksOf(t *testing.T, events []dto.Event) []dto.Chunk {
	t.Helper()
	for _, ev := range events {
		if ev.Type == "rag" {
			var rag struct {
				Ok     bool        `json:"ok"`
				Chunks []dto.Chunk `json:"chunks"`
			}
			if err := json.Unmarshal(ev.Data, &rag); err != nil {
				t.Fatal(err)
			}
			return rag.Chunks
		}
	}
	t.Fatalf("no rag event: %+v", events)
	return nil
}

// TestTrayExclusionFiltersChunk: a per-turn excluded ref drops the chunk from
// the payload and the snapshot records the removal ("Removing a chunk in the
// tray keeps it out of the payload").
func TestTrayExclusionFiltersChunk(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	svc := shardSvc(deps)
	svc.Retriever = &stubRetriever{chunks: []dto.Chunk{
		{ChunkKey: "/v/a.md#0", Path: "/v/a.md", Text: "keep me"},
		{ChunkKey: "/v/b.md#0", Path: "/v/b.md", Text: "drop me"},
	}}
	deps.Assembler = assembler.New()

	task := dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "q",
		Context: &dto.ContextPolicy{Excluded: []dto.ChunkRef{{Path: "/v/b.md", ChunkKey: "/v/b.md#0"}}},
	}
	events, _, snap := runAndCollect(t, deps, task)

	if rag := ragChunksOf(t, events); len(rag) != 1 || rag[0].Path != "/v/a.md" {
		t.Fatalf("rag chunks = %+v, want only /v/a.md (post-exclude)", rag)
	}
	for _, c := range snap.Chunks {
		if c.Path == "/v/b.md" {
			t.Fatalf("excluded chunk present in snapshot: %+v", snap.Chunks)
		}
	}
	var excluded *dto.ContextDrop
	for i := range snap.Drops {
		if snap.Drops[i].Reason == "excluded" {
			excluded = &snap.Drops[i]
		}
	}
	if excluded == nil || excluded.Count != 1 || excluded.Component != "rag" {
		t.Fatalf("excluded drop = %+v, want rag/excluded count 1", excluded)
	}
}

// TestSessionPinPersistsAndResolvesViaGet: a session pin survives into the next
// turn (no Task.context), resolves through Retriever.Get even though it is not
// in the top-k, and is labeled a human override; auto chunks stay unlabeled.
func TestSessionPinPersistsAndResolvesViaGet(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	svc := shardSvc(deps)
	ret := &stubRetriever{
		chunks:    []dto.Chunk{{ChunkKey: "/v/auto.md#0", Path: "/v/auto.md", Text: "auto passage"}},
		getChunks: []dto.Chunk{{ChunkKey: "/v/pin.md#2", Path: "/v/pin.md", Text: "pinned passage"}},
	}
	svc.Retriever = ret
	svc.Sessions.(*stubSessions).policy = json.RawMessage(`{"pinned":[{"path":"/v/pin.md","chunkKey":"/v/pin.md#2"}]}`)
	deps.Assembler = assembler.New()

	events, _, snap := runAndCollect(t, deps, dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "q",
	})

	if ret.getCount() != 1 {
		t.Fatalf("Retriever.Get calls = %d, want 1 for the session pin", ret.getCount())
	}
	if len(snap.Chunks) != 2 {
		t.Fatalf("snapshot chunks = %+v, want auto + pinned", snap.Chunks)
	}
	pinned := snap.Chunks[0]
	if !pinned.Pinned || !pinned.HumanOverride || pinned.Path != "/v/pin.md" {
		t.Fatalf("pinned chunk = %+v, want labeled human override", pinned)
	}
	auto := snap.Chunks[1]
	if auto.Pinned || auto.Path != "/v/auto.md" {
		t.Fatalf("auto chunk = %+v, want unlabeled", auto)
	}
	if rag := ragChunksOf(t, events); len(rag) != 1 || rag[0].Path != "/v/auto.md" {
		t.Fatalf("rag event must stay the auto-retrieved set: %+v", rag)
	}
	var pinnedRow bool
	for _, m := range snap.Messages {
		if m.Component == "rag" && m.Pinned {
			pinnedRow = true
		}
	}
	if !pinnedRow {
		t.Fatalf("snapshot messages lack a pinned rag row: %+v", snap.Messages)
	}
}

// TestAutoRagFalseSkipsRetrievalButAppliesPins: autoRag=false suppresses Query
// but pins are still resolved and included.
func TestAutoRagFalseSkipsRetrievalButAppliesPins(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	svc := shardSvc(deps)
	ret := &stubRetriever{
		chunks:    []dto.Chunk{{ChunkKey: "/v/auto.md#0", Path: "/v/auto.md", Text: "auto"}},
		getChunks: []dto.Chunk{{ChunkKey: "/v/pin.md#0", Path: "/v/pin.md", Text: "pin"}},
	}
	svc.Retriever = ret
	deps.Assembler = assembler.New()

	_, _, snap := runAndCollect(t, deps, dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "q",
		Context: &dto.ContextPolicy{
			AutoRag: boolPtr(false),
			Pinned:  []dto.ChunkRef{{Path: "/v/pin.md", ChunkKey: "/v/pin.md#0"}},
		},
	})

	if got := len(ret.queries()); got != 0 {
		t.Fatalf("Query calls = %d, want 0 when autoRag=false", got)
	}
	if ret.getCount() != 1 {
		t.Fatalf("Get calls = %d, want 1 (pins still apply)", ret.getCount())
	}
	if snap.AutoRag {
		t.Fatalf("snapshot autoRag = true, want false")
	}
	if len(snap.Chunks) != 1 || !snap.Chunks[0].Pinned {
		t.Fatalf("snapshot chunks = %+v, want only the pinned chunk", snap.Chunks)
	}
}

// TestRetrievalQueryOverride: the override query reaches the retriever and the
// snapshot, but never replaces the user message.
func TestRetrievalQueryOverride(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	svc := shardSvc(deps)
	ret := &stubRetriever{}
	svc.Retriever = ret
	deps.Assembler = assembler.New()

	_, _, snap := runAndCollect(t, deps, dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "the real request",
		Context: &dto.ContextPolicy{RetrievalQuery: strPtr("a custom retrieval query")},
	})

	texts := ret.queryTexts()
	if len(texts) != 1 || texts[0] != "a custom retrieval query" {
		t.Fatalf("retrieval texts = %v, want the override", texts)
	}
	if snap.RetrievalQuery != "a custom retrieval query" {
		t.Fatalf("snapshot retrievalQuery = %q", snap.RetrievalQuery)
	}
}

// TestReplaceWhenPresentEmptyPinsSuppressSession: an explicit empty pinned list
// clears the session pins for that turn only.
func TestReplaceWhenPresentEmptyPinsSuppressSession(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	svc := shardSvc(deps)
	ret := &stubRetriever{getChunks: []dto.Chunk{{ChunkKey: "/v/pin.md#0", Path: "/v/pin.md", Text: "pin"}}}
	svc.Retriever = ret
	svc.Sessions.(*stubSessions).policy = json.RawMessage(`{"pinned":[{"path":"/v/pin.md","chunkKey":"/v/pin.md#0"}]}`)
	deps.Assembler = assembler.New()

	_, _, snap := runAndCollect(t, deps, dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "q",
		Context: &dto.ContextPolicy{Pinned: []dto.ChunkRef{}},
	})

	if ret.getCount() != 0 {
		t.Fatalf("Get calls = %d, want 0 (session pins suppressed)", ret.getCount())
	}
	if len(snap.Chunks) != 0 {
		t.Fatalf("snapshot chunks = %+v, want none (pins cleared)", snap.Chunks)
	}
}

// TestPerTurnOverrideDoesNotPersist: the override is ephemeral — the session
// row's policy is unchanged after a turn carrying Task.context.
func TestPerTurnOverrideDoesNotPersist(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	svc := shardSvc(deps)
	svc.Retriever = &stubRetriever{getChunks: []dto.Chunk{{ChunkKey: "/v/pin.md#0", Path: "/v/pin.md", Text: "pin"}}}
	sessions := svc.Sessions.(*stubSessions)
	deps.Assembler = assembler.New()

	_, _, _ = runAndCollect(t, deps, dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "q",
		Context: &dto.ContextPolicy{
			Pinned:  []dto.ChunkRef{{Path: "/v/pin.md", ChunkKey: "/v/pin.md#0"}},
			AutoRag: boolPtr(false),
		},
	})

	raw, err := sessions.ContextPolicy("s1")
	if err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Fatalf("session policy persisted from a per-turn override: %s", raw)
	}
}

// TestPinnedOverBudgetLabeledAndMeterSum: pins+auto over the RAG budget truncate
// (pinned drops labeled human override) and the meter's component sum still
// equals the provider-reported prompt total.
func TestPinnedOverBudgetLabeledAndMeterSum(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	svc := shardSvc(deps)
	long := ""
	for i := 0; i < 400; i++ {
		long += "x"
	}
	svc.Retriever = &stubRetriever{
		chunks: []dto.Chunk{{ChunkKey: "/v/auto.md#0", Path: "/v/auto.md", Text: long}},
		getChunks: []dto.Chunk{
			{ChunkKey: "/v/p1.md#0", Path: "/v/p1.md", Text: long},
			{ChunkKey: "/v/p2.md#0", Path: "/v/p2.md", Text: long},
		},
	}
	svc.Meter = newRealMeter(t)
	deps.Assembler = assembler.New()
	deps.Pipeline = stubPipeline{policy: dto.PipelinePolicy{
		MaxSteps: 6, MaxHistoryTokens: 32000, MaxRagTokens: 120, MaxMentionTokens: 16000, AutoRagTopK: 3,
	}}

	_, _, snap := runAndCollect(t, deps, dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "q",
		Context: &dto.ContextPolicy{Pinned: []dto.ChunkRef{
			{Path: "/v/p1.md", ChunkKey: "/v/p1.md#0"},
			{Path: "/v/p2.md", ChunkKey: "/v/p2.md#0"},
		}},
	})

	var pinnedDrop, autoDrop *dto.ContextDrop
	for i := range snap.Drops {
		if snap.Drops[i].Reason != "rag-budget" {
			continue
		}
		if snap.Drops[i].HumanOverride {
			pinnedDrop = &snap.Drops[i]
		} else {
			autoDrop = &snap.Drops[i]
		}
	}
	if pinnedDrop == nil || pinnedDrop.Count != 1 {
		t.Fatalf("pinned drop = %+v, want labeled count 1", pinnedDrop)
	}
	if autoDrop == nil || autoDrop.Count != 1 {
		t.Fatalf("auto drop = %+v, want count 1", autoDrop)
	}

	m, err := svc.Meter.SessionBreakdown(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	sum := 0
	for _, c := range m.Components {
		sum += c.PromptTokens
	}
	if sum != 14 { // the happy-path provider reports inputTokens=14
		t.Fatalf("meter prompt sum = %d, want 14 (provider total)", sum)
	}
}

// newRealMeter returns a meter over an in-memory meter.db for the invariant test.
func newRealMeter(t *testing.T) meter.Interface {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := meter.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return meter.New(db, nil)
}

// --------------------- /locate chunk anchoring (ADR-0048) ---------------------

// waitForEvent polls the bus until an event of the given type appears.
func waitForEvent(t *testing.T, bus *stubBus, typ string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hasEvent(busEvents(t, bus), typ) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("event %q never appeared: %+v", typ, busEvents(t, bus))
}

func locateEventOf(t *testing.T, events []dto.Event) dto.LocateResult {
	t.Helper()
	for _, ev := range events {
		if ev.Type == "locate" {
			var res dto.LocateResult
			if err := json.Unmarshal(ev.Data, &res); err != nil {
				t.Fatal(err)
			}
			return res
		}
	}
	t.Fatalf("no locate event: %+v", events)
	return dto.LocateResult{}
}

func locateResultInSnapshot(t *testing.T, snap dto.ContextSnapshot) dto.LocateResult {
	t.Helper()
	if len(snap.Locate) == 0 {
		t.Fatal("snapshot has no locate record")
	}
	var res dto.LocateResult
	if err := json.Unmarshal(snap.Locate, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

// Scenario: An exact paste resolves to its block; the command line is stripped
// from the session, the locate event precedes any token, and the snapshot
// records the outcome.
func TestLocateExactStripsCommandAndAnchors(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	deps.Doc = stubDoc{
		path:   "/vault/d1.md",
		blocks: []dto.Block{{ID: "b1", Kind: dto.BlockKindParagraph, Text: "Anchored paragraph.", Hash: "h1"}},
	}
	calls := 0
	deps.Provider = stubProvider{calls: &calls, stream: func(ctx context.Context, emit func(dto.RawEvent)) error {
		emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"ok"}`)})
		emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":10,"outputTokens":2}`)})
		return nil
	}}
	deps.Locate = &stubLocate{result: dto.LocateResult{
		Status: dto.LocateStatusResolved, MatchType: dto.LocateMatchExact, Confidence: 1.0,
		DocumentID: "d1", BlockID: "b1", Path: "/vault/d1.md",
	}}
	sessions := newStubSessions()
	shardSvc(deps).Sessions = sessions

	l := New(deps)
	turnID, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1",
		UserInput: "/locate\nAnchored paragraph.",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	events := busEvents(t, bus)
	loc := locateEventOf(t, events)
	if loc.Status != dto.LocateStatusResolved || loc.BlockID != "b1" {
		t.Fatalf("locate event = %+v, want resolved b1", loc)
	}
	// Locate precedes the answer: the first locate event is before the first token.
	locIdx, tokIdx := -1, -1
	for i, ev := range events {
		if ev.Type == "locate" && locIdx < 0 {
			locIdx = i
		}
		if ev.Type == "token" && tokIdx < 0 {
			tokIdx = i
		}
	}
	if locIdx < 0 || tokIdx < 0 || locIdx > tokIdx {
		t.Fatalf("locate must precede tokens: locate=%d token=%d", locIdx, tokIdx)
	}
	// The command line is stripped from the session; the chunk is what is stored.
	appended := sessions.appendedMessages()
	if len(appended) == 0 || appended[0].Role != "user" || appended[0].Content != "Anchored paragraph." {
		t.Fatalf("appended = %+v, want the stripped chunk", appended)
	}
	// The resolver saw the chunk, not the command.
	sl := deps.Locate.(*stubLocate)
	if len(sl.chunks) != 1 || sl.chunks[0] != "Anchored paragraph." {
		t.Fatalf("resolver chunks = %v", sl.chunks)
	}
	// The snapshot records the locate outcome.
	if got := locateResultInSnapshot(t, snapshotOf(t, events)); got.Status != dto.LocateStatusResolved {
		t.Fatalf("snapshot locate = %+v, want resolved", got)
	}
	// Locating is deterministic and token-free: exactly one model round ran (the
	// answer), so the locate step added no provider call.
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1 (no model call for locate)", calls)
	}
	_ = turnID
}

// Scenario: Not found degrades to plain chat with a labeled warning and no
// anchor; the model still runs on the stripped input.
func TestLocateNotFoundProceedsAsPlainChat(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	deps.Locate = &stubLocate{result: dto.LocateResult{Status: dto.LocateStatusNotFound}}
	sessions := newStubSessions()
	shardSvc(deps).Sessions = sessions

	l := New(deps)
	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1",
		UserInput: "/locate\nSome unlocatable text.",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	events := busEvents(t, bus)
	if got := locateEventOf(t, events); got.Status != dto.LocateStatusNotFound {
		t.Fatalf("locate event = %+v, want not-found", got)
	}
	if !hasEvent(events, "done") {
		t.Fatal("plain chat did not complete")
	}
	appended := sessions.appendedMessages()
	if len(appended) == 0 || appended[0].Role != "user" || appended[0].Content != "Some unlocatable text." {
		t.Fatalf("appended = %+v, want stripped chunk", appended)
	}
	if got := locateResultInSnapshot(t, snapshotOf(t, events)); got.Status != dto.LocateStatusNotFound {
		t.Fatalf("snapshot locate = %+v, want not-found", got)
	}
}

// Scenario: Fuzzy matches require confirmation; the turn waits and resolves on
// the choice, with no model call before the choice.
func TestLocateAmbiguousWaitsAndResolves(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	calls := 0
	deps.Provider = stubProvider{calls: &calls, stream: func(ctx context.Context, emit func(dto.RawEvent)) error {
		emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"rewritten"}`)})
		emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":9,"outputTokens":2}`)})
		return nil
	}}
	deps.Doc = stubDoc{
		path:   "/vault/d1.md",
		blocks: []dto.Block{{ID: "b1", Kind: dto.BlockKindParagraph, Text: "Fuzzy match here.", Hash: "h1"}},
	}
	deps.Locate = &stubLocate{result: dto.LocateResult{
		Status: dto.LocateStatusAmbiguous, MatchType: dto.LocateMatchFuzzy, Confidence: 0.9,
		Candidates: []dto.LocateCandidate{{
			DocumentID: "d1", BlockID: "b1", Path: "/vault/d1.md", ChunkKey: "b1", Score: 0.9,
		}},
	}}
	sessions := newStubSessions()
	shardSvc(deps).Sessions = sessions

	l := New(deps)
	turnID, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1",
		UserInput: "/locate\nFuzzy match heer.",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, bus, "locate")
	if calls != 0 {
		t.Fatalf("provider called %d times before the picker choice", calls)
	}
	if err := l.ResolveLocate(turnID, dto.LocateChoice{ChunkKey: "b1"}); err != nil {
		t.Fatalf("ResolveLocate: %v", err)
	}
	waitDone(t, bus)

	if calls == 0 {
		t.Fatal("provider was never called after the choice")
	}
	if got := locateResultInSnapshot(t, snapshotOf(t, busEvents(t, bus))); got.Status != dto.LocateStatusResolved || got.BlockID != "b1" {
		t.Fatalf("snapshot locate = %+v, want resolved b1", got)
	}
}

// Cancel degrades to plain chat with a labeled not-found outcome.
func TestLocateCancelDegrades(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	deps.Locate = &stubLocate{result: dto.LocateResult{
		Status: dto.LocateStatusAmbiguous, MatchType: dto.LocateMatchFuzzy,
		Candidates: []dto.LocateCandidate{{Path: "/vault/d1.md", ChunkKey: "b1", BlockID: "b1", Score: 0.9}},
	}}
	l := New(deps)
	turnID, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1",
		UserInput: "/locate\nsomething fuzzy",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, bus, "locate")
	if err := l.ResolveLocate(turnID, dto.LocateChoice{Cancel: true}); err != nil {
		t.Fatalf("ResolveLocate(cancel): %v", err)
	}
	waitDone(t, bus)
	if got := locateResultInSnapshot(t, snapshotOf(t, busEvents(t, bus))); got.Status != dto.LocateStatusNotFound {
		t.Fatalf("snapshot locate = %+v, want not-found after cancel", got)
	}
}

// Timeout degrades to plain chat with a labeled fail-open (short injected wait).
func TestLocateTimeoutDegrades(t *testing.T) {
	old := locatePickerTimeout
	locatePickerTimeout = 40 * time.Millisecond
	defer func() { locatePickerTimeout = old }()

	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	deps.Locate = &stubLocate{result: dto.LocateResult{
		Status: dto.LocateStatusAmbiguous, MatchType: dto.LocateMatchFuzzy,
		Candidates: []dto.LocateCandidate{{Path: "/vault/d1.md", ChunkKey: "b1", BlockID: "b1", Score: 0.9}},
	}}
	l := New(deps)
	if _, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1",
		UserInput: "/locate\nsomething fuzzy",
	}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)
	res := locateResultInSnapshot(t, snapshotOf(t, busEvents(t, bus)))
	if res.Status != dto.LocateStatusNotFound || res.Context != "locate-cancelled" {
		t.Fatalf("snapshot locate = %+v, want labeled timeout fail-open", res)
	}
}

// Resolving a turn that is not waiting on a picker is the typed refusal.
func TestResolveLocateUnknownTurn(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	l := New(happyPathDeps(bus))
	if err := l.ResolveLocate("nope", dto.LocateChoice{ChunkKey: "x"}); !errors.Is(err, ErrNoPendingLocate) {
		t.Fatalf("ResolveLocate(unknown) = %v, want ErrNoPendingLocate", err)
	}
}

// Scenario: The anchored turn is block-scoped. A real document store is used:
// the model supplies only `text`, the loop forces blockId + baseHash from the
// anchor, a candidate is staged against the anchored block, and accepting writes
// through to disk (ADR-0048 §5, ADR-0047).
func TestLocateAnchoredEditWritesThrough(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := document.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	doc, err := document.NewStore(db, t.TempDir(), t.TempDir(), textformatter.New())
	if err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(note, []byte("The quick brown fox jumps over the lazy dog.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	open, err := doc.Open(note)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := doc.Blocks(open.DocumentID)
	if err != nil || len(blocks) == 0 {
		t.Fatalf("blocks: %v (%d)", err, len(blocks))
	}
	blockID, blockHash := blocks[0].ID, blocks[0].Hash

	exec := &anchoredExecutor{doc: doc, documentID: open.DocumentID, wantBlock: blockID, wantHash: blockHash}
	step := 0
	provider := stubProvider{stream: func(ctx context.Context, emit func(dto.RawEvent)) error {
		if step == 0 {
			step++
			// The model supplies only `text`; the loop injects the target.
			emit(dto.RawEvent{Type: "tool_call", Data: json.RawMessage(
				`{"id":"tc1","name":"edit_markdown","arguments":"{\"text\":\"A quick brown fox leaps over a lazy dog.\"}"}`)})
			return nil
		}
		emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"done"}`)})
		emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":12,"outputTokens":3}`)})
		return nil
	}}

	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	deps.Doc = doc
	deps.Provider = provider
	deps.Executor = exec
	deps.Locate = &stubLocate{result: dto.LocateResult{
		Status: dto.LocateStatusResolved, MatchType: dto.LocateMatchExact, Confidence: 1.0,
		DocumentID: open.DocumentID, BlockID: blockID, Path: note,
	}}
	l := New(deps)
	if _, err := l.Run(ctx, dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: open.DocumentID,
		UserInput: "/locate\nThe quick brown fox jumps over the lazy dog.",
	}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	if !exec.sawForcedArgs {
		t.Fatal("edit_markdown did not receive the forced blockId/baseHash anchor")
	}
	cands, err := doc.Candidates(open.DocumentID, blockID)
	if err != nil || len(cands) == 0 {
		t.Fatalf("candidates = %d (%v), want the anchored block to be staged", len(cands), err)
	}
	if cands[0].Text == "" || cands[0].BlockID != blockID {
		t.Fatalf("candidate = %+v, want anchored block %s", cands[0], blockID)
	}

	wr, err := doc.Commit(open.DocumentID, dto.CommitOptions{})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !wr.WrittenThrough {
		t.Fatal("commit did not write through")
	}
	onDisk, err := os.ReadFile(note)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "leaps") {
		t.Fatalf("disk file not written through: %q", onDisk)
	}
}

// anchoredExecutor stages an edit through a real document store, asserting the
// loop forced the anchor's blockId + baseHash into the tool args.
type anchoredExecutor struct {
	doc        document.Interface
	documentID string
	wantBlock  string
	wantHash   string

	sawForcedArgs bool
}

func (e *anchoredExecutor) Invoke(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if name != "edit_markdown" {
		return nil, nil
	}
	var in struct {
		BlockID  string `json:"blockId"`
		BaseHash string `json:"baseHash"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if in.BlockID == e.wantBlock && in.BaseHash == e.wantHash {
		e.sawForcedArgs = true
	}
	_, err := e.doc.ApplyEdit(context.Background(), e.documentID, dto.BlockEdit{
		BlockID: in.BlockID,
		Text:    in.Text,
		Guards:  []dto.Guard{{BlockID: in.BlockID, Hash: in.BaseHash}},
	})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(`{"ok":true,"blockId":"` + in.BlockID + `"}`), nil
}

// The production path builds the sealed locate resolver from the open document,
// the turn's workspace Retriever, and the Filesystem when Deps.Locate is nil.
func TestLocateProductionResolverWiring(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	deps.Locate = nil
	deps.Doc = stubDoc{
		path:   "/vault/d1.md",
		blocks: []dto.Block{{ID: "b1", Kind: dto.BlockKindParagraph, Text: "Wired paragraph.", Hash: "h1"}},
	}
	l := New(deps)
	if _, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1",
		UserInput: "/locate\nWired paragraph.",
	}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)
	res := locateEventOf(t, busEvents(t, bus))
	if res.Status != dto.LocateStatusResolved || res.BlockID != "b1" {
		t.Fatalf("locate = %+v, want resolved b1 via the real resolver", res)
	}
}

// --------------------- reasoning policy + budgets (ADR-0051) ---------------------

// c5Deps builds happy-path deps with a C5 pipeline policy, runner, and a
// request-recording provider.
func c5Deps(bus *stubBus, policy dto.PipelinePolicy, runner string, stream func(context.Context, func(dto.RawEvent)) error, reqs *[]dto.Request) Deps {
	deps := happyPathDeps(bus)
	deps.Pipeline = stubPipeline{policy: policy}
	res := deps.Fleet.(stubFleet).res
	res.Model.Runner = runner
	deps.Fleet = stubFleet{res: res}
	deps.Provider = stubProvider{stream: stream, reqs: reqs}
	return deps
}

// TestAutoEscalatesOnceAndLabels: `auto` runs thinking-off, and a structured
// edit failure triggers exactly one thinking-on retry, labeled with the failure
// (ADR-0051 §2).
func TestAutoEscalatesOnceAndLabels(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	var reqs []dto.Request
	round := 0
	deps := c5Deps(bus, dto.PipelinePolicy{MaxSteps: 6, Thinking: dto.ThinkingAuto}, "mlx-lm", func(_ context.Context, emit func(dto.RawEvent)) error {
		round++
		if round == 1 {
			emit(dto.RawEvent{Type: "tool_call", Data: json.RawMessage(`{"id":"c1","name":"edit_markdown","arguments":"{\"blockId\":\"b1\",\"text\":\"new\"}"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"tool_calls"}`)})
		} else {
			emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"fixed"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
			emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":14,"outputTokens":5}`)})
		}
		return nil
	}, &reqs)
	deps.Executor = &recordingExecutor{result: json.RawMessage(`{"ok":false,"error":"invalid-structure"}`)}

	events, _, snap := runAndCollect(t, deps, dto.Task{SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix"})
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want 2 (one escalation)", len(reqs))
	}
	if reqs[0].Thinking == nil || *reqs[0].Thinking {
		t.Fatalf("first attempt thinking = %v, want false", reqs[0].Thinking)
	}
	if reqs[1].Thinking == nil || !*reqs[1].Thinking {
		t.Fatalf("retry thinking = %v, want true", reqs[1].Thinking)
	}
	if snap.Thinking == nil || !snap.Thinking.Escalated || snap.Thinking.EscalationReason != "invalid-structure" {
		t.Fatalf("snapshot thinking = %+v, want escalated invalid-structure", snap.Thinking)
	}
	if !hasEvent(events, "done") {
		t.Fatalf("no done event: %+v", events)
	}
}

// TestAutoNoEscalationOnSuccess: a successful tool round does not escalate.
func TestAutoNoEscalationOnSuccess(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	var reqs []dto.Request
	round := 0
	deps := c5Deps(bus, dto.PipelinePolicy{MaxSteps: 6, Thinking: dto.ThinkingAuto}, "mlx-lm", func(_ context.Context, emit func(dto.RawEvent)) error {
		round++
		if round == 1 {
			emit(dto.RawEvent{Type: "tool_call", Data: json.RawMessage(`{"id":"c1","name":"edit_markdown","arguments":"{\"blockId\":\"b1\",\"text\":\"new\"}"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"tool_calls"}`)})
		} else {
			emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"ok"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
		}
		return nil
	}, &reqs)
	deps.Executor = &recordingExecutor{result: json.RawMessage(`{"ok":true,"blockId":"b1"}`)}

	_, _, snap := runAndCollect(t, deps, dto.Task{SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix"})
	if snap.Thinking == nil || snap.Thinking.Escalated {
		t.Fatalf("snapshot thinking = %+v, want no escalation", snap.Thinking)
	}
	for i, r := range reqs {
		if r.Thinking == nil || *r.Thinking {
			t.Fatalf("request %d thinking = %v, want false (no escalation)", i, r.Thinking)
		}
	}
}

// TestOffToggleAndUnsupportedDegrade: `off` sends the disable toggle on a
// supported runner; an unsupported runner degrades to thinking-on with a label
// (ADR-0051 §1/§3).
func TestOffToggleAndUnsupportedDegrade(t *testing.T) {
	stream := func(_ context.Context, emit func(dto.RawEvent)) error {
		emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"done"}`)})
		emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
		emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":5,"outputTokens":2}`)})
		return nil
	}
	policy := dto.PipelinePolicy{MaxSteps: 6, Thinking: dto.ThinkingOff}

	t.Run("supported", func(t *testing.T) {
		bus := &stubBus{done: make(chan struct{})}
		var reqs []dto.Request
		deps := c5Deps(bus, policy, "mlx-lm", stream, &reqs)
		_, _, snap := runAndCollect(t, deps, dto.Task{SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix"})
		if len(reqs) != 1 || reqs[0].Thinking == nil || *reqs[0].Thinking {
			t.Fatalf("request thinking = %+v, want disabled", reqs)
		}
		if snap.Thinking == nil || snap.Thinking.Effective || snap.Thinking.Unsupported {
			t.Fatalf("snapshot thinking = %+v, want effective=false unsupported=false", snap.Thinking)
		}
	})

	t.Run("unsupported", func(t *testing.T) {
		bus := &stubBus{done: make(chan struct{})}
		var reqs []dto.Request
		deps := c5Deps(bus, policy, "delegate", stream, &reqs)
		_, _, snap := runAndCollect(t, deps, dto.Task{SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix"})
		if len(reqs) != 1 || reqs[0].Thinking == nil || !*reqs[0].Thinking {
			t.Fatalf("request thinking = %+v, want degraded to on", reqs)
		}
		if snap.Thinking == nil || !snap.Thinking.Unsupported || !snap.Thinking.Effective {
			t.Fatalf("snapshot thinking = %+v, want unsupported + effective", snap.Thinking)
		}
	})
}

// TestThinkingEventForwarded: reasoning deltas surface as a `thinking` event and
// the measurement records the exact thinking count (ADR-0051 §4/§11).
func TestThinkingEventForwarded(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := c5Deps(bus, dto.PipelinePolicy{MaxSteps: 6, Thinking: dto.ThinkingOn}, "mlx-lm", func(_ context.Context, emit func(dto.RawEvent)) error {
		emit(dto.RawEvent{Type: "reasoning", Data: json.RawMessage(`{"text":"why "}`)})
		emit(dto.RawEvent{Type: "reasoning", Data: json.RawMessage(`{"text":"not"}`)})
		emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"answer"}`)})
		emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
		emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":9,"outputTokens":7,"thinkingTokens":4}`)})
		return nil
	}, nil)

	events, _, snap := runAndCollect(t, deps, dto.Task{SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix"})
	var thinkingText string
	for _, ev := range events {
		if ev.Type == "thinking" {
			var v struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(ev.Data, &v)
			thinkingText += v.Text
		}
	}
	if thinkingText != "why not" {
		t.Fatalf("thinking event text = %q, want %q", thinkingText, "why not")
	}
	if snap.Measurements == nil || snap.Measurements.ThinkingTokens != 4 {
		t.Fatalf("measurement = %+v, want thinking 4", snap.Measurements)
	}
}

// TestLengthWithoutOutcomeIsError: a `length` turn with neither a tool call nor
// an answer is a labeled terminal error, never an empty done (ADR-0051 §5).
func TestLengthWithoutOutcomeIsError(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := c5Deps(bus, dto.PipelinePolicy{MaxSteps: 6, Thinking: dto.ThinkingOn}, "mlx-lm", func(_ context.Context, emit func(dto.RawEvent)) error {
		emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"length"}`)})
		return nil
	}, nil)

	events, _, snap := runAndCollect(t, deps, dto.Task{SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix"})
	last := events[len(events)-1]
	if last.Type != "error" {
		t.Fatalf("last event = %s, want error: %+v", last.Type, events)
	}
	var ed struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(last.Data, &ed)
	if ed.Code != "thinking-truncated" {
		t.Fatalf("error code = %q, want thinking-truncated", ed.Code)
	}
	if snap.Thinking == nil || !snap.Thinking.Truncated {
		t.Fatalf("snapshot thinking = %+v, want truncated", snap.Thinking)
	}
}

// TestThinkingBudgetTruncationLabeled: exceeding the reasoning cap labels the
// turn thinking-truncated even when an answer still lands.
func TestThinkingBudgetTruncationLabeled(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := c5Deps(bus, dto.PipelinePolicy{MaxSteps: 6, Thinking: dto.ThinkingOn, MaxThinkingTokens: 2}, "mlx-lm", func(_ context.Context, emit func(dto.RawEvent)) error {
		emit(dto.RawEvent{Type: "reasoning", Data: json.RawMessage(`{"text":"` + strings.Repeat("x", 40) + `"}`)}) // 10 tokens > cap
		emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"answer"}`)})
		emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
		return nil
	}, nil)

	_, _, snap := runAndCollect(t, deps, dto.Task{SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix"})
	if snap.Thinking == nil || !snap.Thinking.Truncated {
		t.Fatalf("snapshot thinking = %+v, want truncated", snap.Thinking)
	}
}

// TestCompactionReplacesHistoryMetered: history over the trigger is compacted
// into one metered summary; the snapshot records the range (ADR-0051 §8).
func TestCompactionReplacesHistoryMetered(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)
	sess := newStubSessions()
	sess.hist = []dto.Message{
		{Role: "user", Content: strings.Repeat("a", 400), Timestamp: 1},
		{Role: "assistant", Content: strings.Repeat("b", 400), Timestamp: 2},
		{Role: "user", Content: strings.Repeat("c", 400), Timestamp: 3},
		{Role: "assistant", Content: strings.Repeat("d", 400), Timestamp: 4},
	}
	svc := shardSvc(deps)
	svc.Sessions = sess
	deps.Pipeline = stubPipeline{policy: dto.PipelinePolicy{
		MaxSteps: 6, MaxHistoryTokens: 100000, MaxRagTokens: 100000, MaxMentionTokens: 100000, AutoRagTopK: 3,
		Compaction: dto.CompactionPolicy{Enabled: true, TriggerHistoryTokens: 10, KeepRecentTurns: 1},
	}}
	round := 0
	deps.Provider = stubProvider{stream: func(_ context.Context, emit func(dto.RawEvent)) error {
		round++
		if round == 1 { // the compaction summary call
			emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"a compact summary"}`)})
			emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":200,"outputTokens":20}`)})
		} else {
			emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"answer"}`)})
			emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
			emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":20,"outputTokens":5}`)})
		}
		return nil
	}}

	_, _, snap := runAndCollect(t, deps, dto.Task{SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix"})
	if snap.Compacted == nil {
		t.Fatalf("snapshot compacted = nil, want a range")
	}
	if snap.Compacted.Turns != 1 || !snap.Compacted.CacheCost {
		t.Fatalf("compacted = %+v, want 1 turn + cache cost", snap.Compacted)
	}
	m := svc.Meter.(*stubMeter)
	m.mu.Lock()
	compactions := m.compactions
	m.mu.Unlock()
	if compactions != 1 {
		t.Fatalf("compaction meter rows = %d, want 1", compactions)
	}
}

// TestSessionBudgetSoftAndHard: the soft threshold warns and proceeds; the hard
// threshold refuses (ADR-0051 §7).
func TestSessionBudgetSoftAndHard(t *testing.T) {
	stream := func(_ context.Context, emit func(dto.RawEvent)) error {
		emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"done"}`)})
		emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
		return nil
	}

	t.Run("soft warns and proceeds", func(t *testing.T) {
		bus := &stubBus{done: make(chan struct{})}
		budget := 100
		deps := happyPathDeps(bus)
		svc := shardSvc(deps)
		svc.Sessions = budgetSessions{budget: &budget}
		svc.Meter = &budgetMeter{used: 85}
		deps.Pipeline = stubPipeline{policy: dto.PipelinePolicy{MaxSteps: 6, SessionBudgetSoftRatio: 0.8}}
		deps.Provider = stubProvider{stream: stream}

		events, _, snap := runAndCollect(t, deps, dto.Task{SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix"})
		if snap.SessionBudget == nil || !snap.SessionBudget.Soft || snap.SessionBudget.Hard {
			t.Fatalf("session budget = %+v, want soft only", snap.SessionBudget)
		}
		if !hasEvent(events, "done") {
			t.Fatalf("soft budget should proceed to done: %+v", events)
		}
	})

	t.Run("hard refuses", func(t *testing.T) {
		bus := &stubBus{done: make(chan struct{})}
		budget := 99
		deps := happyPathDeps(bus)
		svc := shardSvc(deps)
		svc.Sessions = budgetSessions{budget: &budget}
		svc.Meter = &budgetMeter{used: 100}
		deps.Pipeline = stubPipeline{policy: dto.PipelinePolicy{MaxSteps: 6, SessionBudgetSoftRatio: 0.8}}
		deps.Provider = stubProvider{stream: stream}

		events, _, _ := runAndCollect(t, deps, dto.Task{SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix"})
		last := events[len(events)-1]
		if last.Type != "error" {
			t.Fatalf("last event = %s, want error", last.Type)
		}
		var ed struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(last.Data, &ed)
		if ed.Code != "session-budget-exceeded" {
			t.Fatalf("error code = %q, want session-budget-exceeded", ed.Code)
		}
	})
}
