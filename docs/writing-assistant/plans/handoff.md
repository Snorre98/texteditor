# Handoff — start implementing

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Read first (in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` — the build order
   (Plan A engine, B serving, C TUI, D router seam; Track 2 in
   `implementation-sequence-future.md`). This decides *order only*, never *what*.
2. `docs/writing-assistant/architecture.md` — the arc42 overview (§2 constraints,
   §5 modules, §9 ADR index).
3. `docs/writing-assistant/contracts/` — `interface.md` (exact Go interfaces +
   pure DTOs), `module-boundaries.md` (module table + dependency graph),
   `data-model.md` (SQLite/manifest schemas), `failure-semantics.md`,
   `state-machine.md`, `concurrency-topology.md`.
4. The ADRs in `docs/writing-assistant/adr/` (0001–0030) for any module you touch
   — they are normative.
5. `docs/writing-assistant/behaviors/*.feature` — the Gherkin acceptance contracts
   each phase must satisfy.

`docs/writing-assistant/research/` is **background only, not authority** (vision,
parked router). Do not implement from it.

## Authority rules

- **Accepted ADRs are immutable.** Do not edit them. If you find an ADR and a
  contract disagree, or a build step seems impossible under the constraints,
  **stop and report the contradiction** — never silently pick a side or work
  around it.
- Contracts (`interface.md`, `data-model.md`, etc.) are the normative spec; code
  must match them exactly.
- The plan references ADRs only as "what to build"; if the plan and an ADR
  conflict, the ADR wins.

## Hard constraints (never violate)

- **Single static Go binary, no CGO** (ADR-0003). SQLite via `modernc.org/sqlite`.
- **Serving is pure llama.cpp / MLX on Metal** (ADR-0030) — no Ollama, no LM
  Studio, no CPU/CUDA path. The engine reaches serving only via
  `Fleet → control daemon` (ADR-0025/0027); it never reads `models.json` or calls
  `serve.sh`.
- **Sealed Go interfaces over pure DTOs** (ADR-0016/0027): shared DTOs live in a
  neutral `shared`/`dto` package; no module reaches another's types/tables/state.
  REST exists only at process boundaries.
- **Contract-first** (ADR-0002/0017): the OpenAPI spec (codegen `ogen`) is
  authored before the server; clients are generated, dumb, no domain logic.
- **Test at the boundary** (Q5, ADR-0022): every public op gets a stub-backed test
  before advancing.

## FIRST — two technical spikes (before trusting the build order)

Do these before A2+; report findings, do not assume.

**Spike 1 — `sqlite-vec` under `modernc.org/sqlite` (the load-bearing one).**
ADR-0003 (no-CGO) + ADR-0004/0020 (sqlite-vec KNN for the Retriever) may be in
tension: sqlite-vec is a C/SIMD loadable extension. Prove it loads and a `vec0`
table answers a KNN query inside pure-Go SQLite. If it does **not**: do **not**
switch to CGO (reverses ADR-0003) or drop KNN (reverses ADR-0004/0020) — report
the finding and the options; the fallback decision is the user's.

**Spike 2 — `ogen` SSE fidelity + the bundled tokenizer (ADR-0024).**
(a) Generate a minimal OpenAPI spec with a `text/event-stream` SSE endpoint and
confirm `ogen` emits typed streaming handlers as ADR-0017 claims. (b) Confirm a
pure-Go per-family tokenizer can count a real model family's reasoning prefix (the
ADR-0024 thinking fallback). Report what actually works vs. what needs adjustment.

## Then proceed

Follow `implementation-sequence.md` exactly: **A1 data layer → A2 repository →
A3 service → A4 API lock → A5 controller → B serving (parallel) → C TUI →
D router seam (off-by-default)**. Milestone gates (Q1–Q5 + `*.feature` scenarios)
are in the plan's verification table. The router (`toolCalling`) defaults to
`native`; build only the seam, never enable Needle (D1 is explicitly deferred).

## Report back

After the spikes, and again at each milestone, summarize: what landed, which tests
pass, and any place the docs forced a stop or a judgment call.
