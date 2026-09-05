# ADR-0026: Sessions as first-class entities

Status: Accepted

Extends: ADR-0016 (module inventory), ADR-0017 (OpenAPI surface), ADR-0020 (storage).
Supersedes: the `conversation_id` naming and the `messages.db` filename in
ADR-0016/0017/0020 (naming only — the leaf's purpose is extended, not reversed).

## Context

The system needs multiple conversations, scoped per document and, in the full
text editor, per selected text region. Concretely:

- **Multiple sessions per file.** A user may have several distinct conversations
  about one document.
- **Multiple live at once.** Highlighting text should open a chat bubble, and
  several such bubbles (plus a doc-level chat) may stream model output
  simultaneously.
- **Selection-anchored sessions.** A bubble opened on a selection is a *distinct*
  session anchored to that block; re-highlighting the same block reopens the same
  session (create-or-resume).
- **Persisted, resumable.** Sessions survive a client disconnect; the multi-day
  academic workflow relies on reopening a bubble later.

The accepted architecture has a **Conversation store** leaf keying `messages` by
an opaque `conversation_id`, but **no first-class Session entity** — no identity,
anchor, scope, multiplicity, concurrency, or lifecycle. `Task` (ADR-0016) carries
`ModeName`/`DocumentID`/`Selection` but no session; `Run` assumes a standalone
turn. The SSE bus correlates by `turn_id` only. The concurrency contract's "1 in
flight" reads as *global*, which contradicts "multiple live at once."

## Decision

### 1. `Session` is a first-class entity

```go
type Session struct {
    ID            string   // UUID, client-facing identity
    DocumentID    string
    AnchorBlockID *string  // nil = doc-level chat; set = selection/bubble anchored to a block
    ModeType      string   // persisted per-session persona (defaults at Create)
    Title         string   // human label, auto-derived or user-edited
    TokenBudget   *int     // optional per-session cumulative-token cap
    CreatedAt     int64
    UpdatedAt     int64
}
```

- "Highlight text → open a chat bubble" = **create-or-resume a session with
  `AnchorBlockID` = that block**. Re-highlighting the same block reopens the same
  session; a fresh block mints a new one.
- Any number of `Session`s share one `DocumentID`.

### 2. The leaf becomes the `SessionStore`, owning a dedicated `sessions.db`

The former **Conversation store** is renamed **Session store** and owns a
dedicated `sessions.db` (not `messages.db`), with two tables:

```go
type SessionStore interface {
    ListByDocument(documentID string) ([]Session, error)
    Create(documentID string, anchorBlockID *string, modeType string) (Session, error)
    Resume(id string) (Session, error)          // find-or-open an anchored session
    Append(sessionID string, msg Message) error
    History(sessionID string) ([]Message, error)
}
```

`sessions.db` schema:

- **`sessions`** — `id PK`, `document_id`, `anchor_block_id NULL`, `mode_type`,
  `title`, `token_budget NULL`, `created_at`, `updated_at`.
- **`messages`** — `id PK`, `session_id FK → sessions.id` (renamed from
  `conversation_id`), `role`, `content`, `ts`. Many messages per session.

### 3. `Run` is session-scoped

`Task` gains a required `SessionID`. The loop reads the session's `History` into
the context assembler and appends each turn's messages back to the session.

`turnID` remains the per-turn bus correlation (ADR-0016). `meter_events` gains a
`session_id` column (joinable to `turn_id`) so token cost and the session budget
aggregate per session.

### 4. Concurrency — one turn per session, sessions parallel

- **One turn in flight per session; sessions run turns concurrently** in
  independent goroutines, each with its own loop state and SSE subscription.
- The machinery is **built to tolerate arbitrary concurrent turns** (the
  one-turn-per-session rule is a *default*, not an architectural ceiling), so it
  is load-testable with N concurrent sessions.
- The Provider still queues to a `-np 1` server; tool dispatch stays serial
  *within* a session.
- **Document-store contention (two sessions anchored in one document):** edits
  are **queue-serialized**. Edits to a single `documentID` apply in arrival order
  across sessions; a second session's `ApplyEdit` waits, is never rejected, and
  never interleaves mid-block.

### 5. Per-session token budget (a data lever)

`Session.TokenBudget` is optional. The Token metering module — which already owns
attribution — checks cumulative session tokens each turn and surfaces a typed
`session-budget-exceeded` when a turn would cross the cap. This makes the session
budget a *governing lever* visible in the meter, consistent with the system's
whole thesis.

## Consequences

- **+** Sessions are first-class, so the frontend (TUI now; Tauri bubbles later)
  can create, resume, list, and stream multiple conversations per file without
  engine rework.
- **+** Selection-anchored bubbles are stable: re-selecting a block reopens its
  session, giving the "edit this paragraph" affordance a durable identity.
- **+** Token cost is attributable per session, enabling the per-session budget
  and a per-session meter view.
- **−** The Session store gains an entity layer (sessions table + FK), and the
  loop's `Run` is no longer stateless w.r.t. a conversation — it depends on
  `SessionStore.History`/`Append`.
- **−** Concurrent sessions writing one document force an explicit edit-queue
  serialization rule (the single-writer Document store already assumed it, but
  now it is load-bearing across sessions).

## Alternatives considered

- **Keep sessions as an opaque `conversation_id` on the store** — rejected: no
  anchor, no lifecycle, no multiplicity; the selection-bubble requirement is
  inexpressible.
- **Ephemeral, connection-scoped sessions** — rejected: kills resume-across-days
  and reopening a bubble later.
- **One turn globally (sessions serialized)** — rejected: defeats multiple
  simultaneous bubbles.
- **Unbounded parallel without any per-session framing** — rejected: no budgeting
  or attribution slicing; the per-session cap requires session scoping.
- **Fold sessions into `app.db`** — rejected: sessions are a distinct aggregate
  from documents; a dedicated `sessions.db` keeps one-file-per-service symmetry.
- **Reject concurrent edits with `conflict`** — rejected: single-user local app;
  queueing is friendlier and matches the single-writer store.
