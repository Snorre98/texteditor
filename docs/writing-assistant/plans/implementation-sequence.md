# Implementation Sequence Plan

The build order for the architecture. This plan references ADRs only as the
source of *what* to build; it decides only the *order*. It does not relitigate
any architectural decision.

Structural fact this plan leans on: every dependency is a **sealed Go interface
over a pure DTO** (ADR-0016, ADR-0027), so any module compiles and is
boundary-testable standalone against a stub of its dependencies (Q5, ADR-0022).
That is what lets the horizontal layer order coexist with "every module works
standalone."

The sequence follows the required order:

1. Data layer
2. Repository layer
3. Service layer
4. Public module API
5. Controller layer (orchestrating server actions)
6. Client API + DTOs
7. Client state management
8. Client TUI UI

The engine (server) is built first; the serving side is a parallel track that the
Fleet gateway depends on; the client is built last, from the locked OpenAPI spec.

## Roadmap overview

Two tracks, in order:

- **Track 1 — proof of concept (build now).** Engine (Plan A) + serving control
  (Plan B) + the TUI client (Plan C). The TUI is the *fastest path to a working
  system with a live token meter* (ADR-0013) — it is the POC accelerator, not the
  destination.
- **Track 2 — mandatory completion (build after the POC).** Engine deployment &
  packaging (Plan E) and the Tauri markdown editor (Plan F). These are **required**,
  not optional: the two-way engine shipping (standalone daemon *and* Tauri sidecar)
  and the rich Tauri editor are the shipped product. Track 2 lives in its own
  dedicated plan — [`implementation-sequence-future.md`](implementation-sequence-future.md)
  (what to build) with the phased execution detail in
  [`implementation-sequence-track2.md`](implementation-sequence-track2.md) (order).

The router (Plan D) is additive and off-by-default in both tracks: its *seam* is
built in Track 1, its *enablement* is deferred (see Plan D).

---

## Mapping: module → layer → source ADR

| Layer | Modules / artifacts | ADR |
|---|---|---|
| 1 — Data | shared DTO package (`shared`/`dto`); SQLite schemas `app.db`/`index.db`/`meter.db`/`sessions.db`; git repo; `config/modes`, `config/tools` data files + schemas; fleet manifest (two-tier) schema + lanes loader | 0027, 0016, 0004, 0020, 0019, 0018, 0026, 0029, 0030 |
| 2 — Repository | Document store; Session store; Mode registry; Tool registry + Tool executor; Chunker; TextFormatter; Retriever | 0016 §4–§10, 0020, 0019, 0004, 0026, 0029 |
| 3 — Service | Provider gateway; Context assembler; Token metering; Fleet gateway; Agent loop; ToolDecider (optional, ADR-0028) | 0016 §1–§3/§6–§7, 0005, 0011, 0024, 0022, 0018, 0025, 0015, 0007, 0028 |
| 4 — Public module API | Sealed Go interfaces + pure DTOs locked (`contracts/interface.md`); 100% stub boundary tests | 0016, 0027, 0001 (R1–R6), 0022 (Q5) |
| 5 — Controller | SSE event bus; API server (OpenAPI routes) | 0016 §11–§12, 0012, 0017, 0003 |
| 6 — Client API/DTOs | Hey API + Zod generated client | 0017 §2, 0003 |
| 7 — Client state | Solid reactive signals | 0023 |
| 8 — Client UI | OpenTUI panels (editor/chat/meter/switcher/RAG/diff) | 0013, 0023, 0014, 0021 |

The serving side (`macos-dev-config`) is a dependency of the Fleet gateway; it is
tracked as its own plan below.

---

## Plan A — Engine (Go server): layers 1 → 5

### A1 · Data layer

Build with zero logic; only formats and schemas.

1. `shared`/`dto` package — every boundary type from the ADR-0027 catalog:
   `Capabilities`, `SamplingParams`, `ContextBudget`, `Mode`, `Model`, `Target`,
   `Resolution`, `LiveState`, `Chunk`, `Message`, `JSONSchema`, `Document`,
   `Block`, `BlockEdit`, `Revision`, `Candidate`, `WordEdit`, `Event`,
   `RawEvent`, `ToolDef`, `Session`, `Payload`, `Breakdown`, `ProviderCounts`,
   `AttributedBreakdown` — plus the edit-verification DTOs `BlockKind`,
   `TextFormatterIssue`, `Guard` (`Block` gains `Hash`, `BlockEdit` gains
   `Guards`) (ADR-0029 §2/§4), and the router DTOs `Decision`, `RouterContext`,
   `RouterUsage`, `RouterResult` (ADR-0028 §1; additive, only used when the
   router is enabled).
2. SQLite schemas + migrations per `contracts/data-model.md` §1:
   - `app.db` — `documents`, `blocks`, `candidates`
   - `index.db` — `blocks_ft`, `vec_chunks`
   - `meter.db` — `meter_events`
   - `sessions.db` — `sessions`, `messages`
3. git repo init (document version history) — ADR-0004 §2, ADR-0020 §2
   (engine-owned working tree + bare-ish history).
4. `config/modes/*.json` + `config/tools/*.json` with their JSON Schemas —
   ADR-0019 §1/§4. Mode schema gains the optional `toolCalling` field
   (`"native"` default; `"router"` later) — ADR-0028 §3. Seed the mode defaults
   (`defaultModel`, `params`, `contextBudget`) per the fleet policy — ADR-0015 §3/§4.
5. Fleet manifest two-tier JSON Schema — ADR-0018 §1 (schema lives in
   `macos-dev-config`; the semantic lanes loader is in Plan B).

### A2 · Repository layer

Each store/registry built standalone, exposed only through its interface.

1. **Document store** — ADR-0016 §9 + ADR-0020 (commit cadence, worktree, UUID
   block IDs, candidate side-table, `ApplyEdit`/`Commit`/`Diff`/`History`/
   `Candidates`) + ADR-0029 (normalize on `ApplyEdit`, verify `BlockEdit.Guards`
   atomically, format on `Commit`/`Save`; `guard-failed`/`invalid-structure`
   typed errors).
2. **Session store** — ADR-0016 §10 + ADR-0026 §2 (`ListByDocument`/`Create`/
   `Resume`/`Append`/`History`).
3. **Mode registry** — ADR-0016 §4 + ADR-0019 §2 (fail-fast validation, typed
   errors). Validate `toolCalling` as a field now (default `native`); the two
   router gates are deferred to Plan D (see the sequencing note below).
4. **Tool registry + Tool executor** — ADR-0016 §5 + ADR-0019 §3 (name-keyed
   handler map, `tool-has-no-handler`). Add the reserved-name guard for
   `request_tool` now (reject any real tool by that name) — ADR-0028 §2.
5. **Chunker** — ADR-0020 §5 (pure leaf, `Chunk(tree []Block, maxTokens)`).
6. **TextFormatter** — ADR-0029 §2 (pure leaf, `Normalize`/`Validate`/`Format`;
   hardcoded opinionated style, per block kind).
7. **Retriever** — ADR-0016 §8 + ADR-0004 §3. Its embed path depends on the
   `Provider.Embed` / `Fleet.Resolve` *interfaces*; build against stubs here,
   wire in A3.

### A3 · Service layer

1. **Provider gateway** — ADR-0016 §2 (pure REST leaf, `Chat`/`Stream`/`Embed`,
   raw-event emission).
2. **Context assembler** — ADR-0016 §6 + ADR-0011 (pure, `Assemble` →
   `Payload` + `Breakdown`).
3. **Token metering** — ADR-0016 §7 + ADR-0022 (Q1 scale-to-total) + ADR-0024
   (thinking fallback).
4. **Fleet gateway** — ADR-0016 §1 + ADR-0018 + ADR-0025 + ADR-0007 (verb
   contract) + ADR-0015 (fallback ladder). **Requires Plan B's control daemon
   (or its stubbed HTTP contract) to be up.**
5. **Agent loop** — ADR-0016 §3 + ADR-0026 §3 + `contracts/state-machine.md` §1
   (thin orchestrator; turn state machine; session-scoped `Run`).

### A4 · Public module API (the lock)

Finalize the sealed Go interfaces + pure DTOs exactly as `contracts/interface.md`
§1–§13; enforce R1–R6 (ADR-0001); land 100% stub-backed boundary tests (ADR-0022
Q5). After this, internals are private and only the public API crosses seams.

### A5 · Controller layer

1. **SSE event bus** — ADR-0016 §11, ADR-0012, `contracts/concurrency-topology.md`.
2. **API server** — ADR-0016 §12, ADR-0017 endpoint surface, `ogen` codegen per
   ADR-0003/0017.

---

## Plan B — Serving side (`macos-dev-config`)

Dependency of A3.4 (Fleet gateway). Runs in parallel with A1–A3.3; its daemon
must complete before A3.4 starts.

1. Fleet manifest data (`models.json`, two-tier) + shared semantic lanes loader —
   ADR-0018 §1/§4 (daemon-owned reader, ADR-0027). Seed `capabilities`/`defaults`/
   `modeTags` per the fleet policy (MoE over dense, 14B+ citation floor,
   temperature sheet) — ADR-0015 §1–§3. The `runner` enum is `llama.cpp | mlx-lm |
   mlx-vlm | delegate` only (no `ollama`/`lmstudio`), every runner on the Metal GPU
   backend — ADR-0030 §1–§2.
2. **Provisioning** — the `provision` verb resolves the manifest `source` and
   downloads via the HF API; enforce the lanes rule (one runner per model) and the
   archive policy (full-precision safetensors archived; run-quants re-pullable);
   dedup via APFS hardlinks — ADR-0008 §1–§4, ADR-0018 §5. `source.kind` is
   `hf | gguf | needle` only (direct MLX/GGUF quants; no `ollama`/`lmstudio`
   import) — ADR-0030 §3.
3. `serve.sh` lifecycle executor (verb contract; receives the parsed manifest
   from the daemon, no `jq` parse) — ADR-0007, ADR-0018 §2, ADR-0025/0027.
4. Control daemon (HTTP transport over the verb contract, sole manifest reader) —
   ADR-0025, ADR-0021 §3 (bind + Tailscale ACL), ADR-0027. Source is authored in
   `macos-dev-config` (`cmd/fleetdaemon/`) and built there to `bin/` — ADR-0033
   (superseding ADR-0032 §1's texteditor-home split).
5. Tailscale ACL entries + pre-bind gate — ADR-0021 §3.
6. **Always-on agents** (`launchd/`) — install/load a named agent for
   reboot-persistent serving (plist templating, `launchctl` load) —
   `module-boundaries.md` §1.

---

## Plan C — Client (TUI): layers 6 → 8

The POC accelerator (ADR-0013). Runs after A4 (spec locked) and A5 (server
reachable); codegen needs the finalized OpenAPI spec. The Tauri editor is the
mandatory shipped client, sequenced separately in
[`implementation-sequence-future.md`](implementation-sequence-future.md) (Plan F).

1. **C6 · Client API + DTOs.** Generate the Hey API + Zod client from the OpenAPI
   spec — ADR-0017 §2, ADR-0003; port discovery per ADR-0021 §1.
2. **C7 · Client state.** Solid reactive signals / store over the generated
   client — ADR-0023.
3. **C8 · Client UI.** OpenTUI panels (markdown editor, chat, live token meter,
   model/mode switcher, RAG results, diff preview) — ADR-0013, ADR-0023;
   three-target capability adapter per ADR-0014.

---

## Plan D — Router increment (optional, ADR-0028)

Strictly additive and toggleable: `toolCalling: "native"` is the byte-identical
baseline, so this phase never blocks Plans A–C. The client is unaffected — the
router is engine-internal and transparent to the TUI.

Two parts, separated on purpose (per `research/parked-needle-router.md`):

- **Seam (build now, off by default) — D2–D5.** The `ToolDecider` module, the loop
  toggle, and the sealed interface are built so `toolCalling: "router"` *can* be
  turned on; nothing is wired until a mode opts in.
- **Enablement (deferred, gated) — D1.** Fine-tuning Needle, serving it, and
  flipping a mode to `router` is done only when an enablement trigger fires (tool
  set > ~15, a measured tool-calling accuracy/refusal problem, or a measurably weak
  writer on an agentic mode) — `research/parked-needle-router.md`.

- **D1 · Enablement (deferred).** `serve-needle.sh` + OpenAI facade resident in
  `macos-dev-config`; manifest gains `daemon "needle"` (`delegate` runner) and
  model `needle-router` (`source.kind: "needle"`, `source.fingerprint`) — ADR-0028
  §7, ADR-0018 §3. Fine-tune + serve + flip a mode is deferred, gated by the
  triggers above.
- **D2 · `ToolDecider` service** — Retriever-style (ADR-0016 §8): resolves
  `needle-router` via Fleet, calls Provider; `SignalTool()` + `Decide(ctx, intent,
  RouterContext)`. Not a leaf — depends on Fleet + Provider. **(landed)**
- **D3 · Loop toggle + routing** — Agent loop: when `mode.toolCalling == "router"`,
  splice `SignalTool()` instead of `AllowlistFor(mode)`; intercept `request_tool`;
  call `Decide`; on `Confidence ≥ τ` dispatch `Invoke`, else the existing
  `planning → answering` transition — ADR-0028 §3/§6, `state-machine.md` §1.
  **(landed; τ is applied inside the decider — refusal = zero Decision — and the
  refusal/error path answers via one bounded writer round with a "no tool" tool
  result, recorded amendment to ADR-0028 §6.2)**
- **D4 · Second meter call** — loop calls the existing `Meter.Attribute` a second
  time with `RouterUsage`; no `meter_events` schema change — ADR-0028 §5.
  **(landed; the router row is tagged `model=needle-router`)**
- **D5 · Public API lock** — add the `ToolDecider` sealed interface to
  `interface.md`/`module-boundaries.md`; add edges `ToolDecider → Fleet`,
  `ToolDecider → Provider`, `Loop → ToolDecider` (conditional); boundary tests per
  Q5 — ADR-0028 §1, ADR-0022. **(landed: §8b/module row existed since A1–A4;
  the seam session added `Completion.FinishReason` (interface.md §2) and
  `FleetGateway.Fingerprint` (interface.md §1), plus Q5 boundary tests for
  tooldecider/routergate/loop-router paths.)**

### Sequencing note (Mode-registry validation wrinkle)

ADR-0028 §4 places `mode-refs-router-unavailable` on the Mode registry, which
needs Fleet (via the daemon) to resolve `needle-router`. To keep the Mode registry
a leaf and standalone-testable in A2, this Fleet-dependent gate is executed at the
**composition root** (where Fleet is already wired), not inside the Mode registry
package. `router-tools-stale` likewise runs at startup where the manifest
fingerprint is reachable. This is an ordering choice, not a change to the ADR's
failure semantics.

Landed (router seam, D2–D5): the manifest fingerprint reaches the composition
root through the daemon `list` projection's optional `fingerprint` field
(`source.kind == "needle"` only — recorded amendment to `daemon-http.md` §2,
canonical in macos-dev-config) via the new `FleetGateway.Fingerprint(name)` op
(recorded amendment to `interface.md` §1). Both gates live in the
`internal/routergate` leaf, invoked by `cmd/texteditor` between the Mode registry
and loop wiring; the engine-side tool-set hash is `routergate.ToolSetHash`.

---

## Verification / acceptance gate (ADR-0022 + behavior contracts)

In addition to Q5's 100% stub-backed boundary tests (landed in A4), each
acceptance criterion and behavior contract is asserted as a CI gate when its
phase lands:

| Gate | Acceptance measure | Source |
|---|---|---|
| Q1 — transparent token cost | breakdown ≤ 100 ms after usage; scaled sum == provider total; overflow labeled | ADR-0022 Q1, `token-metering.feature` |
| Q2 — modifiability | next-turn effect, 0 rebuilds; startup validate ≤ 50 ms | ADR-0022 Q2, `serving-control.feature`, `provider-hotswap.feature` |
| Q3 — hot-swappable serving | fallback ≤ 60 s cold; degradation label guaranteed | ADR-0022 Q3, `provider-hotswap.feature` |
| Q4 — edit integrity | diff ≤ 100 ms; revert isolates blocks | ADR-0022 Q4, `versioning.feature` |
| Q5 — testability | 100% public ops stub-tested (in A4) | ADR-0022 Q5, `client-swap.feature` |
| Router | `tool-routing.feature` scenarios (native passthrough, refusal→answering, startup gates, separate meter row) | ADR-0028, `tool-routing.feature` |
| Edit verification | `edit-integrity.feature` (whole-block replace, guard-failed→re-read, invalid-structure→retry, format on accept/save) | ADR-0029, `edit-integrity.feature` |
| Sessions | `sessions.feature` (per-session concurrency + budget) | ADR-0026 |

**Failure semantics** (`contracts/failure-semantics.md`) are verified alongside
the owning module: retry/backoff + `provider-unreachable` (Provider, A3.1),
`provision-required`/`no-model-available`/`daemon-unreachable` (Fleet, A3.4),
`lanes-conflict`/`start-timeout`/`port-in-use` (serving, Plan B),
`tool-has-no-handler` (A2.4), `session-budget-exceeded` (Meter, A3.3 + ADR-0026),
`guard-failed`/`invalid-structure` (Document store, A2.1 + ADR-0029).

---

## Cross-cutting rules (from ADRs, not new decisions)

- **Standalone + boundary-test every module** before advancing (R5, ADR-0001;
  Q5 100% stub coverage, ADR-0022).
- **Pure leaves first, determinism enforced**: Chunker, TextFormatter, Context
  assembler, Provider, Mode/Tool registries, Session store (R4, ADR-0001;
  ADR-0016, ADR-0029).
- **Serving is only reached via Fleet → daemon**; the engine never reads
  `models.json` or calls `serve.sh` directly (ADR-0025, ADR-0027).
- **Contract-first for clients**: the OpenAPI spec (ADR-0017) is finalized in A5
  before C6 codegen.

## Milestones

1. A1 + A2 → all stores/registries standalone with boundary tests.
2. B1–B6 + A3 → Provider/Assembler/Meter/Fleet/Loop live against the daemon.
3. A4 → interfaces sealed, Q5 at 100%.
4. A5 → engine headless-driveable via `curl`/generated client (ADR-0002).
5. C6–C8 → TUI with live token meter — **proof of concept reached**.
6. D2–D5 → router seam built, off by default (D1 enablement deferred) — **landed**.
7. → Track 2 (mandatory): see `implementation-sequence-future.md` (what) +
   `implementation-sequence-track2.md` (order). **E1 engine primitives landed
   (dynamic port, `ENGINE_PORT`/`ENGINE_BIND`, `baseUrl` on `/health`, graceful
   shutdown, standalone-daemon packaging); E2/E6/E7/F6–F8 gated on the Rust
   toolchain.**
