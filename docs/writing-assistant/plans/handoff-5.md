# Handoff — resume implementation (next session)

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are

Plan B (serving control) and Track 1 loop wiring are **complete and tested**.
Milestones 1–4 of `implementation-sequence.md` are done: the engine is
headless-driveable via `curl`/the generated client, and the control daemon is a
second binary in the same module. **The next session's goal is milestone 5 — the
TUI (Plan C, C6–C8) — the fast path to a working POC with a live token meter**
(ADR-0013).

### Landed + tested this session

- **Control daemon (ADR-0032)** — `cmd/fleetdaemon/` + `internal/fleetdaemon/`:
  manifest load (JSON Schema + **semantic lanes/port/name validation**), live-state
  registry, the seven verbs (`/list`, `/status/{name}` with `bytes/total`,
  blocking `/start` 60s, idempotent `/stop`, async `/provision` 202, `/log`,
  `/reach`), `{code,message}` error envelope (interface.md §12.1), binds
  `127.0.0.1`, **pre-bind gate** (fail-closed before `serve.sh`), `--version`.
  Boundary tests + a **fleet↔fleetdaemon cross-parse test** locking the exact
  `daemon-http.md` wire shape.
- **`macos-dev-config` (drop-shipped, committed separately)** — `tools/serve.sh`
  rewritten to a stateless env-var executor (`RUNNER`/`MODEL`/`HOST`/`PORT`/
  `DELEGATE`); `servers.conf` deleted; `tailscale/acl.hujson` → manifest-projected
  template; `launchd/com.macosdev.fleetdaemon.plist`; `tools/fetch-fleetdaemon.sh`
  + `fleetdaemon.version` + `install.sh` daemon fetch/load.
- **Provider tool-call wire format** — `frameParser` emits `tool_call`/`finish`
  raw events (accumulated by index); `parseCompletion` captures non-streaming
  tool calls; `dto.ToolCall` added. `interface.md §2` amended (recorded change,
  not silent).
- **Agentic loop** — `internal/loop/loop.go` implements the full
  `planning → dispatching → observing → answering` state machine bounded by
  `mode.maxSteps`; `guard-failed`/`invalid-structure` structured results flow back
  to the model (ADR-0029); `candidate`/`diff` events; session append (user +
  assistant messages); per-session `TokenBudget` via `Meter.SessionUsage` →
  `session-budget-exceeded`.
- **Real tool handlers** — `edit_markdown`, `retrieve`, `diff`, `read_note` bound
  in `cmd/texteditor/main.go` to Document/Retriever.

### Settled during this session (do not re-ask)

1. **Provider tool-call wire format (was open item 7)** — resolved: `RawEvent`
   gained `tool_call`/`finish` raw-event shapes (no new shared DTO). Recorded in
   `interface.md §2` + `shared/dto/fleet.go`.
2. **Per-session token budget seam** — `TokenMeter` gained
   `SessionUsage(ctx, sessionID)` (`interface.md §6` amended); `meter_events`
   gained a `"completion"` component row (`data-model.md §1.3` amended) so the
   cumulative tally is meaningful.
3. **Document-scoped tool binding** — `edit_markdown`/`diff` receive the loop's
   `DocumentID` injected at dispatch (never exposed to the model's schema), since
   `ToolExecutor.Invoke(name, args)` has no ctx/document binding.
4. **`dto.Capabilities` JSON tags** — added camelCase tags (`contextLength`/
   `thinkingMode`/`supportsSystemPrompt`) to fix the fleet↔daemon wire.

### Known data bug to fix (blocking a live POC)

The real `models.json` in `macos-dev-config` has a **lanes-conflict**: `text`
(`:8083`) and `summary` (`:8084`) both resolve to
`mlx-community/Llama-3.2-3B-Instruct-4bit`. The daemon correctly fail-closes; the
manifest must be corrected (give one of them its own source) before the daemon
will serve. **Also** `nomic-embed` (the Retriever's embedding model) has a
`source.file` placeholder `REPLACE_WITH_nomic-embed-text.GGUF` — it needs a real
GGUF before RAG/text retrieval will run.

## Next session — Plan C (TUI, C6–C8): milestone 5, the POC

The engine (A5) and serving (B) are done; the OpenAPI spec is finalized and
committed (`api/openapi.yaml`, ADR-0017). Build the TUI against it, contract-first
(ADR-0002/0017: the spec is authored; the client is generated).

### C6 · Client API + DTOs

Generate the client + typed DTOs from `api/openapi.yaml`:
- **Hey API** generated client, **Zod** schema-validated types (ADR-0017 §2,
  ADR-0003) — the TUI is TypeScript (OpenTUI is React-based). Do **not** hand-roll
  the client; regenerate from the locked spec.
- **Port discovery** (ADR-0021 §1): read the engine's advertised base URL via
  `/health` + `/models`, rewrite the client base URL; honor a fixed `ENGINE_PORT`.
- The `/turn` route is SSE (marked `x-ogen-raw-response` in the spec): the client
  needs an EventSource reader over the `Event` schema (ADR-0017 §6 event
  vocabulary: token/meter/candidate/diff/done/error/backpressure).

### C7 · Client state

- **Solid reactive signals** store over the generated client (ADR-0023).
- State slices: models + live state (`/models` + `/models/{name}/status`),
  modes (`/modes`), tools (`/tools`), open document + block tree
  (`/documents`, `/documents/{id}/blocks`), sessions + messages
  (`/sessions`, `/sessions/{id}/messages`), and the live turn stream.
- The live token **meter** view consumes the `meter` SSE event; the model/mode
  switcher drives `/models/{name}/start` / `/stop` (the write side of lifecycle —
  ADR-0007).

### C8 · Client UI (OpenTUI)

Panels per ADR-0013/0023:
- **Markdown editor**, **chat** (session-anchored bubbles), **live token meter**,
  **model/mode switcher**, **RAG results**, **diff preview**.
- **Three-target capability adapter** per ADR-0014 (dynamic/fixed port + LAN).

**POC reached** when: a turn streams tokens with a live meter, a model can be
switched at runtime (start/stop via the TUI), and an edit streams
`candidate`/`diff` into a preview — all driven by `curl`-equivalent generated
calls, no direct engine internals.

## Still open (explicit list; do not silently choose)

### Plan D — router seam (D2–D5, off-by-default)

After the POC (or in parallel if delegate-able), build the **seam** only — the
`ToolDecider` module, the loop toggle for `toolCalling: "router"`, and the sealed
interface (`implementation-sequence.md` Plan D D2–D5). **Enablement (D1) stays
deferred, gated** by the triggers in `research/parked-needle-router.md`. Nothing is
wired until a mode opts in; `native` is the byte-identical baseline. Note the
sequencing note in the implementation plan: the two Fleet-dependent startup gates
(`mode-refs-router-unavailable`, `router-tools-stale`) run at the **composition
root**, not inside the Mode registry.

### Verification gates carried forward

- `serving-control.feature` "TUI switches models" scenario — becomes assertable
  once C7/C8 land (deferred from last session for this reason).
- `tool-routing.feature`, `versioning.feature`, `provider-hotswap.feature` — wire
  as CI assertions when their phases land (router for the former; Track 2 for the
  latter two).

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` — build order only.
2. `docs/writing-assistant/architecture.md` — arc42.
3. `docs/writing-assistant/contracts/` — `interface.md`, `module-boundaries.md`,
   `data-model.md`, `daemon-http.md`, `failure-semantics.md`, `state-machine.md`,
   `concurrency-topology.md`.
4. ADRs `0001–0032` for any module you touch — normative. For the TUI: ADR-0013
   (TUI as POC accelerator), 0014 (targets), 0017 (OpenAPI), 0021 §1 (port
   discovery), 0023 (Solid).
5. `docs/writing-assistant/behaviors/*.feature` — Gherkin acceptance contracts.

## Hard constraints (never violate)

- Single static Go binary, no CGO. SQLite via `modernc.org/sqlite`.
- Serving is pure llama.cpp / MLX on Metal (ADR-0030); engine reaches serving only
  via `Fleet → daemon` (ADR-0025/0027); never reads `models.json` or calls `serve.sh`.
- The daemon's **source** lives in `texteditor` (`cmd/fleetdaemon/`); only its
  binary is drop-shipped to `macos-dev-config` (ADR-0032).
- Sealed Go interfaces over pure DTOs (ADR-0016/0027): shared DTOs in `shared/dto`.
- Contract-first (ADR-0002/0017): the OpenAPI spec is authored before any client;
  the TUI consumes the generated client, never engine internals.
- Test at the boundary (Q5): `CGO_ENABLED=0 go test ./...` stays green in the
  engine; client code is spec-generated, not hand-shaped.

## Then continue

TUI C6–C8 → **D2–D5 router seam** (off-by-default) → Track 2
(`implementation-sequence-future.md`, mandatory Plan E/F). The POC is reached at
C8; Track 2 is the shipped product.

## Report back

At each milestone: what landed, which tests pass, and any place the docs forced a
stop or a judgment call. Specifically flag: (a) the client-generation fit against
the committed `api/openapi.yaml` (any spec gap the codegen surfaced — e.g. the
SSE `/turn` raw-response route), and (b) the **port-discovery** behavior
(ADR-0021 §1) once C6 lands — dynamic vs fixed resolution. Do not silently extend
the OpenAPI spec; surface any needed route/shape change as a recorded contract
amendment first.
