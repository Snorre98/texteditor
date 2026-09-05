# Handoff — resume implementation (next session)

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are

A5 (controller layer), the A3→B resolution, and the control-daemon decision are
**complete**. Two things changed since the last handoff: (1) the controller layer
landed and is tested; (2) **ADR-0032 resolved the control-daemon question** — the
previous "where does the daemon live" ambiguity is now a pinned decision, so
**Plan B is the central focus of this session** with a known approach rather than
an open question.

### Landed + tested (A5 / A3→B)

- **A5.1 SSE event bus** — `internal/eventbus/` (`Emit`/`Subscribe`, bounded-256,
  drop-oldest + single labeled `backpressure` event; non-blocking `Emit`).
- **Fleet daemon client** — `internal/fleet/fleet.go` ships a real HTTP client
  (`NewDaemon`/`NewDaemonWithClient`) over the pinned `contracts/daemon-http.md`
  verb contract. `Resolve` carries merge + capability gates + fallback ladder over
  daemon-returned data (an unexported `daemonEntry` holds daemon-owned `defaults`,
  so they never leak into the public `dto.Model`).
- **A5.2 API server** — `api/openapi.yaml` (full ADR-0017 route table + event
  vocabulary; `/turn` marked `x-ogen-raw-response`) → ogen generates
  `internal/genapi`; `internal/apiserver` implements the typed `Handler` +
  `RawHandler`, hand-framing `/turn` SSE via `http.ResponseController` (turnID
  correlation, done/error terminal).
- **Composition root** — `cmd/texteditor/main.go` wires every store/registry/loop
  to the real gateways; fail-fast on `tool-has-no-handler` and `daemon-unreachable`;
  `tool.Load` + `document.Migrate`/`session.Migrate`. Static `CGO_ENABLED=0`
  binary builds and runs.

### Contract-gap resolutions (settled — do not re-ask)

1. **Provider messages** — resolved via pure `dto.Request` (`shared/dto/request.go`);
   `Chat(ctx, target, req)`/`Stream(ctx, target, req, emit)`; the Provider renders
   the OpenAI wire format; the assembler builds the full request. `interface.md`
   §2/§5, `module-boundaries.md`, `ADR-0027 §2` amended to match.
2. **Meter signature** — already committed (`interface.md §6` == `meter.go`).
3. **Retriever block source** — `BlockReader` seam documented, satisfied by the
   Document store at the composition root.

### Control daemon — RESOLVED this session (ADR-0032, do not re-ask)

ADR-0032 (`adr/0032-control-daemon-source-location.md`) pinned every previously
open serving-control decision. Read it; do not relitigate:

1. **Source lives in `texteditor`** as a second binary: `cmd/fleetdaemon/` (module
   `texteditor`, `CGO_ENABLED=0`, ADR-0003). Only the **binary** is drop-shipped to
   `macos-dev-config` (git-ignored, under `bin/`). That repo keeps script + data
   only — **no Go module, no `go.mod` there**.
2. **Release + tracking** — `go install texteditor/cmd/fleetdaemon@<tag>`, pinned by
   a committed `fleetdaemon.version` in `macos-dev-config`, verified against the
   binary's `--version`. The pin (not the binary) is versioned.
3. **`serve.sh` manifest seam** — per-invocation **env vars** (`RUNNER` / `MODEL` /
   `HOST` / `PORT` / `SERVE_PORT_<NAME>`), not a JSON blob. `serve.sh` is stateless,
   does not parse `models.json` (no `jq`) or `servers.conf` (ADR-0027).
4. **Lanes + port-uniqueness** enforced **in the daemon** at manifest load
   (`lanes-conflict`, `data-model.md §2.4`) — JSON Schema checks structure only.
5. **Pre-bind gate** owned by the daemon (fail-closed before `serve.sh`); the
   Tailscale ACL is **projected from the manifest** and reconciled at daemon
   startup (ADR-0021 §3).
6. **launchd** — one always-on daemon agent (KeepAlive); runners are on-demand, not
   agent-managed.

## Still open (explicit list; do not silently choose)

### Plan B — serving control (CENTRAL focus this session)

The daemon binary is **not built yet** — but ADR-0032 now names exactly where and
how. Build it per `implementation-sequence.md` Plan B items 1–6:

1. **Control daemon** (`texteditor/cmd/fleetdaemon/`) — implement
   `contracts/daemon-http.md` verb-by-verb: `GET /list`, `GET /status/{name}`
   (provisioning adds `bytes/total`), `POST /start/{name}` (blocking, 60s bound),
   `POST /stop/{name}` (idempotent), `POST /provision/{name}` (202 + provisionID),
   `GET /log/{name}`, `GET /reach/{name}`. Error envelope `{code,message}` with the
   `interface.md §12.1` codes. Sole `models.json` reader (ADR-0027); binds
   `127.0.0.1` (ADR-0021 §3); manifest parse + lanes + provision + live state.
   Mirrors `internal/fleet` (the client already parses these exact shapes).
2. **`serve.sh` executor rewrite** — drop its own `servers.conf`/`models.json` parse;
   receive the parsed manifest from the daemon as per-invocation env vars
   (ADR-0032 §3). Keep the runner launch logic (`_binary`/`_cmd`/`_health`) — the
   daemon wraps, does not re-implement (ADR-0025 §1).
3. **Provision + lanes loader** — HF download via the `provision` verb (async,
   observable via `status`), skip-present re-runs; enforce the lanes rule
   (ADR-0018 §4) at daemon manifest load.
4. **Tailscale ACL projection + pre-bind gate** — derive the on-demand fleet's
   ports from the manifest into `acl.hujson`; the daemon refuses a non-`127.0.0.1`
   start unless gated (ADR-0021 §3, ADR-0032 §5).
5. **launchd agent** — one always-on daemon LaunchAgent; plists templated;
   `launchctl load` idempotent in `install.sh` (ADR-0032 §6).

### Track 1 finishing — agentic loop + edit integrity (this repo)

6. **Loop wiring** — implement the agentic cycle per `state-machine.md §1`: native
   tool-calling dispatch/observe bounded by `mode.maxSteps`; edit-integrity retry
   (`guard-failed` → re-read block + re-dispatch; `invalid-structure` → retry with
   issues; both count against `maxSteps`, ADR-0029). Bind real tool handlers in the
   composition root (`edit_markdown`, `retrieve`, `diff`, `read_note`) to
   Document/Retriever. Emit `candidate`/`diff` events.
7. **Provider tool-call wire format — UNPINNED, decide and flag.** The Provider's
   SSE parser (`internal/provider/provider.go` `frameToEvent`) decodes only
   `delta.content` + `usage`. Native tool-calling requires parsing
   `delta.tool_calls` / `finish_reason: "tool_calls"`, and rendering a follow-up
   `role: "tool"` message on the next round-trip. This is a genuine contract gap
   (not in `interface.md §2`, which only pins `token`/`done`/`error` raw events) —
   surface it as a judgment call, don't silently extend the wire format.
8. **Session/context wiring** — append each turn's messages back to the session
   (ADR-0026 §3), enforce per-session `TokenBudget` via the Meter
   (`session-budget-exceeded`).
9. **Verification gates** — `edit-integrity.feature`, `sessions.feature`,
   `token-metering.feature` scenarios become CI assertions when wired.

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` — build order only.
2. `docs/writing-assistant/architecture.md` — arc42.
3. `docs/writing-assistant/contracts/` — `interface.md`, `module-boundaries.md`,
   `data-model.md`, `daemon-http.md`, `failure-semantics.md`, `state-machine.md`,
   `concurrency-topology.md`.
4. ADRs `0001–0032` for any module you touch — normative; **ADR-0032** governs the
   daemon.
5. `docs/writing-assistant/behaviors/*.feature` — Gherkin acceptance contracts.

## Hard constraints (never violate)

- Single static Go binary, no CGO. SQLite via `modernc.org/sqlite`.
- Serving is pure llama.cpp / MLX on Metal (ADR-0030); engine reaches serving only
  via `Fleet → daemon` (ADR-0025/0027); never reads `models.json` or calls `serve.sh`.
- The daemon's **source** lives in `texteditor` (`cmd/fleetdaemon/`); only its
  binary is drop-shipped to `macos-dev-config` (ADR-0032).
- Sealed Go interfaces over pure DTOs (ADR-0016/0027): shared DTOs in `shared/dto`.
- Contract-first (ADR-0002/0017): OpenAPI authored before the server; clients dumb.
- Test at the boundary (Q5): every public op stub-tested first; `CGO_ENABLED=0 go test ./...`.

## Then continue

Plan B done → **Track 1 loop wiring** → **TUI C6–C8** (POC reached) →
**D2–D5 router seam** (off-by-default) → Track 2
(`implementation-sequence-future.md`, mandatory Plan E/F).

## Report back

At each milestone: what landed, which tests pass, and any place the docs forced a
stop or a judgment call. Specifically flag: (a) the daemon-binary contract fit
against `contracts/daemon-http.md` now that ADR-0032 pins the seam, and (b) the
**Provider tool-call wire-format** decision (open item 7) — do not resolve it
silently.
