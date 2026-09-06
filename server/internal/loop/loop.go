// Package loop holds the Agent loop — the thin orchestrator owning only the turn
// state machine (ADR-0016 §3, state-machine.md §1). All real logic lives in the
// leaves; the loop wires them together, is session-scoped (ADR-0026 §3), and
// forwards events to the bus tagged with a `turnID`.
//
// The turn: planning → (dispatching → observing)* → answering → done | error.
// The dispatch/observe cycle is bounded by mode.maxSteps and is entered only for
// agentic modes (maxSteps > 0); a non-agentic mode (maxSteps 0) is a single-shot
// pass (state-machine.md §1.3). Native tool-calling (ADR-0028 default) drives
// tool dispatch from the model's own tool_calls; edit-integrity results
// (guard-failed / invalid-structure, ADR-0029) re-enter dispatching with a
// re-read or the issue list, both counting against maxSteps.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"time"

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

// Decider is the sealed subset of the ToolDecider the loop consumes in router
// mode (interface.md §8b). nil when no mode opts into the router — the native
// baseline wires no ToolDecider (ADR-0028 §3).
type Decider interface {
	SignalTool() dto.ToolDef
	Decide(ctx context.Context, intent string, c dto.RouterContext) (dto.RouterResult, error)
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
	Decider   Decider // optional: wired only when a mode sets toolCalling "router"
}

// The router-mode protocol constants (ADR-0028 §2/§7). The writer's synthetic
// tool name, and the router serving model name (the meter row's model column).
const (
	requestToolName = "request_tool"
	routerModelName = "needle-router"
)

// errRouterUnwired is the defensive guard for a composition-root wiring bug:
// a mode requests the router but no Decider was wired. The startup gates
// (routergate.Check) make this unreachable.
var errRouterUnwired = errors.New("router-unavailable: mode requests router tool-calling but no ToolDecider is wired")

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

	// Append the user's turn input to the session so the conversation is durable
	// (ADR-0026 §3). The assistant's completions are appended after they land.
	if err := l.d.Sessions.Append(task.SessionID, dto.Message{Role: "user", Content: task.UserInput}); err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// Per-session budget gate (ADR-0026 §5) — checked before any model call.
	sess, _ := l.d.Sessions.Resume(task.SessionID)
	if sess.TokenBudget != nil {
		used, err := l.d.Meter.SessionUsage(ctx, task.SessionID)
		if err == nil && meter.SessionExceeded(used, sess.TokenBudget, 1) {
			l.emit(turnID, dto.Event{Type: "error", Data: errorData(meter.ErrSessionBudgetExceeded)})
			return
		}
	}

	var chunks []dto.Chunk
	if m.Agentic {
		chunks, _ = l.d.Retriever.Query(ctx, task.UserInput, 3)
	}

	tools := toolsFor(l.d.Tools, m)

	// Router toggle (ADR-0028 §3): when the mode opts in, the writer sees only
	// the synthetic request_tool; the real allowlist rides along as the
	// Decide candidate set. Native modes never consult the decider.
	var r *route
	if m.ToolCalling == "router" {
		if l.d.Decider == nil {
			l.emit(turnID, dto.Event{Type: "error", Data: errorData(errRouterUnwired)})
			return
		}
		r = &route{
			decider:   l.d.Decider,
			allowlist: tools,
			chunks:    chunks,
			history:   history,
		}
		tools = []dto.ToolDef{l.d.Decider.SignalTool()}
	}

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

	target := dto.Target{BaseURL: res.Model.BaseURL, Capabilities: res.Model.Capabilities}

	// The turn state machine (state-machine.md §1): planning → (dispatching →
	// observing)* → answering. maxSteps bounds the tool loop; a non-agentic mode
	// (maxSteps 0) is a single answering pass with no dispatch.
	maxSteps := m.MaxSteps
	if !m.Agentic {
		maxSteps = 0
	}

	result, err := l.runSteps(ctx, turnID, task, target, payload, tools, maxSteps, r)
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// answering: the final stream already forwarded tokens; now meter + persist.
	if result.counts.InputTokens+result.counts.OutputTokens > 0 {
		if _, err := l.d.Meter.Attribute(ctx, turnID, task.SessionID, res.UsedName, breakdown, result.counts); err != nil {
			l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		}
	}

	// Persist the assistant's final answer (ADR-0026 §3).
	if result.text != "" {
		_ = l.d.Sessions.Append(task.SessionID, dto.Message{Role: "assistant", Content: result.text})
	}

	l.emitFinal(turnID, res, result)
}

// streamResult is the loop's view of one provider stream round: the accumulated
// answer text, any tool calls, the finish reason, and the final usage counts.
type streamResult struct {
	text      string
	toolCalls []dto.ToolCall
	finish    string
	counts    dto.ProviderCounts
}

// route is the router-mode context threaded through runSteps. nil when the
// mode is native — the native path is byte-identical to the accepted baseline.
type route struct {
	decider   Decider
	allowlist []dto.ToolDef // the mode's real allowlist (the Decide candidate set)
	chunks    []dto.Chunk
	history   []dto.Message
}

// runSteps drives the tool-calling loop. It mutates `msgs` (the assembled
// message list) across round-trips, threading assistant tool_calls and tool
// results. It returns the final stream result (the answering round). In router
// mode (r != nil) the writer's request_tool calls are intercepted and resolved
// through the decider (state-machine §1.2: planning → deciding → dispatch |
// answering); native tool-calling is unchanged.
func (l *loop) runSteps(ctx context.Context, turnID string, task dto.Task, target dto.Target, payload dto.Payload, tools []dto.ToolDef, maxSteps int, r *route) (streamResult, error) {
	msgs := payload.Messages

	steps := 0
	for {
		req := dto.Request{
			ModelName:       payload.Request.ModelName,
			Messages:        msgs,
			Tools:           tools,
			EffectiveParams: payload.Request.EffectiveParams,
		}

		var res streamResult
		emit := func(raw dto.RawEvent) {
			switch raw.Type {
			case "token":
				res.text += tokenText(raw)
			case "tool_call":
				tc := parseToolCall(raw)
				if tc.Name != "" {
					res.toolCalls = append(res.toolCalls, tc)
				}
			case "finish":
				res.finish = reasonOf(raw)
			case "done":
				parseDone(raw, &res.counts)
			}
		}

		if steps == 0 && maxSteps == 0 {
			// Single-shot (non-agentic): forward tokens live (the POC happy path).
			err := l.d.Provider.Stream(ctx, target, req, func(raw dto.RawEvent) {
				emit(raw)
				if raw.Type == "token" {
					l.emit(turnID, dto.Event{Type: "token", Data: raw.Data})
				}
			})
			if err != nil {
				return streamResult{}, err
			}
			return res, nil
		}

		if err := l.d.Provider.Stream(ctx, target, req, emit); err != nil {
			return streamResult{}, err
		}

		// No tool calls (or the model stopped) → answering round.
		if len(res.toolCalls) == 0 || res.finish == "stop" {
			l.emitToken(turnID, res.text)
			return res, nil
		}

		// Bound reached without an explicit stop: treat the accumulated text as
		// the answer (bounded loop, never unbounded — ADR-0019).
		if steps >= maxSteps {
			l.emitToken(turnID, res.text)
			return res, nil
		}

		// dispatching → observing: append the assistant tool_call message, then
		// invoke each tool and append its result (state-machine.md §1.2).
		msgs = append(msgs, dto.Message{Role: "assistant", Content: res.text, Timestamp: nowUnix()})

		for _, tc := range res.toolCalls {
			// Router mode: the writer's single tool is the synthetic
			// request_tool; the loop resolves it through the decider
			// (state-machine §1.2: planning → deciding). Confident → dispatch
			// the resolved tool; refusal / router error → a "no tool" result
			// that drives one more bounded writer round (the answering phase).
			if r != nil && tc.Name == requestToolName {
				msgs = append(msgs, dto.Message{Role: "tool", Content: l.routeTool(ctx, turnID, task, tc, r), Timestamp: nowUnix()})
				continue
			}

			rawArgs := json.RawMessage(tc.Arguments)
			if len(rawArgs) == 0 {
				rawArgs = json.RawMessage(`{}`)
			}
			// Document-scoped tools need the turn's documentID; it is injected
			// here (the loop holds task.DocumentID), never exposed to the model in
			// the tool schema (config/tools/*.json expose only the model's fields).
			rawArgs = injectDocumentID(tc.Name, task, rawArgs)

			out, toolErr := l.d.Executor.Invoke(tc.Name, rawArgs)

			// Observe structured edit results for events + retry signals.
			handled := l.observeTool(turnID, task, tc, out, toolErr)

			var resultContent string
			switch {
			case toolErr != nil:
				resultContent = toolErrorMessage(tc.Name, toolErr)
			case handled != nil:
				resultContent = string(handled)
			default:
				resultContent = string(outOrEmpty(out))
			}
			msgs = append(msgs, dto.Message{Role: "tool", Content: resultContent, Timestamp: nowUnix()})
		}

		steps++
	}
}

// injectDocumentID adds task.DocumentID to the args of document-scoped tools
// (edit_markdown, diff). The model never supplies it; the loop owns the document
// binding (ADR-0029: edits target a block in a document the loop is scoped to).
func injectDocumentID(name string, task dto.Task, args json.RawMessage) json.RawMessage {
	if name != "edit_markdown" && name != "diff" {
		return args
	}
	merged := map[string]interface{}{}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &merged)
	}
	merged["documentId"] = task.DocumentID
	out, _ := json.Marshal(merged)
	return out
}

// observeTool inspects a tool result, emits candidate/diff events for edits, and
// returns nil when no special handling occurred (the raw result is passed to the
// model) or a possibly-rewritten result for edit retries.
func (l *loop) observeTool(turnID string, task dto.Task, tc dto.ToolCall, out json.RawMessage, toolErr error) json.RawMessage {
	switch tc.Name {
	case "edit_markdown":
		return l.observeEdit(turnID, task, out, toolErr)
	case "diff":
		// A diff tool result is itself the diff to surface.
		l.emit(turnID, dto.Event{Type: "diff", Data: outOrEmpty(out)})
	case "retrieve", "read_note":
		// Retrieval results surface to the UI as a rag event (recorded
		// amendment: the SSE vocabulary gains `rag` — ADR-0017 §6).
		l.emit(turnID, dto.Event{Type: "rag", Data: outOrEmpty(out)})
	}
	return nil
}

// observeEdit handles the edit_markdown structured result (ADR-0029 §5): a
// successful stage emits a candidate event; guard-failed / invalid-structure
// results are passed back to the model as-is so it can re-read / retry.
func (l *loop) observeEdit(turnID string, task dto.Task, out json.RawMessage, toolErr error) json.RawMessage {
	if toolErr != nil {
		return json.RawMessage(`{"ok":false,"error":"` + toolErr.Error() + `"}`)
	}
	if len(out) == 0 {
		return nil
	}
	var res struct {
		Ok    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(out, &res)
	if res.Ok {
		l.emit(turnID, dto.Event{Type: "candidate", Data: out})
		var d struct {
			Diff json.RawMessage `json:"diff"`
		}
		if json.Unmarshal(out, &d) == nil && len(d.Diff) > 0 {
			l.emit(turnID, dto.Event{Type: "diff", Data: d.Diff})
		}
	}
	// Whether ok or retryable error, pass the structured result back to the model.
	return out
}

// routeTool implements the deciding phase (state-machine §1.2, router mode):
// one writer request_tool → Decide → confident dispatch through the exact
// native machinery, or a "no tool" tool result that drives one more bounded
// writer round. The router call is metered as its own row (D4, ADR-0028 §5)
// whether confident or refused; a Decide error emits a labeled
// router-unreachable event and degrades to answering (failure-semantics §3 —
// no retry loop).
func (l *loop) routeTool(ctx context.Context, turnID string, task dto.Task, tc dto.ToolCall, r *route) string {
	result, err := r.decider.Decide(ctx, intentOf(tc.Arguments), dto.RouterContext{
		ToolDefs:  r.allowlist,
		Chunks:    r.chunks,
		Selection: task.Selection,
		History:   r.history,
		UserInput: task.UserInput,
	})
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorDataWithCode("router-unreachable", err)})
		return noToolResult(err.Error())
	}

	// Second meter row (ADR-0028 §5): model=needle-router, turn-scoped, before
	// the outcome is known — the router call already happened.
	if result.Usage.Counts.InputTokens+result.Usage.Counts.OutputTokens > 0 {
		if _, err := l.d.Meter.Attribute(ctx, turnID, task.SessionID, routerModelName, result.Usage.Breakdown, result.Usage.Counts); err != nil {
			l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		}
	}

	// Refusal / below τ: the decider returned the zero Decision (τ is private
	// to it, interface.md §8b). The writer answers directly next round.
	if result.Decision.Name == "" {
		return noToolResult("")
	}

	// Confident: dispatch the resolved tool exactly like a native call.
	args := injectDocumentID(result.Decision.Name, task, result.Decision.Args)
	out, toolErr := l.d.Executor.Invoke(result.Decision.Name, args)
	handled := l.observeTool(turnID, task, dto.ToolCall{ID: tc.ID, Name: result.Decision.Name, Arguments: string(args)}, out, toolErr)
	switch {
	case toolErr != nil:
		return toolErrorMessage(result.Decision.Name, toolErr)
	case handled != nil:
		return string(handled)
	default:
		return string(outOrEmpty(out))
	}
}

// intentOf extracts the writer's free-text intent from request_tool arguments;
// malformed arguments fall back to the raw JSON string.
func intentOf(arguments string) string {
	var v struct {
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal([]byte(arguments), &v); err == nil && v.Intent != "" {
		return v.Intent
	}
	return arguments
}

// noToolResult renders the request_tool tool result that drives the writer to
// answer directly (interface.md §8b recorded amendment: refusal/error → one
// bounded writer round, no extra router call). reason=="" is the graceful
// refusal; otherwise it is the router-unreachable labeled failure.
func noToolResult(reason string) string {
	if reason == "" {
		b, _ := json.Marshal(map[string]interface{}{
			"ok":      true,
			"tool":    nil,
			"message": "no tool required; answer the user directly",
		})
		return string(b)
	}
	b, _ := json.Marshal(map[string]interface{}{
		"ok":      false,
		"error":   "router-unreachable",
		"message": reason,
	})
	return string(b)
}

// emitFinal emits the terminal done event with degrade/usedModel labeling
// (failure-semantics §3: a substitution is always labeled).
func (l *loop) emitFinal(turnID string, res dto.Resolution, r streamResult) {
	data, _ := json.Marshal(map[string]interface{}{
		"degraded":  res.Degraded,
		"usedModel": res.UsedName,
	})
	l.emit(turnID, dto.Event{Type: "done", Data: data})
}

func (l *loop) emit(turnID string, ev dto.Event) {
	if l.d.Bus == nil {
		return
	}
	ev.TurnID = turnID
	l.d.Bus.Emit(ev)
}

// emitToken forwards one text token event (the answering phase; state-machine
// §1: answering is the single token-emitting phase).
func (l *loop) emitToken(turnID, text string) {
	if text == "" {
		return
	}
	data, _ := json.Marshal(map[string]string{"text": text})
	l.emit(turnID, dto.Event{Type: "token", Data: data})
}

// toolsFor returns the mode's allowlisted tools (in allowlist order, so the
// payload order matches the meter).
func toolsFor(reg tool.Registry, m mode.Mode) []dto.ToolDef {
	return reg.AllowlistFor(m)
}

// tokenText extracts the text from a raw token event.
func tokenText(raw dto.RawEvent) string {
	var v struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw.Data, &v)
	return v.Text
}

// parseToolCall extracts a dto.ToolCall from a raw tool_call event.
func parseToolCall(raw dto.RawEvent) dto.ToolCall {
	var v struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	_ = json.Unmarshal(raw.Data, &v)
	return dto.ToolCall{ID: v.ID, Name: v.Name, Arguments: v.Arguments}
}

func reasonOf(raw dto.RawEvent) string {
	var v struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw.Data, &v)
	return v.Reason
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

func toolErrorMessage(name string, err error) string {
	b, _ := json.Marshal(map[string]interface{}{"ok": false, "error": "tool-error", "tool": name, "message": err.Error()})
	return string(b)
}

func outOrEmpty(out json.RawMessage) json.RawMessage {
	if len(out) == 0 {
		return json.RawMessage(`{}`)
	}
	return out
}

func nowUnix() int64 {
	return time.Now().Unix()
}

func errorData(err error) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"code": codeFor(err), "message": err.Error()})
	return b
}

// errorDataWithCode renders an error event with an explicit code (used for the
// router's labeled mid-turn failure, failure-semantics §5).
func errorDataWithCode(code string, err error) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"code": code, "message": err.Error()})
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
	case errors.Is(err, meter.ErrSessionBudgetExceeded):
		return "session-budget-exceeded"
	default:
		return "error"
	}
}
