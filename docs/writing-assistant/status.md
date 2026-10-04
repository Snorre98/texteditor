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
| Router seam (D2–D5) + enablement seam (D1 minus the ML job) | ✅ committed (`504cd16`) |
| Track 2 — Deployment (E) · Tauri editor (F) | ✅ landed — **frozen** (ADR-0044) |
| Fleet observability surface (ADR-0040 — `/fleet`, batch status, selectors) | ✅ |
| Context-engine refocus (write-through safety, presets, RAG wiring, context inspector, `/locate`, Ratatui TUI) | 🚧 active roadmap — [`plans/implementation-sequence-context-engine.md`](plans/implementation-sequence-context-engine.md) |
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
| SQLite via `modernc.org/sqlite` | ✅ | four per-service files: `app.db`, `index.db`, `meter.db`, `sessions.db` |
| Single OpenAPI/JSON Schema contract | ✅ | `api/openapi.yaml`; codegen → ogen (Go) + Hey API (TS) + `openapi-to-rust` (Rust) |

## Layers

| Layer | Status | Notes |
|---|---|---|
| Layer 3 — Clients (dumb, swappable) | ✅ | TUI v2 (Ratatui, ADR-0046) in progress; OpenTUI + Tauri editor + web frozen; one contract (ADR-0014) |
| API contract | ✅ | 20 routes incl. Track-1.5 + ADR-0038/0040 amendments; the deferred `/sessions/{id}/meter` is intentionally absent |
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
| Agent loop / orchestrator | `internal/loop` | ✅ (router seam ✅; ML enablement 🚧) |
| Mode registry | `internal/mode` | ✅ |
| Tool registry | `internal/tool` | ✅ |
| Context assembler | `internal/assembler` | ✅ |
| Retriever | `internal/retriever` | ✅ (sqlite-vec `vec0` + FTS5 hybrid) |
| Token metering | `internal/meter` | ✅ |
| Document store + versioning | `internal/document` | ✅ (git coarse + block candidates) |
| `ToolDecider` (optional router) | `internal/tooldecider` | ✅ seam; enablement 🚧 |
| Fleet gateway — observability | `internal/fleet` | ✅ `ListStatus` over daemon `status/all` + last-good cache (ADR-0040); daemon-side verb in macos-dev-config (ADR-0007) |

Shipped **modes** (4): `drafter`, `editor`, `proofreader`, `grammar`
(`literature-reviewer` from architecture.md §64 is a future mode, not shipped —
superseded by ADR-0019's "modes are data"). Collapse to prompt presets is
accepted (ADR-0045) but not yet implemented.

Shipped **tools** (4): `diff`, `edit_markdown`, `read_note`, `retrieve`
(`suggest_revision`, `cite`, `search_vault` from architecture.md §65 are future
tools — not shipped; the reserved `request_tool` is the router's synthetic wire
format, never registered).

## Storage & the app database

| Item | Status |
|---|---|
| Document metadata + stable block IDs | ✅ |
| Embeddings (`sqlite-vec` `vec0`, KNN) | ✅ |
| FTS5 full-text index | ✅ |
| Token-metering events + conversation history | ✅ |
| git as the versioning engine | ✅ |

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
chunks, history, thinking, completion length (Q1, ADR-0022). What is not yet
surfaced is the **content** of those components — which messages, which chunks,
what was dropped; the context inspector (ADR-0044, Phase 2) closes that.

## RAG design

| Stage | Status |
|---|---|
| Chunk → embed (`nomic-embed` via Fleet) → `vec0` | 🚧 implemented but **no production caller** — `Retriever.Index` is exercised by tests only; a fresh `index.db` is empty |
| FTS5 full-text index | 🚧 written but never queried — `Query` is vec0-KNN-only; no hybrid fusion, dedupe, or rerank |
| Auto-RAG provenance visible to clients | 🚧 auto-retrieved chunks emit no `rag` event; only tool-invoked retrieval does |
| Literature **bulk ingest** / citation tool | 🚧 thin — per-document `Index` only; no bulk-ingest or `cite`/`search_vault` tool shipped |

## Modularity principles

All five hold ✅ — REST-everywhere, modes/tools as data, one observable choke
point, contract-first, interface-first coupling.

---

## TODO list (actionable, ordered)

The phases (A–F) are detailed in
[`plans/implementation-sequence-context-engine.md`](plans/implementation-sequence-context-engine.md)
(ADR-0044 as extended by ADR-0045–0048).

1. **Phase A — trustworthy write-through (ADR-0047)** — open revalidation + path canonicalization; pre-write conflict check (`file-changed-externally`); no-op re-sync; newest-first, base-validated candidates; guards live on the model path; symlink-safe writes; HTTP-level E2E tests.
2. **Phase B — prompt presets + one pipeline (ADR-0045)** — collapse mode data to name/systemPrompt/defaultModel; `config/pipeline.json`; one agentic loop, all tools, global budgets; park the router seam.
3. **Phase C — real RAG + context inspector (ADR-0044)** — production indexing + vault bulk ingest (`VAULT_ROOT`); hybrid FTS5 + vec0 fusion; auto-RAG `rag` events; labeled truncation; assembler v2 + persisted snapshots + `context` route + `GET /sessions/{id}/meter`.
4. **Phase D — `/locate` (ADR-0048)** — engine-side command parse; normalized exact-then-fuzzy resolver over the open document then the vault; `locate` event + snapshot record; ambiguity picker; anchored guarded edit.
5. **Phase E — Ratatui TUI v2 (ADR-0046)** — `client/tui-rs/`; regenerated Rust client + Rust SSE decoder; preset tabs, meter, context panel, diff/approve, write-through status, bracketed paste, mentions/sessions/cancel; fleet orchestration engine-side + daemon reliability fixes; retire OpenTUI on parity.
6. **Phase F — decision layer (Laya) + thesis validation** — `laya-decider` runner; one global retrieval-gating policy; second meter row; golden-query metrics; model evaluation; budget defaults recorded.
7. **Add CI** — no `.github/workflows` exists, yet the plans frame every acceptance criterion as a "CI gate". Engine `go test ./...`, TUI (`cargo test` for tui-rs; OpenTUI/Tauri gates skipped while frozen); optionally a Gherkin runner for the 13 `.feature` specs (currently prose-only). The build seam is ready: `tools/build-tauri.sh` (ADR-0043) is CI-shaped — no machine-specific paths, frozen lockfile, skippable gates.
8. **Fix provision tooling** — `macos-dev-config/internal/fleetdaemon/provision.go` shells the deprecated `huggingface-cli`; switch to `hf download` (huggingface-hub ≥ 1.27).
9. **D1 ML fine-tune** (deferred by design, trigger-gated; the router seam is parked by ADR-0045) — fine-tune Needle 2 over the `cmd/toolhash` vocabulary → produce `needle2.cact` → `needle-finetune.sh` archives it + records `source.fingerprint` → wiring a mode to the router now requires revisiting ADR-0045. Finalize the `.cact` stdout-format assumption (`needle-facade.md §2`).
10. **`InferenceControl` surface** (architecture.md risk #9) — future sibling interface behind the Provider seam, not a planned phase.
11. **Optional doc-sync** — `macos-dev-config/inference-readme.md` documents the needle2 archive but not the new `serve-needle` facade.
12. **Future tools/modes** — `suggest_revision`, `cite`, `search_vault` tools (per ADR-0045, tools are global; `search_vault` may reuse the `/locate` resolver); preset additions stay three-field data files.
13. **Tauri unfreeze** — requires an explicit decision against ADR-0044 plus Rust codegen regeneration against the then-current OpenAPI spec; deferred chat affordances ("Stop generating", session titles) move with it.

Deferred endpoint note: the bare `/files` read (ADR-0035) still lands only when a
client needs it; `GET /sessions/{id}/meter` is now Phase 2.

## Verification status

- `texteditor`: `CGO_ENABLED=0 go test -count=1 ./...` — green (all 20 modules).
- `macos-dev-config`: `go test ./...` — green (incl. facade parser + daemon needle-projection tests).
- Mirror drift tests pass: `daemon-http.md`, `fleet-manifest.schema.json`, `needle-facade.md`.
- `go run ./cmd/toolhash` hash == `routergate.ToolSetHash`.
- D1 seam committed in `504cd16` — the earlier "uncommitted" note was stale.
- Client suites (`bun test`/typecheck in `client/tui` + `client/tauri`; `cargo test` in `src-tauri`) are claimed green in the plans but were **not re-run** during this review. Tauri suites are frozen with the client (ADR-0044).
