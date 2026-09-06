# Contributing to texteditor

The dev-facing complement to [`README.md`](../README.md) (which covers only build
+ use). Architecture, ADRs, contracts, and behavior specs live in
[`docs/writing-assistant/`](writing-assistant/architecture.md).

## Layout

```
api/openapi.yaml   the single contract every client codegens from (ADR-0017)
server/            the Go engine (cmd/texteditor + internal/* + shared/dto)
client/tui/        the OpenTUI + Solid client (dumb, generated from the spec)
client/tauri/      the Tauri 2 + Vue 3 editor (dumb; generated Rust client +
                   engine spawned as a bundled sidecar, ADR-0021 §1)
docs/writing-assistant/   architecture, ADRs, contracts, behavior specs
tools/             build + install-daemon scripts
deploy/            launchd agent template (standalone daemon)
```

## Contract-first

Any route/shape change lands in [`api/openapi.yaml`](../api/openapi.yaml)
**first**, then all three codegens re-run in lockstep:

```sh
cd server && go generate ./...                                    # ogen (Go server)
cd client/tui && bun run gen                                      # Hey API + Zod (TS)
cd client/tauri && openapi-to-rust generate -c openapi-to-rust.toml   # Rust client
cd client/tauri && bun run gen                                    # Hey API + Zod (TS)
```

Generated code is committed and never hand-shaped; spec extensions are recorded
amendments in the ADRs (ADR-0002/0017). Streaming: `/turn` is
`x-ogen-raw-response`; clients read the SSE stream and decode each payload with
the generated schema keyed by the SSE `event:` name (ADR-0031 §4).

Pinned toolchain notes:
- TUI: `@hey-api/openapi-ts@0.62.0` (newer `sdk` plugin references an
  unpublished package) and `@hey-api/client-fetch@0.7.2` (last version whose
  `Options` carries `client`).
- Tauri: `openapi-to-rust` v0.15.0.

## Engine flags / env

| Flag | Env | Default | |
|---|---|---|---|
| `--bind` | `ENGINE_BIND` | `127.0.0.1` | `0.0.0.0` opts into LAN exposure (ADR-0021 §2) |
| `--port` | `ENGINE_PORT` | `0` | `0` = dynamic free port; pin it for a stable URL |
| `--daemon` | `DAEMON_URL` | `http://127.0.0.1:9300` | control daemon base URL (ADR-0025) |
| `--cors-origins` | `ENGINE_CORS_ORIGINS` | `""` | comma-separated allowlist (empty = CORS off) |
| `--data` | — | `~/.local/share/texteditor` | SQLite files + git worktrees |

The bound base URL is advertised on `GET /health` (`baseUrl`) so dynamic-port
clients can discover rather than assume (ADR-0021 §1).

## Tests

```sh
cd server && CGO_ENABLED=0 go test ./...        # engine — boundary-tested (ADR-0022 Q5)
cd client/tui && bun test && bun run typecheck  # discovery/decoder/store/component
cd client/tauri && bun test && bun run typecheck
cd client/tauri/src-tauri && cargo test         # sidecar handshake (needs the daemon)
```

The 9 `.feature` behavior specs (`docs/writing-assistant/behaviors/`) are
prose contracts, verified through the boundary tests — there is no Gherkin
runner, and no CI yet (see `docs/writing-assistant/status.md`).

## Tauri client details

- **Sidecar handshake** (`src-tauri/src/sidecar.rs`): spawn the bundled engine
  with `-bind 127.0.0.1 -port 0`, bootstrap the base URL from the startup log
  line, adopt `GET /health` → `baseUrl`, stop = SIGTERM then SIGKILL after 5 s
  (ADR-0021 §1). Headlessly tested against the real engine (`cargo test`).
- **Transport** (ADR-0037): the webview talks to the engine **directly over
  HTTP** (fetch + SSE) — not through the Rust core. The sidecar therefore passes
  `--cors-origins` with the local webview origins when it spawns the engine.
- **Manual edits** (ADR-0038): autosave batches keystrokes into a whole-tree
  `PUT /documents/{id}/tree` snapshot (engine-minted block IDs, silence interval
  ~10 s); AI edits remain separate commits.
- **Capability adapter** (ADR-0014): the single per-target seam — Tauri `invoke`
  (`rfd` dialogs in `src-tauri/src/capability.rs`) vs the Web File System Access
  API (`src/capability/web.ts`).
- **Sidecar binary naming**: `src-tauri/binaries/texteditor-<target-triple>`
  (e.g. `texteditor-aarch64-apple-darwin`); build it from `server/` (see the
  README). Rust toolchain on this Mac lives on the external SSD
  (`RUSTUP_HOME=/Volumes/Ex-SSD/caches/rust`, `CARGO_HOME=/Volumes/Ex-SSD/caches/cargo`).

## Conventions

- **Clients are dumb** — no domain logic outside the engine (ADR-0013 §3).
- **Boundary-tested modules** — every engine module compiles standalone against
  stubs of its dependencies (ADR-0022 Q5).
- **Contract mirrors** — `daemon-http.md`, `fleet-manifest.schema.json`, and
  `needle-facade.md` are mirrors of canonical files in `macos-dev-config`; drift
  tests fail when the sibling checkout is present (ADR-0033 §3).
