# Handoff — Plan E (deployment & packaging): the client half

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are

**Plan E's engine half is landed; the client half is next.** The engine already
ships as a single static binary and behaves as a deployable daemon (E1/E3/E4/E5 —
see below). What remains in Plan E is the **client side**: the Tauri **sidecar**
(E2), the **web target** (E6), and the **capability adapter** (E7). These are
gated on the **Rust toolchain** (not yet installed on this Mac) and are interleaved
with Plan F's Tauri editor — E2 is realized inside the F8 Tauri scaffold, and
E6/E7 are realized inside the same Vue app. See
[`implementation-sequence-track2.md`](implementation-sequence-track2.md) for the
phase table; this handoff expands the E-side of it.

### Landed (engine half of Plan E — `implementation-sequence-track2.md` Phase E1)

- **Port + bind policy (E3/E5, ADR-0021 §1/§2)** — `cmd/texteditor/main.go`:
  `--bind`/`ENGINE_BIND` (default `127.0.0.1`; `0.0.0.0` = LAN opt-in with a
  warning) and `--port`/`ENGINE_PORT` (default `0` = dynamic free port);
  `bindListener` binds before the API server is built and derives `baseURL`.
- **"where am I" (E4, ADR-0021 §1)** — `Health.baseUrl` in `api/openapi.yaml`
  (recorded amendment to ADR-0017 §4); `apiserver.Deps.BaseURL` returns it.
- **Graceful shutdown (E2's engine half, ADR-0021 §1)** — `signal.NotifyContext`
  (SIGTERM/SIGINT) + `httpSrv.Shutdown(5s)`. The SIGKILL escalation is the Rust
  core's job, not the engine's.
- **Standalone daemon (E1, ADR-0014 §2)** — `tools/build.sh`,
  `tools/install-daemon.sh`, `deploy/com.texteditor.engine.plist` (KeepAlive
  launchd agent, fixed localhost port 9100 default).

### Settled (do not re-ask)

1. `baseUrl` lives on `/health`, not a dedicated `/where` route.
2. Daemon packaging lives in **texteditor** (`tools/` + `deploy/`), not
   macos-dev-config.
3. `ENGINE_PORT`/`ENGINE_BIND` are the fixed-mode and LAN-opt-in env knobs; the
   engine default is dynamic (port `0`).
4. The TUI stays **fixed-mode** (its `discovery.ts` resolves `ENGINE_URL` →
   `ENGINE_PORT` → `http://127.0.0.1:9100`); dynamic-port discovery is what the
   **Rust core** does (E2), not the TUI.

## Still open (explicit list; do not silently choose)

### E2 · Tauri sidecar (ADR-0021 §1, ADR-0014) — realized inside the F8 scaffold

The Rust core spawns the engine binary as a **Tauri sidecar** child process on
launch, with the launch-time handshake ADR-0021 §1 pins:

- **Spawn** — sidecar child process on app launch (fully self-contained; the
  engine binary is bundled, not installed system-wide).
- **Port** — dynamic-by-default (no `ENGINE_PORT` → engine picks a free port).
  The Rust core reads the chosen port from `/health` → `baseUrl` and injects the
  actual base URL into the client. Fixed mode (`ENGINE_PORT=<port>`) is preserved
  for remote/web use.
- **Stop** — **SIGTERM, then SIGKILL on timeout** (the engine already exits
  cleanly on SIGTERM; the Rust core escalates).

The spawn/stop/discovery code lives in the Rust core (`client/tauri/src-tauri/`),
so E2 cannot be tested independently of the F8 Tauri scaffold. It is the first
thing built once the Rust toolchain is in.

### F6 · Rust client API + DTOs (prereq for E2/E6/E7 — ADR-0017 §3, ADR-0003)

**Contract prerequisite:** ADR-0035/0036 (`handoff-8.md`, the Track-1.5
insertion) must land *first* — they add `GET /directories` and `mentions` to
`api/openapi.yaml`, and the Rust client is generated from that spec. Running F6
codegen before those amendments land produces a client missing `/directories` and
`mentions` that must be regenerated. The dependency is on the **contract**, not
the TUI (the TUI merely exercises the same amended spec first).

Before the Rust core can read `/health` or drive the engine, generate the Rust
client from the same spec the Go/TS clients consume:

1. Install the Rust toolchain (`rustup`). **Machine note:** the internal disk is
   ~full — set `CARGO_HOME`/`RUSTUP_HOME` under `/Volumes/Ex-SSD/caches/` (the
   macos-dev-config `tools/dev-cache.sh` convention) to spare it (~1 GB cost).
2. Scaffold `client/tauri/` (the reserved home, ADR-0034 §2) with a `Cargo.toml`.
3. Generate the client from `../../api/openapi.yaml` with **`openapi-to-rust`**
   (locked now, not deferred — ADR-0017 §3). The spec models streaming in standard
   OpenAPI (`text/event-stream`), no ogen-only extensions, so the tool consumes the
   exact spec ogen/Hey-API do. Verify it models the `/turn` SSE stream (ADR-0031 §4:
   the `x-ogen-raw-response` marker is ignored; standard `text/event-stream` is read).
4. `tauri-typegen` — deferred; it covers Tauri's internal Rust↔JS IPC, not the Go
   API (ADR-0003).

### E7 · Capability adapter (ADR-0014) — the single per-target seam

The one thing that differs per deployment target is how the UI reaches the
filesystem/OS. The adapter is the only per-target code; all app logic stays in the
engine:

- **Tauri target** — `invoke` IPC into the Rust core for native file
  browsing/OS integration (open/save dialogs, menus). All edits and versioning
  still go through the engine (ADR-0013 §3).
- **Web target** — the **Web File System Access API** (`showDirectoryPicker` /
  file handles) in the browser.

The adapter abstracts "pick a file/directory, read/write its path" behind one
interface; the Vue UI is written once against it (ADR-0014's frontend-swap
guarantee).

### E6 · Web target (ADR-0014, ADR-0021 §2)

The same Vue+CodeMirror UI served from a server, talking to the engine over the
same REST/SSE, self-hosted on the user's own machine/LAN. Explicit caveat:
**self-hosting, not a public-infrastructure default** (ADR-0014 consequence;
ADR-0021 §2 privacy trade-off). Requires `ENGINE_BIND=0.0.0.0` (LAN opt-in) and is
guarded by the Tailscale deny-by-default ACL (ADR-0021 §3) — the ACL lives in
macos-dev-config (`tailscale/acl.hujson`), not texteditor.

### F8 · Tauri shell (interleaved — ADR-0013 §2)

The scaffold E2 lands in: **Tauri 2** (Rust core + system WebView) + **Vue 3** +
**CodeMirror 6**; no Node at runtime (Bun/Node build-time only). F7 (Vue state
over the generated client) and the F8 UI (selection popover bubbles via CodeMirror
tooltips, side-by-side candidates via `@codemirror/merge`, autosave cadence) follow
per [`implementation-sequence-track2.md`](implementation-sequence-track2.md) — the
shipped client. Only the deployment seams (E2/E6/E7) are Plan E's own work.

### Ordering (mandatory)

1. Rust toolchain on the SSD → F6 (generated Rust client, verified against the
   locked spec).
2. E2 + F8 scaffold: Tauri 2 app, sidecar spawn + stop + `/health`→`baseUrl`
   handshake (E2) inside the shell (F8).
3. E7 capability adapter (Tauri `invoke` branch) → E6 web branch (File System
   Access API + `ENGINE_BIND`/ACL note).

## Verification gates

- `CGO_ENABLED=0 go test ./...` stays green (no engine change expected this
  session; if the spec is touched for a discovered gap, that is a **recorded
  contract amendment**, and `go generate ./...` + `bun run gen` must be re-run in
  lockstep).
- E2 acceptance: launching the Tauri app spawns the engine, discovers its
  dynamic-port `baseUrl` via `/health`, and the client talks to it without a
  hardcoded port; quitting sends SIGTERM and the process exits cleanly.
- `serving-control.feature` "TUI switches models", `versioning.feature`,
  `provider-hotswap.feature` — still gated on later phases (unchanged).

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` +
   `implementation-sequence-future.md` (what to build) +
   `implementation-sequence-track2.md` (order; E1 landed).
2. `docs/writing-assistant/architecture.md` — arc42.
3. `docs/writing-assistant/contracts/` — `interface.md` (note the §1 `baseUrl`
   addition), `module-boundaries.md` (Tauri editor row), `daemon-http.md` (mirror).
4. ADRs — normative. Track 2: **ADR-0014** (deployment targets), **ADR-0021**
   (sidecar spawn, bind policy, TLS/ACL), **ADR-0013** (clients), **ADR-0017 §3**
   (openapi-to-rust), **ADR-0023** (reactive patterns), **ADR-0031 §4** (SSE
   modeling), **ADR-0034** (`client/tauri/` home).
5. `docs/writing-assistant/behaviors/*.feature` — Gherkin acceptance contracts.

## Hard constraints (never violate)

- Single static Go binary, no CGO (the engine); the Tauri app is Rust + a bundled
  sidecar, no Node/Python at runtime.
- Serving only via `Fleet → daemon` (ADR-0025/0027).
- Contract-first (ADR-0002/0017): the Rust client is generated from
  `api/openapi.yaml`; never hand-shape it, and never silently extend the spec —
  surface route/shape changes as recorded contract amendments.
- The Tauri editor is a **dumb client** (ADR-0013 §3): native file I/O lives in
  the Rust core, but all edits and versioning go through the engine.
- `127.0.0.1` bind is the default; LAN exposure is an explicit, ACL-gated opt-in
  (ADR-0021 §2/§3), documented as the privacy trade-off.

## Then continue

Plan E complete → Plan F finishes (F7 Vue state, F8 UI) → the three targets (TUI,
desktop WebView, web) run one engine over one contract (ADR-0014). D1 (router
enablement) remains deferred unless a trigger fires.

## Report back

At each milestone: what landed, which tests pass, and any place the docs forced a
stop or a judgment call. Specifically flag: (a) the `openapi-to-rust` fit against
the committed `api/openapi.yaml` (any spec gap the codegen surfaced — especially
the `/turn` SSE stream and `Health.baseUrl`), and (b) the **sidecar stop
handshake** (SIGTERM→SIGKILL timing) once E2 lands.
