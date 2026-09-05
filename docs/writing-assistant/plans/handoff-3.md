# Handoff — resume implementation (session 3)

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are (session 2 landed + reported)

`A2` (repository layer), `A3` (service layer), and `A4` (public API lock) are
**complete and tested**. `A5` (controller) and **initial Plan B** (serving) are the
next work — B is pulled forward *in parallel with A5* specifically to resolve the
A3→B dependency that session 2 left as a stub (see "The A3→B dependency" below). No
CGO anywhere; everything still builds with `CGO_ENABLED=0`.

### Spike outcomes (unchanged; do not re-run unless you touch the code)
- sqlite-vec under `modernc.org/sqlite` works (v1.58.0, `vec` subpackage, v0.1.9),
  no CGO. **sqlite-vec is MIT-licensed** — the MIT notice ships with the binary.
- ogen emits typed SSE *clients only*; server-side SSE is hand-framed — **ADR-0031
  is now `Accepted`** (staged, not committed), superseding ADR-0017 §1's rationale.
- Bundled tokenizer works: `tiktoken-go/tokenizer` (`cl100k_base`).

### Decisions already made with the user (do not re-ask)
1. **SSE route**: ogen for typed non-streaming REST; `/turn` uses
   `x-ogen-raw-response` + a thin hand-framed SSE handler (ADR-0031, Accepted).
2. Sector fallback (if modernc vec fails on a target): `ncruces/go-sqlite3` (wazero).
   Pre-authorized.
3. Go module path: `module texteditor`; neutral DTOs at `texteditor/shared/dto`.
4. **A4 scope**: stop at the API lock; A5 is a separate session (this one).

## Landed so far (do not redo)

### A2 — repository layer (complete)
- **A2.1 Document store** — `internal/document/store.go` + `markdown.go`.
  `DocumentStore`: `Open`/`Save`/`Blocks`/`ApplyEdit`/`Commit`/`Diff`/`History`/
  `Candidates`. Owns `app.db` + per-document git repo + worktree (**no text column**;
  `Blocks()` reconstructs text from the worktree). `ApplyEdit` normalizes (TextFormatter)
  + verifies `BlockEdit.Guards` atomically → `guard-failed` naming changed blocks;
  stages a `candidates` row. `Commit`/`Save` format. `Diff` is word-level (internal
  LCS, no extra dep). Per-document edit serialization (`ADR-0026 §4`).
- **A2.2 Session store** — `internal/session/session.go`. `ListByDocument`/`Create`
  (create-or-resume on `(document_id, anchor_block_id)`)/`Resume`/`Append`/`History`.
- **A2.3 Mode registry** — `internal/mode/registry.go` + **`config/embed.go`** (new
  `config` package, `go:embed` of `config/{modes,tools,schemas}/*.json`). `List`/`Get`;
  validates against `mode.schema.json` (dep added: `santhosh-tekuri/jsonschema/v5`).
  Typed errors `schema-invalid`/`mode-refs-unknown-model`/`mode-unreachable-no-tag`/
  `mode-refs-unknown-tool`; `toolCalling` defaults `native`. Router gates stay at the
  composition root. Judgment call: mode data's `$schema` meta-keyword is stripped
  before validation (committed schema is `additionalProperties: false`).
- **A2.4 Tool cross-check** — `internal/tool/crosscheck.go`: `VerifyHandlers` →
  `tool-has-no-handler` (composition-root startup check).
- **A2.7 Retriever** — `internal/retriever/retriever.go`. `Query` (embed via sealed
  `Resolver`/`Embedder`/`BlockReader` seams + vec0 KNN + FTS5) and `Index` (Chunker →
  index.db; embed dim **derived from embed output** at first embed).

### A3 — service layer (complete)
- **A3.1 Provider** — `internal/provider/provider.go`. Pure REST/SSE leaf:
  `Chat`/`Stream`/`Embed`, `token`/`done`/`error` framing, retry/backoff (3×,
  250ms→2s, no retry on 4xx), per-server serialization. `httptest`-backed.
- **A3.2 Assembler** — `internal/assembler/assembler.go`. Pure: `Assemble` →
  `Payload` + deterministic `Breakdown` (documented bytes/4 unit); history/RAG
  truncation to `ContextBudget`; thinking=0 (meter owns reconciliation, ADR-0024).
- **A3.3 Meter** — `internal/meter/meter.go`. Amended `Attribute(ctx, turnID,
  sessionID, model, breakdown, counts)`; scale-to-total (exact-sum Q1); `meter_events`
  rows; approx-labeling; one `meter` event via injected `Emitter` seam.
- **A3.4 Fleet** — `internal/fleet/fleet.go`. `FleetGateway` interface + **two
  constructors**: `NewStub` (in-memory, fallback ladder per ADR-0015 implemented and
  tested) and `NewDaemon(baseURL)` **placeholder returning `daemon-unreachable`** —
  this is the stub the handoff plans to replace in this session.
- **A3.5 Loop** — `internal/loop/loop.go`. Thin orchestrator: async `Run(ctx, task)
  → turnID`, session-scoped, `planning → answering → done|error`, token/done/error
  emission via `Emitter`. `Deps` struct holds all injected interfaces. Stub-backed.

### A4 — public API lock (complete)
- Every module interface renamed to its contracted `interface.md §1–§10` name:
  `FleetGateway`, `ProviderGateway`, `Retriever`, `Chunker`, `TextFormatter`,
  `ContextAssembler`, `TokenMeter`, `AgentLoop`, `DocumentStore`, `SessionStore`,
  `ModeRegistry`, `ToolRegistry`, `ToolExecutor` — `Interface` kept as a local
  type alias in each package (R6 traceability). Q5 coverage gaps closed
  (`DocumentStore.Save`, `Fleet.ListModels`, `Fleet.Start/Stop`).

## Open contract gaps (MUST resolve explicitly — do not silently choose)

1. **(NEW, unresolvec) Provider carries no request messages.** `interface.md §2`
   pins `Chat(ctx, target, params)` / `Stream(ctx, target, params, emit)`, but
   neither `Target` nor `SamplingParams` carries the assembled messages — while §5
   produces a "provider-ready" `Payload.Request`. The assembled messages cannot
   reach the provider through the locked signature. Implemented as-written with a
   `NOTE (contract gap, flag for A4)` in `provider.go`. **Resolution needs the same
   treatment as the Meter amendment below** — extend the Provider signature (e.g.
   `Chat/Stream` take the assembled `[]Message` or `Payload.Request`) and amend
   `interface.md §2`. Decide before wiring the loop through a real provider.
2. **(Resolved) `TokenMeter.Attribute`** now carries `sessionID` + `model` — the
   `interface.md §6` amendment is **already edited in the working tree (staged)**,
   matching `data-model.md §1.3` and ADR-0026/0028. Verify the staged
   `contracts/interface.md` diff matches the implemented signature before commit.
3. **(Judgment call, documented) Retriever block source.** `interface.md §3` locks
   `Index(ctx, documentID)`, but `module-boundaries.md` draws no `Doc → Retriever`
   edge; the retriever takes a narrow `BlockReader` seam at construction, to be
   satisfied by the Document store at the composition root.

## The A3→B dependency (this session's reason for pulling B forward)

`A3.4` shipped `internal/fleet/fleet.go` with `NewStub` (in-memory) because the
real `FleetGateway` is the control daemon's HTTP client (ADR-0025), and the daemon
does not exist yet — it is Plan B (in `macos-dev-config`, a *separate repo*). The
loop, retriever, and any `/models/*` route all depend on `FleetGateway`, so **Plan B
must land its daemon (or a faithful in-process stub of its HTTP contract) before the
engine's Fleet gateway can be completed and A5's `/models` + `/turn` routes can be
wired end-to-end.**

Session 3 resolves this by building **A5 (controller) and initial Plan B together**:

- **In this repo**: A5.1 (SSE event bus), A5.2 (API server) — and, once the daemon
  contract is pinned, replace `fleet.NewStub` with `fleet.NewDaemon` (the real HTTP
  client) so the engine reaches serving only via `Fleet → daemon` (ADR-0025/0027).
- **In `macos-dev-config`**: Plan B items 1–5 (manifest + lanes loader, provisioning,
  `serve.sh` executor, the control daemon over the verb contract, Tailscale ACL) —
  see `implementation-sequence.md` Plan B for the exact ordering and ADRs.

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` — build order (Plan A
   engine, B serving, C TUI, D router seam; Track 2 in `implementation-sequence-future.md`).
   Decides *order only*, never *what*.
2. `docs/writing-assistant/architecture.md` — arc42 (§2 constraints, §5 modules, §9 ADR index).
3. `docs/writing-assistant/contracts/` — `interface.md`, `module-boundaries.md`,
   `data-model.md`, `failure-semantics.md`, `state-machine.md`, `concurrency-topology.md`.
4. ADRs in `docs/writing-assistant/adr/` (0001–0031) for any module you touch —
   normative. ADR-0031 is `Accepted`.
5. `docs/writing-assistant/behaviors/*.feature` — Gherkin acceptance contracts.

`docs/writing-assistant/research/` is background only, not authority.

## Hard constraints (never violate)

- Single static Go binary, no CGO (ADR-0003). SQLite via `modernc.org/sqlite`.
- Serving is pure llama.cpp / MLX on Metal (ADR-0030) — no Ollama/LM Studio/CPU/CUDA.
  Engine reaches serving only via `Fleet → control daemon` (ADR-0025/0027); never
  reads `models.json` or calls `serve.sh`.
- Sealed Go interfaces over pure DTOs (ADR-0016/0027): shared DTOs in `shared/dto`;
  no module reaches another's types/tables/state. REST only at process boundaries.
- Contract-first (ADR-0002/0017): OpenAPI (ogen) authored before the server; clients
  generated, dumb.
- Test at the boundary (Q5, ADR-0022): every public op gets a stub-backed test before
  advancing. Build/test with `CGO_ENABLED=0 go test ./...`.

## What to do next

### A5 — Controller layer (this repo)

1. **A5.1 SSE event bus** — `internal/eventbus/`. `EventBus` (interface.md §11):
   `Emit(event)`, `Subscribe(filter) → <-chan Event`; bounded (256) with
   drop-oldest + `backpressure` event on overflow (concurrency-topology.md §2/§3).
   `dto.Event` already exists. This is a pure in-process fan-out; no HTTP.
2. **A5.2 API server** — thin adapter per ADR-0017 §4/§5: routes table
   (`/health`, `/models*`, `/modes`, `/tools`, `/documents*`, `/sessions*`, `/turn`).
   ogen codegen for the typed non-streaming surface; `/turn` via `x-ogen-raw-response`
   + hand-framed SSE (ADR-0031), subscribing to the event bus with a `turnID` filter
   and correlating one turn's stream to one client connection (ADR-0016 §3).
   `POST /turn` → `Loop.Run` → turnID.

### Plan B — serving (initial, `macos-dev-config`; only as far as resolving A3→B)

Pull forward only the parts the engine's `NewDaemon` client needs to be real:

1. **Fleet manifest data** — `models.json` (two-tier) per `fleet-manifest.schema.json`
   (already at `contracts/assets/fleet-manifest.schema.json`) + the semantic lanes
   loader (ADR-0018 §1/§4). Seed `capabilities`/`defaults`/`modeTags` per ADR-0015.
   Runner enum `llama.cpp|mlx-lm|mlx-vlm|delegate` (ADR-0030 §3); no `ollama`/`lmstudio`.
2. **Control daemon** — HTTP transport over the verb contract (`list|start|stop|status|
   log|reach|provision`, interface.md §12, ADR-0007/0025). **Sole manifest reader**
   (ADR-0027); hands the parsed manifest to `serve.sh`; binds `127.0.0.1` (Tailscale
   ACL when remote, ADR-0021 §3).
3. **Engine side (this repo)** — swap `fleet.NewStub` → `fleet.NewDaemon`: the daemon
   HTTP client implementing `ListModels/Resolve/Status/Start/Stop/Provision` by
   mapping to the verb endpoints, plus `Resolve`'s merge + capability gates + fallback
   ladder against the daemon-returned manifest (the ladder logic already in `fleet.go`
   carries over).

### Then continue the sequence

A5 done + B daemon contract pinned → **C6–C8 TUI** (POC reached) → **D2–D5 router
seam** (off-by-default; D1 Needle enablement deferred) → Track 2 (`implementation-sequence-future.md`,
mandatory: Plan E/F).

## Verification / acceptance gate (unchanged from the plan)

Each acceptance criterion and behavior contract is asserted as a CI gate when its
phase lands (Q1–Q5, router, edit-integrity, sessions — see the table in
`implementation-sequence.md`). Run `CGO_ENABLED=0 go test ./...` after every module.

## Report back

At each milestone summarize: what landed, which tests pass, and any place the docs
forced a stop or a judgment call — especially any decision made to resolve the three
open contract gaps above (Provider messages, Meter amendment verification, Retriever
block source) and the daemon-contract pinning between this repo and `macos-dev-config`.
