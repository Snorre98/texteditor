// Package loop holds the Agent loop — the thin orchestrator owning only the turn
// state machine (ADR-0016 §3, state-machine.md §1). All real logic lives in the
// leaves; the loop wires them together, is session-scoped (ADR-0026 §3), and
// forwards events to the bus tagged with a `turnID`.
//
// One fixed pipeline for every preset (ADR-0045 §2): planning → auto-RAG →
// (dispatching → observing)* → answering → done | error. All registered tools
// are advertised on every turn; retrieval always runs (top-k from the pipeline
// policy); the dispatch/observe cycle is bounded by the global policy.maxSteps.
// Edit-integrity results (guard-failed / invalid-structure, ADR-0029) re-enter
// dispatching with a re-read or the issue list, both counting against maxSteps.
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
	"texteditor/internal/pipeline"
	"texteditor/internal/provider"
	"texteditor/internal/retriever"
	"texteditor/internal/session"
	"texteditor/internal/tool"
	"texteditor/internal/workspace"
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
	Pipeline  pipeline.Interface // the one global turn policy (ADR-0045 §3)
	Workspace workspace.Interface
}

// Mention caps (ADR-0036 §2) — constants in the loop package, not mode data.
const (
	maxMentions    = 8
	mentionReadCap = 256 * 1024 // 256 KiB per mentioned file
)

// Mention failure sentinels (ADR-0036 §2) — mapped to the mention SSE codes by
// codeFor. too-many-mentions is synthesized when the count cap is exceeded.
var (
	errTooManyMentions = errors.New("too-many-mentions: more than the per-turn mention cap")
)

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

// resolveMentions reads every mentioned file through Workspace.Read before the
// turn state machine starts (ADR-0036 §2). Failures are fail-fast and typed:
//   - > maxMentions mentions        → too-many-mentions
//   - not-found / not-regular       → mention-not-found
//   - over mentionReadCap bytes     → mention-too-large
//   - read-failed                   → mention-unreadable
//
// Success resolves to []dto.MentionContent (raw content, token weight budgeted
// later by the assembler).
func (l *loop) resolveMentions(ctx context.Context, mentions []dto.Mention) ([]dto.MentionContent, error) {
	if len(mentions) == 0 {
		return nil, nil
	}
	if len(mentions) > maxMentions {
		return nil, errTooManyMentions
	}
	out := make([]dto.MentionContent, 0, len(mentions))
	for _, m := range mentions {
		data, err := l.d.Workspace.Read(ctx, m.Path, mentionReadCap)
		if err != nil {
			switch {
			case errors.Is(err, workspace.ErrNotFound), errors.Is(err, workspace.ErrNotRegular):
				return nil, errMentionNotFound(m.Path)
			case errors.Is(err, workspace.ErrTooLarge):
				return nil, errMentionTooLarge(m.Path)
			default:
				return nil, errMentionUnreadable(m.Path)
			}
		}
		out = append(out, dto.MentionContent{Path: m.Path, Text: string(data)})
	}
	return out, nil
}

// mentionError is a typed mention-resolution error naming the offending path; it
// carries the SSE code so errorData can render it directly.
type mentionError struct {
	code string
	path string
}

func (e *mentionError) Error() string {
	return e.code + ": " + e.path
}

func errMentionNotFound(path string) error {
	return &mentionError{code: "mention-not-found", path: path}
}
func errMentionTooLarge(path string) error {
	return &mentionError{code: "mention-too-large", path: path}
}
func errMentionUnreadable(path string) error {
	return &mentionError{code: "mention-unreadable", path: path}
}

func (l *loop) runTurn(ctx context.Context, turnID string, task dto.Task) {
	m, res, err := l.validate(task)
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// Resolve mentions first, fail-fast (ADR-0036 §2): read every mentioned file
	// through Workspace.Read before the turn state machine starts. A missing /
	// oversized / unreadable mention (or over the count cap) is a typed,
	// pre-streaming error — never silently dropped.
	mentions, err := l.resolveMentions(ctx, task.Mentions)
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

	// One fixed pipeline (ADR-0045 §2): auto-RAG always runs, with the policy's
	// top-k; all registered tools are advertised.
	policy := l.d.Pipeline.Policy()
	chunks, _ := l.d.Retriever.Query(ctx, task.UserInput, policy.AutoRagTopK)

	tools := toolsFor(l.d.Tools)

	payload, breakdown, err := l.d.Assembler.Assemble(ctx, dto.AssemblerInput{
		Mode:      m,
		ModelName: res.Model.ModelID, // the id the provider accepts (may differ from the manifest name)
		Params:    res.EffectiveParams,
		Tools:     tools,
		Policy:    policy,
		RAGChunks: chunks,
		History:   history,
		Mentions:  mentions,
		UserInput: task.UserInput,
	})
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	target := dto.Target{BaseURL: res.Model.BaseURL, Capabilities: res.Model.Capabilities}

	// The turn state machine (state-machine.md §1): planning → (dispatching →
	// observing)* → answering. policy.MaxSteps is the one global bound.
	result, err := l.runSteps(ctx, turnID, task, target, payload, tools, policy.MaxSteps)
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

// runSteps drives the one tool-calling loop (ADR-0045 §2). It mutates `msgs`
// (the assembled message list) across round-trips, threading assistant
// tool_calls and tool results. It returns the final stream result (the answering
// round), bounded by the global pipeline maxSteps.
func (l *loop) runSteps(ctx context.Context, turnID string, task dto.Task, target dto.Target, payload dto.Payload, tools []dto.ToolDef, maxSteps int) (streamResult, error) {
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

		if err := l.d.Provider.Stream(ctx, target, req, emit); err != nil {
			return streamResult{}, err
		}

		// No tool calls (or the model stopped) → answering round.
		if len(res.toolCalls) == 0 || res.finish == "stop" {
			l.emitToken(turnID, res.text)
			return res, nil
		}

		// Bound reached without an explicit stop: treat the accumulated text as
		// the answer (bounded loop, never unbounded — ADR-0045).
		if steps >= maxSteps {
			l.emitToken(turnID, res.text)
			return res, nil
		}

		// dispatching → observing: append the assistant tool_call message, then
		// invoke each tool and append its result (state-machine.md §1.2).
		msgs = append(msgs, dto.Message{Role: "assistant", Content: res.text, Timestamp: nowUnix()})

		for _, tc := range res.toolCalls {
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
// For edit_markdown it also carries the turn's preset so the staged candidate's
// derived commit message names the mode (ADR-0020 §1); the model never sees
// either field in the tool schema.
func injectDocumentID(name string, task dto.Task, args json.RawMessage) json.RawMessage {
	if name != "edit_markdown" && name != "diff" {
		return args
	}
	merged := map[string]interface{}{}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &merged)
	}
	merged["documentId"] = task.DocumentID
	if name == "edit_markdown" && task.ModeName != "" {
		merged["modeName"] = task.ModeName
	}
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

// toolsFor returns all registered tools (global since ADR-0045 §2), in the
// registry's stable name order so the payload order matches the meter.
func toolsFor(reg tool.Registry) []dto.ToolDef {
	return reg.List()
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

func codeFor(err error) string {
	var me *mentionError
	switch {
	case errors.As(err, &me):
		return me.code
	case errors.Is(err, errTooManyMentions):
		return "too-many-mentions"
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
