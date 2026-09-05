# Interface contracts

The seams between modules. Source ADRs: ADR-0016 (module inventory),
ADR-0018 (fleet manifest), ADR-0020 (storage), ADR-0024 (thinking attribution),
ADR-0025 (control daemon), ADR-0026 (sessions).

All boundary types are **pure DTOs** — plain data, no behavior, no pointers into
another module's state, no embedded foreign types (locked-service tenet).

## 1. Fleet gateway (Go)

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

type LiveState string
const (
    LiveUp          LiveState = "up"
    LiveDown        LiveState = "down"
    LiveStarting    LiveState = "starting"
    LiveStopping    LiveState = "stopping"
    LiveProvisioning LiveState = "provisioning"
    LiveUnknown     LiveState = "unknown"
)

type ResolveOpts struct {
    ModeTag  string          // the mode's name == the fallback tag
    Overrides *SamplingParams // per-call overrides (optional)
}

type Resolution struct {
    Model            Model           // the RESOLVED (possibly fallback) model
    EffectiveParams  SamplingParams  // merged: manifest.defaults ← mode.params ← overrides
    LiveState        LiveState
    Degraded         bool            // true when a fallback served
    UsedName         string          // actual serving name (== fallback when Degraded)
}

type FleetGateway interface {
    ListModels() ([]Model, error)
    Resolve(name string, opts ResolveOpts) (Resolution, error) // merge + gates + fallback
    Status(name string) (LiveState, error)
    Start(name string) error                                    // blocking: up or typed error
    Stop(name string) error
    Provision(ctx context.Context, name string) (provisionID string, err error) // async
}
```

Semantics:
- `Resolve` **merges** `manifest.Defaults` ← `mode.params` ← `opts.Overrides` into
  `EffectiveParams`, **enforces capability gates** (context budget vs contextLength,
  thinking-mode support) surfacing typed errors, and **folds in fallback**: when the
  preferred model is `down`/`not-found`, it walks the models sharing `opts.ModeTag`
  in fleet-policy order (ADR-0015), selects the first `up`, and sets
  `Degraded=true, UsedName=<fallback>`.
- `Resolve` never returns a live `down`/`not-found` model as `UsedName` without a
  fallback unless none exists (`Degraded=false`, and the caller surfaces
  `no-model-available`).
- `Start` blocks only the caller's goroutine; it returns when the server is `up`
  (or a typed error: timeout / port-in-use / binary-missing / model-not-found).
- The Fleet gateway is the daemon's HTTP client (ADR-0025); it never reads the
  manifest file or invokes `serve.sh`.

## 2. Provider gateway (Go)

```go
type Target struct {
    BaseURL      string
    Capabilities Capabilities
}

type SamplingParams struct {
    Temperature float64
    MaxTokens   int
}

type Completion struct {
    Text        string
    InputTokens int  // raw prompt_eval_count
    OutputTokens int // raw eval_count
}

type RawEvent struct {           // unframed, un-attributed
    Type string                  // "token" | "done" | "error"
    Data json.RawMessage
}

type ProviderGateway interface {
    Chat(ctx context.Context, target Target, params SamplingParams) (Completion, error)
    Stream(ctx context.Context, target Target, params SamplingParams, emit func(RawEvent)) error
    Embed(ctx context.Context, target Target, text string) ([]float32, error)
}
```

Semantics: the Provider takes an **already-resolved `Target`** (never a name) and is
a pure REST/SSE leaf. It emits **only raw `token`/`done`/`error`**; attribution is
downstream (the assembler + meter). Retry/backoff and per-server `-np 1`
serialization are hidden internals.

## 3. Retriever (Go interface)

```go
type Chunk struct {
    BlockID string
    Text    string
    Score   float32
    Source  string // citation/provenance marker
}

type Retriever interface {
    Query(ctx context.Context, text string, topK int) ([]Chunk, error)
    Index(ctx context.Context, documentID string) error
}
```

`Query` takes raw text; the Retriever owns embedding (resolves `nomic-embed` via
Fleet, calls `Provider.Embed`), hybrid search, and rerank. `Index` is the write
side: it calls the Chunker then writes `index.db` (vec + FTS).

## 4. Chunker (Go, pure leaf)

```go
type Chunker interface {
    Chunk(document Document, maxTokens int) ([]Chunk, error) // paragraph-aligned, size-bounded
}
```

Pure/deterministic; chunk size is a data tunable (the RAG token lever, ADR-0020).

## 5. Context assembler (Go, pure leaf)

```go
type AssemblerInput struct {
    Mode        Mode
    ToolSchemas []JSONSchema
    RAGChunks   []Chunk
    History     []Message
    UserInput   string
}

type Breakdown struct { // deterministic approximation, documented unit
    SystemPrompt, Tools, Rag, History, User, Thinking int
}

type Payload struct {
    Messages []Message        // the assembled request body
    Request  json.RawMessage  // provider-ready request (undocumented internals stay private)
}

type ContextAssembler interface {
    Assemble(ctx context.Context, in AssemblerInput) (Payload, Breakdown, error)
}
```

Pure: same inputs → same payload/breakdown. It does **not** call the Retriever.

## 6. Token metering (Go)

```go
type ProviderCounts struct {
    InputTokens     int     // prompt_eval_count
    OutputTokens    int     // eval_count
    ThinkingTokens  int     // reasoning count if reported; 0 if omitted
}

type AttributedBreakdown struct {
    SystemPrompt, Tools, Rag, History, User, Thinking int // scaled to exact totals
    ThinkingApprox   bool                                 // true when thinking was tokenized (ADR-0024)
}

type TokenMeter interface {
    Attribute(ctx context.Context, turnID string, b Breakdown, counts ProviderCounts) (AttributedBreakdown, error)
}
```

`Attribute` scales the assembler's `Breakdown` onto the provider's exact totals,
persists `meter_events`, and emits one `meter` event to the bus. Thinking-token
reconciliation is a hidden internal (ADR-0024). No `Subscribe` — fan-out is the
bus's concern.

## 7. Agent loop (Go)

```go
type Selection struct { BlockID string }
type TurnOptions struct {
    Temperature *float64
    Model       string   // force a model for this turn (optional)
}
type Task struct {
    SessionID  string      // the owning session (ADR-0026)
    ModeName   string
    DocumentID string
    UserInput  string
    Selection  *Selection
    Options    *TurnOptions
}

type AgentLoop interface {
    Run(ctx context.Context, task Task) (turnID string, err error) // async
}
```

`Run` starts the turn asynchronously; events carry `turnID`. The loop is a thin
orchestrator owning only the turn state machine (bounded by `mode.maxSteps`). It is
**session-scoped**: it reads `session.History` into the assembler and appends each
turn's messages back to the session (ADR-0026).

## 8. Mode registry + Tool registry + Tool executor (Go)

```go
type Mode struct {
    Name          string
    SystemPrompt  string
    DefaultModel  string
    ToolAllowlist []string
    Params        SamplingParams
    ContextBudget ContextBudget // { MaxHistoryTokens, MaxRagTokens int }
    MaxSteps      int
    Agentic       bool
    Kind          string  // "model" | "assistant"
    Preamble      string
}
type ContextBudget struct{ MaxHistoryTokens, MaxRagTokens int }

type ModeRegistry interface {
    List() []Mode
    Get(name string) (Mode, error)
}

type ToolDef struct {
    Name        string
    Description string
    Parameters  JSONSchema // prompt-spliced function schema
}

type ToolRegistry interface {
    Register(tool ToolDef) error
    List() []ToolDef
    AllowlistFor(mode Mode) []ToolDef
}

type ToolExecutor interface {
    Invoke(name string, args json.RawMessage) (json.RawMessage, error)
}
```

The tool def↔handler bind is the **`name`** (executor owns the private handler map;
startup cross-check fails with `tool-has-no-handler`).

## 9. Document store (Go)

```go
type BlockEdit struct {
    BlockID string
    Text    string
}
type Revision struct { ID, Message string; Timestamp int64 }
type Candidate struct {
    BlockID string
    Text    string
    BaseID  string // the base revision it's diffed against
}
type WordEdit struct { BlockID string; Insertions, Deletions []string }

type DocumentStore interface {
    Open(path string) (documentID string, err error)
    Save(doc Document) error
    Blocks(documentID string) ([]Block, error)
    ApplyEdit(ctx context.Context, documentID string, edit BlockEdit) (Revision, error) // stages a candidate
    Commit(documentID string, msg string) error                                          // accept → one commit
    Diff(documentID string, baseRev, rev string) ([]WordEdit, error)
    History(documentID string) ([]Revision, error)
    Candidates(documentID string, blockID string) ([]Candidate, error)
}
```

Commit cadence and block identity are ADR-0020 (two paths: AI edit == commit; manual
edit == autosave snapshot; block IDs == stable UUIDs).

## 10. Session store (Go, leaf)

Owns a dedicated `sessions.db` (ADR-0026). Source ADR-0026.

```go
type Session struct {
    ID            string   // UUID, client-facing identity
    DocumentID    string
    AnchorBlockID *string  // nil = doc-level chat; set = selection/bubble anchor
    ModeType      string   // persisted per-session persona
    Title         string
    TokenBudget   *int     // optional per-session cumulative-token cap
    CreatedAt     int64
    UpdatedAt     int64
}

type SessionStore interface {
    ListByDocument(documentID string) ([]Session, error)
    Create(documentID string, anchorBlockID *string, modeType string) (Session, error)
    Resume(id string) (Session, error)          // find-or-open an anchored session
    Append(sessionID string, msg Message) error
    History(sessionID string) ([]Message, error)
}
```

`Resume(id)` (or `Create` on an existing `(document_id, anchor_block_id)` pair)
is create-or-resume: re-anchoring to the same block reopens the same session.

## 11. SSE event bus (Go)

```go
type Event struct {
    TurnID string
    Type   string // token|meter|candidate|diff|done|error|backpressure
    Data   json.RawMessage
}

type EventBus interface {
    Emit(ev Event)
    Subscribe(filter func(Event) bool) <-chan Event // bounded; drop + backpressure event on overflow
}
```

## 12. Serving lifecycle — the verb contract (transported by the daemon)

Source ADR-0007 (verbs) + ADR-0025 (transport). The **control daemon** in
`macos-dev-config` exposes the verbs over HTTP; `serve.sh` remains the CLI
executor the daemon wraps. The engine's Fleet gateway consumes **only** the
daemon's HTTP contract.

| Verb | Input | Output | Idempotent? |
|---|---|---|---|
| `list` | — | daemon entries (two-tier) + live status + on-disk discovery | read-only |
| `start` | `name\|all` | background start, wait for health; refuse if port busy | starting a running server is an error unless `status`=`up` |
| `stop` | `name\|all` | stop; no-op + warn if not running | yes |
| `status` | `name\|all` | health via `/health`, `/v1/models`, or `/api/tags` | read-only |
| `log` | `name` | tail the server log | read-only |
| `reach` | `name` | base URL + client env/flag + `curl` example | read-only |
| `provision` | `name` | async HF download; observable via `status` (`provisioning`) | re-running skips present files |

### 12.1 Error codes

| Code | Meaning |
|---|---|
| `unknown-server` | name not in manifest |
| `port-in-use` | target port already bound; includes the remap hint |
| `model-not-found` | `source` file/repo missing and not yet provisioned |
| `binary-missing` | runner binary not on PATH |
| `not-running` | `stop`/`status`/`log` on a server that isn't up |
| `lanes-conflict` | two models resolve to the same source on different daemons (ADR-0018) |

## 13. Invariants

- All boundary types are pure DTOs; no module embeds another module's types (ADR-0016).
- The Fleet gateway is the *only* engine module that may talk to serving — and only
  via the daemon's HTTP contract (ADR-0025).
- The Provider never loads weights, shells out, or resolves names — it only speaks
  REST (ADR-0016).
- The Context assembler and Chunker are pure: same inputs → same outputs (R4).
- The Token metering module owns thinking-token reconciliation; when it tokenizes
  (provider omitted the count), the result is a **labeled approximation** (ADR-0024).
