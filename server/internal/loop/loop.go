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
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"texteditor/internal/assembler"
	"texteditor/internal/document"
	"texteditor/internal/filesystem"
	"texteditor/internal/fleet"
	"texteditor/internal/meter"
	"texteditor/internal/mode"
	"texteditor/internal/pathutil"
	"texteditor/internal/pipeline"
	"texteditor/internal/provider"
	"texteditor/internal/shard"
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
// Retriever/Sessions/Meter are workspace-scoped: the loop resolves the turn's
// workspace and acquires its shard lease at turn start (ADR-0049 §5).
type Deps struct {
	Modes      mode.Interface
	Tools      tool.Registry
	Executor   tool.Executor
	Assembler  assembler.Interface
	Provider   provider.Interface
	Fleet      fleet.Interface
	Doc        document.Interface
	Shards     shard.Resolver
	Workspaces workspace.Interface // registry + fallback resolve-or-create
	Bus        Emitter
	Pipeline   pipeline.Interface   // the one global turn policy (ADR-0045 §3)
	Filesystem filesystem.Interface // mentions read (ADR-0036, ADR-0049 §6)
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
	// errWorkspaceUnresolved is the typed turn-start failure when neither
	// Task.workspaceId nor a document path yields a workspace (ADR-0049 §2/§5).
	errWorkspaceUnresolved = errors.New("workspace-unresolved: could not resolve the turn's workspace")
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

// resolveWorkspaceID resolves the turn's workspace (ADR-0049 §2/§5): an
// explicit Task.workspaceId wins; otherwise the engine resolves-or-creates a
// workspace rooted at the canonical parent directory of the turn's document,
// which keeps pre-workspace clients (the frozen OpenTUI TUI) working.
func (l *loop) resolveWorkspaceID(task dto.Task) (string, error) {
	if task.WorkspaceID != "" {
		if _, err := l.d.Workspaces.Get(task.WorkspaceID); err != nil {
			return "", errWorkspaceUnresolved
		}
		return task.WorkspaceID, nil
	}
	if task.DocumentID == "" {
		return "", errWorkspaceUnresolved
	}
	path, err := l.d.Doc.Path(task.DocumentID)
	if err != nil {
		return "", errWorkspaceUnresolved
	}
	canonical, _ := pathutil.Canonical(path)
	ws, err := l.d.Workspaces.ResolveOrCreate(filepath.Dir(canonical))
	if err != nil {
		return "", errWorkspaceUnresolved
	}
	return ws.ID, nil
}

// resolveMentions reads every mentioned file through Filesystem.Read before the
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
		data, err := l.d.Filesystem.Read(ctx, m.Path, mentionReadCap)
		if err != nil {
			switch {
			case errors.Is(err, filesystem.ErrNotFound), errors.Is(err, filesystem.ErrNotRegular):
				return nil, errMentionNotFound(m.Path)
			case errors.Is(err, filesystem.ErrTooLarge):
				return nil, errMentionTooLarge(m.Path)
			case errors.Is(err, filesystem.ErrPathOutsideAllowedRoots):
				return nil, errPathOutsideAllowedRoots(m.Path)
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
func errPathOutsideAllowedRoots(path string) error {
	return &mentionError{code: "path-outside-allowed-roots", path: path}
}

func (l *loop) runTurn(ctx context.Context, turnID string, task dto.Task) {
	m, res, err := l.validate(task)
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// Resolve mentions first, fail-fast (ADR-0036 §2): read every mentioned file
	// through Filesystem.Read before the turn state machine starts. A missing /
	// oversized / unreadable mention (or over the count cap, or one outside
	// ALLOWED_ROOTS) is a typed, pre-streaming error — never silently dropped.
	mentions, err := l.resolveMentions(ctx, task.Mentions)
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// Resolve the turn's workspace and acquire its shard lease (ADR-0049 §5):
	// sessions, meter, and the index are workspace-scoped. The lease keeps the
	// shard open for the whole turn and is released on return.
	wsID, err := l.resolveWorkspaceID(task)
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}
	lease, err := l.d.Shards.Services(ctx, wsID)
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}
	defer lease.Release()
	svc := &lease.Services
	// Tool handlers (retrieve/read_note) reach this turn's workspace index
	// through the context (ADR-0049 §5).
	ctx = shard.WithServices(ctx, svc)

	// Route the turn to its workspace/session so a finished turn's context
	// snapshot is resolvable via GET /turns/{id}/context (ADR-0044 §4).
	if err := l.d.Workspaces.RouteTurn(turnID, wsID, task.SessionID); err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// planning: read session history + retrieved chunks.
	history, _ := svc.Sessions.History(task.SessionID)

	// Append the user's turn input to the session so the conversation is durable
	// (ADR-0026 §3). The assistant's completions are appended after they land.
	if err := svc.Sessions.Append(task.SessionID, dto.Message{Role: "user", Content: task.UserInput}); err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// Per-session budget gate (ADR-0026 §5) — checked before any model call.
	sess, _ := svc.Sessions.Resume(task.SessionID)
	if sess.TokenBudget != nil {
		used, err := svc.Meter.SessionUsage(ctx, task.SessionID)
		if err == nil && meter.SessionExceeded(used, sess.TokenBudget, 1) {
			l.emit(turnID, dto.Event{Type: "error", Data: errorData(meter.ErrSessionBudgetExceeded)})
			return
		}
	}

	// One fixed pipeline (ADR-0045 §2): auto-RAG runs under the effective policy
	// (unless autoRag is off); all registered tools are advertised.
	policy := l.d.Pipeline.Policy()

	// Merge the persisted session policy with the per-turn Task.context override
	// (ADR-0049 §8): replace-when-present per field, override never persisted.
	eff := mergeContextPolicy(sess.ContextPolicy, task.Context, task.UserInput)

	// auto-RAG retrieval — skipped when the effective autoRag flag is off; pins
	// still apply below.
	var autoChunks []dto.Chunk
	if eff.AutoRag {
		autoChunks, _ = svc.Retriever.Query(ctx, eff.Query, policy.AutoRagTopK)
	}

	// Apply tray exclusions to the auto-retrieved set (the rag event carries the
	// post-exclude set, ADR-0049 §4); each removal is a labeled drop.
	autoChunks, excludedDrops := applyExclusions(autoChunks, eff.Excluded)

	// Resolve pins through the index (ADR-0049 §8): Retriever.Get works even when
	// a pinned chunk is not in the retrieval top-k. Unresolved refs are labeled
	// drops, never silent.
	pinnedChunks, notFoundDrops, err := resolvePins(ctx, svc.Retriever, eff.Pinned)
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// Auto-RAG is visible: the same rag event shape as tool retrieval, emitted
	// before assembly so clients can show what was retrieved even when no tool
	// ran (ADR-0044 §3, "auto-retrieved chunks visible with provenance"). It is
	// the auto-retrieved set only — pins are human overrides (ADR-0049 §11).
	l.emitRag(turnID, autoChunks)

	tools := toolsFor(l.d.Tools)

	payload, breakdown, err := l.d.Assembler.Assemble(ctx, dto.AssemblerInput{
		Mode:      m,
		ModelName: res.Model.ModelID, // the id the provider accepts (may differ from the manifest name)
		Params:    res.EffectiveParams,
		Tools:     tools,
		Policy:    policy,
		Pinned:    pinnedChunks,
		RAGChunks: autoChunks,
		History:   history,
		Mentions:  mentions,
		UserInput: task.UserInput,
	})
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// Build, persist, and emit the turn's context snapshot (ADR-0044 §4). The
	// snapshot is engine-owned data: the loop wraps the assembler's provenance/
	// drops/budget with the turn/session/workspace ids, the effective retrieval
	// metadata, and the retrieved/pinned chunks; the assembler never knows about
	// sessions or workspaces.
	loopDrops := append(excludedDrops, notFoundDrops...)
	snapshot := buildSnapshot(turnID, task, wsID, eff.Query, eff.AutoRag, autoChunks, pinnedChunks, loopDrops, payload)
	rawSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}
	if err := svc.Sessions.SaveContext(turnID, task.SessionID, rawSnapshot); err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}
	l.emit(turnID, dto.Event{Type: "context", Data: rawSnapshot})

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
		if _, err := svc.Meter.Attribute(ctx, turnID, task.SessionID, res.UsedName, breakdown, result.counts); err != nil {
			l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		}
	}

	// Persist the assistant's final answer (ADR-0026 §3).
	if result.text != "" {
		_ = svc.Sessions.Append(task.SessionID, dto.Message{Role: "assistant", Content: result.text})
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

			out, toolErr := l.d.Executor.Invoke(ctx, tc.Name, rawArgs)

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

// emitRag emits the structured retrieval result (the same `{ok, chunks}` shape
// as the retrieve/read_note tools) for auto-RAG, so retrieved chunks with
// provenance reach the client even when no tool was invoked (ADR-0044 §3).
func (l *loop) emitRag(turnID string, chunks []dto.Chunk) {
	if chunks == nil {
		chunks = []dto.Chunk{}
	}
	data, _ := json.Marshal(map[string]interface{}{"ok": true, "chunks": chunks})
	l.emit(turnID, dto.Event{Type: "rag", Data: data})
}

// buildSnapshot wraps the assembler's provenance/drops/budget, the effective
// retrieval metadata, and the auto/pinned chunks into the engine-owned
// ContextSnapshot for one turn (ADR-0044 §4, ADR-0049 §7/§8). It recomputes
// nothing: provenance, drops, and budget all come from the pure assembler; only
// the turn/session/workspace envelope, retrieval metadata, loop-level drops
// (excluded/not-found), and the human-override labels are added here.
func buildSnapshot(turnID string, task dto.Task, workspaceID, retrievalQuery string, autoRag bool, autoChunks, pinnedChunks []dto.Chunk, loopDrops []dto.ContextDrop, payload dto.Payload) dto.ContextSnapshot {
	messages := make([]dto.ContextMessage, 0, len(payload.Provenance))
	for _, p := range payload.Provenance {
		messages = append(messages, dto.ContextMessage{
			Role:      p.Role,
			Component: p.Component,
			Source:    p.Source,
			Tokens:    p.Tokens,
			Pinned:    p.Pinned,
		})
	}
	// Pinned chunks are listed first (matching their front-loaded assembly
	// order) and labeled as human overrides; auto chunks stay unlabeled.
	chunks := make([]dto.Chunk, 0, len(pinnedChunks)+len(autoChunks))
	for _, c := range pinnedChunks {
		c.Pinned = true
		c.HumanOverride = true
		chunks = append(chunks, c)
	}
	chunks = append(chunks, autoChunks...)
	drops := make([]dto.ContextDrop, 0, len(loopDrops)+len(payload.Drops))
	drops = append(drops, loopDrops...)
	drops = append(drops, payload.Drops...)
	budget := payload.Budget
	if budget == nil {
		budget = []dto.BudgetUsage{}
	}
	return dto.ContextSnapshot{
		TurnID:         turnID,
		SessionID:      task.SessionID,
		WorkspaceID:    workspaceID,
		RetrievalQuery: retrievalQuery,
		AutoRag:        autoRag,
		Messages:       messages,
		Chunks:         chunks,
		Drops:          drops,
		Budget:         budget,
		CreatedAt:      time.Now().Unix(),
	}
}

// effectiveContext is the resolved per-turn context policy: the persisted
// session policy with the per-turn override merged replace-when-present per
// field, plus defaults (autoRag true, query = the turn's user input).
type effectiveContext struct {
	Pinned   []dto.ChunkRef
	Excluded []dto.ChunkRef
	AutoRag  bool
	Query    string
}

// mergeContextPolicy resolves the effective policy (ADR-0049 §8). The persisted
// session policy is opaque JSON (nil/empty = none); the per-turn override is
// optional. A field present in the override replaces the session field wholesale
// (an explicit empty list clears it); an absent field inherits. Defaults apply
// when neither layer sets a field. The override is never persisted.
func mergeContextPolicy(sessionRaw json.RawMessage, override *dto.ContextPolicy, userInput string) effectiveContext {
	eff := effectiveContext{AutoRag: true, Query: userInput}
	var session *dto.ContextPolicy
	if len(sessionRaw) > 0 {
		var sp dto.ContextPolicy
		if json.Unmarshal(sessionRaw, &sp) == nil {
			session = &sp
		}
	}
	apply := func(p *dto.ContextPolicy) {
		if p == nil {
			return
		}
		if p.Pinned != nil {
			eff.Pinned = p.Pinned
		}
		if p.Excluded != nil {
			eff.Excluded = p.Excluded
		}
		if p.AutoRag != nil {
			eff.AutoRag = *p.AutoRag
		}
		if p.RetrievalQuery != nil {
			eff.Query = *p.RetrievalQuery
		}
	}
	apply(session)
	apply(override)
	return eff
}

// applyExclusions drops auto-retrieved chunks matching an excluded ref: by
// chunkKey when present, else by canonical path. It returns the kept chunks and
// a labeled drop for the removals (ADR-0049 §8, "Removing a chunk ... keeps it
// out of the payload").
func applyExclusions(chunks []dto.Chunk, excluded []dto.ChunkRef) ([]dto.Chunk, []dto.ContextDrop) {
	if len(excluded) == 0 || len(chunks) == 0 {
		return chunks, nil
	}
	keys := map[string]bool{}
	paths := map[string]bool{}
	for _, ref := range excluded {
		if ref.ChunkKey != "" {
			keys[ref.ChunkKey] = true
		}
		if ref.Path != "" {
			if canonical, _ := pathutil.Canonical(ref.Path); canonical != "" {
				paths[canonical] = true
			}
		}
	}
	out := make([]dto.Chunk, 0, len(chunks))
	dropped := 0
	detail := ""
	for _, c := range chunks {
		if keys[c.ChunkKey] || (c.Path != "" && paths[c.Path]) {
			dropped++
			if detail == "" {
				if c.ChunkKey != "" {
					detail = c.ChunkKey
				} else {
					detail = c.Path
				}
			}
			continue
		}
		out = append(out, c)
	}
	if dropped == 0 {
		return out, nil
	}
	return out, []dto.ContextDrop{{Component: "rag", Reason: "excluded", Count: dropped, Detail: detail}}
}

// chunkGetter is the sealed subset of the Retriever the pin resolution needs.
type chunkGetter interface {
	Get(ctx context.Context, refs []dto.ChunkRef) ([]dto.Chunk, error)
}

// resolvePins resolves pinned ChunkRefs to indexed chunks through Retriever.Get
// (works after resume even when a pinned chunk is not in the top-k). Unresolved
// refs become a labeled "not-found" drop marked as a human override; a
// resolution failure is a typed error the caller turns into a turn error.
func resolvePins(ctx context.Context, r chunkGetter, refs []dto.ChunkRef) ([]dto.Chunk, []dto.ContextDrop, error) {
	if len(refs) == 0 {
		return nil, nil, nil
	}
	chunks, err := r.Get(ctx, refs)
	if err != nil {
		return nil, nil, err
	}
	resolvedKeys := map[string]bool{}
	resolvedPaths := map[string]bool{}
	for _, c := range chunks {
		if c.ChunkKey != "" {
			resolvedKeys[c.ChunkKey] = true
		}
		if c.Path != "" {
			resolvedPaths[c.Path] = true
		}
	}
	var missing []dto.ChunkRef
	for _, ref := range refs {
		if ref.ChunkKey != "" {
			if !resolvedKeys[ref.ChunkKey] {
				missing = append(missing, ref)
			}
			continue
		}
		if ref.Path != "" {
			canonical, _ := pathutil.Canonical(ref.Path)
			if !resolvedPaths[canonical] {
				missing = append(missing, ref)
			}
			continue
		}
		missing = append(missing, ref)
	}
	if len(missing) == 0 {
		return chunks, nil, nil
	}
	detail := missing[0].ChunkKey
	if detail == "" {
		detail = missing[0].Path
	}
	return chunks, []dto.ContextDrop{{Component: "rag", Reason: "not-found", Count: len(missing), Detail: detail, HumanOverride: true}}, nil
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
	case errors.Is(err, errWorkspaceUnresolved):
		return "workspace-unresolved"
	case errors.Is(err, filesystem.ErrPathOutsideAllowedRoots):
		return "path-outside-allowed-roots"
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
