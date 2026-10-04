# ADR-0052: Engine-owned context lifecycle and liveness feed

Status: Accepted

Extends: ADR-0002/ADR-0013 §3 (dumb clients — completes them), ADR-0031
(typed, hand-framed SSE), ADR-0040 (recorded note: fleet orchestration
engine-side), ADR-0047 (approve is the only write boundary), ADR-0049
(workspace/corpus/session scoping).

Amends: ADR-0046 §4 (async bridge — the client stops sequencing), §5 (UI
surface — bootstrap/accept move engine-side), §8 (fleet rendering only), §9
(parity checklist gains lifecycle + feed).

Bounded by: ADR-0017 (every surface lands in `api/openapi.yaml` before client
code). Depends on: Phase E3 (engine-side model orchestration).

## Context

The engine owns *data* (documents, sessions, workspaces, corpus, meters) and
*routing* (id→shard: `workspace.RouteSession`/`RouteTurn`,
`server/internal/workspace/workspace.go:487-538`), but it deliberately left
*selection*, *sequencing*, and *liveness* to clients. The Ratatui TUI (ADR-0046)
is now the only active client, and the cost of those three is visible in its
bridge:

- **No engine `open` flow.** `POST /documents` is internally compound (Open +
  Blocks) but returns only `rootBlockId` (`server/internal/apiserver/apiserver.go:391-410`),
  so the client choreographs: list-directory probe → fall back to open →
  derive the workspace root with `Path::parent()` string arithmetic →
  `create_workspace` → `list_modes` → `load_blocks` → resume/create session
  (`client/tui-rs/src/bridge.rs:248-336`). The engine already owns every
  primitive (`apiserver.resolveWorkspaceFor`,
  `server/internal/apiserver/apiserver.go:559-587`; `loop.resolveWorkspaceID`,
  `server/internal/loop/loop.go:202-226`); only the composition is client-side.
- **Session choice is client policy.** The engine exposes a newest-first list
  (`session.ListByWorkspace`/`ListByDocument`,
  `server/internal/session/session.go:204-221`) and anchor-keyed
  create-or-resume (`:99-127`) but never selects. The TUI takes
  `sessions.into_iter().next()` (`client/tui-rs/src/bridge.rs:374-401`);
  the frozen OpenTUI and Tauri copies do the same (`sessions[0]`).
- **Approve is three routes.** `get_candidates` → `apply_edit` → `commit`
  (`client/tui-rs/src/bridge.rs:704-752`), with the client **relaying the
  candidate text back** through `POST /edits` even though the engine has it
  staged — a redundant round-trip and a trust/race surface the write boundary
  (ADR-0047) should not have.
- **No push but the turn.** `StartTurn` subscribes filtered to one `turnID`
  (`server/internal/apiserver/apiserver.go:934`); the bus itself can subscribe
  unfiltered (`server/internal/eventbus/eventbus.go:64`) but no route exposes
  it. Corpus progress is poll-only (`contracts/interface.md:1066`; the TUI polls
  `GET /corpus`, `client/tui-rs/src/ui.rs:56-75`); fleet is client-polled;
  external disk change is detected only on the next `Open`
  (`server/internal/document/store.go:293-306`).
- **The duplication proves the point.** Every one of the above is copied across
  the TUI, OpenTUI, and Tauri clients. Logic duplicated in three clients belongs
  in the engine.

The anti-pattern this ADR corrects is a client that is *dumb about data* but
*surprisingly smart about flow*: it holds pointer state (open document / session
/ workspace) and orchestrates compound operations. That is not "render and send
commands" (ADR-0013 §3); it is domain sequencing wearing a render-only hat.

Forces:

- One active client (single user, local-first). There is no client identity in
  the engine, and none is needed: idempotent open-or-resume verbs remove the
  client's *policy* without introducing server-side cursor state.
- The `/turns/{id}/locate` and `/turns/{id}/cancel` pattern already proves the
  shape: one verb returns `204`, the outcome arrives on the existing stream
  (`server/internal/apiserver/apiserver.go:743-792`).
- Failures degrade with a label, never silently (Q1, ADR-0022). A dropped feed
  event is labeled; a refused path is the typed `path-outside-allowed-roots`.
- Contract-first (ADR-0017): the exact wire shapes below are intent and land in
  `api/openapi.yaml` before client code. Rust codegen regenerates for the TUI;
  Tauri/TS trees stay untouched while frozen (ADR-0044).
- The control daemon is external (ADR-0025), so a fleet *push* requires the
  engine to poll the daemon and emit on change — no daemon webhook exists.

## Decision

### 1. `POST /open` — engine-owned bootstrap resolver

One verb resolves a path into everything a client needs to start:

- path is a directory → `{kind: "directory", listing, workspace}` (bounded
  listing, `ALLOWED_ROOTS`, typed refusal);
- path is a file → `{kind: "document", document, blocks, workspace, session,
  modes}`.

The engine opens/resolves the workspace, opens the document, loads the block
tree, opens-or-resumes the session (§2), and returns the presets. The client
makes one call and renders; it performs no path arithmetic and no fallback
probing. `POST /documents` and `GET /directories` remain for composability and
backward compatibility.

### 2. `POST /documents/{id}/session` — open-or-resume

Given `{anchorBlockId?}`, the engine returns the session to use: the anchor-keyed
session when an anchor is given, otherwise the document's most recently updated
session, creating one if none exists. The "newest" policy and the
create-or-resume semantics are engine data, not client convention. The client no
longer lists sessions and picks `[0]`.

### 3. `POST /documents/{id}/blocks/{bid}/accept` — atomic approve

The approve write boundary becomes one server-side operation:
stage-if-needed → re-validate the base hash → format → git commit → write-through
(the existing sequence in `server/internal/document/store.go:647-848`). The
request carries only `{overwrite?}`; the client **never supplies candidate
text**. The response is the `Revision` (with `writtenThrough`/`path`) or the
typed `file-changed-externally` 409, unchanged from ADR-0047. `GET
.../candidates`, `POST /edits`, and `POST /commits` remain for the staged path
and compatibility.

### 4. `GET /events` — engine liveness feed

A long-lived SSE feed carries non-turn events so clients stop polling:

| Event | Producer | Replaces |
|---|---|---|
| `corpus` (job start/progress/done) | corpus job loop (`server/internal/corpus/corpus.go:236-320`) | 1 s `GET /corpus` poll |
| `document` (external-change / commit) | the `DocHook` (`server/internal/corpus/corpus.go:510-543`) and the open/commit path | lazy external-change detection on `Open` |
| `session` (create / rename) | the session store / session routes | post-mutation refetch |
| `fleet` (live-state change) | an engine-side bounded `ListStatus` poller emitting on change | 10 s `GET /fleet` poll |

The feed is bounded and drops are labeled (`backpressure`), reusing the bus
contract. The per-turn `/turn` stream is unchanged and coexists; a client may
hold both. An optional `?workspaceId=` filter is reserved for future use.

### 5. The client returns to render-only

With §1–§4, the TUI's bridge sheds bootstrap sequencing, session selection,
approve choreography, and corpus/fleet polling. What remains is: send a verb,
render the response, reduce typed events. UI state — overlays, selections,
input buffers, pane visibility — correctly stays client-side; it is
presentation, not application state.

### 6. No server-side active cursor

A per-client "active document/session/workspace" is **rejected** for the current
single-client topology: it needs client identity the engine does not model, and
idempotent open-or-resume (§1–§2) already removes the client's policy. Recorded
as a deferred note gated on a real multi-client requirement.

### 7. Contract surface (OpenAPI-first, additive)

Recorded as intent; exact names and shapes land in `api/openapi.yaml` at
implementation (ADR-0017), with no breaking changes. `behaviors/client-swap.feature`,
`behaviors/context-management.feature`, and `behaviors/fleet-observability.feature`
are normative; tests follow.

## Consequences

- **+** The client is genuinely render-only: one verb and typed events, with no
  selection policy, path arithmetic, or multi-call choreography — duplicated
  logic collapses to one engine path shared by every future client.
- **+** Approve is atomic and trustworthy: the write boundary no longer trusts
  client-supplied candidate text, closing a race the staged/commit split
  allowed.
- **+** Liveness replaces polling: corpus and fleet updates arrive as typed
  events, so the TUI needs no poll loops and external changes can surface
  without a re-open.
- **−** Net-new contract and engine work: a lifecycle service (or handlers), a
  second SSE route with framing and backpressure, and producers in corpus,
  document, session, and fleet.
- **−** Fleet push requires an engine-side daemon poller because the daemon is
  external (ADR-0025); this is small but real and is the one feed producer that
  is not a direct in-process emit.
- **−** Doc/governance churn: ADR-0046 §4/§5/§8/§9 amended; the three behavior
  features, `architecture.md`, `traceability.md`, `status.md`, and the active
  plan gain the phase.
- **−** Two SSE routes coexist; clients must not confuse the turn stream with
  the feed (documented in the contract).

## Alternatives considered

- **Extend `POST /documents` with a bootstrap mode** instead of `POST /open` —
  kept as a pin-at-implementation variant: no new path, but it overloads a route
  whose single-job contract ("open a document") is currently clean. `/open` is
  the recommended shape.
- **A server-side "active session/document" cursor** — rejected (§6): needs
  client identity; not justified by the single-client topology.
- **Keep client-side approve** — rejected: the client relays text the engine
  already holds; atomic accept removes the trust/race surface and the extra
  round-trip.
- **WebSocket for the feed** — rejected: SSE is the established, typed,
  hand-framed transport (ADR-0031); the feed is server-push only.
- **Fold liveness into `/turn`** — rejected: `/turn` is one turn per connection,
  filtered by `turnID`; non-turn events must not depend on an active turn.
- **Client polling as the only liveness mechanism (status quo)** — rejected:
  duplicated per client, latency-bound, and blind to changes between polls.

## Deferred / recorded notes

- **File watcher for proactive `document` events**: the feed surfaces an
  external change once detected; a proactive `fsnotify` watch that emits without
  an intervening `Open` is a later refinement, not required by this ADR.
- **`/events` filter granularity** (`?workspaceId=`, event-type subscription) is
  reserved but unused until a second client exists.
- **Model-switch verb**: the single start-new→stop-old orchestration is E3's
  concern (ADR-0040 recorded note); this ADR only guarantees the client issues
  raw lifecycle verbs and renders `fleet` from the feed.
- **OpenTUI retirement**: freezing remains (ADR-0046 §9); the feed and lifecycle
  verbs are TUI-only until a parity/freeze decision changes that.
- **Tauri/TS regeneration**: the frozen trees are not regenerated for these
  additive routes; a Tauri unfreeze (ADR-0044 note) must regenerate and may
  adopt `/open` and `/events`.
