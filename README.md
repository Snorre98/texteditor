# texteditor

A local-first writing assistant: a single static Go **engine** (REST/SSE) plus a
terminal **TUI** client. Every token that goes into a model call is metered and
visible; all domain logic lives in the engine, and the TUI is a dumb,
spec-generated client (ADR-0013 §3).

Architecture, ADRs, contracts, and behavior specs live in
[`docs/writing-assistant/`](docs/writing-assistant/architecture.md). The engine
is reached *only* through the OpenAPI contract [`api/openapi.yaml`](api/openapi.yaml);
serving is controlled by `macos-dev-config`'s control daemon, which the engine's
Fleet gateway talks to over HTTP (ADR-0025/0027).

## Layout

```
api/openapi.yaml   the single contract every client codegens from (ADR-0017)
server/            the Go engine (cmd/texteditor + internal/* + shared/dto)
client/tui/        the OpenTUI + Solid client (dumb, generated from the spec)
docs/writing-assistant/   architecture, ADRs, contracts, behavior specs
tools/             build + install-daemon scripts
deploy/            launchd agent template (standalone daemon)
```

## Prerequisites

- **Go** 1.26 (engine; no CGO).
- **Bun** (TUI build/run only — no Node at runtime).
- **`macos-dev-config`** (separate repo) running the **control daemon** — the
  engine reaches serving through it and never reads `models.json` directly
  (ADR-0025/0027).

## Quick start — run the TUI

From the repo root:

```sh
# 1. control daemon (separate repo) — serves the fleet over HTTP on :9300
cd ../macos-dev-config && go run ./cmd/fleetdaemon

# 2. engine — binds 127.0.0.1 (dynamic port by default)
cd ../texteditor/server && go run ./cmd/texteditor
# (log line prints the actual base URL; --port 9100 pins it)

# 3. TUI (new terminal)
cd ../texteditor/client/tui
bun install
bun run gen          # regenerate src/generated from ../../api/openapi.yaml
bun run src/index.tsx /path/to/note.md
```

## Engine flags / env

| Flag | Env | Default | |
|---|---|---|---|
| `--bind` | `ENGINE_BIND` | `127.0.0.1` | `0.0.0.0` opts into LAN exposure (ADR-0021 §2) |
| `--port` | `ENGINE_PORT` | `0` | `0` = dynamic free port; pin it for a stable URL |
| `--daemon` | `DAEMON_URL` | `http://127.0.0.1:9300` | control daemon base URL (ADR-0025) |
| `--data` | — | `~/.local/share/texteditor` | SQLite files + git worktrees |

The bound base URL is advertised on `GET /health` (`baseUrl`) so clients can
discover it rather than assume (ADR-0021 §1).

## TUI

The TUI is a **dumb client**: its whole surface is generated from the contract
(Hey API + Zod), every response is zod-validated at the boundary, and all domain
logic lives in the engine. See [`client/tui/README.md`](client/tui/README.md) for
the codegen notes and full layout.

Connection / discovery (fixed mode — `client/tui/src/api/discovery.ts`):

1. `ENGINE_URL` — full base URL override (web/LAN).
2. `ENGINE_PORT` — a fixed port on 127.0.0.1.
3. the spec's `servers[0]` default `http://127.0.0.1:9100`.

…then verified against `/health`; an unreachable engine is an explicit error
screen, never a silent failure.

Open a document with a path argument or `TEXTEDITOR_DOC`:

```sh
bun run src/index.tsx /path/to/note.md
TEXTEDITOR_DOC=/path/to/note.md bun run src/index.tsx
ENGINE_URL=http://127.0.0.1:9123 bun run src/index.tsx /path/to/note.md
```

### Panels

Markdown editor · chat (session bubbles + live stream) · live token meter ·
model/mode switcher · RAG results · diff preview.

### Keys

- `esc` — quit
- `a` — accept the staged candidate (diff preview: apply + commit through the engine)
- chat input `enter` — submit a turn (`POST /turn`, SSE stream)

### TUI scripts (`client/tui/`)

```sh
bun install && bun run gen   # install + regenerate from the spec
bun test                     # discovery/decoder/store/component tests
bun run typecheck            # tsc --noEmit
bun run src/index.tsx        # run
```

## Standalone daemon (optional)

Ship the engine as a per-user launchd agent (KeepAlive) on a fixed port 9100:

```sh
./tools/build.sh           # -> bin/texteditor (single static binary, no CGO)
ENGINE_PORT=9100 ./tools/install-daemon.sh
```

See [`tools/install-daemon.sh`](tools/install-daemon.sh) and
[`deploy/com.texteditor.engine.plist`](deploy/com.texteditor.engine.plist).

## Tests

```sh
cd server && CGO_ENABLED=0 go test ./...     # engine (boundary-tested; ADR-0022 Q5)
cd client/tui && bun test && bun run typecheck
```

## Contract-first

Any route/shape change lands in [`api/openapi.yaml`](api/openapi.yaml) **first**,
then `go generate ./...` (ogen server) and `bun run gen` (TS client) are
regenerated from it. The generated code is committed and never hand-shaped; spec
extensions are recorded amendments in the ADRs (ADR-0002/0017).
