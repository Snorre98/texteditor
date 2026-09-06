package loop

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"texteditor/internal/provider"
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

func (s stubTools) Register(dto.ToolDef) error          { return nil }
func (s stubTools) List() []dto.ToolDef                 { return s.defs }
func (s stubTools) AllowlistFor(dto.Mode) []dto.ToolDef { return s.defs }

type stubExecutor struct{}

func (stubExecutor) Invoke(string, json.RawMessage) (json.RawMessage, error) { return nil, nil }

// recordingExecutor records invocations and returns a fixed result.
type recordingExecutor struct {
	mu      sync.Mutex
	calls   []string
	result  json.RawMessage
	errResp error
}

func (r *recordingExecutor) Invoke(name string, args json.RawMessage) (json.RawMessage, error) {
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
}

func (s stubProvider) Chat(context.Context, dto.Target, dto.Request) (dto.Completion, error) {
	return dto.Completion{}, nil
}
func (s stubProvider) Stream(ctx context.Context, t dto.Target, r dto.Request, emit func(dto.RawEvent)) error {
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
func (s stubFleet) Start(string) error                                      { return nil }
func (s stubFleet) Stop(string) error                                       { return nil }
func (s stubFleet) Provision(context.Context, string) (string, error)       { return "", nil }
func (s stubFleet) Fingerprint(string) (string, error)                      { return "", nil }

type stubDoc struct{}

func (stubDoc) Open(string) (string, error)        { return "", nil }
func (stubDoc) Save(dto.Document) error            { return nil }
func (stubDoc) Blocks(string) ([]dto.Block, error) { return nil, nil }
func (stubDoc) ApplyEdit(context.Context, string, dto.BlockEdit) (dto.Revision, error) {
	return dto.Revision{}, nil
}
func (stubDoc) Commit(string, string) error                         { return nil }
func (stubDoc) Diff(string, string, string) ([]dto.WordEdit, error) { return nil, nil }
func (stubDoc) History(string) ([]dto.Revision, error)              { return nil, nil }
func (stubDoc) Candidates(string, string) ([]dto.Candidate, error)  { return nil, nil }

type stubRetriever struct{ chunks []dto.Chunk }

func (s stubRetriever) Query(context.Context, string, int) ([]dto.Chunk, error) { return s.chunks, nil }
func (stubRetriever) Index(context.Context, string) error                       { return nil }

type stubSessions struct{ hist []dto.Message }

func (s stubSessions) ListByDocument(string) ([]dto.Session, error) { return nil, nil }
func (stubSessions) Create(string, *string, string) (dto.Session, error) {
	return dto.Session{}, nil
}
func (stubSessions) Resume(string) (dto.Session, error)      { return dto.Session{}, nil }
func (stubSessions) Append(string, dto.Message) error        { return nil }
func (s stubSessions) History(string) ([]dto.Message, error) { return s.hist, nil }

// stubWorkspace implements the loop's workspace.Interface over an in-memory
// map keyed by path → (content, error). Used to drive mention resolution.
type stubWorkspace struct {
	files map[string]string
	err   map[string]error
}

func (s *stubWorkspace) List(context.Context, string) ([]workspace.Entry, error) { return nil, nil }
func (s *stubWorkspace) Read(_ context.Context, path string, maxBytes int) ([]byte, error) {
	if e, ok := s.err[path]; ok {
		return nil, e
	}
	b := []byte(s.files[path])
	if maxBytes > 0 && len(b) > maxBytes {
		return nil, workspace.ErrTooLarge
	}
	return b, nil
}

type stubMeter struct {
	mu     sync.Mutex
	called int
}

func (s *stubMeter) Attribute(context.Context, string, string, string, dto.Breakdown, dto.ProviderCounts) (dto.AttributedBreakdown, error) {
	s.mu.Lock()
	s.called++
	s.mu.Unlock()
	return dto.AttributedBreakdown{}, nil
}
func (s *stubMeter) SessionUsage(context.Context, string) (int, error) { return 0, nil }

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
		Doc:       stubDoc{},
		Retriever: stubRetriever{},
		Sessions:  stubSessions{},
		Meter:     &stubMeter{},
		Bus:       bus,
	}
}

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
		"editor": {Name: "editor", DefaultModel: "gemma4-12b", Agentic: true, MaxSteps: 4},
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
		"drafter": {Name: "drafter", DefaultModel: "mistral-24b", Agentic: true, MaxSteps: 4},
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
	sawRag := false
	for _, ev := range bus.events {
		if ev.Type == "rag" {
			sawRag = true
			var d struct {
				Chunks []struct {
					BlockID string `json:"blockId"`
					Text    string `json:"text"`
				} `json:"chunks"`
			}
			if err := json.Unmarshal(ev.Data, &d); err != nil || len(d.Chunks) != 1 || d.Chunks[0].Text != "cited" {
				t.Fatalf("rag data = %s, want structured chunks", ev.Data)
			}
		}
	}
	if !sawRag {
		t.Fatalf("no rag event: %+v", bus.events)
	}
}

// TestBudgetExceeded surfaces session-budget-exceeded before any model call.
func TestBudgetExceeded(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := happyPathDeps(bus)

	budget := 10
	deps.Sessions = budgetSessions{budget: &budget}
	deps.Meter = &budgetMeter{used: 11}

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
func (budgetSessions) Create(string, *string, string) (dto.Session, error) {
	return dto.Session{}, nil
}
func (b budgetSessions) Resume(string) (dto.Session, error) {
	return dto.Session{TokenBudget: b.budget}, nil
}
func (budgetSessions) Append(string, dto.Message) error      { return nil }
func (budgetSessions) History(string) ([]dto.Message, error) { return nil, nil }

type budgetMeter struct{ used int }

func (b budgetMeter) Attribute(context.Context, string, string, string, dto.Breakdown, dto.ProviderCounts) (dto.AttributedBreakdown, error) {
	return dto.AttributedBreakdown{}, nil
}
func (b budgetMeter) SessionUsage(context.Context, string) (int, error) { return b.used, nil }

// --------------------- router-mode stubs (ADR-0028) ---------------------

// stubDecider implements the loop's sealed Decider subset (interface.md §8b).
type stubDecider struct {
	mu       sync.Mutex
	calls    int
	intents  []string
	decision dto.Decision
	err      error
}

func (s *stubDecider) SignalTool() dto.ToolDef {
	return dto.ToolDef{
		Name:        "request_tool",
		Description: "Request an external action.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"intent":{"type":"string"}},"required":["intent"]}`),
	}
}

func (s *stubDecider) Decide(_ context.Context, intent string, _ dto.RouterContext) (dto.RouterResult, error) {
	s.mu.Lock()
	s.calls++
	s.intents = append(s.intents, intent)
	s.mu.Unlock()
	if s.err != nil {
		return dto.RouterResult{}, s.err
	}
	return dto.RouterResult{
		Decision: s.decision,
		Usage: dto.RouterUsage{
			Breakdown: dto.Breakdown{SystemPrompt: 40, User: 5},
			Counts:    dto.ProviderCounts{InputTokens: 45, OutputTokens: 10},
		},
	}, nil
}

// recordingMeter records the model name of every Attribute call (the router row
// must be tagged needle-router, ADR-0028 §5).
type recordingMeter struct {
	mu   sync.Mutex
	rows []string
}

func (r *recordingMeter) Attribute(_ context.Context, _, _, model string, _ dto.Breakdown, _ dto.ProviderCounts) (dto.AttributedBreakdown, error) {
	r.mu.Lock()
	r.rows = append(r.rows, model)
	r.mu.Unlock()
	return dto.AttributedBreakdown{}, nil
}
func (r *recordingMeter) SessionUsage(context.Context, string) (int, error) { return 0, nil }

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

// requestToolRound + answerRound are the writer's two router-mode rounds.
func requestToolRound(emit func(dto.RawEvent)) {
	emit(dto.RawEvent{Type: "tool_call", Data: json.RawMessage(`{"id":"c1","name":"request_tool","arguments":"{\"intent\":\"rewrite block b9\"}"}`)})
	emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"tool_calls"}`)})
}

func answerRound(emit func(dto.RawEvent)) {
	emit(dto.RawEvent{Type: "token", Data: json.RawMessage(`{"text":"answered"}`)})
	emit(dto.RawEvent{Type: "finish", Data: json.RawMessage(`{"reason":"stop"}`)})
	emit(dto.RawEvent{Type: "done", Data: json.RawMessage(`{"inputTokens":14,"outputTokens":5}`)})
}

func routerDeps(bus *stubBus, decider Decider, prov provider.Interface) Deps {
	deps := happyPathDeps(bus)
	deps.Modes = stubMode{modes: map[string]dto.Mode{
		"editor": {Name: "editor", DefaultModel: "gemma4-12b", Agentic: true, MaxSteps: 4, ToolCalling: "router"},
	}}
	deps.Decider = decider
	deps.Provider = prov
	return deps
}

// --------------------- router-mode tests (tool-routing.feature) ---------------------

// TestRouterConfidentDispatch: the writer emits request_tool; Decide returns a
// confident Decision; the loop invokes the resolved tool and meters the router
// call as its own needle-router row (ADR-0028 §5, D4).
func TestRouterConfidentDispatch(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	decider := &stubDecider{decision: dto.Decision{
		Name:       "edit_markdown",
		Args:       json.RawMessage(`{"blockId":"b9","text":"new"}`),
		Confidence: 0.9,
	}}
	exec := &recordingExecutor{result: json.RawMessage(`{"ok":true,"blockId":"b9","diff":{"ok":true}}`)}
	meter := &recordingMeter{}

	round := 0
	prov := &reqProvider{stream: func(ctx context.Context, req dto.Request, emit func(dto.RawEvent)) error {
		round++
		if round == 1 {
			requestToolRound(emit)
		} else {
			answerRound(emit)
		}
		return nil
	}}

	deps := routerDeps(bus, decider, prov)
	deps.Executor = exec
	deps.Meter = meter
	l := New(deps)

	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "editor", DocumentID: "d1", UserInput: "rewrite it",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	exec.mu.Lock()
	calls := append([]string(nil), exec.calls...)
	exec.mu.Unlock()
	if len(calls) != 1 || calls[0] != "edit_markdown" {
		t.Fatalf("executor calls = %v, want [edit_markdown] (the resolved tool)", calls)
	}

	decider.mu.Lock()
	gotIntent := append([]string(nil), decider.intents...)
	decider.mu.Unlock()
	if len(gotIntent) != 1 || gotIntent[0] != "rewrite block b9" {
		t.Fatalf("intents = %v, want [rewrite block b9]", gotIntent)
	}

	meter.mu.Lock()
	rows := append([]string(nil), meter.rows...)
	meter.mu.Unlock()
	if len(rows) != 2 || rows[0] != "needle-router" || rows[1] != "gemma4-12b" {
		t.Fatalf("meter rows = %v, want [needle-router gemma4-12b] (router row + writer row)", rows)
	}

	events := busEvents(t, bus)
	if !hasEvent(events, "candidate") || !hasEvent(events, "done") {
		t.Fatalf("want candidate + done, got %+v", events)
	}
}

// TestRouterRefusalAnswers: a refused/empty Decide (zero Decision) appends a
// "no tool" result and drives one more writer round whose stream is the
// answering phase — no error event, done still lands (state-machine §1.2).
func TestRouterRefusalAnswers(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	decider := &stubDecider{decision: dto.Decision{}} // refusal
	meter := &recordingMeter{}

	round := 0
	prov := &reqProvider{stream: func(ctx context.Context, req dto.Request, emit func(dto.RawEvent)) error {
		round++
		if round == 1 {
			requestToolRound(emit)
		} else {
			answerRound(emit)
		}
		return nil
	}}

	deps := routerDeps(bus, decider, prov)
	deps.Meter = meter
	l := New(deps)

	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "editor", DocumentID: "d1", UserInput: "rewrite it",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	events := busEvents(t, bus)
	if !hasEvent(events, "done") {
		t.Fatalf("want done, got %+v", events)
	}
	for _, ev := range events {
		if ev.Type == "error" {
			t.Fatalf("refusal must emit no error event, got %+v", events)
		}
	}
	meter.mu.Lock()
	defer meter.mu.Unlock()
	if len(meter.rows) != 2 || meter.rows[0] != "needle-router" {
		t.Fatalf("meter rows = %v, want a needle-router row even on refusal", meter.rows)
	}
}

// TestRouterDecideErrorDegrades: a Decide transport error emits a labeled
// router-unreachable event, then the same bounded writer round answers — no
// retry loop (failure-semantics §3).
func TestRouterDecideErrorDegrades(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	decider := &stubDecider{err: errors.New("needle down mid-turn")}

	round := 0
	prov := &reqProvider{stream: func(ctx context.Context, req dto.Request, emit func(dto.RawEvent)) error {
		round++
		if round == 1 {
			requestToolRound(emit)
		} else {
			answerRound(emit)
		}
		return nil
	}}

	deps := routerDeps(bus, decider, prov)
	l := New(deps)

	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "editor", DocumentID: "d1", UserInput: "rewrite it",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	events := busEvents(t, bus)
	var sawRouterErr bool
	for _, ev := range events {
		if ev.Type == "error" {
			var v struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(ev.Data, &v)
			if v.Code == "router-unreachable" {
				sawRouterErr = true
			}
		}
	}
	if !sawRouterErr || !hasEvent(events, "done") {
		t.Fatalf("want router-unreachable error + graceful done, got %+v", events)
	}
}

// TestRouterSplicesRequestTool: in router mode the writer's payload carries
// exactly the single synthetic request_tool, never the mode allowlist.
func TestRouterSplicesRequestTool(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	decider := &stubDecider{decision: dto.Decision{}} // refusal; tools still asserted

	round := 0
	prov := &reqProvider{stream: func(ctx context.Context, req dto.Request, emit func(dto.RawEvent)) error {
		round++
		if round == 1 {
			requestToolRound(emit)
		} else {
			answerRound(emit)
		}
		return nil
	}}

	deps := routerDeps(bus, decider, prov)
	deps.Tools = stubTools{defs: []dto.ToolDef{
		{Name: "edit_markdown"}, {Name: "retrieve"}, {Name: "diff"},
	}}
	l := New(deps)

	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "editor", DocumentID: "d1", UserInput: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	prov.mu.Lock()
	defer prov.mu.Unlock()
	if len(prov.reqs) == 0 {
		t.Fatal("provider never called")
	}
	tools := prov.reqs[0].Tools
	if len(tools) != 1 || tools[0].Name != "request_tool" {
		t.Fatalf("tools = %+v, want exactly [request_tool]", tools)
	}
}

// TestNativeNeverConsultsDecider: a wired decider is invisible to native modes
// (the byte-identical baseline, ADR-0028 §3).
func TestNativeNeverConsultsDecider(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	decider := &stubDecider{decision: dto.Decision{Name: "retrieve"}}

	deps := happyPathDeps(bus)
	deps.Decider = decider
	l := New(deps)

	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "proofreader", DocumentID: "d1", UserInput: "fix",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	decider.mu.Lock()
	defer decider.mu.Unlock()
	if decider.calls != 0 {
		t.Fatalf("decider consulted %d times in native mode, want 0", decider.calls)
	}
}

// TestRouterUnwiredEmitsError: a router mode with no wired decider (a
// composition-root bug the startup gates prevent) surfaces an error event
// instead of half-running.
func TestRouterUnwiredEmitsError(t *testing.T) {
	bus := &stubBus{done: make(chan struct{})}
	deps := routerDeps(bus, nil, stubProvider{stream: func(ctx context.Context, emit func(dto.RawEvent)) error {
		return nil
	}})
	l := New(deps)

	_, err := l.Run(context.Background(), dto.Task{
		SessionID: "s1", ModeName: "editor", DocumentID: "d1", UserInput: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, bus)

	events := busEvents(t, bus)
	if len(events) == 0 || events[0].Type != "error" {
		t.Fatalf("want an error event, got %+v", events)
	}
}

// --------------------- mention resolution (ADR-0036 §2) ---------------------

// recordingAssembler captures the MentionContent handed to it, asserting the
// loop resolves mentions through Workspace and passes them to the assembler.
type recordingAssembler struct {
	mu       sync.Mutex
	mentions []dto.MentionContent
}

func (r *recordingAssembler) Assemble(_ context.Context, in dto.AssemblerInput) (dto.Payload, dto.Breakdown, error) {
	r.mu.Lock()
	r.mentions = append([]dto.MentionContent(nil), in.Mentions...)
	r.mu.Unlock()
	return dto.Payload{Request: dto.Request{ModelName: "gemma4-12b"}}, dto.Breakdown{SystemPrompt: 10, User: 4}, nil
}

func mentionDeps(bus *stubBus) Deps {
	d := happyPathDeps(bus)
	d.Workspace = &stubWorkspace{
		files: map[string]string{"/notes/a.md": "mentioned content"},
		err:   map[string]error{},
	}
	return d
}

// TestMentionsResolvedAndSpliced: valid mentions resolve through Workspace and
// reach the assembler as MentionContent.
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
		{"not-found", workspace.ErrNotFound, "mention-not-found"},
		{"not-regular", workspace.ErrNotRegular, "mention-not-found"},
		{"too-large", workspace.ErrTooLarge, "mention-too-large"},
		{"read-failed", workspace.ErrReadFailed, "mention-unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := &stubBus{done: make(chan struct{})}
			deps := happyPathDeps(bus)
			deps.Workspace = &stubWorkspace{
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
	deps.Workspace = &stubWorkspace{files: map[string]string{}, err: map[string]error{}}

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
