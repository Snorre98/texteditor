# Status — Academic Writing Assistant

Implementation status of [`architecture.md`](architecture.md), cross-checked
against the ADR set (`adr/`), the build plans (`plans/implementation-sequence*.md`),
and the actual code (`server/`, `client/tui/`, `client/tauri/`, `api/openapi.yaml`,
plus the `macos-dev-config` sibling repo). Markers:

- ✅ **Complete** — landed, tested, consistent with the ADRs.
- 🚧 **TODO** — not done (deferred by an ADR, or an open gap).

Last verified: 2026-09-06.

## Snapshot

| Area | Status |
|---|---|
| Track 1 — Engine (A) · Serving control (B) · TUI (C) | ✅ |
| Router seam (D2–D5) + enablement seam (D1 minus the ML job) | ✅ (D1 **uncommitted**) |
| Track 2 — Deployment (E) · Tauri editor (F) | ✅ |
| D1 ML fine-tune (Needle 2 `.cact` + flip a mode to `router`) | 🚧 deferred by trigger |
| CI automation | 🚧 none |
| `InferenceControl` surface (risk #9) | 🚧 deferred |
| Behavior specs executable (`.feature` → runner) | 🚧 prose-only |

## Stack

| Item | Status | Notes |
|---|---|---|
| Go engine — single static binary, no CGO (ADR-0003) | ✅ | `server/cmd/texteditor`; 20 `internal/*` packages |
| OpenTUI TUI (TS/Solid) | ✅ | `client/tui/` |
| Tauri 2 + Vue 3 + CodeMirror 6 editor | ✅ | `client/tauri/` (landed in Track 2; no longer "later") |
| Model serving — external, over REST | ✅ | reached via the `macos-dev-config` control daemon; runners `llama.cpp \| mlx-lm \| mlx-vlm \| delegate` (ADR-0030 — **no Ollama/LM Studio**) |
| SQLite via `modernc.org/sqlite` | ✅ | four per-service files: `app.db`, `index.db`, `meter.db`, `sessions.db` |
| Single OpenAPI/JSON Schema contract | ✅ | `api/openapi.yaml`; codegen → ogen (Go) + Hey API (TS) + `openapi-to-rust` (Rust) |

## Layers

| Layer | Status | Notes |
|---|---|---|
| Layer 3 — Clients (dumb, swappable) | ✅ | TUI + Tauri editor + web, one contract (ADR-0014) |
| API contract | ✅ | 19 routes incl. Track-1.5 + ADR-0038 amendments; the deferred `/sessions/{id}/meter` is intentionally absent |
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

Shipped **modes** (4): `drafter`, `editor`, `proofreader`, `grammar`
(`literature-reviewer` from architecture.md §64 is a future mode, not shipped —
superseded by ADR-0019's "modes are data").

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
| OpenTUI TUI | ✅ | 6 panels (editor, chat, meter, switcher, RAG, diff); dumb, generated |
| Tauri markdown editor | ✅ | CodeMirror selection bubble, `@codemirror/merge` candidates, autosave (`PUT /documents/{id}/tree`, ADR-0038) |

## Deployment targets

| Target | Status |
|---|---|
| Standalone daemon (launchd, fixed port) | ✅ `tools/build.sh` + `tools/install-daemon.sh` + `deploy/*.plist` |
| Tauri sidecar (spawn + SIGTERM/SIGKILL) | ✅ `client/tauri/src-tauri/src/sidecar.rs`, headlessly tested |
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
chunks, history, thinking, completion length (Q1, ADR-0022).

## RAG design

| Stage | Status |
|---|---|
| Chunk → embed (`nomic-embed` via Fleet) → `vec0` + FTS5 | ✅ |
| Hybrid retrieval + source markers | ✅ |
| Literature **bulk ingest** / citation tool | 🚧 thin — per-document `Index` only; no bulk-ingest or `cite`/`search_vault` tool shipped |

## Modularity principles

All five hold ✅ — REST-everywhere, modes/tools as data, one observable choke
point, contract-first, interface-first coupling.

---

## TODO list (actionable, ordered)

1. **Commit the D1 seam** — texteditor (`cmd/toolhash`, `routergate/contract_mirror_test.go`, `contracts/needle-facade.md`, plan docs) and `macos-dev-config` (`cmd/serve-needle`, `tools/serve-needle.sh`, `tools/needle-finetune.sh`, `docs/contracts/needle-facade.md`, `models.json`, `daemon_test.go`).
2. **D1 ML fine-tune** (deferred by design, trigger-gated) — fine-tune Needle 2 over the `cmd/toolhash` vocabulary → produce `needle2.cact` → `needle-finetune.sh` archives it + records `source.fingerprint` → flip one mode to `toolCalling:"router"` → `router-tools-stale` gate clears. Finalize the `.cact` stdout-format assumption (`needle-facade.md §2`).
3. **Add CI** — no `.github/workflows` exists, yet the plans frame every acceptance criterion as a "CI gate". Add CI for `go test`, `bun test` + typecheck (tui/tauri), `cargo test`; optionally a Gherkin runner for the 9 `.feature` specs (currently prose-only).
4. **Fix provision tooling** — `macos-dev-config/internal/fleetdaemon/provision.go` shells the deprecated `huggingface-cli`; switch to `hf download` (huggingface-hub ≥ 1.27).
5. **`InferenceControl` surface** (architecture.md risk #9) — future sibling interface behind the Provider seam, not a planned phase.
6. **Optional doc-sync** — `macos-dev-config/inference-readme.md` documents the needle2 archive but not the new `serve-needle` facade.
7. **Deferred endpoints** — `GET /sessions/{id}/meter` (ADR-0017), bare `/files` read (ADR-0035); land only when a client needs them.
8. **Future tools/modes** — `suggest_revision`, `cite`, `search_vault` tools and a `literature-reviewer` mode (architecture.md §64–§65), as data files per ADR-0019.

## Verification status

- `texteditor`: `CGO_ENABLED=0 go test -count=1 ./...` — green (all 20 modules).
- `macos-dev-config`: `go test ./...` — green (incl. facade parser + daemon needle-projection tests).
- Mirror drift tests pass: `daemon-http.md`, `fleet-manifest.schema.json`, `needle-facade.md`.
- `go run ./cmd/toolhash` hash == `routergate.ToolSetHash`.
- Client suites (`bun test`/typecheck in `client/tui` + `client/tauri`; `cargo test` in `src-tauri`) are claimed green in the plans but were **not re-run** during this review.
