package loop

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

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
