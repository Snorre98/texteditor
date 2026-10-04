# Status — Academic Writing Assistant

Implementation status of [`architecture.md`](architecture.md), cross-checked
against the ADR set (`adr/`), the build plans (`plans/implementation-sequence*.md`),
and the actual code (`server/`, `client/tui/`, `client/tauri/`, `api/openapi.yaml`,
plus the `macos-dev-config` sibling repo). Markers:

- ✅ **Complete** — landed, tested, consistent with the ADRs.
- 🚧 **TODO** — not done (deferred by an ADR, or an open gap).

Last verified: 2026-10-04.

## Snapshot

| Area | Status |
|---|---|
| Track 1 — Engine (A) · Serving control (B) · TUI (C) | ✅ |
| Router seam (D2–D5) + enablement seam (D1 minus the ML job) | ✅ committed (`504cd16`); **parked** (ADR-0045) — packages in-tree, unwired |
| Track 2 — Deployment (E) · Tauri editor (F) | ✅ landed — **frozen** (ADR-0044) |
| Fleet observability surface (ADR-0040 — `/fleet`, batch status, selectors) | ✅ |
| Context-engine refocus (write-through safety, presets, RAG wiring, context inspector, `/locate`, context management — workspaces, multi-root corpus, tray — Ratatui TUI + reader pane) | 🚧 active roadmap — Phases A, B, C landed (C1–C4); D–F remain — [`plans/implementation-sequence-context-engine.md`](plans/implementation-sequence-context-engine.md) |
| D1 ML fine-tune (Needle 2 `.cact` + flip a mode to `router`) | 🚧 deferred by trigger |
| CI automation | 🚧 none |
| `InferenceControl` surface (risk #9) | 🚧 deferred |
| Behavior specs executable (`.feature` → runner) | 🚧 prose-only |

## Stack

| Item | Status | Notes |
|---|---|---|
| Go engine — single static binary, no CGO (ADR-0003) | ✅ | `server/cmd/texteditor`; 20 `internal/*` packages |
| OpenTUI TUI (TS/Solid) | ⏸ frozen | `client/tui/` — replaced by the Ratatui TUI v2 (ADR-0046); retired on parity |
| Tauri 2 + Vue 3 + CodeMirror 6 editor | ⏸ frozen | `client/tauri/` (landed in Track 2; frozen by ADR-0044 — no new work); Tailwind v4 + shadcn-vue (ADR-0042) power the floating chat window |
| Model serving — external, over REST | ✅ | reached via the `macos-dev-config` control daemon; runners `llama.cpp \| mlx-lm \| mlx-vlm \| delegate` (ADR-0030 — **no Ollama/LM Studio**) |
| SQLite via `modernc.org/sqlite` | ✅ | global `app.db` + `workspaces.db` (+ git/worktree); per-workspace context-state shards `workspaces/<id>/{index.db,sessions.db,meter.db}` with lazy open + LRU close (ADR-0049 §5, C1) |
| Single OpenAPI/JSON Schema contract | ✅ | `api/openapi.yaml`; codegen → ogen (Go) + Hey API (TS) + `openapi-to-rust` (Rust) |

## Layers

| Layer | Status | Notes |
|---|---|---|
| Layer 3 — Clients (dumb, swappable) | ✅ | TUI v2 (Ratatui, ADR-0046) in progress, incl. a read-only reader pane (ADR-0050); OpenTUI + Tauri editor + web frozen; one contract (ADR-0014) |
| API contract | ✅ | Track-1.5 + ADR-0038/0040 amendments; C1 added `/sessions` `workspaceId` + rag provenance; C2 added `/workspaces`, `/corpus`, `/corpus/index`, `/corpus/documents/{id}` and the typed ALLOWED_ROOTS 403; C3 landed `/turns/{id}/context` + `/sessions/{id}/meter`, the `context` SSE event, and the typed `NotFound` 404; C4 added `ContextPolicy` (`pinned`/`excluded`/`autoRag`/`retrievalQuery`), optional `Task.context` + `Session.contextPolicy`, `PUT /sessions/{id}/context`, and optional `pinned`/`humanOverride` snapshot labels |
| Layer 2 — Engine | ✅ | all modules below |
| Layer 0 — Model serving | ✅ | via control daemon (ADR-0025/0027/0033), not a raw Ollama port |

## Codegen toolchain

| Tool | Status | Notes |
|---|---|---|
| Go server — `ogen` (SSE support) | ✅ | locked at A5 |
| TS TUI — Hey API + Zod | ✅ | locked at C6 |
| Rust/Tauri — `openapi-to-rust` | ✅ | locked + landed (F6) |
| `tauri-typegen` | 🚧 deferred | covers Rust↔JS IPC, not the Go API (ADR-0003 §23) |

## Layer 2 — Engine modules

| Module | Package | Status |
|---|---|---|
| Provider gateway | `internal/provider` | ✅ |
| Agent loop / orchestrator | `internal/loop` | ✅ one fixed pipeline for every preset (ADR-0045); router seam parked |
| Mode registry | `internal/mode` | ✅ prompt presets (name/prompt/model) |
| Pipeline policy | `internal/pipeline` | ✅ one global turn policy, validated at startup (ADR-0045) |
| Tool registry | `internal/tool` | ✅ all tools global (ADR-0045) |
| Context assembler | `internal/assembler` | ✅ |
| Retriever | `internal/retriever` | ✅ C1: per-workspace shard, heading-aware chunks with provenance, real FTS5 bm25 + vec0 KNN fused with RRF, idempotent eviction, `IndexPath`, `Status` |
| Token metering | `internal/meter` | ✅ per-workspace shard (ADR-0049 §5) |
| Document store + versioning | `internal/document` | ✅ (git coarse + block candidates); global `app.db`; `Path(documentID)` provenance seam |
| `ToolDecider` (optional router) | `internal/tooldecider` | ✅ seam, **parked/unwired** (ADR-0045); enablement 🚧 |
| Fleet gateway — observability | `internal/fleet` | ✅ `ListStatus` over daemon `status/all` + last-good cache (ADR-0040); daemon-side verb in macos-dev-config (ADR-0007) |
| Filesystem (renamed from Workspace) | `internal/filesystem` | ✅ C1: shallow listing + bounded reads, canonicalized, bounded by `ALLOWED_ROOTS` with typed `path-outside-allowed-roots` (ADR-0049 §6) |
| Workspace store | `internal/workspace` | ✅ C1: global `workspaces.db` (registry, corpus roots/scope, jobs, tombstones, routing); create-or-resume by canonical root; most-specific nested resolution |
| Shard manager | `internal/shard` | ✅ C1: lazy open + per-shard migrate + LRU close (cap 4) + reference-counted leases; yields `{Retriever, Sessions, Meter}` |
| Corpus service | `internal/corpus` | ✅ C2: multi-root glob walk (hidden excluded), path-keyed index-only reconcile, per-document status (indexed/stale/pending/evicted/error), async single-flight jobs, idempotent eviction, `DocHook` lifecycle; `internal/glob` matcher |

Shipped **presets** (4): `drafter`, `editor`, `proofreader`, `grammar` — each
exactly `name` + `systemPrompt` + `defaultModel` (ADR-0045 collapse landed;
behavioral mode fields removed). One pipeline serves every preset: all tools are
global, auto-RAG always runs, and the step cap and context budgets live in
`config/pipeline.json`. (`literature-reviewer` from architecture.md §64 is a
future preset, not shipped — superseded by ADR-0019's "modes are data".)

Shipped **tools** (4): `diff`, `edit_markdown`, `read_note`, `retrieve` — all
advertised on every turn (ADR-0045). (`suggest_revision`, `cite`, `search_vault`
from architecture.md §65 are future tools — not shipped; the reserved
`request_tool` is the parked router's synthetic wire format, never registered.)

## Storage & the app database

| Item | Status |
|---|---|
| Document metadata + stable block IDs | ✅ |
| Embeddings (`sqlite-vec` `vec0`, KNN) | ✅ C1: per-workspace shard, `chunk_key`-keyed (no id collision), RRF-fused with FTS5 |
| FTS5 full-text index | ✅ C1: bm25-queried and fused; re-index deletes stale rows first |
| Token-metering events + conversation history | ✅ |
| git as the versioning engine | ✅ |
| Workspace registry + corpus scope (`workspaces.db`) | ✅ C1 (registry + scope persistence); ✅ C2 (routes, status, jobs, tombstones, routing) |
| Per-workspace context-state shards (`index.db`/`sessions.db`/`meter.db`) | ✅ C1 (lazy open + LRU + leases); ✅ C2 (corpus indexing/status/eviction); ✅ C3 (per-turn `turn_context` snapshots + 100/session retention); tray policy 🚧 C4 |

## Layer 3 — Clients

| Client | Status | Notes |
|---|---|---|
| OpenTUI TUI | ✅ | 6 panels (editor, chat, meter, switcher, RAG, diff); dumb, generated; fleet poll + control banner (ADR-0040) |
| Tauri editor — engine side | ✅ | sidecar handshake, Vue store, generated client, autosave, `@codemirror/merge` candidates |
| Tauri editor — UI surface | ✅ | floating, draggable chat window over the workspace (ADR-0041): drag/resize/minimize/snap-dock/persist, session list + resume, sanitized-markdown bubbles, streaming, collapsible meter/RAG, mode + fleet model selectors (ADR-0040/0042); selection trigger via the toolbar button routes into the window's session list |

## Deployment targets

| Target | Status |
|---|---|
| Standalone daemon (launchd, fixed port) | ✅ `tools/build.sh` + `tools/install-daemon.sh` + `deploy/*.plist` |
| Tauri sidecar (spawn + SIGTERM/SIGKILL) | ✅ `client/tauri/src-tauri/src/sidecar.rs`, headlessly tested; bundled by `tools/build-tauri.sh` (ADR-0043) |
| Tauri editor UI | ✅ | floating chat window (ADR-0041) over a full-viewport workspace; Tailwind v4 + shadcn-vue component system (ADR-0042) |
| Web (self-host caveat) | ✅ capability adapter web branch; `ENGINE_BIND` opt-in |
| mDNS LAN discovery | 🚧 deferred (ADR-0021 §1 — `baseUrl` on `/health` is the landed answer) |

## Versioning

| Level | Status |
|---|---|
| Document history (git, coarse) | ✅ |
| Block candidates (fine, per-block) | ✅ |
| Frontend word-diff render (`@codemirror/merge` + tooltips) | ✅ |

## The core learning surface (token metering)

All six levers metered and surfaced ✅ — system prompt & mode, tool schemas, RAG
chunks, history, thinking, completion length (Q1, ADR-0022). C3 completes the
**content** half engine-side: every turn persists a `ContextSnapshot` (per-message
component/provenance, retrieved chunks, labeled drops, per-component budget) and
emits it as the `context` SSE event; `GET /turns/{id}/context` replays it. The
TUI context panel that renders it is Phase E.

## RAG design

| Stage | Status |
|---|---|
| Chunk → embed (`nomic-embed` via Fleet) → `vec0` | ✅ C1: heading-aware chunks (versioned + `ChunkMarkdown` for corpus), per-workspace shard, idempotent per unchanged content; corpus bulk ingest/status 🚧 C2 |
| FTS5 full-text index | ✅ C1: `Query` runs FTS5 bm25 + vec0 KNN fused with RRF (k=60) + dedupe, with path/heading provenance |
| Auto-RAG provenance visible to clients | ✅ C3: the loop emits a `rag` event at turn start (same shape as tool retrieval) and persists the retrieved chunks in the context snapshot; chunks carry provenance |
| Literature **bulk ingest** / citation tool | ✅ C2: multi-root corpus scope + `POST /corpus/index` async bulk ingest + per-document status + idempotent eviction; `cite`/`search_vault` tools remain future (ADR-0045) |

## Modularity principles

All five hold ✅ — REST-everywhere, modes/tools as data, one observable choke
point, contract-first, interface-first coupling.

---

## TODO list (actionable, ordered)

The phases (A–F) are detailed in
[`plans/implementation-sequence-context-engine.md`](plans/implementation-sequence-context-engine.md)
(ADR-0044 as extended by ADR-0045–0051).

1. **Phase A — trustworthy write-through (ADR-0047)** — open revalidation + path canonicalization; pre-write conflict check (`file-changed-externally`); no-op re-sync; newest-first, base-validated candidates; guards live on the model path; symlink-safe writes; HTTP-level E2E tests.
2. ✅ **Phase B — prompt presets + one pipeline (ADR-0045)** — landed: modes are `name`/`systemPrompt`/`defaultModel`; `config/pipeline.json` is the one validated policy (maxSteps + budgets + autoRagTopK); one agentic loop, all tools global, auto-RAG always; router parked/unwired.
3. **Phase C — real RAG + context management + context inspector (ADR-0044, ADR-0049)** — ✅ **C1 landed** (retriever correctness + storage foundation). ✅ **C2 landed** (corpus management surface: `/workspaces`, `/corpus` scope/status/jobs, `POST /corpus/index`, idempotent `DELETE /corpus/documents/{id}`, multi-root glob walk, ALLOWED_ROOTS typed 403, `DocHook` lifecycle). ✅ **C3 landed** (assembler v2 provenance/drops/budget; per-turn `ContextSnapshot` persisted with 100/session retention; auto-RAG `rag` event + `context` SSE; `GET /turns/{id}/context`, `GET /sessions/{id}/meter` typed 404; `RouteTurn`). ✅ **C4 landed** (session context policy + tray overrides: `Task.context` + `PUT /sessions/{id}/context` replace-when-present merge over the persisted `sessions.context_policy`; autoRag/retrievalQuery, `excluded` labeled drops, `pinned` resolution via `Retriever.Get` off the top-k; pins front-loaded and budget-shared, never bypassing budgets; humanOverride labels on snapshot chunks/drops; `tools/smoke-rag.sh` live smoke). 🚧 **C5 planned (ADR-0051)**: thinking policy (`off|auto|on`, one labeled escalation), exact thinking metering + `thinking` event, per-turn context-window gate with labeled drops / typed refusal, `Session.tokenBudget` soft/hard semantics, metered compaction, and per-turn latency/token/window measurements.
4. **Phase D — `/locate` (ADR-0048)** — engine-side command parse; normalized exact-then-fuzzy resolver over the open document then the vault; `locate` event + snapshot record; ambiguity picker; anchored guarded edit.
5. **Phase E — Ratatui TUI v2 (ADR-0046)** — `client/tui-rs/`; regenerated Rust client + Rust SSE decoder; preset tabs, read-only reader pane (ADR-0050 — rendered markdown over the block tree, `tui-markdown`, write path unwired), meter, context panel, diff/approve, write-through status, bracketed paste, mentions/sessions/cancel; workspace open/resume, corpus tree (multi-root scope, status, index/evict/rebuild, allowed-roots UX), context tray (pin/remove, retrieval query, auto-RAG, pin-for-session); fleet orchestration engine-side + daemon reliability fixes; retire OpenTUI on parity.
6. **Phase F — decision layer (Laya) + thesis validation** — `laya-decider` runner; one global retrieval-gating policy; second meter row; golden-query metrics; model evaluation; budget defaults recorded.
7. **Add CI** — no `.github/workflows` exists, yet the plans frame every acceptance criterion as a "CI gate". Engine `go test ./...`, TUI (`cargo test` for tui-rs; OpenTUI/Tauri gates skipped while frozen); optionally a Gherkin runner for the 14 `.feature` specs (currently prose-only). The build seam is ready: `tools/build-tauri.sh` (ADR-0043) is CI-shaped — no machine-specific paths, frozen lockfile, skippable gates.
8. **Fix provision tooling** — `macos-dev-config/internal/fleetdaemon/provision.go` shells the deprecated `huggingface-cli`; switch to `hf download` (huggingface-hub ≥ 1.27).
9. **D1 ML fine-tune** (deferred by design, trigger-gated; the router seam is parked by ADR-0045) — fine-tune Needle 2 over the `cmd/toolhash` vocabulary → produce `needle2.cact` → `needle-finetune.sh` archives it + records `source.fingerprint` → wiring a mode to the router now requires revisiting ADR-0045. Finalize the `.cact` stdout-format assumption (`needle-facade.md §2`).
10. **`InferenceControl` surface** (architecture.md risk #9) — future sibling interface behind the Provider seam, not a planned phase.
11. **Optional doc-sync** — `macos-dev-config/inference-readme.md` documents the needle2 archive but not the new `serve-needle` facade.
12. **Future tools/modes** — `suggest_revision`, `cite`, `search_vault` tools (per ADR-0045, tools are global; `search_vault` may reuse the `/locate` resolver); preset additions stay three-field data files.
13. **Tauri unfreeze** — requires an explicit decision against ADR-0044 plus Rust codegen regeneration against the then-current OpenAPI spec; deferred chat affordances ("Stop generating", session titles) move with it.

Deferred endpoint note: the bare `/files` read (ADR-0035) still lands only when a
client needs it; `GET /sessions/{id}/meter` landed in Phase C3.

## Verification status

- `texteditor`: `CGO_ENABLED=0 go test -count=1 ./...` — green (all 20 modules).
- `macos-dev-config`: `go test ./...` — green (incl. facade parser + daemon needle-projection tests).
- Mirror drift tests pass: `daemon-http.md`, `fleet-manifest.schema.json`, `needle-facade.md`.
- `go run ./cmd/toolhash` hash == `routergate.ToolSetHash`.
- D1 seam committed in `504cd16` — the earlier "uncommitted" note was stale.
- Phase B gates (ADR-0045): `CGO_ENABLED=0 go test -count=1 ./...`, `go vet ./...`, and `gofmt -l server/` all clean; `client/tui` `bun test` (34 pass) + `bun run typecheck` green; the TUI generated client was regenerated against the reduced `/modes` schema.
- Client suites: `client/tui` re-run for Phase B (above); `client/tauri` + `src-tauri` suites remain frozen with the client (ADR-0044) and were not re-run.
- Phase C1 gates: `CGO_ENABLED=0 go test -count=1 ./...`, `go vet ./...`, `gofmt -l server/` clean; `client/tui` `bun test` + `bun run typecheck` green after the additive contract regen. New tests: retriever (stale-FTS, vec-id collision, idempotent eviction, RRF, `IndexPath` idempotency, temp-vault two-root E2E), markdown chunker, Filesystem allowlist/symlink escape, WorkspaceStore (alias/nested/scope), shard manager (lazy migrate/LRU/leases), loop fallback workspace resolution.
- Phase C2 gates: same Go/vet/gofmt gates plus the HTTP E2E `TestCorpusHTTPE2E` (real `workspaces.db` + shard manager + retriever + corpus service over temp vaults: scope set, async index, per-document status incl. stale, idempotent eviction, rebuild, typed ALLOWED_ROOTS refusal on `/corpus` and `/directories`); corpus service tests (default scope/hidden exclusion, reconcile-evict, union-of-roots dedupe, `NotifyChanged`, `DocHook` write boundaries); `internal/glob` matcher tests. TUI regenerated against the additive contract and green.
- Phase C3 gates: `CGO_ENABLED=0 go test -count=1 ./...`, `CGO_ENABLED=0 go vet ./...`, `gofmt -l server/` all clean; `client/tui` `bun test` (35 pass) + `bun run typecheck` green after the additive contract regen. New tests: assembler (per-message provenance, labeled history/RAG/mention drops with counts, budget utilization, payload purity); loop (auto-RAG `rag` event + `context` snapshot persisted and retrievable by turnID, `RouteTurn` written); session (`SaveContext`/`TurnContext` round-trip, newest-100 retention per session); meter (`SessionBreakdown` per-component aggregate + total); apiserver (`GET /turns/{id}/context`, `GET /sessions/{id}/meter`, typed `NotFound` 404s); TUI `context` event decode.
- Phase C4 gates: `CGO_ENABLED=0 go test -count=1 ./...`, `CGO_ENABLED=0 go vet ./...`, `gofmt -l server/` all clean; `client/tui` `bun test` (35 pass) + `bun run typecheck` green after the additive contract regen (Tauri/Rust untouched). New tests: assembler (pinned front-load order + provenance labels, pinned truncation labeled `humanOverride`, zero-budget drops all labels); session (`SetContextPolicy`/`ContextPolicy` round-trip, file-reopen durability, invalid-JSON typed error, unknown-session `ErrNotFound`); loop (tray exclusion filters payload + labeled drop, session pin resolves via `Get` off the top-k and is labeled human override, `autoRag=false` skips `Query` but applies pins, `retrievalQuery` override, explicit-empty pins suppress session pins for one turn, per-turn override does not persist, pins over budget truncate and the meter sum still equals provider totals); apiserver (`PUT /sessions/{id}/context` persists + reads back, `Task.context` decodes incl. explicit-empty preservation, unknown session typed 404). Live smoke: `tools/smoke-rag.sh` (workspace → multi-root corpus → index → turn `rag`+`context` → snapshot fetch → idempotent eviction → allowed-roots refusal).
