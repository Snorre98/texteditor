# Handoff — Plan F (Tauri editor): the shipped client (F7 + F8)

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are

**Plan E is fully landed; F6 (Rust client) and the F8 Tauri shell are landed.**
What remains in Track 2 is the shipped client itself: **F7** (Vue state over the
generated client) and **F8** (the CodeMirror UI — selection bubbles, side-by-side
candidates, autosave). Everything before this was deployment seams; this is the
editor the user actually types in. See
[`implementation-sequence-track2.md`](implementation-sequence-track2.md) for the
phase table; this handoff expands the F-side of it.

### Landed (Plan E + F6 + F8 shell — do not redo)

- **E1/E3/E4/E5 engine primitives** — `--bind`/`ENGINE_BIND`, `--port`/`ENGINE_PORT`
  (dynamic default), `baseUrl` on `/health`, graceful shutdown, standalone daemon
  (`tools/` + `deploy/`).
- **E2 sidecar** — `client/tauri/src-tauri/src/sidecar.rs`: spawn (dynamic port),
  discover (bootstrap from the engine's **stderr** `listening on http://…` line,
  then adopt `/health` → `baseUrl` as authoritative), stop (SIGTERM → 5 s grace →
  SIGKILL). Headlessly tested in `tests/sidecar.rs` against the real engine binary.
- **F6 Rust client** — `client/tauri/src-tauri/src/generated/`, generated from
  `api/openapi.yaml` with `openapi-to-rust v0.15.0`, committed, never hand-shaped.
- **F8 shell** — `client/tauri/src-tauri/src/shell.rs` (behind the `tauri` feature):
  spawns the sidecar on `setup`, exposes `get_engine_base_url`/`pick_directory`/
  `pick_file` via `invoke`, stops the sidecar on `RunEvent::Exit`. Tauri 2 + Vue 3
  scaffold; `cargo check`/`cargo build --features tauri` green.
- **E7 capability adapter** — `client/tauri/src/capability/` (one `CapabilityAdapter`
  interface; Tauri `invoke` → `rfd` dialogs, web → File System Access API).
- **E6 web target** — documented self-hosting caveat, `ENGINE_BIND`/ACL note.

### Settled (do not re-ask)

1. Sidecar discovery: stderr log line *bootstraps*, `/health` → `baseUrl` is the
   authoritative value (recorded in `implementation-sequence-track2.md` E2).
2. Stop = SIGTERM, then SIGKILL after 5 s (matches the engine's `httpSrv.Shutdown`).
3. `openapi-to-rust` models `/turn` as a **raw byte stream** (`start_turn` →
   `bytes::Bytes` with `Accept: text/event-stream`); the SSE vocabulary is *separate*
   payload schemas keyed by the `event:` name — **no typed "event union" client** is
   configured. Whoever consumes the stream must frame-parse `event:`/`data:` and
   deserialize each payload against its per-type schema (TokenEvent/MeterEvent/
   CandidateEvent/DiffEvent/RagEvent/DoneEvent/ErrorEvent/BackpressureEvent) — exactly
   as the TUI's Zod decoders do (ADR-0031 §4).
4. The Tauri editor is a **dumb client** (ADR-0013 §3): native file dialogs live in
   the Rust core, but **all edits and versioning go through the engine**.
5. Rust toolchain lives on the SSD (`RUSTUP_HOME`/`CARGO_HOME` under
   `/Volumes/Ex-SSD/caches/`); the `tauri` feature + `required-features` bin keep the
   generated client + sidecar testable with no WebView.
6. The generated client and frontend are committed; regen only from the spec —
   `openapi-to-rust generate -c client/tauri/openapi-to-rust.toml`.
7. F7 transport = **(a) direct HTTP from the webview** (ADR-0037); the Rust
   generated client's shipped role is `/health` discovery only; the TS client is
   regenerated Hey API + Zod; per-session state is keyed by `sessionId`.
8. F8 manual-edit wire path = **`PUT /documents/{id}/tree`** (ADR-0038), a
   whole-tree snapshot with a dedicated `BlockWrite` schema (`id?` absent = engine
   mints); sync commit-on-receipt, client holds the silence timer; manual save of a
   block drops its open candidates.

## Remaining work (both open decisions recorded — F7 transport, F8 manual-edit route)

### F7 · Vue state over the generated client (ADR-0023, ADR-0013 §2)

Transfer the reactive store from the TUI's Solid store
(`client/tui/src/state/store.ts`) to Vue 3. The TUI store is the reference for
**slices** and **actions**: connection, models (+liveState), modes, tools, document
(+block tree +history), sessions (+messages), and the live turn stream (tokens,
meter, candidate/diff/rag queues, done/error, backpressure) — `createAppStore`
wires the generated client into reactive signals; components render signals, never
call the API directly.

**F7 transport — DECIDED: (a) direct HTTP from the webview** (recorded in
ADR-0037 and `client/tauri/README.md`). The Vue store calls the engine over
`fetch` + `ReadableStream` SSE using the base URL from the E2 handshake; the Rust
core keeps the generated client only for `/health` discovery. Why: ADR-0014's
diagram mandates the Vue frontend talk REST+SSE to the engine with the capability
adapter as the *only* per-target seam — routing every call through
`invoke`/Tauri events (shape (b)) would re-introduce a per-target transport
difference the web target cannot reproduce. Enabler: a new engine CORS policy
(ADR-0037) — the engine serves no `Access-Control-Allow-Origin` today and the
macOS webview cannot disable CORS, so plain `fetch` to the sidecar's dynamic port
is otherwise blocked. The TS client is regenerated Hey API + Zod into
`client/tauri/src/generated/` (mirroring the TUI); `sse.ts` is ported verbatim.

The SSE decoder remains hand-written transport glue (allowed for a dumb client —
not domain logic); per-type decoding is the TUI's **Zod** `safeParse` (ADR-0017
§2), not hand-rolled type-narrowing. Session scoping: multiple selection-anchored
bubbles + one doc-level chat stream simultaneously, one turn per session, sessions
parallel (ADR-0026 §1/§4) — the store keys `{messages, turn}` **per session**
(`sessionStates: Record<sessionId, {messages, turn}>`), not once globally (the
TUI has a single `turn`/`messages` slice; the Vue store generalizes both to a map
keyed by `sessionId`).

### F8 · Client UI (CodeMirror 6) — ADR-0013 §2, ADR-0026

1. **Selection popover chat bubble** via the CodeMirror tooltip API; **side-by-side
   candidates** via `@codemirror/merge` (ADR-0013 §2). A bubble on a selection =
   create-or-resume a session with `AnchorBlockID` = that block; re-selecting the
   same block reopens the same session (ADR-0026 §1–§3). The contract already
   covers this: `POST /sessions {documentId, anchorBlockId, modeType}` is
   "created-or-resumed"; `GET /sessions?documentId=` lists; `GET /sessions/{id}/messages`
   restores history.
2. **Native file browsing/OS integration** in the Rust core (`pick_directory`/
   `pick_file` already exist); feed the returned absolute path to
   `GET /directories?path=` (ADR-0035) and `POST /documents {path}`. All edits +
   versioning go through the engine (ADR-0013 §3).
3. **Manual-edit cadence** — human keystrokes batch into an autosave snapshot on a
   silence interval (10 s / N min), distinct from AI-edit commits (ADR-0020 §1).

   **✅ Contract question — DECIDED: (ii) a new route** (recorded in ADR-0038).
   `ApplyEdit` always stages a candidate (ADR-0029 §1) and cannot express ADR-0020
   §1's "no accept" manual path, so the manual-edit wire path is a **new route**:
   `PUT /documents/{id}/tree` (operationId `saveDocument`) — a whole-tree snapshot
   (`SaveTreeRequest { blocks: BlockWrite[] }`, `BlockWrite { id?, kind, parentId?,
   text }`, array order = position, no `hash`/`guards`), engine reconciles (mint/
   drop/retype/move) + formats + commits `autosave @ ts` iff changed. Cadence: the
   client holds the silence timer and sends one tree per interval; the engine
   commits on receipt (synchronous `Revision`). Manual save of a block drops its
   open candidates. This is a **recorded contract amendment**: `api/openapi.yaml` +
   the three codegens (`go generate ./...` + `bun run gen` + `openapi-to-rust`)
   re-run in lockstep, and ADR-0017 §4's endpoint table + `interface.md` §9's
   `Save` signature are amended accordingly.
4. **Session bubbles** — create-or-resume anchored to a block; re-selecting a block
   reopens its session (ADR-0026 §1–§3).

### Ordering (mandatory)

1. **F7 state decision is settled (a)** — build the Vue store (slices + actions)
   over direct HTTP per ADR-0037; add the engine CORS middleware + the
   `--cors-origins`/`ENGINE_CORS_ORIGINS` knob (sidecar passes the local origins).
2. F7 SSE decoder (hand-framed, per-type schemas) + a per-session turn slice.
3. **F8 manual-edit route is settled (ADR-0038)** — land `PUT /documents/{id}/tree`
   in `api/openapi.yaml` + the three-codegen lockstep first, then CodeMirror 6
   editor shell → selection tooltip bubbles → `@codemirror/merge` candidates → the
   manual-edit cadence.

## Verification gates

- `CGO_ENABLED=0 go test -count=1 ./...` stays green — **except** the manual-edit
  route (ADR-0038) is a **recorded amendment**: `api/openapi.yaml` changes first,
  then the three codegens re-run in lockstep (`go generate ./...` + `bun run gen` +
  `openapi-to-rust generate`), never hand-shaped.
- `client/tauri`: `bun run typecheck` + `bun test` green; `cargo test` +
  `cargo check --features tauri` green (no regression in the sidecar handshake).
- Behavior: `sessions.feature` (create/resume/concurrent/persist/budget) and the
  manual-edit + `versioning.feature` (Q4 revert) scenarios are the F8 acceptance —
  assert as Vue/bun tests where client-side, or as the engine tests the contract
  already covers.
- The shipped client must work end to end: `bun run tauri dev` (with the control
  daemon up) spawns the engine, discovers its base URL, and the editor edits a real
  markdown file through the engine with selection bubbles streaming.

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` +
   `implementation-sequence-future.md` + `implementation-sequence-track2.md`
   (F7/F8 sections; E1–E7/F6/F8-shell landed).
2. `docs/writing-assistant/architecture.md` — arc42.
3. `docs/writing-assistant/contracts/` — `interface.md` (§9 Document store, §10
   Session store), `module-boundaries.md` (Tauri editor row), `data-model.md`.
4. ADRs — normative. **ADR-0013** (dumb clients, CodeMirror affordances),
   **ADR-0023** (reactive patterns → Vue), **ADR-0026** (sessions/bubbles),
   **ADR-0020** (manual-edit cadence, commit paths), **ADR-0017 §3/§6** (Rust
   client, SSE vocabulary), **ADR-0029** (edit verification — guard-failed /
   invalid-structure shape the bubble's accept UI), **ADR-0035/0036** (workspace
   listing + mentions for the `@`-picker), **ADR-0037** (CORS — F7 transport
   enabler), **ADR-0038** (manual-edit route — F8 autosave path).
5. `docs/writing-assistant/behaviors/*.feature` — `sessions.feature`,
   `versioning.feature`, `client-swap.feature`, `edit-integrity.feature`.

## Hard constraints (never violate)

- The Tauri editor is a **dumb client** (ADR-0013 §3): native file I/O in the Rust
  core, but all edits and versioning through the engine; never version or diff in
  the frontend.
- Contract-first (ADR-0002/0017): any new route/shape is a **recorded amendment**
  — spec first, then `go generate ./...` + `bun run gen` + `openapi-to-rust`
  regen, never hand-shaped.
- Single static Go binary (engine) + Rust sidecar; no Node/Python at runtime
  (Bun/Node build-time only).
- Serving only via Fleet → daemon (ADR-0025/0027); `127.0.0.1` bind is the default.
- The sidecar handshake (E2) must keep passing — do not regress `tests/sidecar.rs`.

## Then continue

F7 + F8 complete → Track 2 is done: the three targets (TUI, desktop WebView, web)
run one engine over one contract (ADR-0014) — the frontend-swap guarantee realized.
D1 (router enablement) remains deferred unless a trigger fires.

## Report back

At each milestone: what landed, which tests pass, and any place the docs forced a
stop or a judgment call. Specifically flag: (a) **F7 transport — resolved as (a)
direct HTTP from the webview**, recorded in ADR-0037 + `client/tauri/README.md`;
engine CORS is the enabler (explicit allowlist, no `*`, consumer opt-in), (b) the
**manual-edit wire path — resolved as (ii) a new route**, recorded in ADR-0038:
`PUT /documents/{id}/tree` whole-tree snapshot (`BlockWrite`, engine mints IDs,
sync commit-on-receipt), a recorded contract amendment with three-codegen lockstep,
and (c) how the per-session turn slice was keyed for concurrent bubbles (ADR-0026 §4).