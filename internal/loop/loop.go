// Package loop holds the Agent loop — the thin orchestrator owning only the turn
// state machine (ADR-0016 §3, state-machine.md §1). All real logic lives in the
// leaves; the loop wires them together, is session-scoped (ADR-0026 §3), and
// forwards events to the bus tagged with a `turnID`.
//
// The turn: planning → (dispatching → observing)* → answering → done | error.
// The dispatch/observe cycle is bounded by mode.maxSteps and is only entered for
// agentic modes; a non-agentic mode (maxSteps 0) is a single-shot pass
// (state-machine.md §1.3).
package loop

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"texteditor/internal/assembler"
	"texteditor/internal/document"
	"texteditor/internal/fleet"
	"texteditor/internal/meter"
	"texteditor/internal/mode"
	"texteditor/internal/provider"
	"texteditor/internal/retriever"
	"texteditor/internal/session"
	"texteditor/internal/tool"
	"texteditor/shared/dto"
)

// AgentLoop is the Agent loop public API (interface.md §7).
type AgentLoop interface {
	Run(ctx context.Context, task dto.Task) (turnID string, err error)
}

// Interface is an alias for AgentLoop (the contracted name, interface.md §7).
type Interface = AgentLoop

// Emitter is the sealed subset of the event bus the loop needs (fan-out of
// turn events). The bus owns subscription (interface.md §11).
type Emitter interface {
	Emit(ev dto.Event)
}

// Deps holds the loop's injected dependencies (the composition root wires these).
type Deps struct {
	Modes     mode.Interface
	Tools     tool.Registry
	Executor  tool.Executor
	Assembler assembler.Interface
	Provider  provider.Interface
	Fleet     fleet.Interface
	Doc       document.Interface
	Retriever retriever.Interface
	Sessions  session.Interface
	Meter     meter.Interface
	Bus       Emitter
}

// loop is the concrete Agent loop.
type loop struct {
	d Deps
}

// New returns an Agent loop over the supplied dependencies.
func New(d Deps) AgentLoop {
	return &loop{d: d}
}

// Run starts a turn asynchronously, returning its turnID. Events are tagged with
// the turnID; correlation to the requesting client is the API server's job.
func (l *loop) Run(ctx context.Context, task dto.Task) (string, error) {
	turnID := uuid.NewString()
	go l.runTurn(ctx, turnID, task)
	return turnID, nil
}

// validate checks the task resolves: mode exists and a model serves it.
func (l *loop) validate(task dto.Task) (mode.Mode, dto.Resolution, error) {
	m, err := l.d.Modes.Get(task.ModeName)
	if err != nil {
		return mode.Mode{}, dto.Resolution{}, err
	}
	res, err := l.d.Fleet.Resolve(m.DefaultModel, dto.ResolveOpts{ModeTag: m.Name})
	if err != nil {
		return m, dto.Resolution{}, err
	}
	return m, res, nil
}

func (l *loop) runTurn(ctx context.Context, turnID string, task dto.Task) {
	m, res, err := l.validate(task)
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// planning: read session history + retrieved chunks.
	history, _ := l.d.Sessions.History(task.SessionID)

	var chunks []dto.Chunk
	if m.Agentic {
		chunks, _ = l.d.Retriever.Query(ctx, task.UserInput, 3)
	}

	tools := toolsFor(l.d.Tools, m)

	payload, breakdown, err := l.d.Assembler.Assemble(ctx, dto.AssemblerInput{
		Mode:      m,
		ModelName: res.UsedName,
		Params:    res.EffectiveParams,
		Tools:     tools,
		RAGChunks: chunks,
		History:   history,
		UserInput: task.UserInput,
	})
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// answering: stream, forward tokens, then meter + persist.
	target := dto.Target{BaseURL: res.Model.BaseURL, Capabilities: res.Model.Capabilities}
	var lastCounts dto.ProviderCounts
	emit := func(raw dto.RawEvent) {
		switch raw.Type {
		case "done":
			parseDone(raw, &lastCounts)
			l.emit(turnID, dto.Event{Type: "done", Data: raw.Data})
		case "token":
			l.emit(turnID, dto.Event{Type: "token", Data: raw.Data})
		case "error":
			l.emit(turnID, dto.Event{Type: "error", Data: raw.Data})
		}
	}
	if err := l.d.Provider.Stream(ctx, target, payload.Request, emit); err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// attribute + persist + append history.
	if _, err := l.d.Meter.Attribute(ctx, turnID, task.SessionID, res.UsedName, breakdown, lastCounts); err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
	}
}

func (l *loop) emit(turnID string, ev dto.Event) {
	if l.d.Bus == nil {
		return
	}
	ev.TurnID = turnID
	l.d.Bus.Emit(ev)
}

// toolSchemasFor returns the mode's allowlisted tools (in allowlist order, so the
// payload order matches the meter).
func toolsFor(reg tool.Registry, m mode.Mode) []dto.ToolDef {
	return reg.AllowlistFor(m)
}

func parseDone(raw dto.RawEvent, counts *dto.ProviderCounts) {
	var v struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	}
	if err := json.Unmarshal(raw.Data, &v); err != nil {
		return
	}
	counts.InputTokens = v.InputTokens
	counts.OutputTokens = v.OutputTokens
}

func errorData(err error) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"code": codeFor(err), "message": err.Error()})
	return b
}

func codeFor(err error) string {
	switch {
	case errors.Is(err, fleet.ErrModelNotFound):
		return "model-not-found"
	case errors.Is(err, fleet.ErrNoModelAvailable):
		return "no-model-available"
	case errors.Is(err, provider.ErrProviderUnreachable):
		return "provider-unreachable"
	default:
		return "error"
	}
}
