# Handoff — resume implementation (session 4)

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are (session 3 landed + reported)

`A5` (controller layer) and the **A3→B resolution** are **complete and tested**:

- **A5.1 SSE event bus** — `internal/eventbus/` (`Emit`/`Subscribe`, bounded-256,
  drop-oldest + single labeled `backpressure` event; non-blocking `Emit`).
- **Fleet daemon client** — `internal/fleet/fleet.go` now ships a real HTTP client
  (`NewDaemon`/`NewDaemonWithClient`) over the pinned **`contracts/daemon-http.md`**
  verb contract; `NewStub` is removed. `Resolve` carries merge + capability gates +
  fallback ladder over daemon-returned data (an unexported `daemonEntry` holds
  daemon-owned `defaults`, so they never leak into the public `dto.Model`).
- **A5.2 API server** — `api/openapi.yaml` (full ADR-0017 route table + event
  vocabulary; `/turn` marked `x-ogen-raw-response`) → ogen generates
  `internal/genapi` (`go generate ./...` runs `internal/ogen`); `internal/apiserver`
  implements the typed `Handler` + `RawHandler`, hand-framing `/turn` SSE via
  `http.ResponseController` (turnID correlation, done/error terminal).
- **Composition root** — `cmd/texteditor/main.go` wires every store/registry/loop to
  the real gateways; fail-fast on `tool-has-no-handler` and `daemon-unreachable`;
  added `tool.Load` and `document.Migrate`/`session.Migrate`. Static `CGO_ENABLED=0`
  binary builds and runs.

### Contract-gap resolutions (session 3, settled — do not re-ask)

1. **Provider messages** — resolved via pure `dto.Request` (`shared/dto/request.go`);
   `Chat(ctx, target, req)`/`Stream(ctx, target, req, emit)`; the Provider renders the
   OpenAI wire format; the assembler builds the full request. `interface.md §2/§5`,
   `module-boundaries.md`, and `ADR-0027 §2` amended to match.
2. **Meter signature** — already committed (`interface.md §6` == `meter.go`).
3. **Retriever block source** — `BlockReader` seam documented, satisfied by the
   Document store at the composition root.

### `macos-dev-config` (Plan B data, session 3 — landed)

- `models.json` migrated to two-tier / Metal-only (ADR-0030); schema synced to the
  authoritative `contracts/assets/fleet-manifest.schema.json`; `SERVING-MANIFEST.md`
  documents the update process (references this repo). Migrated file validates.

## Still open (explicit list; do not silently choose)

1. **Plan B daemon binary is NOT built.** The engine's `NewDaemon` client is done,
   but the actual **control daemon** (sole `models.json` reader, wraps `serve.sh`)
   in `macos-dev-config` does not exist yet. The pinned contract
   (`contracts/daemon-http.md`) is the spec; build the daemon against it
   (Plan B items 2–5: daemon, `serve.sh` executor receiving the parsed manifest,
   provision verb, Tailscale ACL, launchd agents).
2. **The agentic loop is not exercised end-to-end.** Modes are single-shot
   (`maxSteps 0`); the loop's `planning → dispatching → observing` tool-call cycle
   (`state-machine.md §1`) and the edit-integrity retry loop (ADR-0029) are specified
   but not implemented in `internal/loop`. Tool handlers bound in `cmd/texteditor`
   are stubs returning "not yet implemented".
3. **No end-to-end turn against a real provider** has been validated (the happy path
   is stub/httptest-only).

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` — build order only.
2. `docs/writing-assistant/architecture.md` — arc42.
3. `docs/writing-assistant/contracts/` — `interface.md`, `module-boundaries.md`,
   `data-model.md`, `daemon-http.md`, `failure-semantics.md`, `state-machine.md`,
   `concurrency-topology.md`.
4. ADRs `0001–0031` for any module you touch — normative.
5. `docs/writing-assistant/behaviors/*.feature` — Gherkin acceptance contracts.

## Hard constraints (never violate)

- Single static Go binary, no CGO. SQLite via `modernc.org/sqlite`.
- Serving is pure llama.cpp / MLX on Metal (ADR-0030); engine reaches serving only
  via `Fleet → daemon` (ADR-0025/0027); never reads `models.json` or calls `serve.sh`.
- Sealed Go interfaces over pure DTOs (ADR-0016/0027): shared DTOs in `shared/dto`.
- Contract-first (ADR-0002/0017): OpenAPI authored before the server; clients dumb.
- Test at the boundary (Q5): every public op stub-tested first; `CGO_ENABLED=0 go test ./...`.

## What to do next (session 4 scope — pick from the sequence)

### Track 1 finishing — the agentic loop + edit integrity (this repo)

1. **Loop wiring** — implement the agentic cycle per `state-machine.md §1`: native
   tool-calling dispatch/observe bounded by `mode.maxSteps`; tool results routed to
   observability; edit-integrity retry (`guard-failed` → re-read block + re-dispatch,
   `invalid-structure` → retry with issues, both counting against `maxSteps`,
   ADR-0029). Bind real tool handlers in the composition root (`edit_markdown`,
   `retrieve`, `diff`, `read_note`) to Document/Retriever. Emit `candidate`/`diff`
   events.
2. **Session/context wiring** — append each turn's messages back to the session
   (ADR-0026 §3), enforce per-session `TokenBudget` via the Meter
   (`session-budget-exceeded`).
3. **Verification gates** — `edit-integrity.feature`, `sessions.feature`,
   `token-metering.feature` scenarios become CI assertions when wired.

### Plan B — serving (`macos-dev-config`)

4. **Control daemon binary** — implement the daemon against `contracts/daemon-http.md`
   (sole manifest reader per ADR-0027, wraps `serve.sh`, blocks `start` 60s, async
   `provision`; binds `127.0.0.1`; Tailscale ACL per ADR-0021 §3).
5. **`serve.sh` executor** — receive the parsed manifest from the daemon (drop its
   own `models.json`/`servers.conf` parse, ADR-0027).
6. **Provision + lanes loader** — HF download via the `provision` verb; enforce the
   lanes rule (ADR-0018 §4) at manifest load.

### Then continue

A5 + B daemon → **TUI C6–C8** (POC reached) → **D2–D5 router seam** (off-by-default)
→ Track 2 (`implementation-sequence-future.md`, mandatory Plan E/F).

## Report back

At each milestone: what landed, which tests pass, and any place the docs forced a
stop or a judgment call — especially the daemon-binary contract fit against
`contracts/daemon-http.md` and any loop/state-machine decision not already pinned.
