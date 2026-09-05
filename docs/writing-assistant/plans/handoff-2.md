# Handoff — resume implementation (session 2)

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are (session 1 landed + reported)

The two technical spikes are **done and resolved**; `A1` (data layer) is complete;
`A2` (repository layer) is **in progress**. No CGO anywhere; everything builds
with `CGO_ENABLED=0`.

### Spike outcomes (do not re-run; trust these, verify only if you touch the code)
- **Spike 1 — sqlite-vec under `modernc.org/sqlite` WORKS.** `modernc.org/sqlite`
  v1.58.0 ships a CGo-free `vec` subpackage; blank-import `_ "modernc.org/sqlite/vec"`
  auto-registers sqlite-vec v0.1.9. `vec0` tables + `MATCH … AND k = ?` KNN
  verified on darwin/arm64, no CGO, self-contained in the static binary. Honors
  both ADR-0003 and ADR-0004/0020 — no fallback needed. **Consequence to record in
  any packaging/licensing work you touch: sqlite-vec is MIT-licensed** (distinct
  from modernc's BSD-3 and SQLite's public domain); the MIT notice ships with the
  binary.
- **Spike 2a — ogen emits typed SSE *clients only*.** Server-side SSE response
  encoding is unimplemented (issue #1742). `x-ogen-raw-response: true` on the
  `text/event-stream` media type yields `RawHandler.StartTurn(ctx, req, w http.ResponseWriter) error`.
  This contradiction with ADR-0017 §1 is documented in a new draft ADR (below).
- **Spike 2b — bundled tokenizer WORKS.** `github.com/tiktoken-go/tokenizer`
  (pure Go) with `cl100k_base` counts a reasoning prefix; for exact per-family
  fidelity use `github.com/sugarme/tokenizer` (`bpe.NewBpeFromFiles(vocab, merges)`).

### New artifact — draft ADR-0031 (PROPOSED, needs review)
`docs/writing-assistant/adr/0031-sse-server-hand-framed-ogen-scope.md` — "SSE
server transport is hand-framed; ogen scope clarified." It supersedes ADR-0017 §1's
rationale (typed handler claim) and records the `x-ogen-raw-response` usage. **The
authority rule (accepted ADRs are immutable) means I must NOT have edited ADR-0017
myself; this is a *new* ADR for the user to adopt.** Flag it to the user for
adoption before you lean on it.

### Decisions already made with the user (do not re-ask)
1. **SSE route**: keep ogen for the typed non-streaming REST surface; `/turn` uses
   `x-ogen-raw-response` + a thin hand-framed SSE state handler in the API server
   (typed `Event` DTOs; event vocab stays in the spec for client codegen).
2. **Spike-1 fallback** (only if modernc vec ever fails on a target): `ncruces/go-sqlite3`
   (wazero, no CGO). Pre-authorized.
3. **Go module path**: `module texteditor` (root `go.mod`); neutral DTOs at
   `texteditor/shared/dto`.

### Open contract gap (MUST resolve at A4 — do not silently choose)
`TokenMeter.Attribute(ctx, turnID, breakdown, counts)` (interface.md §6) supplies
no `session_id`/`model`, but `meter.db.meter_events` requires both (data-model.md
§1.3) and failure-semantics/ADR-0028 require them populated. The loop already holds
both (`task.SessionID`, `Resolution.UsedName`). At A4 the API lock you must extend
the signature (or otherwise source these) and note the interface.md amendment
explicitly. This is a contract gap between two normative docs.

## Landed so far (do not redo)

- `go.mod` (module `texteditor`, Go 1.26.5), `.gitignore`, deps: `modernc.org/sqlite`
  v1.58.0, `github.com/go-git/go-git/v5`, `github.com/tiktoken-go/tokenizer`.
- **A1.1** `shared/dto/` — full ADR-0027 catalog: `fleet.go` (Capabilities,
  SamplingParams, Model, Target, LiveState, ResolveOpts, Resolution, Completion,
  RawEvent), `context.go` (Message, BlockKind, Block, Document, Chunk),
  `assembler.go` (AssemblerInput, Breakdown, Payload, ProviderCounts,
  AttributedBreakdown), `mode.go` (ContextBudget, Mode, ToolDef), `loop.go`
  (Selection, TurnOptions, Task), `router.go` (Decision, RouterContext, RouterUsage,
  RouterResult), `session.go` (Session), `event.go` (Event), `document.go`
  (TextFormatterIssue, Guard, BlockEdit, Revision, Candidate, WordEdit). JSONSchema
  = `json.RawMessage` alias in `doc.go`.
- **A1.2** `internal/sqlmigrate/` (versioned migration runner over `PRAGMA user_version`)
  + per-module schemas: `internal/document/schema.go` (app.db), `internal/retriever/schema.go`
  (index.db; FTS5 + vec0 with caller-supplied emb dim), `internal/meter/schema.go`
  (meter.db), `internal/session/schema.go` (sessions.db). All have `*_test.go`
  passing.
- **A1.3** `internal/document/git.go` — `historyStore` (go-git bare repo + engine
  working tree; `writeFile`, `commit`, tree/blob storage, append-only history).
  `git_test.go` passes. **Note: blocks table has NO text column; canonical text is
  in the worktree file (ADR-0020 §2) — `Blocks()` must reconstruct text from the
  worktree.**
- **A1.4** `config/modes/{proofreader,editor,drafter,grammar}.json` + `config/tools/
  {edit_markdown,retrieve,read_note,diff}.json` + `config/schemas/{mode,tool}.schema.json`.
- **A1.5** `docs/writing-assistant/contracts/assets/fleet-manifest.schema.json`
  (two-tier, runner enum `llama.cpp|mlx-lm|mlx-vlm|delegate`; source.kind `hf|gguf|needle`).
- **A2 (partial)** — pure leaves done + tested: `internal/textformatter/` (Normalize/
  Validate/Format, ADR-0029) and `internal/chunker/` (Chunk, ADR-0020 §5). Also
  `internal/tool/` (Registry: Register/List/AllowlistFor with duplicate + reserved
  `request_tool` guards; `ExecutorImpl` name-keyed handler map with `Bind`/`Invoke`/
  `HandlerNames`).

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` — build order (Plan A
   engine, B serving, C TUI, D router seam; Track 2 in `implementation-sequence-future.md`).
   Decides *order only*, never *what*.
2. `docs/writing-assistant/architecture.md` — arc42 (§2 constraints, §5 modules, §9 ADR index).
3. `docs/writing-assistant/contracts/` — `interface.md`, `module-boundaries.md`,
   `data-model.md`, `failure-semantics.md`, `state-machine.md`, `concurrency-topology.md`.
4. ADRs in `docs/writing-assistant/adr/` (0001–0030, plus the new draft 0031) for
   any module you touch — normative.
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

## What to do next (pick up A2 exactly here)

Finish **A2 — repository layer**, each store standalone, exposed only through its interface:

1. **A2.1 Document store** (NOT done — `internal/document/` currently has only
   `schema.go` + `git.go`). Implement `DocumentStore` (interface.md §9): `Open`,
   `Save`, `Blocks`, `ApplyEdit` (normalize via TextFormatter + verify `BlockEdit.Guards`
   atomically → stage candidate, `guard-failed`/`invalid-structure` typed errors),
   `Commit`, `Diff`, `History`, `Candidates` — per ADR-0016 §9 + ADR-0020 + ADR-0029.
   **Depends on TextFormatter (already built) — Document store is NOT a leaf.**
   It owns the block↔worktree-range mapping (reconstruct `Blocks()` text from worktree).
2. **A2.2 Session store** (NOT done — only `schema.go`). Implement `SessionStore`
   (interface.md §10, ADR-0026 §2): `ListByDocument`, `Create` (create-or-resume on
   `(document_id, anchor_block_id)`), `Resume`, `Append`, `History`.
3. **A2.3 Mode registry** (NOT started). `internal/mode/`: `List`/`Get`; load +
   validate `config/modes/*.json` (go:embed) against `config/schemas/mode.schema.json`,
   fail-fast with `mode-refs-unknown-model`/`mode-unreachable-no-tag`/
   `mode-refs-unknown-tool`/`schema-invalid` (ADR-0019 §2). Validate `toolCalling`
   field now (default `native`); **the two router gates (`mode-refs-router-unavailable`,
   `router-tools-stale`) run at the composition root, NOT here** (see
   implementation-sequence.md "Sequencing note"). Mode registry stays a leaf.
4. **A2.4 Tool registry + executor** — registry DONE (`internal/tool/`); still need
   the startup cross-check logic (registered name ↔ bound handler → `tool-has-no-handler`)
   if not already wired at the composition root.
5. **A2.5 Chunker** — DONE.
6. **A2.6 TextFormatter** — DONE.
7. **A2.7 Retriever** (NOT started). `internal/retriever/`: `Query`/`Index`
   (interface.md §3, ADR-0016 §8, ADR-0004 §3). Embed path depends on
   `Provider.Embed`/`Fleet.Resolve` **interfaces — build against stubs here**, wire
   in A3. `Index` calls the Chunker then writes index.db (vec + FTS).

Then continue the sequence: **A3 service → A4 API lock → A5 controller → B serving
(parallel) → C TUI → D router seam (off-by-default; D1 Needle enablement deferred).**

## Report back

At each milestone summarize: what landed, which tests pass (`CGO_ENABLED=0 go test ./...`),
and any place the docs forced a stop or a judgment call.
