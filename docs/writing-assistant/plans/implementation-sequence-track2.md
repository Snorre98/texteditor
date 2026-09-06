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

## Sequencing summary

Track 2 splits into an **engine-side phase (E1 — do first, no Rust)** and a
**client-side phase (E2/E6/E7/F6/F7/F8 — gated on the Rust toolchain)**. Locked
decisions: `baseUrl` on `/health` (the "where am I" answer), texteditor owns the
daemon packaging, Rust is deferred until its phase.

| # | Phase | Side | Rust? | Depends on |
|---|---|---|---|---|
| E1 | Deployment primitives (E1+E3+E4+E5) | engine (Go) | no | — |
| F6 | Rust client (`openapi-to-rust`) | client | yes | E1 (spec amended) |
| E2 | Tauri sidecar spawn | client | yes | F8 scaffold |
| F7 | Vue state | client | no (node/bun) | F6 |
| F8 | Tauri 2 + Vue 3 + CodeMirror 6 UI | client | yes | F6/F7 |
| E6/E7 | Web target + capability adapter | client | no | F8 |

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

Prereq: E1's spec amendment is locked (so `openapi-to-rust` consumes the same
spec the Go/TS clients do — ADR-0017 §3).

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

1. **E2 · sidecar spawn** — Rust core spawns the engine binary on launch as a
   Tauri sidecar child process; reads `/health` → `baseUrl` and rewrites the
   client base URL (dynamic-by-default handshake, ADR-0021 §1); `ENGINE_PORT`
   fixed mode preserved. **Stop = SIGTERM, then SIGKILL on timeout**
   (ADR-0021 §1).
2. **F8 · shell** — Tauri 2 (Rust core + system WebView) + Vue 3 + CodeMirror 6;
   no Node at runtime (Bun/Node build-time only — ADR-0013 §2).

## Phase F7 — Client state (Vue)

Vue 3 reactivity over the generated Rust client; reactive patterns transfer from
the TUI's Solid store (`client/tui/src/state/store.ts`, ADR-0023).
Session-scoped state: multiple selection-anchored bubbles + doc-level chat
streaming simultaneously (ADR-0026 §1/§4).

## Phase F8 — Client UI (CodeMirror)

1. Selection popover chat bubble (CodeMirror tooltip API); side-by-side
   candidates via `@codemirror/merge` (ADR-0013 §2).
2. Native file browsing/OS integration in the Rust core; **all edits +
   versioning go through the engine** (ADR-0013 §3).
3. Manual-edit cadence: keystrokes batch into an autosave snapshot on silence
   (10 s / N min), distinct from AI-edit commits (ADR-0020 §1).
4. Session bubbles: create-or-resume anchored to a block; re-selecting a block
   reopens its session (ADR-0026 §1–§3).

## Phase E6/E7 — Web target + capability adapter

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
  `baseUrl` is the only one this phase).
- Boundary tests green at each step (`go test ./...`; `bun test` + `tsc --noEmit`
  in `client/tui`).

## Milestones (from `implementation-sequence-future.md`)

1. E1–E5 → engine ships as standalone daemon *and* Tauri sidecar with
   dynamic-port discovery + localhost bind.
2. E6–E7 → web target + capability adapter.
3. F6–F8 → Tauri editor with selection bubbles, side-by-side candidates, and
   autosave-backed manual editing.
