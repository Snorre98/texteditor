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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"texteditor/internal/assembler"
	"texteditor/internal/document"
	"texteditor/internal/filesystem"
	"texteditor/internal/fleet"
	"texteditor/internal/laya"
	"texteditor/internal/locate"
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
	// ResolveLocate answers an open `/locate` ambiguity picker for a waiting
	// turn (ADR-0048 §4): a chosen candidate resumes the anchored turn, a cancel
	// degrades it to plain chat. ErrNoPendingLocate (typed 409) is returned when
	// the turn is not waiting on a picker.
	ResolveLocate(turnID string, choice dto.LocateChoice) error
	// Cancel cancels a still-running turn (user-initiated, POST
	// /turns/{id}/cancel). The turn ends with a labeled terminal outcome and any
	// partial usage is metered. ErrTurnNotRunning (typed 409) is returned when
	// the turn is known but no longer running.
	Cancel(turnID string) error
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
	// Locate is the deterministic `/locate` resolver (ADR-0048 §2). When nil,
	// `/locate` degrades to plain chat with a not-found label.
	Locate locate.Resolver
	// Decision is the Laya decision layer (ADR-0053). When nil, the layer is
	// unavailable and a turn that requests it is labeled decision-degraded.
	Decision laya.Interface
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
	// errThinkingTruncated is the labeled terminal error when the thinking
	// budget is reached before a tool call or answer (ADR-0051 §5) — never an
	// empty done.
	errThinkingTruncated = errors.New("thinking-truncated: the thinking budget was reached before a tool call or answer")
	// errNoOutcome is the labeled terminal error when a turn produces neither a
	// tool call nor an answer (ADR-0051 §2).
	errNoOutcome = errors.New("no-outcome: the turn produced neither a tool call nor an answer")
	// ErrTurnNotRunning is the typed refusal (mapped to HTTP 409) when
	// POST /turns/{id}/cancel targets a turn that is known but no longer running
	// (already finished or cancelled). An id that was never a turn is the
	// API-level typed 404 instead.
	ErrTurnNotRunning = errors.New("turn-not-running: this turn is not currently running")
)

// loop is the concrete Agent loop.
type loop struct {
	d Deps

	// pending holds one open `/locate` ambiguity picker per waiting turn
	// (ADR-0048 §4). It is in-memory and turn-scoped: a choice resumes the turn,
	// a cancel/timeout degrades it to plain chat.
	pendingMu sync.Mutex
	pending   map[string]*pendingLocate

	// cancels holds the cancel func of every running turn; cancelled records
	// user-initiated cancels so a turn can label its terminal outcome
	// (`done {cancelled:true}`) distinctly from a provider/context error
	// (ADR-0046 E2). Both are in-memory and turn-scoped.
	cancelMu  sync.Mutex
	cancels   map[string]context.CancelFunc
	cancelled map[string]bool
}

// New returns an Agent loop over the supplied dependencies.
func New(d Deps) AgentLoop {
	return &loop{
		d:         d,
		pending:   map[string]*pendingLocate{},
		cancels:   map[string]context.CancelFunc{},
		cancelled: map[string]bool{},
	}
}

// Run starts a turn asynchronously, returning its turnID. Events are tagged with
// the turnID; correlation to the requesting client is the API server's job. The
// turn runs on a cancellable context so POST /turns/{id}/cancel can interrupt it.
func (l *loop) Run(ctx context.Context, task dto.Task) (string, error) {
	turnID := uuid.NewString()
	turnCtx, cancel := context.WithCancel(ctx)
	l.cancelMu.Lock()
	l.cancels[turnID] = cancel
	l.cancelMu.Unlock()
	go func() {
		defer func() {
			cancel()
			l.cancelMu.Lock()
			delete(l.cancels, turnID)
			delete(l.cancelled, turnID)
			l.cancelMu.Unlock()
		}()
		l.runTurn(turnCtx, turnID, task)
	}()
	return turnID, nil
}

// Cancel cancels a still-running turn. It marks the turn as user-cancelled
// (so runTurn labels the terminal outcome) and cancels its context. A turn that
// is not running is ErrTurnNotRunning (the API maps it to a typed 409).
func (l *loop) Cancel(turnID string) error {
	l.cancelMu.Lock()
	cancel, ok := l.cancels[turnID]
	if ok {
		l.cancelled[turnID] = true
	}
	l.cancelMu.Unlock()
	if !ok {
		return ErrTurnNotRunning
	}
	cancel()
	return nil
}

// wasCancelled reports whether Cancel was called for the turn.
func (l *loop) wasCancelled(turnID string) bool {
	l.cancelMu.Lock()
	defer l.cancelMu.Unlock()
	return l.cancelled[turnID]
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
	// `/locate` is deterministic preprocessing, parsed at the very start before
	// mentions and session append (ADR-0048 §1/§6). The command line is stripped;
	// the pasted chunk becomes the effective user input used for the session,
	// the default retrieval query, and assembly. No Task change, no model call.
	effectiveInput, locateChunk, isLocate := parseLocateCommand(task.UserInput)
	task.UserInput = effectiveInput

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

	// `/locate`: resolve the pasted chunk deterministically (the open document
	// first, then the workspace corpus index), emit the `locate` event, and — for
	// a fuzzy match — wait for the ambiguity picker before any model call or edit
	// (ADR-0048 §2/§4). Not-found and cancel/timeout degrade to plain chat; the
	// locate outcome is recorded in the snapshot either way.
	var anchor *anchor
	var locateRaw json.RawMessage
	if isLocate {
		anchor, locateRaw = l.locateTurn(ctx, turnID, task, svc, locateChunk)
	}

	// planning: read session history + retrieved chunks.
	startedAt := time.Now()
	history, _ := svc.Sessions.History(task.SessionID)

	// Append the user's turn input to the session so the conversation is durable
	// (ADR-0026 §3). The assistant's completions are appended after they land.
	if err := svc.Sessions.Append(task.SessionID, dto.Message{Role: "user", Content: task.UserInput}); err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// Per-session budget state (ADR-0026 §5, ADR-0051 §7) — resolved here and
	// enforced after assembly so it uses the real payload size.
	sess, _ := svc.Sessions.Resume(task.SessionID)

	// One fixed pipeline (ADR-0045 §2): auto-RAG runs under the effective policy
	// (unless autoRag is off); all registered tools are advertised.
	policy := l.d.Pipeline.Policy()

	// Merge the persisted session policy with the per-turn Task.context override
	// (ADR-0049 §8): replace-when-present per field, override never persisted.
	eff := mergeContextPolicy(sess.ContextPolicy, task.Context, task.UserInput)

	// Metered compaction (ADR-0051 §8): when history exceeds the trigger, the
	// oldest turns are replaced by one labeled summary before assembly. The
	// summary call is its own metered model row; pins and recent turns survive.
	var compacted *dto.CompactionRecord
	if policy.Compaction.Enabled && estimateHistoryTokens(history) > policy.Compaction.TriggerHistoryTokens {
		if rec, err := l.compact(ctx, turnID, task, res, svc, history, policy); err == nil && rec != nil {
			compacted = rec
			history, _ = svc.Sessions.History(task.SessionID)
		}
	}

	// Laya decision layer — planner (ADR-0053): decide retrieve-or-not, the
	// retrieval breadth, and the thinking level BEFORE retrieval (retrieve-or-not
	// cannot be decided after chunks exist). Human/policy outranks the model; a
	// failure degrades fail-open and labeled.
	selectionText := ""
	if anchor != nil {
		selectionText = anchor.context
	}
	dres := l.decide(ctx, turnID, task, svc, policy, eff, m.Name, task.UserInput, history, selectionText)

	// auto-RAG retrieval — skipped when autoRag is off or the planner says no;
	// the depth is the planner's breadth (bounded), else the policy topK. Pins
	// still apply below.
	var autoChunks []dto.Chunk
	if dres.retrieve {
		autoChunks, _ = svc.Retriever.Query(ctx, eff.Query, dres.topK)
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

	// Laya decision layer — gate (ADR-0053): filter the post-exclude auto set by
	// per-chunk relevance. Pins/mentions/anchor bypass the gate (ADR-0049 §11);
	// the gate never touches them. The rag event stays the pre-gate candidate set.
	preGate := autoChunks
	var gateDrops []dto.ContextDrop
	if dres.record != nil && dres.retrieve {
		autoChunks, gateDrops = l.gate(ctx, turnID, task, svc, policy, dres.record, eff.Query, autoChunks)
	}

	// Auto-RAG is visible: the same rag event shape as tool retrieval, emitted
	// before assembly so clients can show what was retrieved even when no tool
	// ran (ADR-0044 §3, "auto-retrieved chunks visible with provenance"). It is
	// the auto-retrieved set only — pins are human overrides (ADR-0049 §11).
	l.emitRag(turnID, preGate)

	tools := toolsFor(l.d.Tools)

	// Anchored turn (ADR-0048 §5): inject the anchored block and its neighbors as
	// a mention-component attachment (no new meter component) and append a
	// deterministic replacement-only instruction to the assembled user input. The
	// session keeps the clean chunk; only the model call sees the instruction. The
	// anchor is also set as the turn's selection for router context/snapshot.
	assemblyInput := task.UserInput
	if anchor != nil {
		mentions = append(mentions, dto.MentionContent{
			Path: anchor.path + "#" + anchor.blockID,
			Text: anchor.context,
		})
		assemblyInput = task.UserInput + "\n\n" + locateInstruction
		task.Selection = &dto.Selection{BlockID: anchor.blockID}
	}

	payload, breakdown, err := l.d.Assembler.Assemble(ctx, dto.AssemblerInput{
		Mode:          m,
		ModelName:     res.Model.ModelID, // the id the provider accepts (may differ from the manifest name)
		Params:        res.EffectiveParams,
		Tools:         tools,
		Policy:        policy,
		Pinned:        pinnedChunks,
		RAGChunks:     autoChunks,
		History:       history,
		Mentions:      mentions,
		UserInput:     assemblyInput,
		ContextLength: res.Model.Capabilities.ContextLength,
	})
	if err != nil {
		// A typed context-window refusal happens before any provider call
		// (ADR-0051 §6); every other assembler error is generic.
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// Resolve the thinking policy (ADR-0051 §1): pipeline default ← session
	// policy ← per-turn override, then the runner mapping (§3). An unsupported
	// runner degrades to thinking-on with a labeled thinking-unsupported outcome.
	thinkLevel := policy.Thinking
	if dres.thinking != "" {
		thinkLevel = dres.thinking
	}
	if eff.Thinking != "" {
		thinkLevel = eff.Thinking
	}
	st := resolveThinking(thinkLevel, res.Model.Runner)

	// Build, persist, and emit the turn's context snapshot (ADR-0044 §4). The
	// snapshot is engine-owned data: the loop wraps the assembler's provenance/
	// drops/budget with the turn/session/workspace ids, the effective retrieval
	// metadata, and the retrieved/pinned chunks; the assembler never knows about
	// sessions or workspaces.
	loopDrops := append(excludedDrops, notFoundDrops...)
	loopDrops = append(loopDrops, gateDrops...)
	snapshot := buildSnapshot(turnID, task, wsID, eff.Query, eff.AutoRag, autoChunks, pinnedChunks, loopDrops, locateRaw, payload)
	snapshot.Compacted = compacted
	snapshot.Thinking = st.snapshot()
	snapshot.Decision = dres.record

	// Session budget gate (ADR-0051 §7) — after assembly (real payload size),
	// before any provider call. Soft = labeled warning (proceed); hard = typed
	// refusal unless metered compaction rescues the turn.
	if sess.TokenBudget != nil {
		used, _ := svc.Meter.SessionUsage(ctx, task.SessionID)
		soft, hard := meter.SessionBudgetState(used, sess.TokenBudget, policy.SessionBudgetSoftRatio, payload.Window.Used)
		snapshot.SessionBudget = &dto.SessionBudget{Soft: soft, Hard: hard, Used: used, Budget: *sess.TokenBudget}
		if hard && compacted == nil && policy.Compaction.Enabled && len(history) > 0 {
			if rec, err := l.compact(ctx, turnID, task, res, svc, history, policy); err == nil && rec != nil {
				compacted = rec
				snapshot.Compacted = rec
				snapshot.SessionBudget.Hard = false
			}
		}
		if snapshot.SessionBudget.Hard {
			l.persistAndEmitContext(turnID, task, svc, snapshot)
			l.emit(turnID, dto.Event{Type: "error", Data: errorData(meter.ErrSessionBudgetExceeded)})
			return
		}
	}

	l.persistAndEmitContext(turnID, task, svc, snapshot)

	target := dto.Target{BaseURL: res.Model.BaseURL, Capabilities: res.Model.Capabilities, Runner: res.Model.Runner}

	// The turn state machine (state-machine.md §1): planning → (dispatching →
	// observing)* → answering. policy.MaxSteps is the one global bound.
	result, err := l.runSteps(ctx, turnID, task, target, payload, tools, anchor, policy, &st)
	if err != nil {
		// User-initiated cancel is a labeled non-error terminal outcome, not an
		// engine error (ADR-0046 E2): preserve any partial answer, meter the
		// partial usage, record `cancelled` in the snapshot, and end with
		// `done {cancelled:true}`.
		if l.wasCancelled(turnID) {
			l.cancelledOutcome(turnID, task, svc, res, breakdown, result, startedAt, payload, &st, snapshot)
			return
		}
		// Label the terminal outcome (thinking-truncated / no-outcome) in the
		// snapshot before the error event terminates the stream.
		snapshot.Thinking = st.snapshot()
		l.persistAndEmitContext(turnID, task, svc, snapshot)
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}

	// answering: the final stream already forwarded tokens; now meter + persist.
	meterBreakdown := breakdown
	if result.counts.ThinkingTokens == 0 && result.reasoningTokens > 0 {
		// The provider streamed reasoning but omitted the count: use the
		// engine's estimate so the meter labels it as an approximation
		// (ADR-0024/ADR-0051 §4).
		meterBreakdown.Thinking = result.reasoningTokens
	}
	measurement := dto.TurnMeasurement{
		PromptTokens:      result.counts.InputTokens,
		ThinkingTokens:    result.counts.ThinkingTokens,
		CompletionTokens:  result.counts.OutputTokens,
		LatencyMs:         time.Since(startedAt).Milliseconds(),
		Model:             res.UsedName,
		Quant:             quantOf(res.UsedName),
		WindowUtilization: payload.Window.Utilization,
	}
	if measurement.ThinkingTokens == 0 {
		measurement.ThinkingTokens = result.reasoningTokens
	}
	if result.counts.InputTokens+result.counts.OutputTokens > 0 || result.reasoningTokens > 0 {
		if _, err := svc.Meter.Attribute(ctx, turnID, task.SessionID, res.UsedName, meterBreakdown, result.counts, measurement); err != nil {
			l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		}
	}

	// Re-persist and re-emit the snapshot with the post-turn labels
	// (escalation/truncation, measurements) so live clients see them before done.
	snapshot.Thinking = st.snapshot()
	snapshot.Measurements = &measurement
	l.persistAndEmitContext(turnID, task, svc, snapshot)

	// Persist the assistant's final answer (ADR-0026 §3).
	if result.text != "" {
		_ = svc.Sessions.Append(task.SessionID, dto.Message{Role: "assistant", Content: result.text})
	}

	l.emitFinal(turnID, res, result)
}

// cancelledOutcome finishes a user-cancelled turn (ADR-0046 E2). It emits the
// partial answer (if any) so the client does not lose already-generated text,
// meters whatever partial usage the provider reported, records `cancelled` and
// the measurements in the persisted snapshot, persists the partial assistant
// message, and emits the labeled terminal `done {cancelled:true}`. It is a
// normal terminal outcome, never an error.
func (l *loop) cancelledOutcome(turnID string, task dto.Task, svc *shard.Services, res dto.Resolution, breakdown dto.Breakdown, partial streamResult, startedAt time.Time, payload dto.Payload, st *thinkingState, snapshot dto.ContextSnapshot) {
	st.reasoningTokens = partial.reasoningTokens
	st.truncated = false

	meterBreakdown := breakdown
	if partial.counts.ThinkingTokens == 0 && partial.reasoningTokens > 0 {
		meterBreakdown.Thinking = partial.reasoningTokens
	}
	measurement := dto.TurnMeasurement{
		PromptTokens:      partial.counts.InputTokens,
		ThinkingTokens:    partial.counts.ThinkingTokens,
		CompletionTokens:  partial.counts.OutputTokens,
		LatencyMs:         time.Since(startedAt).Milliseconds(),
		Model:             res.UsedName,
		Quant:             quantOf(res.UsedName),
		WindowUtilization: payload.Window.Utilization,
	}
	if measurement.ThinkingTokens == 0 {
		measurement.ThinkingTokens = partial.reasoningTokens
	}
	// The turn context is already cancelled; persist through a non-cancelled
	// context so the partial meter row and snapshot still land.
	if partial.counts.InputTokens+partial.counts.OutputTokens > 0 || partial.reasoningTokens > 0 {
		if _, err := svc.Meter.Attribute(context.WithoutCancel(context.Background()), turnID, task.SessionID, res.UsedName, meterBreakdown, partial.counts, measurement); err != nil {
			// Best-effort: the cancel outcome still terminates the turn.
			_ = err
		}
	}

	snapshot.Thinking = st.snapshot()
	snapshot.Measurements = &measurement
	snapshot.Cancelled = true

	// Emit any partial answer so the client shows it before the terminal event.
	l.emitToken(turnID, partial.text)
	l.persistAndEmitContext(turnID, task, svc, snapshot)
	if partial.text != "" {
		_ = svc.Sessions.Append(task.SessionID, dto.Message{Role: "assistant", Content: partial.text})
	}
	l.emitCancelled(turnID, res)
}

// persistAndEmitContext marshals, persists, and emits one context snapshot. It
// is called twice per turn: once before the provider call (assembly known) and
// once after (post-turn labels known). SaveContext upserts by turn id.
func (l *loop) persistAndEmitContext(turnID string, task dto.Task, svc *shard.Services, snapshot dto.ContextSnapshot) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}
	if err := svc.Sessions.SaveContext(turnID, task.SessionID, raw); err != nil {
		l.emit(turnID, dto.Event{Type: "error", Data: errorData(err)})
		return
	}
	l.emit(turnID, dto.Event{Type: "context", Data: raw})
}

// thinkingState is the loop's per-turn thinking outcome (ADR-0051 §1–§5).
type thinkingState struct {
	level            dto.ThinkingLevel
	effective        bool
	escalated        bool
	escalationReason string
	unsupported      bool
	truncated        bool
	reasoningTokens  int
}

// resolveThinking maps the resolved policy level and the runner to the initial
// thinking state (ADR-0051 §1/§3). A runner that cannot disable thinking
// degrades a thinking-off turn to thinking-on, labeled thinking-unsupported.
func resolveThinking(level dto.ThinkingLevel, runner string) thinkingState {
	st := thinkingState{level: level}
	switch level {
	case dto.ThinkingOn:
		st.effective = true
	case dto.ThinkingOff:
		if provider.SupportsThinkingToggle(runner) {
			st.effective = false
		} else {
			st.effective = true
			st.unsupported = true
		}
	case dto.ThinkingAuto:
		if provider.SupportsThinkingToggle(runner) {
			st.effective = false
		} else {
			st.effective = true
			st.unsupported = true
		}
	default:
		st.level = dto.ThinkingOn
		st.effective = true
	}
	return st
}

// escalate flips the turn to thinking-on once (ADR-0051 §2), recording the
// structured failure that triggered it.
func (st *thinkingState) escalate(reason string) {
	if st.escalated {
		return
	}
	st.escalated = true
	st.escalationReason = reason
	st.effective = true
}

// snapshot renders the wire form of the thinking outcome.
func (st *thinkingState) snapshot() *dto.ThinkingSnapshot {
	return &dto.ThinkingSnapshot{
		Level:            st.level,
		Effective:        st.effective,
		Escalated:        st.escalated,
		EscalationReason: st.escalationReason,
		Unsupported:      st.unsupported,
		Truncated:        st.truncated,
	}
}

// estimateHistoryTokens is the bytes/4 history-token estimate used by the
// compaction trigger (ADR-0051 §8).
func estimateHistoryTokens(history []dto.Message) int {
	total := 0
	for _, m := range history {
		total += (len(m.Content) + 3) / 4
	}
	return total
}

// estimateReasoning is the bytes/4 reasoning-token estimate used for the
// thinking budget when the provider omits an exact count (ADR-0024/0051 §4).
func estimateReasoning(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// summaryMaxTokens bounds the compaction summary call (ADR-0051 §8).
const summaryMaxTokens = 1024

// compact runs the metered compaction call (ADR-0051 §8): one provider call
// with thinking off summarizes the oldest turns, the summary replaces them via
// the Session store, and the call is metered as its own model row. It returns
// the summarized range for the snapshot, or nil when there is nothing to do.
func (l *loop) compact(ctx context.Context, turnID string, task dto.Task, res dto.Resolution, svc *shard.Services, history []dto.Message, policy dto.PipelinePolicy) (*dto.CompactionRecord, error) {
	if len(history) == 0 {
		return nil, nil
	}
	var b strings.Builder
	for _, m := range history {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	off := false
	req := dto.Request{
		ModelName: res.Model.ModelID,
		Messages: []dto.Message{
			{Role: "system", Content: "Summarize the following conversation excerpt concisely, preserving facts, decisions, and open questions. Output only the summary."},
			{Role: "user", Content: b.String()},
		},
		EffectiveParams: dto.SamplingParams{Temperature: 0, MaxTokens: summaryMaxTokens},
		Thinking:        &off,
	}
	target := dto.Target{BaseURL: res.Model.BaseURL, Capabilities: res.Model.Capabilities, Runner: res.Model.Runner}
	var summary string
	var counts dto.ProviderCounts
	emit := func(raw dto.RawEvent) {
		switch raw.Type {
		case "token":
			summary += tokenText(raw)
		case "done":
			parseDone(raw, &counts)
		}
	}
	if err := l.d.Provider.Stream(ctx, target, req, emit); err != nil {
		return nil, err
	}
	if strings.TrimSpace(summary) == "" {
		return nil, nil
	}
	fromTs, toTs, turns, err := svc.Sessions.CompactHistory(task.SessionID, summary, policy.Compaction.KeepRecentTurns)
	if err != nil {
		return nil, err
	}
	if turns == 0 {
		return nil, nil
	}
	if err := svc.Meter.AttributeCompaction(ctx, turnID, task.SessionID, res.UsedName, counts); err != nil {
		return nil, err
	}
	return &dto.CompactionRecord{
		FromTs:        fromTs,
		ToTs:          toTs,
		Turns:         turns,
		SummaryTokens: estimateReasoning(summary),
		CacheCost:     true,
	}, nil
}

// streamResult is the loop's view of one provider stream round: the accumulated
// answer text, any tool calls, the finish reason, the final usage counts, and
// the accumulated reasoning-token estimate (ADR-0051 §4).
type streamResult struct {
	text            string
	toolCalls       []dto.ToolCall
	finish          string
	counts          dto.ProviderCounts
	reasoningTokens int
}

// runSteps drives the one tool-calling loop (ADR-0045 §2). It mutates `msgs`
// (the assembled message list) across round-trips, threading assistant
// tool_calls and tool results. It returns the final stream result (the answering
// round), bounded by the global pipeline maxSteps. `auto` thinking escalates
// once after a structured failure (ADR-0051 §2); a `length` turn with neither a
// tool call nor an answer is a labeled error, never an empty done (§5).
func (l *loop) runSteps(ctx context.Context, turnID string, task dto.Task, target dto.Target, payload dto.Payload, tools []dto.ToolDef, anchor *anchor, policy dto.PipelinePolicy, st *thinkingState) (streamResult, error) {
	msgs := payload.Messages

	steps := 0
	for {
		thinking := st.effective
		req := dto.Request{
			ModelName:       payload.Request.ModelName,
			Messages:        msgs,
			Tools:           tools,
			EffectiveParams: payload.Request.EffectiveParams,
			Thinking:        &thinking,
		}

		var res streamResult
		var reasoning strings.Builder
		emit := func(raw dto.RawEvent) {
			switch raw.Type {
			case "token":
				res.text += tokenText(raw)
			case "reasoning":
				t := reasoningText(raw)
				reasoning.WriteString(t)
				l.emitThinking(turnID, t)
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
			// Return the partial result so a cancelled turn can still meter
			// whatever usage the provider reported before the interruption, and
			// preserve any partial answer text (E2 cancel).
			return res, err
		}

		// Exact thinking accounting (ADR-0051 §4): the provider's count when
		// reported, else the engine's accumulated estimate (labeled by the
		// meter). The reasoning cap (§5) labels a truncated pass.
		st.reasoningTokens += estimateReasoning(reasoning.String())
		res.reasoningTokens = st.reasoningTokens
		if policy.MaxThinkingTokens > 0 && st.reasoningTokens >= policy.MaxThinkingTokens {
			st.truncated = true
		}

		// No tool calls (or the model stopped) → answering round.
		if len(res.toolCalls) == 0 || res.finish == "stop" {
			// A round with neither a tool call nor an answer is a structured
			// failure (ADR-0051 §2): `auto` retries once with thinking-on.
			if len(res.toolCalls) == 0 && strings.TrimSpace(res.text) == "" {
				if st.level == dto.ThinkingAuto && !st.escalated {
					st.escalate("no-outcome")
					continue
				}
				if res.finish == "length" {
					st.truncated = true
					return streamResult{}, errThinkingTruncated
				}
				return streamResult{}, errNoOutcome
			}
			l.emitToken(turnID, res.text)
			return res, nil
		}

		// Bound reached without an explicit stop: treat the accumulated text as
		// the answer (bounded loop, never unbounded — ADR-0045).
		if steps >= policy.MaxSteps {
			l.emitToken(turnID, res.text)
			return res, nil
		}

		// dispatching → observing: append the assistant tool_call message, then
		// invoke each tool and append its result (state-machine.md §1.2).
		msgs = append(msgs, dto.Message{Role: "assistant", Content: res.text, Timestamp: nowUnix()})

		retryReason := ""
		for _, tc := range res.toolCalls {
			rawArgs := json.RawMessage(tc.Arguments)
			if len(rawArgs) == 0 {
				rawArgs = json.RawMessage(`{}`)
			}
			// Document-scoped tools need the turn's documentID; it is injected
			// here (the loop holds task.DocumentID), never exposed to the model in
			// the tool schema (config/tools/*.json expose only the model's fields).
			// An anchored `/locate` turn also forces edit_markdown's blockId and
			// baseHash from the anchor, overriding anything the model supplied
			// (ADR-0048 §5), so the model only has to produce the replacement text.
			rawArgs = injectDocumentID(tc.Name, task, anchor, rawArgs)

			out, toolErr := l.d.Executor.Invoke(ctx, tc.Name, rawArgs)

			// Observe structured edit results for events + retry signals.
			handled := l.observeTool(turnID, task, tc, out, toolErr)
			if r, ok := retryableEditFailure(handled); ok && retryReason == "" {
				retryReason = r
			}

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

		// A structured edit failure triggers one `auto` escalation: the retry
		// round runs thinking-on (ADR-0051 §2), labeled with the failure.
		if retryReason != "" && st.level == dto.ThinkingAuto && !st.escalated {
			st.escalate(retryReason)
		}

		steps++
	}
}

// reasoningText extracts the text from a raw reasoning event.
func reasoningText(raw dto.RawEvent) string {
	var v struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw.Data, &v)
	return v.Text
}

// retryableEditFailure reports whether a structured edit result is a retryable
// failure (`invalid-structure` | `guard-failed`, ADR-0051 §2).
func retryableEditFailure(handled json.RawMessage) (string, bool) {
	if len(handled) == 0 {
		return "", false
	}
	var res struct {
		Ok    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(handled, &res) != nil || res.Ok {
		return "", false
	}
	switch res.Error {
	case "invalid-structure", "guard-failed":
		return res.Error, true
	}
	return "", false
}

// injectDocumentID adds task.DocumentID to the args of document-scoped tools
// (edit_markdown, diff). The model never supplies it; the loop owns the document
// binding (ADR-0029: edits target a block in a document the loop is scoped to).
// For edit_markdown it also carries the turn's preset so the staged candidate's
// derived commit message names the mode (ADR-0020 §1); the model never sees
// either field in the tool schema. An anchored `/locate` turn forces the target
// block and its base-hash guard from the anchor (ADR-0048 §5), overriding any
// model-supplied values.
func injectDocumentID(name string, task dto.Task, anchor *anchor, args json.RawMessage) json.RawMessage {
	if name != "edit_markdown" && name != "diff" {
		return args
	}
	merged := map[string]interface{}{}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &merged)
	}
	merged["documentId"] = task.DocumentID
	if name == "edit_markdown" {
		if task.ModeName != "" {
			merged["modeName"] = task.ModeName
		}
		if anchor != nil {
			merged["blockId"] = anchor.blockID
			merged["baseHash"] = anchor.baseHash
		}
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

// emitCancelled emits the labeled terminal `done {cancelled:true}` for a
// user-cancelled turn (ADR-0046 E2).
func (l *loop) emitCancelled(turnID string, res dto.Resolution) {
	data, _ := json.Marshal(map[string]interface{}{
		"degraded":  res.Degraded,
		"usedModel": res.UsedName,
		"cancelled": true,
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
func buildSnapshot(turnID string, task dto.Task, workspaceID, retrievalQuery string, autoRag bool, autoChunks, pinnedChunks []dto.Chunk, loopDrops []dto.ContextDrop, locateRaw json.RawMessage, payload dto.Payload) dto.ContextSnapshot {
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
	snap := dto.ContextSnapshot{
		TurnID:         turnID,
		SessionID:      task.SessionID,
		WorkspaceID:    workspaceID,
		RetrievalQuery: retrievalQuery,
		AutoRag:        autoRag,
		Messages:       messages,
		Chunks:         chunks,
		Drops:          drops,
		Budget:         budget,
		Locate:         locateRaw,
		CreatedAt:      time.Now().Unix(),
	}
	if payload.Window.ContextLength > 0 {
		w := payload.Window
		snap.Window = &w
	}
	return snap
}

// effectiveContext is the resolved per-turn context policy: the persisted
// session policy with the per-turn override merged replace-when-present per
// field, plus defaults (autoRag true, query = the turn's user input).
type effectiveContext struct {
	Pinned   []dto.ChunkRef
	Excluded []dto.ChunkRef
	AutoRag  bool
	Query    string
	Thinking dto.ThinkingLevel
	Decision *dto.DecisionOverride
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
		if p.Thinking != nil {
			eff.Thinking = *p.Thinking
		}
		if p.Decision != nil {
			eff.Decision = p.Decision
		}
	}
	apply(session)
	apply(override)
	return eff
}

// decisionResolution is the resolved decision-layer effect for one turn
// (ADR-0053): the record to persist, and the effective retrieval/thinking the
// rest of the turn uses. record is nil when the layer is disabled.
type decisionResolution struct {
	record   *dto.DecisionRecord
	retrieve bool
	topK     int
	thinking dto.ThinkingLevel // "" inherits the policy default
}

// decide runs the planner call (ADR-0053) and resolves the effective retrieval
// and thinking. Human/policy outranks the model: decision=off runs no Laya call;
// session autoRag=false disables retrieval (Laya cannot re-enable it); a non-nil
// ContextPolicy.thinking overrides the model. When there is nothing left for
// Laya to choose (retrieval off AND thinking already resolved) the call is
// skipped. Every failure degrades fail-open and labeled.
func (l *loop) decide(ctx context.Context, turnID string, task dto.Task, svc *shard.Services, policy dto.PipelinePolicy, eff effectiveContext, modeName, request string, history []dto.Message, selection string) decisionResolution {
	pol := policy.Decision
	res := decisionResolution{retrieve: eff.AutoRag, topK: policy.AutoRagTopK}
	enabled := pol.Enabled
	if eff.Decision != nil {
		enabled = *eff.Decision == dto.DecisionOn
	}
	if !enabled {
		return res
	}
	rec := &dto.DecisionRecord{Enabled: true}
	res.record = rec

	// Nothing for Laya to decide: retrieval is closed and thinking is resolved.
	thinkingOpen := eff.Thinking == "" && policy.Thinking == dto.ThinkingAuto
	if !eff.AutoRag && !thinkingOpen {
		return res
	}
	if l.d.Decision == nil {
		rec.Degraded = true
		rec.Reason = dto.DecisionDegradedUnreachable
		return res
	}
	plan := l.d.Decision.Plan(ctx, dto.DecisionPlannerInput{
		Request:   request,
		History:   tailMessages(history, pol.MaxHistoryTurns),
		Selection: selection,
		Mode:      modeName,
	})
	rec.Planner = &plan
	if plan.Degraded {
		rec.Degraded = true
		rec.Reason = plan.Reason
		return res
	}
	l.meterDecision(ctx, turnID, task.SessionID, svc, decisionModel(pol, plan.Checkpoint), "planner", plan.PromptTokens, plan.CompletionTokens)

	// Human/policy outranks Laya.
	if eff.AutoRag {
		res.retrieve = plan.Retrieve
		if res.retrieve {
			res.topK = breadthTopK(pol.BreadthTopK, plan.Breadth)
			if max := pol.MaxCandidates; max > 0 && res.topK > max {
				res.topK = max
			}
			if res.topK < 1 {
				res.retrieve = false
			}
		}
	}
	if eff.Thinking == "" {
		res.thinking = plan.Thinking
	}
	return res
}

// gate runs the gate call (ADR-0053) over the post-exclude auto set and returns
// the surviving chunks plus a labeled drop for the removals. The retrieval topK
// is already bounded by maxCandidates, so the gate input is too. On failure it
// keeps every chunk (fail-open) and labels the record.
func (l *loop) gate(ctx context.Context, turnID string, task dto.Task, svc *shard.Services, policy dto.PipelinePolicy, rec *dto.DecisionRecord, request string, chunks []dto.Chunk) ([]dto.Chunk, []dto.ContextDrop) {
	if len(chunks) == 0 {
		return chunks, nil
	}
	pol := policy.Decision
	cands := chunks
	if pol.MaxCandidates > 0 && len(cands) > pol.MaxCandidates {
		cands = cands[:pol.MaxCandidates]
	}
	var gres dto.DecisionGateResult
	if l.d.Decision == nil {
		gres = dto.DecisionGateResult{Degraded: true, Reason: dto.DecisionDegradedUnreachable, Threshold: pol.GateThreshold}
	} else {
		gres = l.d.Decision.Gate(ctx, dto.DecisionGateInput{Request: request, Chunks: cands, Threshold: pol.GateThreshold})
	}
	rec.Gate = &gres
	if gres.Degraded {
		rec.Degraded = true
		if rec.Reason == "" {
			rec.Reason = gres.Reason
		}
		return chunks, nil // fail-open: keep all
	}
	l.meterDecision(ctx, turnID, task.SessionID, svc, decisionModel(pol, gres.Checkpoint), "gate", gres.PromptTokens, gres.CompletionTokens)

	kept := make([]dto.Chunk, 0, len(cands))
	for i, c := range cands {
		if i < len(gres.Chunks) && gres.Chunks[i].Keep {
			kept = append(kept, c)
		}
	}
	// Any candidates beyond the cap were not gated; keep them (fail-open).
	if len(chunks) > len(cands) {
		kept = append(kept, chunks[len(cands):]...)
	}
	var drops []dto.ContextDrop
	if n := len(chunks) - len(kept); n > 0 {
		drops = append(drops, dto.ContextDrop{Component: "rag", Reason: "gate", Count: n})
	}
	return kept, drops
}

// meterDecision records one Laya call as its own model row (ADR-0053). A zero
// usage (e.g. a refusal with no tokens) writes no row.
func (l *loop) meterDecision(ctx context.Context, turnID, sessionID string, svc *shard.Services, model, role string, in, out int) {
	if in == 0 && out == 0 {
		return
	}
	_ = svc.Meter.AttributeDecision(ctx, turnID, sessionID, model, role, dto.ProviderCounts{InputTokens: in, OutputTokens: out})
}

// decisionModel is the routed checkpoint when Laya reported one, else the
// configured model name (strict Jev mode omits routing).
func decisionModel(pol dto.DecisionPolicy, checkpoint string) string {
	if checkpoint != "" {
		return checkpoint
	}
	return pol.Model
}

// breadthTopK maps the planner's breadth choice to a retrieval topK.
func breadthTopK(b dto.DecisionBreadthTopK, breadth dto.DecisionBreadth) int {
	switch breadth {
	case dto.DecisionBreadthNone:
		return b.None
	case dto.DecisionBreadthMany:
		return b.Many
	default:
		return b.Few
	}
}

// tailMessages returns the last n turns (≈2 messages each) of history, so the
// planner sees a bounded recent window (ADR-0053).
func tailMessages(history []dto.Message, n int) []dto.Message {
	if n <= 0 || len(history) == 0 {
		return nil
	}
	limit := n * 2
	if len(history) <= limit {
		return history
	}
	return history[len(history)-limit:]
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

// emitThinking forwards one raw reasoning delta as a `thinking` event so clients
// can render a thinking indicator (ADR-0051 §4).
func (l *loop) emitThinking(turnID, text string) {
	if text == "" {
		return
	}
	data, _ := json.Marshal(map[string]string{"text": text})
	l.emit(turnID, dto.Event{Type: "thinking", Data: data})
}

// quantPattern extracts a quantization marker (e.g. "4bit") from a model name.
var quantPattern = regexp.MustCompile(`(?i)(\d+)\s*bit`)

// quantOf is a best-effort quantization label for the measurement record
// (ADR-0051 §11); empty when the model name carries none.
func quantOf(model string) string {
	if m := quantPattern.FindString(model); m != "" {
		return strings.ToLower(m)
	}
	return ""
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
		InputTokens    int `json:"inputTokens"`
		OutputTokens   int `json:"outputTokens"`
		ThinkingTokens int `json:"thinkingTokens"`
	}
	if err := json.Unmarshal(raw.Data, &v); err != nil {
		return
	}
	counts.InputTokens = v.InputTokens
	counts.OutputTokens = v.OutputTokens
	counts.ThinkingTokens = v.ThinkingTokens
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
	case errors.Is(err, assembler.ErrContextWindowExceeded):
		return "context-window-exceeded"
	case errors.Is(err, errThinkingTruncated):
		return "thinking-truncated"
	case errors.Is(err, errNoOutcome):
		return "no-outcome"
	default:
		return "error"
	}
}
