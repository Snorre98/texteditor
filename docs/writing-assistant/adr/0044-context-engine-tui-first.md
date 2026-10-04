# ADR-0044: Context-engine north-star — TUI-first, explainable context

Status: Accepted

Freezes (active development only, not supersession): the Tauri/web-facing parts
of ADR-0013 §2, ADR-0014, ADR-0021, ADR-0037, ADR-0039, ADR-0041 (floating chat
window), ADR-0042, ADR-0043. Those decisions remain accepted and their artifacts
remain in the tree; they receive no new work until an explicit unfreeze.

Extends: ADR-0011 (assembler as the single metered choke point), ADR-0004
(Retriever behind an interface), ADR-0028 (the optional-decider pattern), ADR-0022
(quality goals as measurable scenarios).

## Context

The product's hard problem for long-form academic writing — a master's thesis of
60–80 pages — is not a richer editor. It is **what reaches the model**: which
passages are selected, why, at what cost, and what was left out. The engine is
already shaped for this (a single metered assembler, a Retriever behind an
interface, modes and tools as data, a second-metered-call precedent), but the
as-built pipeline does not yet realize it:

- `Retriever.Index` has **no production caller** — only tests exercise it. A
  fresh `index.db` is empty, so pre-turn retrieval silently returns zero chunks.
- FTS5 is written but never queried: `Query` is vec0-KNN-only. The "hybrid
  retrieval" claim is aspirational.
- Auto-retrieved chunks emit no `rag` event; only tool-invoked retrieval does.
  Clients can show an empty RAG panel while chunks were injected.
- History and RAG truncation is **silent**; only mentions label overflow.
- There is no context inspector anywhere: the assembled payload never crosses
  the API, and no turn-level snapshot exists. Clients see token counts only.
- There is no general decision layer; the only decider is the dormant,
  tool-only `ToolDecider` (ADR-0028), and no mode enables it.

Meanwhile Track 2 built a full Tauri/web editor — sidecar spawn, CORS, capability
adapters, floating chat window, design system — a large maintained surface whose
value is secondary to context quality, and which duplicates engine-domain fleet
orchestration in both client stores.

Forces:

- Single user, local-first; Mac mini M4 / 32 GB / 120 GB/s; one large model
  resident at a time (ADR-0015, ADR-0030). Context size is a latency and memory
  lever, not just a quality one.
- Q1 discipline (ADR-0011/0022): every token that enters a model call must be
  visible. "Visible" must grow from per-component counts to **which content**.
- The thesis workflow needs cross-chapter continuity (retrieval, summaries,
  consistency) — a chat over an indexed vault, not a WYSIWYG editor.
- Dumb clients (ADR-0002/0013): the inspector is engine-owned data; clients
  render it.
- Contract-first (ADR-0017): new surfaces land in `api/openapi.yaml` before any
  client code.
- A typed-decision encoder (Laya) is available as a local Python sidecar; the
  second-metered-model-call pattern already exists (ADR-0028).

## Decision

1. **North star: the engine is a context engine.** Its pipeline is
   `vault → index → retrieve → decision gate → assemble → meter → provider`.
   Editing tools remain first-class capabilities, but context selection,
   budgeting, and explanation are the product.

2. **TUI-first.** The OpenTUI client is the only actively developed client.
   The Tauri desktop editor and web target are **frozen**: landed, tested, kept
   in the tree, excluded from active gates and roadmap. The frontend-swap
   guarantee (ADR-0002/0013) is unchanged; unfreezing requires an explicit
   decision recorded against this ADR.

3. **RAG becomes real before anything else.** Indexing runs in production
   (document open/save plus a vault bulk-ingest surface), retrieval is genuinely
   hybrid (FTS5 BM25 + vec0 KNN fusion), auto-retrieved chunks emit provenance
   through the contract, and truncation is never silent — every drop is a
   labeled record.

4. **The context inspector is first-class.** Every turn persists a context
   snapshot: assembled messages with component and provenance, retrieval and
   decision outcomes, budget accounting, and labeled drops. The snapshot is
   retrievable through the contract and rendered by the TUI; clients never
   reconstruct it. A new behavior contract
   (`behaviors/context-inspector.feature`) is normative.

5. **A decision layer joins the pipeline.** A typed-decision model (Laya)
   performs turn routing (mode/model/retrieve) and per-chunk retrieval gating as
   a **second metered model call**, off by default and enabled per mode. Tool
   routing remains ADR-0028's concern. A missing decision service degrades
   fail-open and is labeled.

6. **Roadmap supersession.** Track 2 is no longer "required, not optional". The
   active roadmap is
   [`plans/implementation-sequence-context-engine.md`](../plans/implementation-sequence-context-engine.md)
   (Phases 1–5); `implementation-sequence-future.md` records the freeze.

## Consequences

- **+** The differentiator is built first: retrieval, gating, budgets, and
  explanation become real and testable against a thesis-sized corpus.
- **+** "Every token is visible" (Q1) is completed: not only how many tokens,
  but which content, why it was kept, and what was dropped.
- **+** The maintained surface shrinks to engine + TUI; fleet orchestration can
  move engine-side (ADR-0040 recorded note) with one consumer.
- **+** Laya adds a metered, local, explainable System-1 layer without touching
  the writer model or the provider contract.
- **−** Tauri/web users get no new features while frozen; the Rust codegen and
  sidecar paths can drift from the OpenAPI spec (regenerate on unfreeze).
- **−** The decision layer adds a local service dependency and per-turn latency;
  fail-open keeps turns working but ungated.
- **−** Persisted context snapshots grow with usage; a retention policy is
  required (recorded as implementation work, not optional).
- **−** Doc churn: the arc42 set, status, traceability, and the Track 2 plans
  all need the freeze recorded in lockstep.

## Alternatives considered

- **GUI-first editor** — rejected: already landed; more editor surface does not
  address retrieval quality, context noise, or explanation.
- **RAG only, no decision layer** — rejected: retrieval noise is the dominant
  context-quality lever; a typed, metered gate is cheap and explainable.
- **Full Laya orchestration up front** (mode, model, tools, chunks) — rejected:
  harder to validate and debug; start gate-only and widen with evidence.
- **Separate RAG service** — rejected: the Retriever/Assembler/Meter seams
  already exist; another process adds operations cost for no gain.
- **Remove the Tauri/web code** — rejected: deletion destroys optionality and
  the frontend-swap proof; freezing is sufficient.
- **Delete the Track 2 plans** — rejected: landed reality is documented history;
  the plans are marked frozen, not removed.

## Deferred / recorded notes

- **Tauri unfreeze criteria**: explicit user decision, recorded against this ADR,
  plus Rust codegen regeneration against the then-current OpenAPI spec.
- **Laya fine-tune** on the author's own decisions if zero-shot gating proves
  weak; the shipped checkpoints work zero-shot but fine-tuning is the documented
  accuracy path.
- **PDF literature ingest** stays out of scope; the vault is markdown-only for
  now.
- **Engine-side fleet orchestration** (ADR-0040 recorded note) is folded into
  Phase 4 of the context-engine plan.
