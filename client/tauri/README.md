# texteditor-tauri — the Tauri editor client (Plan F / Plan E client half)

A **dumb client** for the writing-assistant engine (ADR-0013 §3): the whole API
surface is generated from [`api/openapi.yaml`](../../api/openapi.yaml), and the
engine is bundled and spawned as a **Tauri sidecar** on launch (ADR-0021 §1,
ADR-0014). Tauri 2 (Rust core + system WebView) + Vue 3 + CodeMirror 6; no Node
at runtime (Bun/Node build-time only, ADR-0013 §2).

## Layout

```
openapi-to-rust.toml   F6 codegen config (input: ../../api/openapi.yaml)
src-tauri/             the Rust core
  Cargo.toml           crate texteditor-tauri (feature "tauri" gates the shell)
  src/generated/       F6 — Rust client generated with openapi-to-rust (committed)
  src/sidecar.rs       E2 — engine spawn/discovery/stop handshake (tokio + libc)
  src/capability.rs    E7 — native-dialog invoke commands (behind "tauri")
  src/main.rs          E2+F8 — Tauri shell: spawn sidecar on launch, stop on exit
  tests/sidecar.rs     E2 acceptance (spawns the real engine, asserts the handshake)
src/                   the Vue 3 + CodeMirror frontend (F7/F8)
  generated/           F7 — Hey API + Zod client, regenerated (mirrors the TUI)
  sse.ts               F7 — hand-framed /turn SSE decoder (port of the TUI's)
  capability/          E7 adapter — one interface, Tauri invoke + web FS Access API
```

## Prerequisites

- **Rust** (rustup). This Mac keeps `RUSTUP_HOME`/`CARGO_HOME` on the external
  SSD to spare the internal disk:
  `RUSTUP_HOME=/Volumes/Ex-SSD/caches/rust CARGO_HOME=/Volumes/Ex-SSD/caches/cargo`
  (the `macos-dev-config` `tools/dev-cache.sh` convention; ~1 GB cost).
- **`openapi-to-rust`** — `cargo install --locked openapi-to-rust` (v0.15.0).
- **Go** (to build the bundled engine sidecar) and **Bun** (frontend build only).
- The **control daemon** (`macos-dev-config` `cmd/fleetdaemon` on `:9300`) — the
  engine fails fast at startup when it is unreachable (ADR-0025).

## Codegen (F6, ADR-0017 §3)

From `client/tauri/`:

```sh
openapi-to-rust generate -c openapi-to-rust.toml
```

The generated client is committed and never hand-shaped (`src-tauri/src/generated/`).
It consumes the exact spec the Go (`ogen`) and TS (Hey API + Zod) clients do.

**Streaming fit (recorded):** the generator reads standard `text/event-stream`
(ignoring the `x-ogen-raw-response` marker, ADR-0031 §4) and emits `start_turn`
as a live byte stream. The SSE event vocabulary is a set of *separate* payload
schemas keyed by the SSE `event:` name — not a single discriminator-tagged union —
so the tool's opt-in typed "event union" client does not apply; the F7 client
decodes the raw stream against the per-type schemas (`TokenEvent`/`MeterEvent`/
…), exactly as the TUI's Zod decoders do.

## F7 transport decision (recorded, ADR-0037)

The webview talks to the engine **directly over HTTP** — `fetch` +
`ReadableStream` SSE — not through the Rust core. This is the ADR-0014 shape: the
Vue+CodeMirror frontend does REST+SSE to the engine, and the capability adapter is
the *only* per-target seam. (Routing every call through `invoke`/Tauri events would
be a second per-target transport difference the web target cannot reproduce.)

- **Rust core** — the generated client (`src-tauri/src/generated/`) is used for
  `/health` discovery only (`sidecar.rs`); the frontend is the real client.
- **Frontend** — a regenerated Hey API + Zod client (`src/generated/`) plus a
  ported `sse.ts` (hand-framed decoder), mirroring the TUI exactly (ADR-0017 §2,
  ADR-0031 §4).
- **CORS** — the engine must serve `Access-Control-Allow-Origin` for this to work
  (the macOS webview cannot disable CORS, and the engine serves none today). The
  sidecar passes `--cors-origins` with the local webview origins when it spawns the
  engine; ADR-0037 defines the allowlist and the OPTIONS-preflight handling.

State is keyed per session — `sessionStates: Record<sessionId, {messages, turn}>` —
so several selection-anchored bubbles plus the doc-level chat stream concurrently
(ADR-0026 §1/§4); the TUI's single `turn`/`messages` slice is generalized to that
map.

## Sidecar handshake (E2, ADR-0021 §1)

`src-tauri/src/sidecar.rs`:

1. **Spawn** — the bundled engine binary as a child, `-bind 127.0.0.1 -port 0`
   (dynamic free port by default; fixed via `-port <n>`).
2. **Discover** — bootstrap the base URL from the engine's startup log line
   (`texteditor listening on http://…`, emitted to **stderr** by Go `log`), then
   adopt `GET /health` → `baseUrl` as the advertised source of truth.
3. **Stop** — SIGTERM, then SIGKILL after a 5 s grace (matching the engine's own
   `httpSrv.Shutdown` timeout); a `Drop` net SIGTERMs a handle that was never
   stopped.

The handshake is testable headlessly — no WebView needed:

```sh
# from client/tauri/src-tauri — with the control daemon running on :9300
cargo test            # spawn → discover dynamic port → /health → SIGTERM clean exit
                      # + SIGTERM-ignoring process → SIGKILL escalation
```

## Capability adapter (E7, ADR-0014)

The single per-target seam is filesystem/OS reach. The frontend is written once
against `CapabilityAdapter` (`src/capability/adapter.ts`):

- **Tauri target** — `invoke("pick_directory")` / `invoke("pick_file")` into the
  Rust core (`src-tauri/src/capability.rs`, `rfd` native dialogs), returning an
  absolute path that is then handed to the engine (`GET /directories`,
  `POST /documents`). All edits + versioning go through the engine (ADR-0013 §3).
- **Web target** — the Web File System Access API (`showDirectoryPicker` /
  `showOpenFilePicker`) in the browser (`src/capability/web.ts`); no Rust.

## Web target (E6, ADR-0014, ADR-0021 §2)

The same Vue UI served from a local server, talking to the engine over the same
REST/SSE. Explicit caveat: **self-hosting on the user's own machine/LAN, not a
public-infrastructure default** (the privacy trade-off). It requires
`ENGINE_BIND=0.0.0.0` (LAN opt-in) and is guarded by the Tailscale
deny-by-default ACL in `macos-dev-config` (`tailscale/acl.hujson`, ADR-0021 §3) —
that ACL lives in macos-dev-config, not here. Cross-origin `fetch` from the served
UI also requires the engine's CORS allowlist to include the serving origin
(`ENGINE_CORS_ORIGINS`, ADR-0037).

## Build (Tauri shell, F8)

```sh
# from client/tauri
bun install
bun run tauri build      # bundles bin/texteditor-<target-triple> as the sidecar
```

The engine sidecar binary must be named `texteditor-<target-triple>` (e.g.
`texteditor-aarch64-apple-darwin`) under `src-tauri/binaries/` (ADR-0021 §1
"bundled, not installed system-wide"); `tools/build.sh` at the repo root builds
`bin/texteditor` for the standalone daemon.
