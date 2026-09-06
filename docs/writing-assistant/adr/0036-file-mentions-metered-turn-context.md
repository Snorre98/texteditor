# ADR-0036: File mentions — metered, turn-scoped context attachments

Status: Accepted

Extends: ADR-0016 (module inventory), ADR-0017 (OpenAPI surface), ADR-0035
(Workspace capability).

## Context

Typing `@filename` in the chat should attach that file's content to the turn as
**context** (not merely switch the open document). Three constraints shape the
design:

1. **Client-neutrality.** The mention mechanism must work for every client —
   the TUI today, the Tauri editor and web target later — without rework. It
   therefore lives entirely in the engine + contract, reached via codegen
   (ADR-0013, ADR-0017).
2. **The meter sees it.** The context assembler is the pedagogical core (Q1,
   ADR-0011/0016): every token that enters a call is attributed. Mentioned-file
   content that bypassed the breakdown would be an invisible lever — the one
   thing this architecture cannot afford. Mentions become a **metered
   component**.
3. **Read-only.** A mentioned file is reference material for one turn. It must
   not acquire a surrogate id, block UUIDs, or git history — that is what
   `DocumentStore.Open` is for, and only the active document gets it.

The current turn shape (`Task`: sessionId/modeName/documentId/userInput,
ADR-0016 §3; `Task` schema in `api/openapi.yaml`) has no channel for this.
The TUI's `@`-picker is client-side presentation over the ADR-0035 listing —
that part is not this ADR's concern.

## Decision

### 1. `Task` gains `mentions`

```go
type Mention struct {
    Path string // absolute path; resolved by the client (workspace-relative → absolute)
}

type Task struct {
    // ... existing fields ...
    Mentions []Mention // turn-scoped context attachments; capped (see §3)
}
```

- The client sends **absolute** paths (it holds the workspace dir as
  presentation state, ADR-0035 §3). The engine trusts absolute paths exactly
  as `DocumentStore.Open` does — the trust boundary is the bind policy
  (ADR-0021), not path shapes.
- Mentions are **turn-scoped**: they are *not* persisted into session history
  and carry no identity. Persisting per-message mention records is explicitly
  deferred (see Alternatives).

### 2. The loop resolves mentions first, fail-fast

`Loop.Run` reads every mention through `Workspace.Read` before the turn state
machine starts. Failures are **fail-fast, typed, and pre-streaming** — a
missing attachment is never silently dropped:

| Outcome | SSE `error` code |
|---|---|
| path doesn't resolve / not a regular file | `mention-not-found` |
| `Workspace.Read` over the byte cap | `mention-too-large` |
| read I/O failure | `mention-unreadable` |
| more mentions than the count cap | `too-many-mentions` |

Caps: **8 mentions per turn** and a per-mention byte cap (256 KiB) — both
constants in the loop's config, not mode data. A mention's *token* weight is
budgeted by the assembler (§4), its *byte* weight by the cap here.

### 3. The assembler splices mentions as a metered component

```go
type MentionContent struct {
    Path string
    Text string // raw content, truncated by MaxMentionTokens (§4)
}

type AssemblerInput struct {
    // ... existing fields ...
    Mentions []MentionContent
}

type Breakdown struct { // deterministic approximation, documented unit
    // ... existing components ...
    Mentions int
}
```

- Spliced **after history, before user input**, in mention order, each wrapped
  with a path marker line so the model can cite it and clients can show
  provenance — the same discipline as RAG source markers.
- The assembler remains pure (ADR-0016 §6): the loop reads, the assembler
  lays out.

### 4. `ContextBudget` gains `MaxMentionTokens`

```go
type ContextBudget struct{ MaxHistoryTokens, MaxRagTokens, MaxMentionTokens int }
```

- A mode-level data tunable (ADR-0019: modes are data) — the mention lever is
  per-mode visible and editable, like RAG. `0` means "no mention budget" and
  truncates all mention content.
- Over-budget mentions truncate **from the tail** (last mention first) and the
  overflow is reported as a labeled overflow line (failure-semantics §4),
  never folded silently.

### 5. The meter carries the component end to end

- `AttributedBreakdown` (interface.md §6) gains `Mentions`.
- `meter_events.component` enum gains `mentions` (data-model §1.3).
- The `meter` SSE event gains a required `mentions` field; the components sum
  to the scaled provider totals exactly (Q1). This is a recorded amendment to
  ADR-0017 §6, like the earlier `rag` addition.
- The `Task`/`MeterEvent` schema changes land in `api/openapi.yaml` at
  implementation, in lockstep with `internal/genapi` and `bun run gen`
  regeneration (handoff-6: no silent spec extension).

### 6. Mentioned files are never documents

`Workspace.Read` is the only touch point: no `DocumentStore.Open`, no `app.db`
row, no block IDs, no git history for mentioned files. The active document is
the only versioned entity in a turn.

## Consequences

- **+** `@filename` = attach-as-context, metered and visible in the live meter
  — the pedagogical loop extends to a new lever (Q1).
- **+** Client-neutral by construction: the Tauri editor and web target get
  mentions from codegen with zero extra work (ADR-0017).
- **+** Read-only guarantee is structural (ADR-0035's Workspace), not
  procedural.
- **−** The turn request and meter wire shapes change: a recorded contract
  amendment; existing SSE readers must accept the new `mentions` field.
- **−** The loop gains a new dependency (`Loop → Workspace`), recorded in the
  module graph.

## Alternatives considered

- **Mention = open/switch the document** — rejected: the user wants context
  attachment, not navigation; switching is already `POST /documents`.
- **Persist mentions in session history** (`SessionStore.Append` with a
  mentions field) — deferred, not rejected: it is the honest long-term shape
  (reopening a session should show what was attached), but it touches the
  session schema, history rendering, and every client at once. Turn-scoped
  mentions ship first; a follow-up ADR can make them persistent.
- **No new meter component** (fold mention text into `user`) — rejected:
  silently merges a lever into the user component; Q1 demands the breakdown
  show what mentions cost.
- **Engine resolves workspace-relative paths** — rejected: the engine is
  workspace-stateless (ADR-0035 §3); absolute paths keep `POST /documents`
  and mentions on one trust model.
