package loop

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

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
func (s stubFleet) ListStatus() ([]dto.ModelState, error)                   { return nil, nil }
func (s stubFleet) Start(string) error                                      { return nil }
func (s stubFleet) Stop(string) error                                       { return nil }
func (s stubFleet) Provision(context.Context, string) (string, error)       { return "", nil }
func (s stubFleet) Fingerprint(string) (string, error)                      { return "", nil }

type stubDoc struct{}

func (stubDoc) Open(string) (dto.OpenResult, error) { return dto.OpenResult{}, nil }
func (stubDoc) SaveTree(string, []dto.BlockWrite, dto.SaveOptions) (dto.WriteResult, error) {
	return dto.WriteResult{}, nil
}
func (stubDoc) Blocks(string) ([]dto.Block, error) { return nil, nil }
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
	mu     sync.Mutex
	chunks []dto.Chunk
	calls  []int // topK per Query
}

func (s *stubRetriever) Query(_ context.Context, _ string, topK int) ([]dto.Chunk, error) {
	s.mu.Lock()
	s.calls = append(s.calls, topK)
	s.mu.Unlock()
	return s.chunks, nil
}
func (*stubRetriever) Index(context.Context, string) error { return nil }

func (s *stubRetriever) queries() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.calls...)
}

// stubPipeline supplies the one global policy (ADR-0045 §3) to the loop.
type stubPipeline struct{ policy dto.PipelinePolicy }

func (s stubPipeline) Policy() dto.PipelinePolicy { return s.policy }

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
		Retriever: &stubRetriever{},
		Sessions:  stubSessions{},
		Meter:     &stubMeter{},
		Bus:       bus,
		Pipeline: stubPipeline{policy: dto.PipelinePolicy{
			MaxSteps:         6,
			MaxHistoryTokens: 32000,
			MaxRagTokens:     16000,
			MaxMentionTokens: 16000,
			AutoRagTopK:      3,
		}},
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
			deps.Retriever = ret
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
// asserting the loop resolves mentions through Workspace and always retrieves.
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

// TestInjectDocumentIDCarriesMode covers ADR-0020 §1: edit_markdown args get the
// turn's preset (for the derived commit message) alongside the document binding;
// other tools never see either field.
func TestInjectDocumentIDCarriesMode(t *testing.T) {
	task := dto.Task{DocumentID: "d1", ModeName: "proofreader"}

	out := injectDocumentID("edit_markdown", task, json.RawMessage(`{"blockId":"b1","text":"x"}`))
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["documentId"] != "d1" || m["modeName"] != "proofreader" {
		t.Fatalf("edit args = %v, want documentId + modeName", m)
	}

	out = injectDocumentID("diff", task, json.RawMessage(`{}`))
	m = map[string]interface{}{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["modeName"]; ok {
		t.Fatalf("diff args = %v, must not carry modeName", m)
	}
}
