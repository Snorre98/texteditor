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

---

## Mapping: module → layer → source ADR

| Layer | Modules / artifacts | ADR |
|---|---|---|
| 1 — Data | shared DTO package (`shared`/`dto`); SQLite schemas `app.db`/`index.db`/`meter.db`/`sessions.db`; git repo; `config/modes`, `config/tools` data files + schemas; fleet manifest (two-tier) schema + lanes loader | 0027, 0016, 0004, 0020, 0019, 0018, 0026 |
| 2 — Repository | Document store; Session store; Mode registry; Tool registry + Tool executor; Chunker; Retriever | 0016 §4–§10, 0020, 0019, 0004, 0026 |
| 3 — Service | Provider gateway; Context assembler; Token metering; Fleet gateway; Agent loop | 0016 §1–§3/§6–§7, 0005, 0011, 0024, 0022, 0018, 0025, 0015, 0007 |
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
   `AttributedBreakdown`.
2. SQLite schemas + migrations per `contracts/data-model.md` §1:
   - `app.db` — `documents`, `blocks`, `candidates`
   - `index.db` — `blocks_ft`, `vec_chunks`
   - `meter.db` — `meter_events`
   - `sessions.db` — `sessions`, `messages`
3. git repo init (document version history) — ADR-0004 §2, ADR-0020 §2
   (engine-owned working tree + bare-ish history).
4. `config/modes/*.json` + `config/tools/*.json` with their JSON Schemas —
   ADR-0019 §1/§4.
5. Fleet manifest two-tier JSON Schema — ADR-0018 §1 (schema lives in
   `macos-dev-config`; the semantic lanes loader is in Plan B).

### A2 · Repository layer

Each store/registry built standalone, exposed only through its interface.

1. **Document store** — ADR-0016 §9 + ADR-0020 (commit cadence, worktree, UUID
   block IDs, candidate side-table, `ApplyEdit`/`Commit`/`Diff`/`History`/
   `Candidates`).
2. **Session store** — ADR-0016 §10 + ADR-0026 §2 (`ListByDocument`/`Create`/
   `Resume`/`Append`/`History`).
3. **Mode registry** — ADR-0016 §4 + ADR-0019 §2 (fail-fast validation, typed
   errors).
4. **Tool registry + Tool executor** — ADR-0016 §5 + ADR-0019 §3 (name-keyed
   handler map, `tool-has-no-handler`).
5. **Chunker** — ADR-0020 §5 (pure leaf, `Chunk(document Document, maxTokens)`).
6. **Retriever** — ADR-0016 §8 + ADR-0004 §3. Its embed path depends on the
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
   ADR-0018 §1/§4 (daemon-owned reader, ADR-0027).
2. `serve.sh` lifecycle executor (verb contract; receives the parsed manifest
   from the daemon, no `jq` parse) — ADR-0007, ADR-0018 §2, ADR-0025/0027.
3. Control daemon (HTTP transport over the verb contract, sole manifest reader) —
   ADR-0025, ADR-0021 §3 (bind + Tailscale ACL), ADR-0027.
4. Tailscale ACL entries + pre-bind gate — ADR-0021 §3.

---

## Plan C — Client (TUI): layers 6 → 8

Runs after A4 (spec locked) and A5 (server reachable); codegen needs the
finalized OpenAPI spec.

1. **C6 · Client API + DTOs.** Generate the Hey API + Zod client from the OpenAPI
   spec — ADR-0017 §2, ADR-0003; port discovery per ADR-0021 §1.
2. **C7 · Client state.** Solid reactive signals / store over the generated
   client — ADR-0023.
3. **C8 · Client UI.** OpenTUI panels (markdown editor, chat, live token meter,
   model/mode switcher, RAG results, diff preview) — ADR-0013, ADR-0023;
   three-target capability adapter per ADR-0014.

---

## Cross-cutting rules (from ADRs, not new decisions)

- **Standalone + boundary-test every module** before advancing (R5, ADR-0001;
  Q5 100% stub coverage, ADR-0022).
- **Pure leaves first, determinism enforced**: Chunker, Context assembler,
  Provider, Mode/Tool registries, Session store (R4, ADR-0001; ADR-0016).
- **Serving is only reached via Fleet → daemon**; the engine never reads
  `models.json` or calls `serve.sh` directly (ADR-0025, ADR-0027).
- **Contract-first for clients**: the OpenAPI spec (ADR-0017) is finalized in A5
  before C6 codegen.

## Milestones

1. A1 + A2 → all stores/registries standalone with boundary tests.
2. B1–B3 + A3 → Provider/Assembler/Meter/Fleet/Loop live against the daemon.
3. A4 → interfaces sealed, Q5 at 100%.
4. A5 → engine headless-driveable via `curl`/generated client (ADR-0002).
5. C6–C8 → TUI with live token meter.
