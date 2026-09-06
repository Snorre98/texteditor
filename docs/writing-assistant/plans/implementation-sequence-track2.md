# Track 2 — Deployment & packaging (Plan E) → Tauri editor (Plan F)

The detailed execution plan for Track 2 of the roadmap. Source of authority:
[`implementation-sequence-future.md`](implementation-sequence-future.md) (build
order) and [`implementation-sequence.md`](implementation-sequence.md)
(verification gates), plus the Track 2 ADRs (0014, 0021, 0013, 0017 §3, 0023,
0026, 0031, 0034). This plan references ADRs only as the source of *what* to
build; it decides only the *order* and records implementation choices that the
ADRs left open.

Nothing here contradicts an ADR; where an implementation choice is made it is
called out as a **recorded amendment**, not a silent change.

Handoff docs (fresh-session resumes): [`handoff-plan-e.md`](handoff-plan-e.md)
(the Plan E client half — E2/E6/E7, gated on the Rust toolchain) and
[`handoff-8.md`](handoff-8.md) (the Track-1.5 insertion — ADR-0035/0036).

## Sequencing summary

Track 2 splits into an **engine-side phase (E1 — do first, no Rust)** and a
**client-side phase (E2/E6/E7/F6/F7/F8 — gated on the Rust toolchain)**. Locked
decisions: `baseUrl` on `/health` (the "where am I" answer), texteditor owns the
daemon packaging, Rust is deferred until its phase.

| # | Phase | Side | Rust? | Depends on |
|---|---|---|---|---|
| E1 | Deployment primitives (E1+E3+E4+E5) | engine (Go) | no | — **(landed)** |
| F6 | Rust client (`openapi-to-rust`) | client | yes | E1 (spec amended) **(landed)** |
| E2 | Tauri sidecar spawn | client | yes | F8 scaffold **(landed)** |
| F7 | Vue state | client | no (node/bun) | F6 |
| F8 | Tauri 2 + Vue 3 + CodeMirror 6 UI | client | yes | F6/F7 **(shell landed; UI next)** |
| E6/E7 | Web target + capability adapter | client | no | F8 **(landed)** |

---

## Phase E1 — Engine deployment primitives (E1, E3, E4, E5) — execute first

**Status: landed.** All pure-Go, boundary-tested; unblocks the Rust phases.
Touches `server/cmd/texteditor/main.go`, `server/internal/apiserver/apiserver.go`,
`api/openapi.yaml`, and adds a repo-root `tools/` + `deploy/`.

### 1. Port + bind policy (E3, E5) — `main.go`

- Replace the single `--addr` flag with `--bind` (default `127.0.0.1`) and
  `--port` (default `0` = dynamic), sourced from `ENGINE_BIND` / `ENGINE_PORT`
  (mirror the `envOr` pattern already used for `DAEMON_URL`).
- `ENGINE_BIND=0.0.0.0` is the explicit LAN opt-in (ADR-0021 §2); log a
  prominent "privacy trade-off" warning when it is set. Keep `--data` and
  `--daemon` as-is.
- This is internal flag/config churn, not a spec change — no ADR amendment, but
  note it in the plan/README.

### 2. Dynamic port + graceful shutdown (E3, E2's engine half)

- `net.Listen("tcp", net.JoinHostPort(bind, port))`; when `port == 0`, read the
  OS-assigned port from the listener and log the actual `http://<bind>:<port>`
  base URL. Pass the listener (not an `Addr` string) to `http.Server.Serve`.
- Wire `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`; on signal,
  `httpSrv.Shutdown` with a short timeout and close the four SQLite handles so a
  SIGTERM stops cleanly — this is the engine side of ADR-0021 §1's "stop =
  SIGTERM, then SIGKILL on timeout" (the SIGKILL escalation is the *Rust core's*
  job, not the engine's).

### 3. "where am I" advertisement (E4) — recorded contract amendment

- `api/openapi.yaml`: add an optional `baseUrl: { type: string }` to the `Health`
  schema (additive, backward-compatible).
- Thread the computed base URL into the API server (e.g.
  `apiserver.New(deps, bus, baseURL)`), and return it from `GetHealth`. The
  generated client (`internal/genapi`) is regenerated via `go generate ./...`.
- **Recorded amendment:** append a note to ADR-0017 §4 (the endpoint table's
  `/health` row) and a header comment in the spec: `/health` now advertises the
  actual bound base URL so a dynamic-port client can discover rather than assume
  (ADR-0021 §1). `servers[0]` remains the *fixed-mode* default; dynamic mode
  overrides it via the handshake.

### 4. Standalone-daemon packaging (E1) — texteditor-owned

- Add a repo-root `tools/` mirroring the `macos-dev-config` convention:
  - `tools/build.sh` — `CGO_ENABLED=0 go build -o bin/texteditor ./cmd/texteditor`
    (single static binary, no CGO; ADR-0003).
  - `tools/install-daemon.sh` + a `deploy/com.texteditor.engine.plist` template —
    installs/loads a `KeepAlive` launchd agent that runs the engine with
    `ENGINE_BIND=127.0.0.1` and dynamic port by default (matches the always-on
    daemon agent pattern in `macos-dev-config/launchd/`, but owned here because
    the engine is a consumer app, not the control plane).

### 5. Verification (Q5 + behavior)

- `CGO_ENABLED=0 go test ./...` stays green; add unit tests for: env/flag
  parsing (`ENGINE_PORT`/`ENGINE_BIND`), dynamic-port selection, and `/health`
  returning `baseUrl` (apiserver-level via `httptest`).
- Manual smoke: `go run ./cmd/texteditor` (no port) → picks a free port, logs
  the base URL, `curl /health` shows `{status:"ok", baseUrl:…}`;
  `ENGINE_PORT=9200` → fixed; `SIGTERM` → clean exit.
- Update `implementation-sequence-future.md` + `implementation-sequence.md`
  milestone 1 (E1/E3/E4/E5 landed) per the docs-as-code discipline.

---

## Phase F6 — Rust client API + DTOs (gated: Rust toolchain)

**Status: landed.** Rust toolchain installed under `/Volumes/Ex-SSD/caches/`
(`RUSTUP_HOME`/`CARGO_HOME`, the `macos-dev-config` `tools/dev-cache.sh`
convention); `client/tauri/src-tauri/` scaffolded with a `Cargo.toml` (the
reserved home, ADR-0034 §2); the client generated from `../../api/openapi.yaml`
with **`openapi-to-rust` v0.15.0** and committed (`src-tauri/src/generated/`).
`cargo check` green.

**`openapi-to-rust` fit against the committed spec (recorded, not a spec
change):**

- `x-ogen-raw-response` is ignored; standard `text/event-stream` is read, so
  `POST /turn` generates `start_turn` returning a live byte stream with
  `Accept: text/event-stream` — the `/turn` SSE stream is modeled.
- `Health.baseUrl` → `Health.base_url: Option<String>`; the `servers[0]`
  `http://127.0.0.1:9100` becomes the generated client's default base URL, and
  `HttpClient::with_base_url` is the injection seam the sidecar uses.
- `Task.mentions`, `MeterEvent.mentions` (required), `GET /directories`
  (`list_directory`), and the full SSE vocabulary (`Event`/`EventType` +
  `TokenEvent`/`MeterEvent`/`CandidateEvent`/`DiffEvent`/`RagEvent`/`DoneEvent`/
  `ErrorEvent`/`BackpressureEvent`) are all captured as typed schemas.
- **Spec-shape observation (not a blocker):** the SSE vocabulary is a set of
  *separate* payload schemas keyed by the SSE `event:` name — not a single
  discriminator-tagged union — so the tool's opt-in typed "event union"
  streaming client is not configured. F7 decodes the raw stream against the
  per-type schemas, exactly as the TUI's Zod decoders do (ADR-0031 §4's "typed
  SSE decoders keyed by event name").

1. Install the Rust toolchain (`rustup`, `CARGO_HOME`/`RUSTUP_HOME` on
   `/Volumes/Ex-SSD` to spare the full internal disk — flag the ~1 GB cost).
2. Scaffold `client/tauri/` (the reserved home, ADR-0034 §2) with a `Cargo.toml`;
   generate the Rust client from `../../api/openapi.yaml` with
   **`openapi-to-rust`** (locked now, not deferred — ADR-0017 §3). No ogen-only
   extensions in the spec, so it consumes the exact spec the Go/TS clients do.
3. Verify the generated client models `text/event-stream` as the SSE stream
   (ADR-0031 §4 — the `x-ogen-raw-response` marker is ignored by
   `openapi-to-rust`, which reads standard `text/event-stream`).

---

## Phase E2 + F8 scaffold — Tauri 2 app + sidecar spawn

**Status: landed (scaffold + handshake; CodeMirror UI is F8 proper, later).**
`src-tauri/src/sidecar.rs` (plain tokio + libc, no Tauri dep) implements the
spawn/discovery/stop handshake; `src-tauri/src/shell.rs` (behind the `tauri`
feature) wires it into the Tauri 2 shell: spawn on `setup`, `invoke`-exposed
`get_engine_base_url`, stop on `RunEvent::Exit`. `tests/sidecar.rs` exercises
the acceptance gate headlessly against the real engine binary
(`cargo test` green; `cargo check`/`cargo build --features tauri` green).

**Discovery bootstrap (recorded implementation choice, not an ADR change):**
the engine logs `texteditor listening on http://…` to **stderr** (Go `log`),
so the Rust core bootstraps the base URL from that line, then adopts
`GET /health` → `baseUrl` as the advertised source of truth — ADR-0021 §1's
"reads the chosen port from /health" is the authoritative step; the log line is
only how the core first learns where to probe. Stop = SIGTERM, then SIGKILL
after a 5 s grace (matching the engine's own `httpSrv.Shutdown`); the
SIGTERM → SIGKILL escalation is unit-tested with a SIGTERM-ignoring process.

1. **E2 · sidecar spawn** — Rust core spawns the engine binary on launch as a
   Tauri sidecar child process; reads `/health` → `baseUrl` and rewrites the
   client base URL (dynamic-by-default handshake, ADR-0021 §1); `ENGINE_PORT`
   fixed mode preserved. **Stop = SIGTERM, then SIGKILL on timeout**
   (ADR-0021 §1).
2. **F8 · shell** — Tauri 2 (Rust core + system WebView) + Vue 3 + CodeMirror 6;
   no Node at runtime (Bun/Node build-time only — ADR-0013 §2).

## Phase F7 — Client state (Vue)

**Status: landed.** Vue 3 reactivity over the generated client (`src/state/store.ts`),
mirroring the TUI's Solid store (`client/tui/src/state/store.ts`, ADR-0023).
**Transport (recorded, ADR-0037):** direct HTTP from the webview — a regenerated
Hey API + Zod client (`src/generated/`), a ported `sse.ts` (`src/api/sse.ts`), and
a zod-validated API boundary (`src/api/client.ts`). Session-scoped state: `{messages,
turn}` is keyed per session (`sessionStates: Record<sessionId, …>`, ADR-0026 §1/§4),
so several selection-anchored bubbles plus the doc-level chat stream concurrently.
The enabler is the engine CORS middleware (`--cors-origins`/`ENGINE_CORS_ORIGINS`,
an explicit allowlist, no `*`); the sidecar passes the local webview origins.

## Phase F8 — Client UI (CodeMirror)

**Status: landed.** `src/editor/Editor.vue` (CodeMirror 6, markdown) with:

1. **Selection popover chat bubble** via the CodeMirror tooltip API — create-or-
   resume a session anchored to the selected block; re-selecting the block reopens
   its session (ADR-0026 §1–§3).
2. **Side-by-side candidates** via `@codemirror/merge` (`CandidateMerge.vue`, ADR-0013 §2).
3. **Native file browsing** in the Rust core (`pick_file`/`pick_directory`, E7);
   the returned path feeds `POST /documents`. All edits + versioning through the
   engine (ADR-0013 §3).
4. **Manual-edit cadence** — keystrokes batch into an autosave snapshot on a silence
   interval (`editor/autosave.ts`, 10 s default), distinct from AI-edit commits
   (ADR-0020 §1); the wire path is `PUT /documents/{id}/tree` (ADR-0038).

## Phase E6/E7 — Web target + capability adapter

**Status: landed.** E7: `src/capability/` (frontend) — one `CapabilityAdapter`
interface with a Tauri branch (`invoke("pick_directory")`/`invoke("pick_file")`
→ `src-tauri/src/capability.rs` `rfd` dialogs) and a web branch (File System
Access API, `src/capability/web.ts`); `src/engine.ts` resolves the engine base
URL per target (Tauri `invoke` handshake vs `VITE_ENGINE_URL`/`VITE_ENGINE_PORT`/
spec default) and `/health`-probes it. E6: the web-target caveat is documented
in `client/tauri/README.md` and the web adapter — self-hosting on the user's own
machine/LAN, `ENGINE_BIND=0.0.0.0` opt-in, ACL in macos-dev-config. Frontend
`vue-tsc`/`bun test`/`vite build` green.

- **E7 · capability adapter** — the single per-target seam: Tauri `invoke` IPC
  vs Web File System Access API; all app logic stays in the engine (ADR-0014).
- **E6 · web target** — same Vue UI served locally, engine self-hosted on the
  user's machine/LAN; explicit self-hosting caveat, not a public-infra default
  (ADR-0014, ADR-0021 §2).

---

## Cross-cutting rules (unchanged)

- Engine reached only via the OpenAPI contract; Tauri editor is a **dumb
  client**, generated from the spec (ADR-0013, ADR-0017).
- Serving only via Fleet → daemon (ADR-0025/0027); single static Go binary, no
  CGO.
- Contract-first: route/shape changes surface as **recorded amendments** (E1's
  `baseUrl` is the only one this phase — no spec change in F6/E2/E6/E7).
- Boundary tests green at each step (`go test ./...`; `bun test` + `tsc --noEmit`
  in `client/tui`; `cargo test` + `cargo check --features tauri` in
  `client/tauri/src-tauri`).

## Milestones (from `implementation-sequence-future.md`)

1. E1–E5 → engine ships as standalone daemon *and* Tauri sidecar with
   dynamic-port discovery + localhost bind. **(E1/E3/E4/E5 + E2 landed — the
   sidecar spawn/discovery/stop handshake is built and headlessly tested;
   F6 landed)**
2. E6–E7 → web target + capability adapter. **(landed — one adapter, Tauri
   `invoke` + web File System Access API branches)**
3. F6–F8 → Tauri editor with selection bubbles, side-by-side candidates, and
   autosave-backed manual editing. **(landed — F6, F7, F8, E2, E6, E7 all landed;
   the three targets run one engine over one contract, ADR-0014)**
