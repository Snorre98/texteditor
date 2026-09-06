# Interface contracts

The seams between modules. Source ADRs: ADR-0016 (module inventory),
ADR-0018 (fleet manifest), ADR-0020 (storage), ADR-0024 (thinking attribution),
ADR-0025 (control daemon), ADR-0026 (sessions), ADR-0027 (shared-DTO ownership).

All boundary types are **pure DTOs** — plain data, no behavior, no pointers into
another module's state, no embedded foreign types beyond other pure DTOs
(locked-service tenet, ADR-0016; clarified by ADR-0027). Shared, owner-free DTOs
live in one neutral package (`shared`/`dto`), owned by no single module; see the
catalog below.

## 0. Shared DTO catalog (owner-free, ADR-0027)

The following types cross one or more module boundaries and are **shared,
owner-free** — defined in the neutral `shared`/`dto` package, pure (no methods,
channels, or pointers), and imported by modules rather than owned by any one of
them. No module may add methods to a shared DTO or define a boundary type that
embeds a sibling module's package type.

| DTO | Used across | Defined in § |
|---|---|---|
| `Capabilities` | Fleet (`Model`), Provider (`Target`) | 1, 2 |
| `SamplingParams` | Provider, Fleet (`Resolution.EffectiveParams`), `Mode` | 1, 2, 8 |
| `ContextBudget` | `Mode` | 8 |
| `Model` | Fleet | 1 |
| `Target` | Provider | 2 |
| `Resolution`, `LiveState` | Fleet | 1 |
| `Chunk` | Retriever, Chunker, Assembler | 3, 4, 5 |
| `Message` | Session store, Assembler, `Payload` | 0b |
| `Request` | Assembler (`Payload`), Provider | 5, 2 |
| `JSONSchema` | `ToolDef` | 0b |
| `Document` | Document store | 0b |
| `Block` | Document store, Chunker | 0b |
| `BlockEdit`, `Revision`, `Candidate`, `WordEdit` | Document store | 9 |
| `Event`, `RawEvent` | EventBus, Provider | 2, 11 |
| `ToolDef` | Tool registry | 8 |
| `BlockKind`, `TextFormatterIssue` | `TextFormatter` | 4b |
| `Guard` | Document store (`BlockEdit`) | 9 |
| `Session` | Session store | 10 |
| `Payload`, `Breakdown` | Context assembler | 5 |
| `MentionContent`, `Mention` | Context assembler / Agent loop | 5, 7 |
| `ProviderCounts`, `AttributedBreakdown` | Token metering | 6 |

## 0b. Shared DTO definitions — the unpinned catalog types

Most catalog DTOs are defined in their owning section below. Four are named
everywhere but pinned nowhere; they are defined here (owner-free, `shared`/`dto`):

```go
// JSONSchema is an unparsed JSON Schema, spliced verbatim into the payload
// (function/parameters schemas); its size is metered (ADR-0011, ADR-0019).
type JSONSchema = json.RawMessage

// Message is one conversation entry (role ∈ user | assistant | tool).
type Message struct {
    Role      string // user | assistant | tool
    Content   string // tool messages carry the tool result (JSON) in Content
    Timestamp int64  // unix epoch seconds
}

// Block is a Markdown block element in the document tree (ADR-0020 §3).
type Block struct {
    ID       string    // stable UUID, minted at creation (ADR-0020 §3)
    ParentID *string   // nil = root level
    Kind     BlockKind // paragraph | heading | list_item | code_fence | blockquote | table
    Position int       // sibling order
    Text     string    // canonical content (normalized + formatted, ADR-0029)
    Hash     string    // hash of the canonical Text — the guard anchor (ADR-0029)
}

// Document is document metadata; the content is the block tree, read via
// DocumentStore.Blocks (a document is a tree of blocks — ADR-0020 §3).
type Document struct {
    ID          string // surrogate id (UUID)
    Path        string // absolute path; the Document store's open resolver
    RootBlockID string // id of the root block
    UpdatedAt   int64  // unix epoch seconds
}
```

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
    Fingerprint(name string) (string, error) // list projection's fingerprint; "" when absent (ADR-0028 §4 gate)
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
- `Fingerprint` returns the daemon `list` projection's optional `fingerprint`
  field (populated only for `source.kind == "needle"` entries, per
  `daemon-http.md §2`). It exists solely for ADR-0028 §4's `router-tools-stale`
  startup gate — a recorded amendment to ADR-0016 §1, which otherwise keeps the
  engine from learning source fields.
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
    Text         string
    ToolCalls    []ToolCall // native tool calls when finish_reason == tool_calls
    FinishReason string     // the response's finish_reason (stop | tool_calls | length | tool …)
    InputTokens  int        // raw prompt_eval_count
    OutputTokens int        // raw eval_count
}

type RawEvent struct {           // unframed, un-attributed
    Type string                  // "token" | "tool_call" | "finish" | "done" | "error"
    Data json.RawMessage         // payload shapes per ADR-0016 §2:
                                 //   token     → {"text": "…"}
                                 //   tool_call → {"id": "…", "name": "…", "arguments": "…"}
                                 //   finish    → {"reason": "tool_calls" | "stop" | …}
                                 //   done      → {"inputTokens": n, "outputTokens": n}
                                 //   error     → {"code": "…", "message": "…"}
}

type ProviderGateway interface {
    Chat(ctx context.Context, target Target, req Request) (Completion, error)
    Stream(ctx context.Context, target Target, req Request, emit func(RawEvent)) error
    Embed(ctx context.Context, target Target, text string) ([]float32, error)
}
```

Semantics: the Provider takes an **already-resolved `Target`** (never a name) plus
an **already-assembled `Request`** (the `Payload.Request` from §5) and is a pure
REST/SSE leaf. It emits **only raw `token`/`tool_call`/`finish`/`done`/`error`**;
attribution is downstream (the assembler + meter). Retry/backoff and per-server
`-np 1` serialization are hidden internals.

(Amended at the agentic-loop milestone: `RawEvent` gained `tool_call` (from SSE
`delta.tool_calls`, accumulated by index) and `finish` (from `finish_reason`), so
the loop can observe a native-tool-calling turn's tool calls and render the
follow-up `role: "tool"` message on the next round-trip. These are raw,
un-attributed provider events like `token`/`done`/`error` — the wire shapes are
`json.RawMessage`, no new shared DTO. This resolves the tool-call wire-format gap:
`interface.md §2` previously pinned only `token`/`done`/`error`.)

(Amended at A5: `Chat`/`Stream` now carry `Request` — the assembled messages,
tools, serving model name, and merged params. This closes the earlier §2/§5 gap
where neither `Target` nor `SamplingParams` could carry the assembled payload to
the Provider. The Provider renders `Request` to the OpenAI-compatible wire format
and owns nothing upstream.)

(Amended at the router-seam milestone (D2–D5): `Completion` gains `ToolCalls`
(recorded when it landed with the agentic loop) and `FinishReason` — the
non-streaming response's `finish_reason`, carried so the `ToolDecider` can read
the router facade's decision signal (ADR-0028 §7). Additive; existing callers
ignore it.)

## 3. Retriever (Go interface)

```go
type Chunk struct {
    BlockID string  `json:"blockId"`
    Text    string  `json:"text"`
    Score   float32 `json:"score"`
    Source  string  `json:"source"` // citation/provenance marker
}

type Retriever interface {
    Query(ctx context.Context, text string, topK int) ([]Chunk, error)
    Index(ctx context.Context, documentID string) error
}
```

*Amendment (Track 1, TUI session):* `Chunk` gains camelCase JSON tags — it
crosses the API wire via the `rag` SSE event, which must stay camelCase like
every other wire shape (recorded alongside the ADR-0017 §6 amendment).


`Query` takes raw text; the Retriever owns embedding (resolves `nomic-embed` via
Fleet, calls `Provider.Embed`), hybrid search, and rerank. `Index` is the write
side: it calls the Chunker then writes `index.db` (vec + FTS).

## 4. Chunker (Go, pure leaf)

```go
type Chunker interface {
    Chunk(tree []Block, maxTokens int) ([]Chunk, error) // tree = the document block tree (ADR-0020 §5); paragraph-aligned, size-bounded
}
```

Pure/deterministic; chunk size is a data tunable (the RAG token lever, ADR-0020).

## 4b. TextFormatter (Go, pure leaf)

Owns formatting — the model never reproduces bytes (ADR-0029). Pure and
deterministic; the style is hardcoded code, not data.

```go
type BlockKind string // paragraph | heading | list_item | code_fence | blockquote | table

type TextFormatterIssue struct {
    Line    int
    Message string
}

type TextFormatter interface {
    Normalize(kind BlockKind, text string) (canonical string, changes []string) // semantic-preserving whitespace
    Validate(kind BlockKind, text string) []TextFormatterIssue                   // structural integrity
    Format(kind BlockKind, text string) (formatted string, changes []string)     // opinionated style
}
```

- `Normalize` — canonical indentation, list markers, table pipe alignment, line
  endings, trailing whitespace. Run on every `ApplyEdit`.
- `Validate` — structural checks (table column counts, balanced fences, list depth).
  Run pre-flight by the edit-tool handler.
- `Format` — the hardcoded opinionated style. Run on `Commit` (accept) and
  autosave. `Normalize` is a strict subset of `Format`.

## 5. Context assembler (Go, pure leaf)

```go
type MentionContent struct { // ADR-0036: turn-scoped context attachment (read-only)
    Path string
    Text string
}

type AssemblerInput struct {
    Mode        Mode
    ModelName   string        // the actually-resolved serving model (usedName)
    Params      SamplingParams // merged effective params
    Tools       []ToolDef     // the mode's allowlisted tools, in splices order
    RAGChunks   []Chunk
    History     []Message
    Mentions    []MentionContent // ADR-0036; spliced after history, before user input
    UserInput   string
}

type Breakdown struct { // deterministic approximation, documented unit
    SystemPrompt, Tools, Rag, History, Mentions, User, Thinking int
}

type Request struct {     // the fully-assembled provider request (pure DTO)
    ModelName       string         // the resolved serving model
    Messages        []Message      // system + history + rag + user, in order
    Tools           []ToolDef      // spliced function definitions
    EffectiveParams SamplingParams // merged defaults ← mode.params ← overrides
}

type Payload struct {
    Messages []Message // the assembled message list
    Request  Request   // the provider-ready request handed verbatim to the Provider
}

type ContextAssembler interface {
    Assemble(ctx context.Context, in AssemblerInput) (Payload, Breakdown, error)
}
```

Pure: same inputs → same payload/breakdown. It does **not** call the Retriever.

(Amended at A5: `AssemblerInput` now carries the resolved `ModelName`, merged
`Params`, and `Tools []ToolDef` (not `[]JSONSchema`); `Payload.Request` is the typed
`Request` DTO, not a `json.RawMessage`. The assembler produces the complete
request — messages, tools, serving model, merged params — which the loop hands
verbatim to `Provider.Chat`/`Stream`, closing the §2 gap.)

(Amended by ADR-0036: `AssemblerInput` gains `Mentions []MentionContent` and
`Breakdown` gains `Mentions int` — mention text is spliced after history and
before the user input, in mention order, each wrapped in a path marker line.
The `Mentions` budget is `Mode.ContextBudget.MaxMentionTokens`; over-budget
mentions are truncated from the tail with a labeled overflow line.)

## 6. Token metering (Go)

```go
type ProviderCounts struct {
    InputTokens     int     // prompt_eval_count
    OutputTokens    int     // eval_count
    ThinkingTokens  int     // reasoning count if reported; 0 if omitted
}

type AttributedBreakdown struct {
    SystemPrompt, Tools, Rag, History, Mentions, User, Thinking int // scaled to exact totals
    ThinkingApprox   bool                                 // true when thinking was tokenized (ADR-0024)
}

type TokenMeter interface {
    Attribute(ctx context.Context, turnID, sessionID, model string, b Breakdown, counts ProviderCounts) (AttributedBreakdown, error)
    SessionUsage(ctx context.Context, sessionID string) (int, error) // cumulative tokens per session (budget)
}
```

`Attribute` scales the assembler's `Breakdown` onto the provider's exact totals,
persists `meter_events` rows (tagged with `sessionID` and the actually-used `model`),
and emits one `meter` event to the bus. Thinking-token
reconciliation is a hidden internal (ADR-0024). No `Subscribe` — fan-out is the
bus's concern.

(Amended to add `sessionID`/`model` inputs: `data-model.md` §1.3 requires both
`meter_events.session_id` and `meter_events.model`, and ADR-0026 §5 requires
per-session budget checks — the loop holds both and passes them in.)

(Amended at the agentic-loop milestone: `SessionUsage(ctx, sessionID)` was added
so the loop can enforce a session's `TokenBudget` (ADR-0026 §5) before a turn —
the meter owns the cumulative tally, so the budget check reads it from here and
surfaces `session-budget-exceeded`.)

(Amended by ADR-0036: `AttributedBreakdown` gains `Mentions` and
`meter_events.component` gains the `mentions` value (data-model §1.3); the
`meter` SSE event gains a required `mentions` field. Components still sum to
the scaled provider totals exactly (Q1).)

## 7. Agent loop (Go)

```go
type Selection struct { BlockID string }
type TurnOptions struct {
    Temperature *float64
    Model       string   // force a model for this turn (optional)
}
type Mention struct { // ADR-0036
    Path string // absolute path; client resolves workspace-relative → absolute
}
type Task struct {
    SessionID  string      // the owning session (ADR-0026)
    ModeName   string
    DocumentID string
    UserInput  string
    Selection  *Selection
    Mentions   []Mention   // turn-scoped context attachments (ADR-0036)
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

(Amended by ADR-0036: `Task` gains `Mentions []Mention`. `Run` resolves every
mention through `Workspace.Read` **before** the turn state machine starts;
failures are fail-fast, pre-streaming, typed SSE errors: `mention-not-found`,
`mention-too-large`, `mention-unreadable`, `too-many-mentions`. Mentions are
turn-scoped — they are not persisted into session history.)

## 8. Mode registry + Tool registry + Tool executor (Go)

```go
type Mode struct {
    Name          string
    SystemPrompt  string
    DefaultModel  string
    ToolAllowlist []string
    Params        SamplingParams
    ContextBudget ContextBudget // { MaxHistoryTokens, MaxRagTokens, MaxMentionTokens int }
    MaxSteps      int
    Agentic       bool
    Kind          string  // "model" | "assistant"
    Preamble      string
    ToolCalling   string  // "native" | "router" (default "native")
}
type ContextBudget struct{ MaxHistoryTokens, MaxRagTokens, MaxMentionTokens int }

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

## 8b. Tool decider (Go, optional)

Wired into the loop only when a mode sets `toolCalling: "router"` (ADR-0028). When
absent, the loop uses native tool-calling. Types live in `shared`/`dto` (ADR-0027).

```go
type Decision struct {
    Name       string          // real tool name (== a ToolDef.Name)
    Args       json.RawMessage // schema-valid arguments for that tool
    Confidence float32         // 0..1; < τ ⇒ "no tool, answer now"
}

type RouterContext struct {    // argument-binding context the loop re-bundles
    ToolDefs  []ToolDef        // the mode's allowlisted tools (candidate set)
    Chunks    []Chunk          // retrieved chunks (citation/note provenance for args)
    Selection *Selection       // the anchored block, when the session is block-scoped
    History   []Message        // recent conversation (arg context)
    UserInput string           // the turn's original request
}

type RouterUsage struct {      // the router call's own metering inputs
    Breakdown Breakdown        // router prompt's per-component split (reuses ADR-0016 §6)
    Counts    ProviderCounts   // router provider's exact counts
}

type RouterResult struct {
    Decision Decision
    Usage    RouterUsage
}

type ToolDecider interface {
    SignalTool() ToolDef   // the synthetic request_tool definition (not a registered tool)
    Decide(ctx context.Context, intent string, c RouterContext) (RouterResult, error)
}
```

Semantics: `SignalTool` returns the single `request_tool` definition the loop
splices into the writer's payload in router mode; it is **not** a registered tool
(no handler) and must not enter the Tool registry. `Decide` is a self-contained call
(resolves `needle-router` via Fleet, calls Provider internally). `Confidence < τ`
(or refusal/empty) is a normal result, not an error; a transport failure is a
labeled error the loop maps to `answering`. The loop routes `result.Usage` to
`Meter.Attribute`.

(Amended at the router-seam milestone (D2–D5) with the implemented semantics,
recording the ADR-0028 §6/§7 resolution choices:

- **τ is applied inside the decider.** `Decide` returns a `Decision` with
  `Name != ""` only when the reported confidence is ≥ τ; a refusal is the
  zero-value `Decision` (`Name == ""`). The loop dispatches iff `Name != ""`.
- **Confidence channel (deviation from ADR-0028 §7's "stop-reason/header"):**
  the router facade's confident response is a non-streaming completion whose
  `FinishReason` is `"tool"` and whose content is compact JSON
  `{"name","args","confidence"}`; refusal/empty is an empty completion
  (`FinishReason "stop"`). The decider parses that content — the Provider carries
  it untouched, so no shared wire type beyond `Completion.FinishReason` (§2).
- **Refusal → answering** is realized as: append a "no tool needed" tool-result
  message for `request_tool`, run **one** more writer round (bounded by
  `mode.maxSteps` like any dispatch), and treat that round's `stop` stream as the
  answering phase (state-machine §1.2 `deciding → answering`). No error event.
  A transport error instead emits `error`/`router-unreachable` first, then the
  same bounded writer round — no retry loop.
- The router call is metered as its own `Meter.Attribute` row
  (`model = "needle-router"`) at dispatch time, whether the outcome is confident
  or a refusal (ADR-0028 §5).)

## 9. Document store (Go)

```go
type BlockEdit struct {
    BlockID string
    Text    string
    Guards  []Guard // optional block-level context guards (ADR-0029)
}
type Guard struct {
    BlockID string // a sibling/context block the edit relies on
    Hash    string // short hash of its canonical content
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

`Block` gains a `Hash` field — the hash of its canonical content, surfaced in the
edit read path (`{blockID, kind, content, hash}`) so the model can echo it as a
guard.

Edit semantics (ADR-0029):

- `ApplyEdit` **normalizes** `edit.Text` to canonical form and **verifies
  `edit.Guards` atomically** before staging. A guard whose `Hash` no longer matches
  the block's current canonical content fails with a typed `guard-failed` error
  naming the changed blocks. A successful stage returns the candidate's `Revision`.
- `Commit` and `Save` **format** the accepted/edited blocks to the opinionated
  style before persisting.
- **Canonical-content invariant:** blocks are always stored canonical; therefore
  content hashes are stable per revision.

Commit cadence and block identity are ADR-0020 (two paths: AI edit == commit; manual
edit == autosave snapshot; block IDs == stable UUIDs).

## 9b. Workspace (Go, leaf)

Read-only filesystem reach (ADR-0035). Source ADR-0035.

```go
type Entry struct {
    Name  string // bare file/dir name
    Path  string // absolute path
    IsDir bool
}

type Workspace interface {
    List(ctx context.Context, dir string) ([]Entry, error)
    Read(ctx context.Context, path string, maxBytes int) ([]byte, error)
}
```

- `List` is shallow, non-recursive, sorted by name (case-insensitive). Hidden
  entries are returned; filtering for display is client-side presentation.
- `Read` is bounded by `maxBytes` and returns raw bytes only — it never
  registers, versions, or indexes anything. Mentioned-file context (ADR-0036)
  reads through here, so a mention is provably side-effect-free.
- Typed errors: `not-found`, `not-a-directory`, `not-regular`, `too-large`,
  `read-failed`. The loop maps them to the mention SSE codes (ADR-0036 §2);
  the API server maps `List` failures to `not-found` / `not-a-directory`.

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
    Type   string // token|meter|candidate|diff|rag|done|error|backpressure
    Data   json.RawMessage
}

type EventBus interface {
    Emit(ev Event)
    Subscribe(filter func(Event) bool) <-chan Event // bounded; drop + backpressure event on overflow
}
```

*Amendment (Track 1, TUI session):* the vocabulary gains `rag` — the agent
loop emits one `rag` event when it observes a `retrieve`/`read_note` result,
carrying the structured tool output (the TUI's RAG-results panel consumes it,
ADR-0013). Recorded alongside the ADR-0017 §6 amendment; not a silent change.


## 12. Serving lifecycle — the verb contract (transported by the daemon)

Source ADR-0007 (verbs) + ADR-0025 (transport). The **control daemon** in
`macos-dev-config` exposes the verbs over HTTP; `serve.sh` remains the CLI
executor the daemon wraps. The engine's Fleet gateway consumes **only** the
daemon's HTTP contract. The precise paths and JSON shapes for that contract are
pinned in `contracts/daemon-http.md` (REST projection of the table below).

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
