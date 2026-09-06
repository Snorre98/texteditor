# Handoff — resume implementation (next session)

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are

**Milestone 5 (TUI, C6–C8) landed: the POC acceptance is reached at the wire
level.** The TUI boots, discovers the engine, renders all six panels, and drives
turns/model-switching through the generated client — verified headlessly against
the live engine (labeled `no-model-available` error event when nothing is
served). A *live token stream* needs real weights on the SSD (see below), which
is a provisioning step, not a code step.

### Landed + tested this session

- **Recorded contract amendments (not silent):**
  - `api/openapi.yaml` SSE vocabulary rewritten to the committed wire: payload
    schemas per event type (`TokenEvent`, `MeterEvent`, `CandidateEvent`,
    `DiffEvent`, **`RagEvent`**, `DoneEvent`, `ErrorEvent`, `BackpressureEvent`),
    framing documented on `/turn` (`event:` = type, `data:` = payload, one turn
    per connection, no `turnId` in payloads). ADR-0017 §6 amended with a recorded
    amendment note; `interface.md` §11 (vocabulary gains `rag`) and §3
    (`Chunk` gains camelCase JSON tags — it now crosses the wire via `rag`)
    amended. `internal/genapi` regenerated; zero route drift; all Go tests green.
  - Engine: `internal/loop/loop.go` `observeTool` emits a `rag` event on
    `retrieve`/`read_note` observation + boundary test (`TestAgenticRetrieveEmitsRag`).
- **`macos-dev-config` data fix (committed separately, 7fcc866):** `summary`
  gets its own source (`Llama-3.2-1B-Instruct-4bit`) — lanes-conflict gone;
  `llama-8b` → `llama3.1-8b` + added `gemma4-26b`/`gemma4-12b` daemons so the
  engine's mode defaults resolve; `nomic-embed` points at a real GGUF
  (`nomic-embed-text-v1.5.Q8_0.gguf`). Verified: daemon serves `/list`, engine
  boots with all modes/models resolving.
- **C6 · Client API + DTOs** (`tui/`): Hey API + Zod generated client from the
  committed spec (`src/generated`, `bun run gen`; committed like `internal/genapi`).
  Zod response validation via client-fetch `responseValidator` per call. Port
  discovery (fixed mode): `ENGINE_URL` > `ENGINE_PORT` > spec `servers[0]` 9100
  + `/health` probe; unreachable engine is an explicit screen. `/turn` SSE reader
  (`src/api/sse.ts`): fetch + ReadableStream, generated zod schema per `event:`
  name, labeled validation rejections, terminal at `done`/`error`.
- **C7 · State** (`src/state/store.ts`): Solid signals store — models(+liveState),
  modes, tools, document+blocks, sessions+messages, live turn (tokens, meter +
  cumulative session tally, candidate/diff/rag queues, done/error/backpressure).
  `switchModel` = start new → up → stop old; failed start labeled
  `model-switch-failed` and the old model stays (serving-control.feature).
  `acceptCandidate` = fetch staged candidate → ApplyEdit → Commit.
- **C8 · UI** (`src/ui/`): OpenTUI + Solid panels (editor=markdown renderable,
  chat=sticky scrollbox + input, meter, mode/model selects, RAG, diff preview),
  `esc` quit, `a` accept, input enter = turn. Component smoke tests via
  `testRender` capture panels and document content.
- **28 `bun test` (discovery/decoder/store/component) + `tsc --noEmit` green;
  `CGO_ENABLED=0 go test ./...` green.**

### Codegen-fit findings (report item a — recorded, not silent)

1. **openapi-ts v0.99.0 is broken** for SDK generation (its `sdk` plugin
   resolves to an unpublished `@hey-api/sdk` npm package → "this.handler is not
   a function"). **Pinned `@hey-api/openapi-ts@0.62.0`** (plugins
   `["zod", "@hey-api/sdk"]`, `client: "@hey-api/client-fetch"`).
2. **client-fetch pinned to `0.7.2`**: `Options.client` (which the generated
   `sdk.gen.ts` uses) was removed in 0.7.3+.
3. The `/turn` raw-response route generates a plain `startTurn` POST as
   expected (ADR-0031); the stream reader is hand-written over the generated
   zod payload schemas — exactly the "typed SSE decoders" contract. 0.62 does
   not auto-wire zod response validation, so `src/api/client.ts` binds each
   generated call to its `zXxxResponse` schema explicitly.
4. OpenTUI reconciler quirks (recorded in code comments): sibling Solid
   control-flow components and fallback-less `<Show>` mount orphan text nodes
   in the universal renderer → panels use one-`For`-over-rows / per-`Show`
   wrapper-box patterns and always provide `fallback`. `solid-js` pinned to
   **1.9.12 exactly** per the OpenTUI docs.

### Port discovery (report item b)

Fixed-mode only, as planned: dynamic discovery + the "where am I" base-URL
advertisement are **Plan E** (`implementation-sequence-future.md` E3/E4). The
engine still has no `ENGINE_PORT` env read (`-addr` flag only) and `/health`
does not advertise a base URL — both are Plan E work; no spec change was needed.

### Renderer note (judgment call, ADR-resolved)

The previous handoff said "OpenTUI is React-based". ADR-0023 (accepted,
supersedes ADR-0013's dangling note) decides **Solid** — followed (`@opentui/solid`,
snake_case intrinsics, `useKeyboard`, `testRender`).

### Still blocking a *live* token-stream demo (external, not code)

`macos-dev-config` has no weights on disk (`models/huggingface`, `models/gguf`
empty on the SSD). Provisioning needs real downloads (`huggingface-cli` for the
MLX models, the nomic GGUF) — e.g. `provision` a small model (`summary`, ~700MB)
or `text` (3B) for the POC demo. Nothing in `texteditor` blocks this.

## Still open (explicit list; do not silently choose)

### Plan D — router seam (D2–D5, off-by-default)

Unchanged from the last handoff: build the `ToolDecider` module, the loop toggle
for `toolCalling: "router"`, and the sealed interface. Enablement (D1) stays
deferred/gated; `native` is the byte-identical baseline. The two Fleet-dependent
startup gates run at the composition root, not inside the Mode registry.

### Verification gates carried forward

- `tool-routing.feature`, `versioning.feature`, `provider-hotswap.feature` — wire
  as CI assertions when their phases land (router for the former; Track 2 for the
  latter two).
- `serving-control.feature` "TUI switches models" is now asserted as store unit
  tests (`tui/tests/store.test.ts`); a UI-level assertion can still be added later.

## Then continue

**D2–D5 router seam** (off-by-default) → Track 2 (`implementation-sequence-future.md`,
mandatory Plan E/F).

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` — build order only.
2. `docs/writing-assistant/architecture.md` — arc42.
3. `docs/writing-assistant/contracts/` — `interface.md`, `module-boundaries.md`,
   `data-model.md`, `daemon-http.md`, `failure-semantics.md`, `state-machine.md`,
   `concurrency-topology.md`.
4. ADRs `0001–0032` for any module you touch — normative. For the TUI: ADR-0013,
   0014, 0017 (amended §6), 0021 §1, 0023.
5. `docs/writing-assistant/behaviors/*.feature` — Gherkin acceptance contracts.

## Hard constraints (never violate)

- Single static Go binary, no CGO. SQLite via `modernc.org/sqlite`.
- Serving is pure llama.cpp / MLX on Metal (ADR-0030); engine reaches serving only
  via `Fleet → daemon` (ADR-0025/0027); never reads `models.json` or calls `serve.sh`.
- The daemon's source lives in `texteditor` (`cmd/fleetdaemon/`); only its binary
  is drop-shipped to `macos-dev-config` (ADR-0032).
- Sealed Go interfaces over pure DTOs (ADR-0016/0027): shared DTOs in `shared/dto`.
- Contract-first (ADR-0002/0017): the OpenAPI spec is authored before any client;
  the TUI consumes the generated client, never engine internals. Do not silently
  extend the spec — surface route/shape changes as recorded contract amendments.
- Test at the boundary (Q5): `CGO_ENABLED=0 go test ./...` stays green; client
  code is spec-generated (`bun run gen`), not hand-shaped. `bun test` + `bunx
  tsc --noEmit` green in `tui/`.

## Machine note (this Mac)

The internal disk is ~full. Per `macos-dev-config`, dev caches live under
`/Volumes/Ex-SSD/caches/<tool>` (managed by `tools/dev-cache.sh`); the bun install
cache was relocated there (`caches/bun`, symlinked from `~/.bun/install/cache`).
Keep TUI cache writes on the SSD; `caches/npm` is the npm cache (`~/.npmrc`).
