# ADR-0016: Module inventory — exact public APIs, pure-DTO boundaries

Status: Accepted

Supersedes: ADR-0005 (Resolve/Provider wording), ADR-0009 (ResolveModel), ADR-0010
(tool registry split), ADR-0004 (§4 SQLite ownership).

## Context

The base model (ADR-0001) demands sealed-by-default modules with narrow, stable,
contracted public APIs (R1–R6). The prior artifacts stopped short of exact
signatures: `contracts/module-boundaries.md` listed operations in prose,
`interface.md` gave Go signatures for only five modules, and several accepted
ADRs disagreed with each other:

- ADR-0005 puts `Resolve` on the *Provider* gateway ("maps a model name to … an
  endpoint"); ADR-0006, `interface.md` §1, and the §6 sequence diagram put it on
  the *Fleet* gateway.
- `interface.md` §7 asserts the Context assembler "is pure"; the dependency graph
  in `module-boundaries.md` §2 draws `Assembler --> Retriever`.
- `data-model.md` §4 asserts "SQLite owned by the Document store only," yet
  `meter_events`, `messages`, `vec_chunks`, and `blocks_ft` are tables other
  modules necessarily touch.

A session tenet hardened the base model further: **internal module boundaries are
sealed Go interfaces over pure DTOs, never REST.** Each module is a locked
service; the only thing that crosses a boundary is a plain data type — no
behavior, no pointers into another module's state, no embedding of another
module's types. REST/HTTP exists only at process boundaries (client↔engine,
engine↔serving).

## Decision

Pin every engine module's public API to exact signatures over pure DTOs, and
resolve the three contradictions. The module list and their acyclic dependency
graph are specified normatively in `contracts/module-boundaries.md`.

### 1. Fleet gateway — owns model discovery, resolution, and lifecycle

```go
type Capabilities struct {
    ContextLength        int
    ThinkingMode         bool
    SupportsSystemPrompt bool
}

type Model struct {
    Name         string
    BaseURL      string       // http://host:port/v1
    Capabilities Capabilities
    ModeTags     []string
}

type Resolution struct {
    Model           Model
    EffectiveParams SamplingParams // merged defaults ← mode.params ← call overrides
    LiveState       LiveState      // up|down|starting|stopping|provisioning|unknown
    Degraded        bool           // true when a fallback served the request
    UsedName        string        // the model name that actually served (== fallback when Degraded)
}

type LiveState string

type FleetGateway interface {
    ListModels() ([]Model, error)
    Resolve(name string, opts ResolveOpts) (Resolution, error) // merges params, enforces gates, folds in fallback
    Status(name string) (LiveState, error)                    // typed enum, no magic string
    Start(name string) error                                  // BLOCKING: returns only on up, or typed error
    Stop(name string) error
    Provision(ctx context.Context, name string) (provisionID string, err error) // async; observe via Status/lifecycle event
}
```

- `Resolve` **lives on Fleet**. It is the single choke point that: (a) merges
  `manifest.defaults` ← `mode.params` ← per-call overrides into
  `EffectiveParams`; (b) enforces capability gates (context budget vs
  contextLength, thinking-mode support) surfacing typed errors; (c) folds in live
  availability and fallback — when the preferred model is `down`/`not-found`, it
  walks the models sharing the mode's `modeTag` ordered by fleet policy
  (ADR-0015), selects the first `up`, and sets `Degraded=true, UsedName=<fallback>`.
- **`Runner`, `source`, and all provisioning fields do not cross this boundary.**
  The engine never learns which binary serves a model. Those fields are owned by
  the control daemon (ADR-0025); the Fleet gateway holds neither the manifest file
  nor the runner/source/provisioning fields.
- `Start` is **blocking**: it returns only when the server is `up` (or a typed
  error: port-in-use, binary-missing, model-not-found, timeout). The 60s health
  wait lives once, in the lifecycle executor. `Status` returns the typed
  `LiveState` enum.

Resolves the Fleet-vs-Provider `Resolve` contradiction: **Provider no longer
does name/resolution.**

### 2. Provider gateway — pure REST leaf

```go
type Target struct {
    BaseURL      string
    Capabilities Capabilities
}

type SamplingParams struct {
    Temperature float64
    MaxTokens   int
}

type ProviderGateway interface {
    Chat(ctx context.Context, target Target, params SamplingParams) (Completion, error)
    Stream(ctx context.Context, target Target, params SamplingParams, emit func(raw Event)) error
    Embed(ctx context.Context, target Target, text string) ([]float32, error)
}
```

- Takes an **already-resolved `Target`**, never a name. Zero knowledge of names,
  the manifest, or the fleet.
- Emits **only raw, unframed model output**: `token(text)`,
  `done({inputTokens, outputTokens})` (the raw `prompt_eval_count`/`eval_count`),
  `error(code, message)`. **No attribution** — the breakdown is the assembler's
  and is computed downstream.
- Retry/backoff (failure-semantics §1) and per-server `-np 1` serialization are
  hidden internals.
- `Embed` keeps REST framing in exactly one module; the Retriever resolves
  `nomic-embed` via Fleet and calls `Provider.Embed`.

### 3. Agent loop / orchestrator — thin composition root

```go
type Task struct {
    ModeName     string
    DocumentID   string
    UserInput    string
    Selection    *Selection // {BlockID string} — the "edit this paragraph" popover
    Options      *TurnOptions // per-turn overrides (temperature/model)
}

type AgentLoop interface {
    Run(ctx context.Context, task Task) (turnID string, err error) // async; events carry turnID
}
```

- One operation. All real logic is pushed into the leaves. The loop owns only the
  turn state machine (bounded by `maxSteps`, state-machine §1).
- `Run` is **async**; it starts the turn and returns a `turnID`. The loop forwards
  `token/candidate/diff/done` to the SSE bus tagged with `turnID` (wrapping the
  Provider's raw `emit` callback); the Meter emits `meter` events tagged `turnID`.
  Correlating per-turn events to the requesting client is the API server's job,
  via the bus filter.

### 4. Mode registry — pure data leaf

```go
type Mode struct {
    Name          string
    SystemPrompt  string
    DefaultModel  string
    ToolAllowlist []string
    Params        SamplingParams
    ContextBudget ContextBudget // {MaxHistoryTokens, MaxRagTokens}
    MaxSteps      int           // per-mode bound on dispatch/observe (default from policy)
    Agentic       bool          // multi-turn tool loop vs single-shot pass
    Kind          string        // "model" | "assistant" (reserved)
    Preamble      string        // spliced before systemPrompt (e.g. citation line)
}

type ModeRegistry interface {
    List() []Mode
    Get(name string) (Mode, error)
}
```

- `ResolveModel(mode)` is **removed** — it was a one-line delegation to Fleet
  (a shallow module). The loop calls
  `Fleet.Resolve(mode.DefaultModel, {modeTag: mode.Name})` directly.

### 5. Tool registry (definitions) + Tool executor (execution) — split

```go
type ToolRegistry interface { // definitions — pure data leaf, consumed by the assembler
    Register(tool ToolDef) error
    List() []ToolDef
    AllowlistFor(mode Mode) []ToolDef
}

type ToolDef struct {
    Name        string
    Description string
    Parameters  JSONSchema // the prompt-spliced function schema
}

type ToolExecutor interface { // execution — consumed by the loop
    Invoke(name string, args json.RawMessage) (result json.RawMessage, err error)
}
```

- The bind between a definition and its Go handler is the **`name`**: the executor
  owns a private `map[name]→handler func`; there is no reflection and no
  data-reaching-into-code. Startup cross-checks registry names against executor
  handlers and fails fast with `tool-has-no-handler` (ADR-0019).

### 6. Context assembler — pure leaf

```go
type AssemblerInput struct {
    Mode        Mode
    ToolSchemas []JSONSchema
    RAGChunks   []Chunk
    History     []Message
    UserInput   string
}

type Breakdown struct { // deterministic approximation, in a documented unit
    SystemPrompt, Tools, Rag, History, User, Thinking int
}

type ContextAssembler interface {
    Assemble(ctx context.Context, in AssemblerInput) (Payload, Breakdown, error)
}
```

- **Pure**: same inputs → same payload/breakdown. It does **not** call the
  Retriever — the loop fetches chunks and hands them in. The `Assembler --> Retriever`
  edge is removed from the graph, fixing the purity contradiction.

### 7. Token metering — attribution + persistence

```go
type TokenMeter interface {
    Attribute(ctx context.Context, turnID string, breakdown Breakdown, counts ProviderCounts) (AttributedBreakdown, error)
}
```

- **One write op.** `Attribute` scales the assembler's `Breakdown` onto the
  provider's exact `prompt_eval_count`/`eval_count`, persists `meter_events` rows,
  and emits one `meter` event to the bus. It does **not** own fan-out — `Subscribe`
  is dropped (the SSE bus owns subscription). Historical meter read-back, if the
  TUI needs it, is a read path deferred to the API server.
- Attribution split: the **assembler** says "what's in each component"; the
  **meter** scales that onto exact totals and emits. The sum of scaled components
  equals the provider total by construction (Q1, ADR-0022).

### 8. Retriever — retrieval (semantic + lexical)

```go
type Retriever interface {
    Query(ctx context.Context, text string, topK int) ([]Chunk, error)
    Index(ctx context.Context, documentID string) error // write side; calls the Chunker, writes index.db
}
```

- `Query` takes **raw text**, not an embedding. The Retriever owns
  embedding end-to-end: resolves `nomic-embed` via Fleet, calls `Provider.Embed`,
  runs hybrid semantic+lexical + optional rerank, returns ranked chunks with
  provenance.
- Consequence: the Retriever is **not** a leaf (depends on Fleet + Provider for
  the embed call); Provider is now the pure REST leaf.

### 9. Document store — document, blocks, versions

```go
type DocumentStore interface {
    Open(path string) (documentID string, err error)
    Save(doc Document) error
    Blocks(documentID string) ([]Block, error)
    ApplyEdit(ctx context.Context, documentID string, edit BlockEdit) (newRevision Revision, err error)
    Commit(documentID string, msg string) error
    Diff(documentID string, baseRev, rev string) (wordEdits []WordEdit, err error)
    History(documentID string) ([]Revision, error)
    Candidates(documentID string, blockID string) ([]Candidate, error)
}
```

- Word-level diff is a hidden internal (go-diff); exposed only via `Diff()`. No
  separate diff module.
- Owns `app.db` (documents/blocks) + the git repo. Commit cadence and block
  identity are ADR-0020's concern.

### 10. Conversation store — conversation history leaf

```go
type ConversationStore interface {
    Append(ctx context.Context, conversationID string, msg Message) error
    History(ctx context.Context, conversationID string) ([]Message, error)
    ListConversations(ctx context.Context) ([]Meta, error)
}
```

- Owns `messages.db`. Keeps the loop a pure orchestrator.

### 11. SSE event bus

```go
type Event struct {
    TurnID string          // correlation id (empty for non-turn events)
    Type   string          // token|meter|candidate|diff|done|error|backpressure
    Data   json.RawMessage
}

type EventBus interface {
    Emit(ev Event)
    Subscribe(filter func(Event) bool) <-chan Event // bounded; drops with backpressure event on overflow
}
```

- Single fan-out mechanism. Producers (loop, meter) never know HTTP exists. The
  API server subscribes with a `turnID` filter and forwards to the requesting
  client's SSE connection.

### 12. API server — the versioned REST/SSE surface

- Routes are the OpenAPI spec (ADR-0017). The server is a thin adapter: validate,
  dispatch to the locked services, stream events.

*(Amended by ADR-0035/0036 — recorded, not silent:)* the inventory gains the
**Workspace** leaf (`List`/`Read`, ADR-0035) — read-only filesystem reach kept
out of the Document store — and three existing APIs grow a field for file
mentions (ADR-0036): `Task.Mentions`, `AssemblerInput.Mentions` +
`Breakdown.Mentions`, and `ContextBudget.MaxMentionTokens`. Precise signatures
are in `contracts/interface.md`.

## Consequences

- **+** Every seam is a sealed Go interface over pure DTOs — the locked-service
  tenet made concrete; no module reaches another's types, tables, or state (R3).
- **+** The three contradictions are resolved: `Resolve` is Fleet's; the assembler
  is pure; SQLite is partitioned per service.
- **+** Provider is a true leaf (pure REST); the loop is a thin orchestrator; two
  new leaves (Chunker ADR-0020, Conversation store) absorb storage concerns that
  would otherwise pool in one module.
- **−** The Retriever is no longer a leaf — it depends on Fleet + Provider for the
  embed call. Accepted: REST framing stays centralized, and the embed dependency
  is narrow (one resolved `Target`, one `Embed` call).
- **−** More interfaces to write before implementation; the discipline cost of
  R1–R6 is now fully realized.

## Alternatives considered

- **Keep `Resolve` on Provider** — rejected: couples REST framing to discovery and
  forces Mode registry and the loop to depend on Provider for names; contradicts
  ADR-0006/`interface.md` §1.
- **Keeps `ResolveModel`/`Observe`/`Subscribe` shallow ops** — rejected as shallow
  modules (one-line delegations or duplicated responsibilities).
- **Document store owns all SQLite** — rejected as a god module / DB-bus; per-service
  files give clean locked isolation.
- **Keep the assembler calling the Retriever** — rejected: breaks purity (R4) and
  determinism.
