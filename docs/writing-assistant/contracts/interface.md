# Interface contracts

The seams between modules. Source ADRs: ADR-0016 (module inventory),
ADR-0018 (fleet manifest), ADR-0020 (storage), ADR-0024 (thinking attribution),
ADR-0025 (control daemon), ADR-0026 (sessions), ADR-0027 (shared-DTO ownership),
ADR-0035 (filesystem reach; leaf renamed Filesystem by ADR-0049 §5), ADR-0036
(mentions), ADR-0044 (context engine), ADR-0047 (canonical paths), ADR-0049
(workspaces, corpus, sharded context storage).

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
| `SamplingParams` | Provider, Fleet (`Resolution.EffectiveParams`) | 1, 2 |
| `PipelinePolicy` | Pipeline policy, Assembler (`AssemblerInput`), Agent loop | 5, 7, 8c |
| `Model` | Fleet | 1 |
| `Target` | Provider | 2 |
| `Resolution`, `LiveState` | Fleet | 1 |
| `Chunk` | Retriever, Chunker, Assembler | 3, 4, 5 |
| `ChunkRef` | Retriever (`Get`), context tray / `Task`, `ContextPolicy` | 3, 7 |
| `ContextPolicy` | Session store (`Session.contextPolicy`), Agent loop (`Task.Context`), API server | 10, 7 |
| `IndexedDocument` | Retriever (`Status`), corpus status | 3 |
| `Message` | Session store, Assembler, `Payload` | 0b |
| `Request` | Assembler (`Payload`), Provider | 5, 2 |
| `JSONSchema` | `ToolDef` | 0b |
| `Document` | Document store | 0b |
| `Block` | Document store, Chunker | 0b |
| `BlockEdit`, `Revision`, `Candidate`, `WordEdit`, `BlockWrite` | Document store | 9 |
| `Event`, `RawEvent` | EventBus, Provider | 2, 11 |
| `ToolDef` | Tool registry | 8 |
| `BlockKind`, `TextFormatterIssue` | `TextFormatter` | 4b |
| `Guard` | Document store (`BlockEdit`) | 9 |
| `Session` | Session store | 10 |
| `Payload`, `Breakdown`, `MessageProvenance` | Context assembler | 5 |
| `ContextSnapshot`, `ContextMessage`, `ContextDrop`, `BudgetUsage` | Context inspector / API | 5 |
| `MentionContent`, `Mention` | Context assembler / Agent loop | 5, 7 |
| `ProviderCounts`, `AttributedBreakdown`, `SessionMeter` | Token metering | 6 |
| `Entry` | Filesystem | 9b |
| `Workspace`, `CorpusScope`, `CorpusDocumentStatus`, `CorpusJob` | Workspace store, corpus surface | 9c |

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
    // ModelID is the id the serving endpoint accepts in the OpenAI `model`
    // field when it differs from Name (e.g. the HF repo id an mlx runner
    // serves); the daemon projects it as `modelId` in `list`. Empty means
    // Name is the wire id. Internal only — never exposed in the client-facing
    // API (ADR-0016 §1). The loop sends it as the provider request's `model`
    // (ADR-0011), so name-validating runners accept the request.
    ModelID string
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

type ModelState struct {           // one row of the daemon's `status/all` projection (ADR-0040)
    Name  string
    State LiveState
}

type ResolveOpts struct {
    ModeTag  string          // the mode's name == the fallback tag
    Overrides *SamplingParams // per-call overrides (optional)
}

type Resolution struct {
    Model            Model           // the RESOLVED (possibly fallback) model
    EffectiveParams  SamplingParams  // merged: manifest.defaults ← opts.Overrides
    LiveState        LiveState
    Degraded         bool            // true when a fallback served
    UsedName         string          // actual serving name (== fallback when Degraded)
}

type FleetGateway interface {
    ListModels() ([]Model, error)
    Resolve(name string, opts ResolveOpts) (Resolution, error) // merge + gates + fallback
    Status(name string) (LiveState, error)
    ListStatus() ([]ModelState, error) // daemon GET /status/all — one batch roundtrip (ADR-0040)
    Start(name string) error                                    // blocking: up or typed error
    Stop(name string) error
    Provision(ctx context.Context, name string) (provisionID string, err error) // async
    Fingerprint(name string) (string, error) // list projection's fingerprint; "" when absent (ADR-0028 §4 gate)
}
```

Semantics:
- `Resolve` **merges** `manifest.Defaults` ← `opts.Overrides` into
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
- `ListStatus` is the batch `status/all` read (ADR-0040): every model's state in
  one roundtrip, manifest order, `unknown → down` folded exactly like `Status`.
- The **observability reads** `ListModels`/`ListStatus` serve the **last-good
  projection** when the daemon is unreachable: `ListModels` returns the cached
  list and `ListStatus` the cached names with `unknown` states, both
  *alongside* the wrapped `daemon-unreachable` error so callers can label the
  staleness (ADR-0040 §3). `Resolve`/`Status`/`Start`/`Stop` keep hard-fail
  semantics — no resolution against stale state.
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
    // Runner is the serving runner kind (`llama.cpp` | `mlx-lm` | `mlx-vlm` |
    // `delegate`), projected by the daemon and consumed by the Provider to map
    // the per-runner thinking toggle (ADR-0051 §3). Empty = unknown runner.
    Runner string
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
    ThinkingTokens int      // provider-reported reasoning count; 0 when omitted (ADR-0051 §4)
}

type RawEvent struct {           // unframed, un-attributed
    Type string                  // "token" | "reasoning" | "tool_call" | "finish" | "done" | "error"
    Data json.RawMessage         // payload shapes per ADR-0016 §2:
                                 //   token     → {"text": "…"}
                                 //   reasoning → {"text": "…"}   (raw thinking delta, ADR-0051 §4)
                                 //   tool_call → {"id": "…", "name": "…", "arguments": "…"}
                                 //   finish    → {"reason": "tool_calls" | "stop" | …}
                                 //   done      → {"inputTokens": n, "outputTokens": n, "thinkingTokens": n}
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

(Amended by ADR-0051 §3/§4 (Phase C5): `Target` gains `Runner`, so the Provider
maps the thinking toggle per runner — mlx-lm / llama.cpp
`chat_template_kwargs: {"enable_thinking": bool}`, OpenAI-compatible
`reasoning_effort`; an unsupported runner renders nothing and the loop labels
`thinking-unsupported`. `RawEvent` gains `reasoning` (a raw thinking delta) and
`done` carries `thinkingTokens`; `Completion` gains `ThinkingTokens`.
`dto.Request` gains `Thinking *bool` (nil = runner default, false = disable, true
= enable for the `auto` escalation). `SupportsThinkingToggle(runner)` is the
provider's capability predicate.)

## 3. Retriever (Go interface)

```go
type Chunk struct {
    BlockID  string  `json:"blockId"`
    ChunkKey string  `json:"chunkKey,omitempty"` // path#index (corpus) | documentID#index (versioned)
    Text     string  `json:"text"`
    Score    float32 `json:"score"`
    Source   string  `json:"source"` // citation/provenance marker
    Path     string  `json:"path,omitempty"`    // canonical absolute file path
    Heading  string  `json:"heading,omitempty"` // heading stack / nearest heading
    Pinned        bool `json:"pinned,omitempty"`        // snapshot human pin (C4)
    HumanOverride bool `json:"humanOverride,omitempty"` // snapshot override (C4)
}

type IndexedDocument struct { // Retriever.Status
    Path        string
    DocumentID  string // empty for path-keyed corpus files
    ContentHash string
    ChunkCount  int
    IndexedAt   int64
}

type ChunkRef struct { // tray pin/exclude reference (ADR-0049 §8)
    Path     string
    ChunkKey string // optional; omitted = every chunk under Path
    Hash     string // optional content hash (advisory in C4)
}

type Retriever interface {
    Query(ctx context.Context, text string, topK int) ([]Chunk, error)
    SearchText(ctx context.Context, query string, limit int) ([]Chunk, error) // FTS5-only, embedding-free (ADR-0048 §2)
    Index(ctx context.Context, documentID string) error
    IndexPath(ctx context.Context, path, contentHash string) error
    Evict(ctx context.Context, chunkKeyOrPath string) error
    Status() ([]IndexedDocument, error)
    Get(ctx context.Context, refs []ChunkRef) ([]Chunk, error)
}
```

*Amendment (Phase D, ADR-0048 §2):* the Retriever gains `SearchText`, a
deterministic **FTS5-only** lexical search (sanitized MATCH, no embedding, no
vec0 KNN) returning provenance-bearing chunks best-first. `/locate` uses it so
locating is independent of the embedding model and token-free. Zero matches is
not an error.

*Amendment (Track 1, TUI session):* `Chunk` gains camelCase JSON tags — it
crosses the API wire via the `rag` SSE event, which must stay camelCase like
every other wire shape (recorded alongside the ADR-0017 §6 amendment).

*Amendment (Phase C, ADR-0044 §3 / ADR-0049 §4/§5):* the Retriever is
**workspace-scoped** — one instance per workspace shard (`index.db` lives under
`<data>/workspaces/<id>/`). `Chunk` gains provenance (`ChunkKey`, `Path`,
`Heading`). `Index` keeps the versioned-document path (chunkKey
`documentID#index`); `IndexPath` indexes a corpus file path-keyed (chunkKey
`path#index`) and is idempotent per unchanged content; `Evict` removes both the
vec0 and FTS rows for a chunk/path/document and is idempotent; `Status` returns
per-file hash/chunk-count rows for the corpus status surface; `Get` resolves
stable `ChunkRef`s (tray pins, including after resume). `Query` is genuinely
hybrid: FTS5 `bm25` + vec0 KNN fused with reciprocal rank fusion (k=60) plus
dedupe. Index reads corpus files through the Filesystem leaf, so `ALLOWED_ROOTS`
bounds indexing (ADR-0049 §6).

## 4. Chunker (Go, pure leaf)

```go
type Chunker interface {
    Chunk(tree []Block, maxTokens int) ([]Chunk, error) // tree = the document block tree (ADR-0020 §5); heading-aware, paragraph-aligned, size-bounded
}

// ChunkMarkdown splits raw markdown into heading-aware chunks (ADR-0044 §3,
// ADR-0049 §4): corpus files are indexed path-keyed with no documents row, so
// the Retriever reads the file and calls this directly. Pure.
func ChunkMarkdown(raw string, maxTokens int) ([]Chunk, error)
```

Pure/deterministic; chunk size is a data tunable (the RAG token lever, ADR-0020).
Chunks carry `Heading` (the markdown heading stack for `ChunkMarkdown`, the
nearest preceding heading for `Chunk`).

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
    Tools       []ToolDef     // all registered tools (global; ADR-0045), in splice order
    Policy      PipelinePolicy // the one global pipeline policy (budgets; ADR-0045 §3)
    Pinned      []Chunk       // human-pinned chunks (ADR-0049 §7/§11), front-loaded
    RAGChunks   []Chunk       // auto-retrieved chunks, spliced after Pinned
    History     []Message
    Mentions    []MentionContent // ADR-0036; spliced after history, before user input
    UserInput   string
    ContextLength int           // resolved model window (ADR-0051 §6); 0 = gate disabled
}

type Breakdown struct { // deterministic approximation, documented unit
    SystemPrompt, Tools, Rag, History, Mentions, User, Thinking int
}

type Request struct {     // the fully-assembled provider request (pure DTO)
    ModelName       string         // the resolved serving model
    Messages        []Message      // system + history + rag + user, in order
    Tools           []ToolDef      // spliced function definitions
    EffectiveParams SamplingParams // merged defaults ← opts.Overrides
    Thinking        *bool          // nil = runner default; false/true = disable/enable (ADR-0051 §3)
}

type Payload struct {
    Messages   []Message           // the assembled message list
    Request    Request             // the provider-ready request handed verbatim to the Provider
    Provenance []MessageProvenance // component + provenance for every assembled message (ADR-0044 §4)
    Drops      []ContextDrop       // labeled truncation/drop records (never silent)
    Budget     []BudgetUsage       // per-component utilization vs the PipelinePolicy limit
    Window     WindowUsage         // per-turn context-window accounting (ADR-0051 §6)
}

// MessageProvenance: one assembled message's component and source. Component ∈
// system | history | rag | mention | user. Tokens is the deterministic estimate
// contributing to Breakdown; Pinned is a human override (Phase C4; false in C3).
type MessageProvenance struct {
    Role      string
    Component string
    Source    string
    Tokens    int
    Pinned    bool
}

// ContextMessage / ContextDrop / BudgetUsage are the wire forms of the snapshot
// (ADR-0044 §4); the snapshot is engine data clients render verbatim.
type ContextMessage struct { Role, Component, Source string; Tokens int; Pinned bool }
type ContextDrop struct { Component, Reason string; Count int; Detail string; HumanOverride bool }
type BudgetUsage struct { Component string; Used, Limit int }

// ContextPolicy is the context-tray decision set (ADR-0049 §7/§8). The engine
// persists one per session (Session.contextPolicy) and accepts per-turn
// overrides (Task.Context). Merge is replace-when-present per field: a non-nil
// slice/pointer replaces the lower layer wholesale (an explicit empty list
// clears), a nil field inherits. It carries decisions, never payload text.
type ContextPolicy struct {
    Pinned         []ChunkRef
    Excluded       []ChunkRef
    AutoRag        *bool
    RetrievalQuery *string
    Thinking       *ThinkingLevel // off | auto | on (ADR-0051 §1); nil inherits
}

// ThinkingLevel ∈ off | auto | on (ADR-0051 §1).
type ThinkingLevel string

// ContextSnapshot is the persisted per-turn record (GET /turns/{id}/context and
// the `context` SSE payload). Decision (Phase F) is a reserved optional record;
// Locate (Phase D) carries the LocateResult JSON for a `/locate` turn. The
// Thinking/Measurements/Compacted/Window/SessionBudget fields are the ADR-0051
// additions: the resolved thinking outcome, the per-turn measurements, the
// summarized range, the window accounting, and the soft/hard budget state.
type ContextSnapshot struct {
    TurnID, SessionID, WorkspaceID string
    RetrievalQuery                 string
    AutoRag                        bool
    Messages                       []ContextMessage
    Chunks                         []Chunk
    Drops                          []ContextDrop
    Budget                         []BudgetUsage
    Decision, Locate               json.RawMessage // Decision reserved; Locate = LocateResult
    Thinking                       *ThinkingSnapshot
    Measurements                   *TurnMeasurement
    Compacted                      *CompactionRecord
    Window                         *WindowUsage
    SessionBudget                  *SessionBudget
    CreatedAt                      int64
}

// LocateResult is the deterministic `/locate` chunk-anchoring outcome
// (ADR-0048 §3). Status ∈ resolved | ambiguous | not-found; MatchType ∈
// exact | fuzzy. It is emitted as the `locate` SSE event (TurnID populated on
// the event only, so a client can answer the picker) and recorded in
// ContextSnapshot.Locate (without TurnID; the envelope already has it).
type LocateResult struct {
    TurnID     string            // event only; empty in the snapshot record
    Status     string
    MatchType  string
    Confidence float64
    DocumentID string
    Path       string
    BlockID    string
    ChunkKey   string
    Span       []string          // first..last block ids (multi-block match)
    Candidates []LocateCandidate // ranked, ambiguous only
    Stale      bool
    Context    string            // anchor block + neighbors, or a degrade label
}

type LocateCandidate struct {
    DocumentID, Path, BlockID, ChunkKey, TextPreview string
    Score                                            float64
    Stale                                            bool
}

// LocateChoice is the picker's answer (POST /turns/{id}/locate): pick a
// candidate by ChunkKey, or Cancel to plain chat.
type LocateChoice struct {
    ChunkKey string
    Cancel   bool
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
The `Mentions` budget is `PipelinePolicy.MaxMentionTokens`; over-budget
mentions are truncated from the tail with a labeled overflow line. ADR-0045
moved the history/RAG/mention budgets from the mode to `PipelinePolicy`.)

(Amended by ADR-0044 §4 (Phase C3): `Payload` gains `Provenance`, `Drops`, and
`Budget`. The assembler builds one `MessageProvenance` per assembled message
(component `system|history|rag|mention|user`; `source` is the chunk/mention path
for rag/mention rows); records labeled history/RAG/mention truncation drops with
counts (`history-budget`, `rag-budget`, `mention-budget`); and reports
per-component budget utilization against `PipelinePolicy` (`limit` only for the
three budgeted components). The signature is unchanged and the leaf stays
pure/deterministic: the assembler never knows about sessions, workspaces, or
snapshots.)

(Amended by ADR-0049 §7/§8/§11 (Phase C4): `AssemblerInput` gains `Pinned
[]Chunk`; pinned chunks are front-loaded (emitted immediately after the system
message, before history and auto RAG chunks) in ref order and share the RAG
budget with `RAGChunks`, so pins never bypass budgets. `MessageProvenance.Pinned`
labels pinned rows; `ContextDrop.HumanOverride` labels a pinned chunk dropped by
truncation. Snapshot `Chunk` rows carry `Pinned`/`HumanOverride`. The assembler
stays pure and signature-unchanged.)

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
    Attribute(ctx context.Context, turnID, sessionID, model string, b Breakdown, counts ProviderCounts, m TurnMeasurement) (AttributedBreakdown, error)
    AttributeCompaction(ctx context.Context, turnID, sessionID, model string, counts ProviderCounts) error // own model row (ADR-0051 §8)
    SessionUsage(ctx context.Context, sessionID string) (int, error) // cumulative tokens per session (budget)
    SessionBreakdown(ctx context.Context, sessionID string) (SessionMeter, error) // per-component cumulative meter
}

// SessionMeter is a session's cumulative per-component token meter
// (GET /sessions/{id}/meter, ADR-0044 §4, ADR-0026 §5): meter_events grouped by
// component in canonical order, plus the cumulative prompt+completion total.
type SessionMeter struct {
    SessionID  string
    Components []SessionMeterComponent // system|tools|rag|history|mentions|user|thinking|completion|compaction
    Total      int
}
type SessionMeterComponent struct {
    Component        string
    PromptTokens     int
    CompletionTokens int
    Approx           bool // labeled approximation (thinking, ADR-0024)
}

// TurnMeasurement is the per-turn measurement record (ADR-0051 §11), persisted
// in meter_measurements and carried on the meter event.
type TurnMeasurement struct {
    PromptTokens, ThinkingTokens, CompletionTokens int
    LatencyMs                                      int64
    Model, Quant                                   string
    WindowUtilization                              float64
}

// SessionBudgetState classifies cumulative usage against a session budget
// (ADR-0051 §7): soft (warn, proceed) / hard (refuse unless compaction rescues).
func SessionBudgetState(used int, budget *int, softRatio float64, nextTokens int) (soft, hard bool)
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

(Amended by ADR-0044 §4 (Phase C3): `SessionBreakdown(ctx, sessionID)` was added
so `GET /sessions/{id}/meter` can aggregate the workspace shard's `meter_events`
by component. The meter is workspace-scoped by shard (ADR-0049 §5) — the API
server resolves the session's shard, then reads this aggregate; there is no
global meter total. `Attribute` and its scale-to-provider-total invariant are
unchanged.)

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
    SessionID   string      // the owning session (ADR-0026)
    ModeName    string
    DocumentID  string
    WorkspaceID string      // workspace shard owning this turn (ADR-0049 §5); optional
    UserInput   string
    Selection   *Selection
    Mentions    []Mention   // turn-scoped context attachments (ADR-0036)
    Options     *TurnOptions
    Context     *ContextPolicy // optional per-turn context override (ADR-0049 §8)
}

type AgentLoop interface {
    Run(ctx context.Context, task Task) (turnID string, err error) // async
    // ResolveLocate answers a waiting `/locate` ambiguity picker (ADR-0048 §4):
    // a chosen candidate resumes the anchored turn, a cancel degrades it to
    // plain chat. ErrNoPendingLocate (typed 409) when the turn is not waiting.
    ResolveLocate(turnID string, choice LocateChoice) error
    // Cancel cancels a still-running turn (POST /turns/{id}/cancel). The turn
    // ends with a labeled terminal `done {cancelled:true}`, partial usage is
    // metered; ErrTurnNotRunning (typed 409) when it is no longer running.
    Cancel(turnID string) error
}
```

`Run` starts the turn asynchronously; events carry `turnID`. The loop is a thin
orchestrator owning only the turn state machine (bounded by the global
`PipelinePolicy.MaxSteps`, ADR-0045). It is **session-scoped**: it reads
`session.History` into the assembler and appends each turn's messages back to the
session (ADR-0026).

(Amended by ADR-0036: `Task` gains `Mentions []Mention`. `Run` resolves every
mention through `Filesystem.Read` **before** the turn state machine starts;
failures are fail-fast, pre-streaming, typed SSE errors: `mention-not-found`,
`mention-too-large`, `mention-unreadable`, `too-many-mentions`, and
`path-outside-allowed-roots`. Mentions are turn-scoped — they are not persisted
into session history.)

(Amended by ADR-0049 §5: `Task` gains optional `WorkspaceID`. At turn start the
loop resolves the workspace — explicit id, else resolve-or-create rooted at the
canonical parent directory of the turn's document (`workspace-unresolved` on
failure) — acquires that shard's lease, and runs the turn against its
Retriever/Session/Meter. The lease is released when the turn ends.)

(Amended by ADR-0045: one fixed pipeline for every preset — all registered tools
advertised, auto-RAG always runs (`PipelinePolicy.AutoRagTopK`), one agentic loop
bounded by `PipelinePolicy.MaxSteps`. The single-shot `maxSteps=0` branch and all
per-mode branches are gone.)

(Amended by ADR-0044 §4 (Phase C3): at turn start the loop writes
`Workspaces.RouteTurn(turnID, workspaceID, sessionID)` so a finished turn's
snapshot resolves; emits the auto-RAG `rag` event (the same `{ok, chunks}`
shape as tool retrieval) after `Query`; and, after `Assemble`, wraps the
assembler's provenance/drops/budget plus the retrieved chunks into a
`ContextSnapshot`, persists it through the shard Session store
(`SaveContext`), and emits the `context` SSE event carrying the snapshot bytes.
The assembler never knows about sessions/workspaces; the loop builds the
envelope from the assembler output + retrieval results.)

(Amended by ADR-0049 §8/§11 (Phase C4): `Task` gains optional `Context
*ContextPolicy`. At turn start the loop merges it replace-when-present per field
over the persisted session policy (`Session.contextPolicy`): an override field
replaces the session field wholesale (an explicit empty list clears), an absent
field inherits; defaults are `autoRag=true` and `retrievalQuery=Task.UserInput`.
It then runs auto-RAG only when the effective `autoRag` is true, drops
auto-retrieved chunks matching `excluded` (by `chunkKey`, else canonical
`path`) with a labeled `excluded` drop, resolves `pinned` refs through
`Retriever.Get` (works off the top-k) and labels unresolved refs as `not-found`
human-override drops, passes pins to the assembler as `Pinned` (front-loaded,
budget-shared), emits the `rag` event for the post-exclude/pre-truncation auto
set only, and records the effective `retrievalQuery`/`autoRag` plus pinned/
auto chunks (pins labeled `pinned`/`humanOverride`) in the snapshot. The
override is never persisted.)

(Amended by ADR-0048 §1/§4/§5 (Phase D): `Task` is unchanged — `/locate` is a
leading-line command parsed at the very start of `Run` before mentions and the
session append. The command line is stripped; the pasted chunk becomes the
effective user input used for the session, the default retrieval query, and
assembly. The loop resolves the chunk (open document → workspace corpus index)
through the sealed `internal/locate` resolver, emits the `locate` SSE event
(the `LocateResult` JSON, with `turnId` on the event), and records it in
`ContextSnapshot.Locate`. A fuzzy (or multi-exact) outcome opens an in-memory
picker keyed by turnID and **waits** (bounded, 120s) — no model call and no edit
before the choice; `ResolveLocate` answers it. A resolved open-document match
anchors the turn: the anchor block plus one neighbor on each side is injected as
a `mention`-component attachment (`Source: <path>#<blockId>`), a deterministic
replacement-only instruction is appended to the assembled user input (not the
session), `Task.Selection` is set, and `edit_markdown`'s `blockId` + `baseHash`
are forced from the anchor so the model supplies only `text`. Not-found and
cancel/timeout degrade to plain chat with a labeled outcome. Locating is
deterministic and token-free.)

(Amended by ADR-0046 §5 (Phase E2): the loop gains a turn-scoped cancel registry.
`Run` derives a cancellable context per turn; `Cancel(turnID)` marks the turn
user-cancelled and cancels it, returning `ErrTurnNotRunning` (typed 409) when the
turn is not running. A cancelled turn ends with a labeled terminal
`done {cancelled:true}` (never an error): any partial answer text is emitted and
persisted, whatever partial usage the provider reported is metered, and the
snapshot records `cancelled`. The API server emits a `turn` SSE event
(`{turnId, sessionId}`) as the first event of the stream, right after
subscribing, so a client can address `POST /turns/{id}/cancel` and
`POST /turns/{id}/locate` while the turn runs. `SessionStore.Create` gains a
`title` argument and `Rename` sets it (`PUT /sessions/{id}`); `title` was already
persisted.)

## 8. Mode registry + Tool registry + Tool executor (Go)

```go
type Mode struct { // ADR-0045: a prompt preset
    Name         string
    SystemPrompt string
    DefaultModel string
}

type ModeRegistry interface {
    List() []Mode
    Get(name string) (Mode, error)
}

type ToolDef struct {
    Name        string
    Description string
    Parameters  JSONSchema // prompt-spliced function schema
}

type ToolRegistry interface { // ADR-0045: tools are global; no per-mode allowlist
    Register(tool ToolDef) error
    List() []ToolDef
}

type ToolExecutor interface {
    Invoke(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}
```

The tool def↔handler bind is the **`name`** (executor owns the private handler map;
startup cross-check fails with `tool-has-no-handler`).

(Amended by ADR-0049 §5: `Invoke` takes a `context.Context`. The loop enriches it
with the turn's shard lease, so `retrieve`/`read_note` reach the correct
workspace index; a handler invoked without shard services fails with
`retriever-unavailable`.)

## 8b. Tool decider (Go, optional — parked)

**Parked by ADR-0045**: the loop no longer reads `toolCalling` (the field is
removed), the composition root wires no `ToolDecider`, and the startup gates are
unwired. The package, types, and tests stay in-tree for a future unpark. The
contract below is retained as the seam's definition.

When wired (historically, when a mode set `toolCalling: "router"`), the loop used
the decider; otherwise native tool-calling. Types live in `shared`/`dto` (ADR-0027).

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
  message for `request_tool`, run **one** more writer round (bounded by the
  global `PipelinePolicy.MaxSteps` like any dispatch), and treat that round's
  `stop` stream as the answering phase (state-machine §1.2 `deciding → answering`).
  No error event. A transport error instead emits `error`/`router-unreachable`
  first, then the same bounded writer round — no retry loop.
- The router call is metered as its own `Meter.Attribute` row
  (`model = "needle-router"`) at dispatch time, whether the outcome is confident
  or a refusal (ADR-0028 §5).)

## 8c. Pipeline policy (Go)

The one global turn policy (ADR-0045 §3), loaded from the embedded
`config/pipeline.json`, schema-validated fail-fast at startup with the typed
error `pipeline-invalid` (failure-semantics §2).

```go
type PipelinePolicy struct {
    MaxSteps         int // global dispatch/observe bound (≥ 1)
    MaxHistoryTokens int // assembler history budget (0 drops all)
    MaxRagTokens     int // assembler auto-RAG budget (0 drops all)
    MaxMentionTokens int // assembler mention budget (0 truncates all, labeled)
    AutoRagTopK      int // auto-RAG retrieval depth, every turn (≥ 1)
    Thinking               ThinkingLevel // default thinking policy (ADR-0051 §1)
    MaxThinkingTokens      int           // reasoning-token cap; hitting it labels thinking-truncated (§5)
    ReserveOutputTokens    int           // window-gate output reserve (§6)
    SessionBudgetSoftRatio float64       // soft session-budget threshold ratio (§7)
    Compaction             CompactionPolicy // {Enabled, TriggerHistoryTokens, KeepRecentTurns} (§8)
}

type CompactionPolicy struct {
    Enabled              bool
    TriggerHistoryTokens int
    KeepRecentTurns      int
}

type PipelinePolicyRegistry interface {
    Policy() PipelinePolicy
}
```

The loop holds the registry, passes `Policy()` into `AssemblerInput`, and bounds
its one agentic loop with `MaxSteps`; there is no per-mode policy (ADR-0045).

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
// BlockWrite is one block in a manual whole-tree save (ADR-0038). ID is nil for
// a new block (the engine mints); a changed kind/parent retypes/moves. No hash/guards.
type BlockWrite struct {
    ID       *string
    ParentID *string
    Kind     BlockKind
    Text     string
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
    SaveTree(documentID string, tree []BlockWrite, writeThrough bool) (Revision, error) // manual autosave (ADR-0038); writeThrough mirrors to the opened file (ADR-0039)
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
- `Commit` and `SaveTree` **format** the accepted/edited blocks to the opinionated
  style before persisting.
- **Canonical-content invariant:** blocks are always stored canonical; therefore
  content hashes are stable per revision.

Commit cadence and block identity are ADR-0020 (two paths: AI edit == commit; manual
edit == autosave snapshot; block IDs == stable UUIDs).

`SaveTree` semantics (ADR-0038): the incoming `[]BlockWrite` is a whole-tree
snapshot — array order is position. A block with a nil `ID` is minted; an existing
block absent from the tree is dropped; a changed `Kind`/`ParentID` retypes/moves.
The engine reconciles, normalizes on write and formats on commit, and commits an
`autosave @ <ts>` snapshot iff the tree changed (otherwise it returns the current
HEAD with no new commit). A manual save of a block drops its open candidates.
When `writeThrough` is true (explicit Save / Cmd+S, not the periodic autosave),
the canonical markdown is also mirrored back to the opened file path (ADR-0039);
`Commit` always mirrors when a candidate was applied.

## 9b. Filesystem (Go, leaf)

Read-only filesystem reach bounded by `ALLOWED_ROOTS` (ADR-0035; renamed from
Workspace by ADR-0049 §5; boundary ADR-0049 §6). Source ADR-0035, ADR-0049.

```go
type Entry struct {
    Name  string // bare file/dir name
    Path  string // canonical absolute path
    IsDir bool
}

type Filesystem interface {
    List(ctx context.Context, dir string) ([]Entry, error)
    Read(ctx context.Context, path string, maxBytes int) ([]byte, error)
    AllowedRoots() []string // canonical allowlist (typed refusal payload)
}

func New(allowedRoots []string) (Filesystem, error) // fail-fast; empty = $HOME
```

- `List` is shallow, non-recursive, sorted by name (case-insensitive). Hidden
  entries are returned; filtering for display is client-side presentation.
- `Read` is bounded by `maxBytes` and returns raw bytes only — it never
  registers, versions, or indexes anything. Mentioned-file context (ADR-0036)
  and corpus indexing read through here, so both are provably side-effect-free
  and bounded.
- Every path is canonicalized (`EvalSymlinks` + case-fold, ADR-0047 §2) and must
  lie inside an allowed root; a path outside — including a symlink that resolves
  outside — is refused with the typed `path-outside-allowed-roots`, never
  silently. This holds even with `ENGINE_BIND=0.0.0.0` (ADR-0021, ADR-0049 §6).
- Typed errors: `not-found`, `not-a-directory`, `not-regular`, `too-large`,
  `read-failed`, `path-outside-allowed-roots`. The loop maps them to the mention
  SSE codes (ADR-0036 §2); the API server maps `List` failures to the matching
  typed refusals.

## 9c. Workspace store (Go, leaf)

Owns the global `workspaces.db` registry (ADR-0049 §2/§3/§5): workspace records,
corpus roots/scope, index-job progress, eviction tombstones, and the
turn/session routing index. Source ADR-0049.

```go
type Workspace struct {
    ID        string // UUID, client-facing identity
    Root      string // canonical absolute directory (EvalSymlinks + case-fold)
    Name      string // display label (default: basename of Root)
    CreatedAt int64
    UpdatedAt int64
}

type CorpusScope struct {
    Roots   []string // canonical absolute paths
    Include []string // globs; default **/*.md
    Exclude []string // globs
}

type WorkspaceStore interface {
    ResolveOrCreate(root string) (Workspace, error) // most-specific containing root, else create
    Get(id string) (Workspace, error)
    List() ([]Workspace, error)
    FindContaining(path string) (Workspace, bool, error)
    Scope(workspaceID string) (CorpusScope, error)
    SetScope(workspaceID string, scope CorpusScope) (CorpusScope, error) // idempotent
    // corpus jobs, eviction tombstones, per-path errors, turn/session routing
    // (interface.md §9c; see the Go interface for the full method set)
}
```

- **Workspace ≠ Corpus** (ADR-0049 §1): the root bounds browsing/editing only;
  the corpus is an independent multi-root set that may reach outside it.
- Create-or-resume by canonical root: aliases and symlinks resolve to one
  workspace; opening a nested directory resolves to the most specific existing
  containing workspace (explicit nested creation is deferred).
- A new workspace is seeded with its root as the first corpus root and
  `**/*.md` as the default include (hidden directories are excluded by the
  walker).
- `workspaces.db` is the only global index; per-workspace context state lives in
  the shard (`<data>/workspaces/<id>/{index.db,sessions.db,meter.db}`), opened
  lazily and closed LRU by the composition root's shard Manager.

## 9d. Corpus service (Go)

The engine-owned, index-only retrieval scope over a workspace's multi-root
corpus (ADR-0049 §3/§4). It owns no database: it resolves scope through the
Workspace store, walks it through the Filesystem leaf, indexes through the
shard's Retriever, and surfaces per-document status + job progress. Corpus files
are never documents rows and never versioned — no open, no version, no write.

```go
type CorpusState struct {
    WorkspaceID string
    Roots       []string
    Include     []string
    Exclude     []string
    Documents   []CorpusDocumentStatus // indexed | stale | pending | evicted | error
    Job         *CorpusJob             // latest job (polled via GET /corpus)
}

type Corpus interface {
    Get(ctx context.Context, workspaceID string) (CorpusState, error)
    SetScope(ctx context.Context, workspaceID string, scope CorpusScope) (CorpusState, error) // idempotent; async reconcile
    Index(ctx context.Context, workspaceID string) (CorpusJob, error)                          // async; idempotent per unchanged content
    Evict(ctx context.Context, workspaceID, documentID string) error                           // idempotent
    NotifyChanged(path string) // non-blocking lifecycle hook (Open/Commit/write-through save)
}
```

- **Scope** = roots + include/exclude globs; defaults: the workspace root as the
  first root, `**/*.md`, hidden directories/files excluded. Roots may lie outside
  the workspace root (Workspace ≠ Corpus) but must lie inside `ALLOWED_ROOTS`;
  an outside root is refused with `path-outside-allowed-roots` before anything is
  persisted.
- **Indexing is path-keyed and index-only**: `IndexPath` per file; unchanged
  content is a no-op. A successful index clears the file's eviction tombstone
  and error record. Per-path failures are recorded (`error` status), never
  silent; a job whose every file failed ends `error`.
- **Reconcile** (scope change) indexes the new scope and evicts corpus rows that
  fell out of it; no document file is created, modified, or deleted.
- **Eviction** (`DELETE /corpus/documents/{id}`) records a tombstone and removes
  the file's chunks; the path-derived id is `sha256(canonical path)` prefix
  (interface §3 `ChunkRef`/pathutil.DocID).
- **Jobs** are single-flight per workspace and coalesce while running; progress
  is polled through `GET /corpus` (Phase C pin; no SSE event).
- **Lifecycle**: `DocHook` decorates the Document store so a successful
  Open/Commit/write-through SaveTree enqueues a re-index when the canonical path
  is in any workspace's corpus; the write is never blocked.
- Typed errors: `path-outside-allowed-roots` (403 at the API), workspace
  `not-found`, and per-path index errors surfaced as status.

## 10. Session store (Go, leaf)

Owns a dedicated `sessions.db` — per workspace shard since ADR-0049 §5
(`<data>/workspaces/<id>/sessions.db`). Source ADR-0026, amended by ADR-0049.

```go
type Session struct {
    ID            string   // UUID, client-facing identity
    DocumentID    string
    AnchorBlockID *string  // nil = doc-level chat; set = selection/bubble anchor
    ModeType      string   // persisted per-session persona
    Title         string
    TokenBudget   *int     // optional per-session cumulative-token cap
    ContextPolicy json.RawMessage // persisted session policy JSON (ADR-0049 §8); nil = none
    CreatedAt     int64
    UpdatedAt     int64
}

type SessionStore interface {
    ListByDocument(documentID string) ([]Session, error)
    ListByWorkspace() ([]Session, error) // workspace-scoped by shard
    Create(documentID string, anchorBlockID *string, modeType, title string) (Session, error)
    Resume(id string) (Session, error)          // find-or-open an anchored session
    Rename(id, title string) error              // set the human title (E2); ErrNotFound when unknown
    Append(sessionID string, msg Message) error
    History(sessionID string) ([]Message, error)
    SaveContext(turnID, sessionID string, snapshot json.RawMessage) error // persisted per-turn snapshot
    TurnContext(turnID string) (json.RawMessage, error)                   // ErrNotFound when unknown
    SetContextPolicy(sessionID string, policy json.RawMessage) error      // ADR-0049 §8
    ContextPolicy(sessionID string) (json.RawMessage, error)              // nil when none
}
```

`Resume(id)` (or `Create` on an existing `(document_id, anchor_block_id)` pair)
is create-or-resume: re-anchoring to the same block reopens the same session.
The API server routes a session id to its workspace shard through the
`workspaces.db` routing index; `Session.workspaceId` is populated on responses.
`SaveContext` persists one turn's `ContextSnapshot` (opaque JSON passthrough —
the leaf never parses it) and prunes the session to the newest 100 snapshots in
one transaction (ADR-0044 §4 retention); `TurnContext` reads it back by turn id.
`SetContextPolicy` validates the JSON against `ContextPolicy` (typed
`ErrInvalidContextPolicy`) and persists it (`ErrNotFound` for an unknown
session); `ContextPolicy` reads it back. Both are opaque to the leaf, which
never interprets pins/excludes.

## 11. SSE event bus (Go)

```go
type Event struct {
    TurnID string
    Type   string // turn|token|meter|candidate|diff|rag|context|locate|thinking|done|error|backpressure
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

*Amendment (ADR-0044 §4, Phase C3):* the vocabulary gains `context`. The loop
emits one `context` event after assembly whose payload is the turn's persisted
`ContextSnapshot` (the snapshot itself is the contract; clients never
reconstruct it). Auto-RAG also emits a `rag` event at turn start with the same
`{ok, chunks}` shape as tool retrieval. Clients that do not understand `context`
must ignore it without dropping the stream (labeled, never fatal).

*Phase C4 (ADR-0049 §8) added no new SSE event.* The context tray rides
`Task.context` and `PUT /sessions/{id}/context`; the `rag` event remains the
post-exclude, pre-truncation auto-retrieved set, and pins appear only in the
`context` snapshot (labeled `pinned`/`humanOverride`).


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
- One SQLite file per service *instance* (ADR-0016 as amended by ADR-0049 §5):
  `app.db`/`workspaces.db`/git/worktree are global; Retriever, Session store, and
  Token meter each own a per-workspace shard file. No SQLite file is shared
  across modules, and document identity/git never shard.
- The Fleet gateway is the *only* engine module that may talk to serving — and only
  via the daemon's HTTP contract (ADR-0025).
- The Provider never loads weights, shells out, or resolves names — it only speaks
  REST (ADR-0016).
- The Context assembler and Chunker are pure: same inputs → same outputs (R4).
- The Token metering module owns thinking-token reconciliation; when it tokenizes
  (provider omitted the count), the result is a **labeled approximation** (ADR-0024).
