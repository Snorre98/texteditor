# Implementation Sequence — Context Engine (Active)

The active roadmap after [ADR-0044](../adr/0044-context-engine-tui-first.md),
as extended by [ADR-0045](../adr/0045-prompt-presets-one-pipeline.md) (prompt
presets, one pipeline), [ADR-0046](../adr/0046-ratatui-tui-v2.md) (Ratatui TUI
v2), [ADR-0047](../adr/0047-auto-write-through-conflicts.md) (auto
write-through + conflicts), [ADR-0048](../adr/0048-locate-chunk-anchoring.md)
(`/locate`), and [ADR-0049](../adr/0049-context-management-corpus-tray.md)
(workspaces, multi-root corpus, context tray, workspace-sharded context
storage). Track 2 (deployment + Tauri editor) is **landed and frozen**
([`implementation-sequence-future.md`](implementation-sequence-future.md));
nothing here resumes it.

This plan references ADRs only as the source of *what* to build; it decides only
the *order*. Phases are sequential unless marked parallel. Each phase ends with
an acceptance gate; the engine is boundary-tested per ADR-0001/0016 and the
contract changes land in `api/openapi.yaml` **before** client code (ADR-0017).
Rust codegen regenerates for the Ratatui TUI (ADR-0046); the Tauri trees stay
untouched while frozen.

The order is **engine-first**: the write boundary and the pipeline are made
trustworthy before the client is rebuilt, so the new TUI is written against a
fixed engine.

---

## Phase A — Approve → file updated, reliably (ADR-0047)

Priority one: the user must be able to trust that an approved edit lands in the
markdown file — or is refused with a labeled conflict. No client rewrite is
needed to verify this.

1. **Open revalidation.** Canonicalize `documents.path`
   (`EvalSymlinks` + case-fold); on an existing path, compare the disk
   hash/mtime to the engine's last-known hash; re-read into the worktree on a
   mismatch and record an `external-change` notice. Aliases resolve to one
   document.
2. **Pre-write conflict check.** Immediately before the atomic rename, compare
   the on-disk hash; mismatch → typed `file-changed-externally` (409) with the
   current hash; explicit opt-in overwrite only.
3. **No-op re-sync.** Remove the silent `canonical == worktree` short-circuit:
   mirror when there is no pending engine change, or report the conflict.
4. **Deterministic, validated candidates.** Newest-first ordering
   (`ts DESC, rowid DESC`); `Commit` re-validates each candidate's base hash
   (`guard-failed`); `edit_markdown` carries and passes the base-hash guard.
5. **Symlink-safe write; derived commit messages; no empty commits.**
6. **Contract.** Commit response gains `writtenThrough`, `path`, `revision`;
   `file-changed-externally` documented; regenerate ogen/TS/Rust.
7. **Tests.** Store-level: symlink, external change, no-op re-sync, ordering,
   stale guard. HTTP-level E2E: open → stubbed turn emits `edit_markdown` →
   approve → file bytes changed; external change → 409, no write. A manual
   live-model smoke script.

Gate: copy from Obsidian → open → rewrite → approve → `cat` shows the change;
edit the file externally mid-turn → conflict, no clobber.

---

## Phase B — Prompt presets + one pipeline (ADR-0045)

1. **Collapse mode data** to `name` + `systemPrompt` + `defaultModel`; rewrite
   the four files; delete the behavioral fields from the JSON Schema.
2. **`config/pipeline.json`** (embedded, validated): `maxSteps`,
   `maxHistoryTokens`, `maxRagTokens`, `maxMentionTokens`, `autoRagTopK`.
3. **One fixed loop.** All registered tools on every turn; one agentic loop
   with the global step cap; remove the per-mode branches. Park the router
   wiring (packages stay in-tree, unwired).
4. **Keep** fail-fast validation (`mode-refs-unknown-model`,
   `mode-refs-unknown-tool`, `mode-unreachable-no-tag`) and `modeTags`
   fallback; `/modes` keeps serving `{name, systemPrompt, defaultModel}`.
5. **Tests.** Every preset can produce an edit (the drafter trap is gone);
   pipeline-policy tests; delete obsolete mode-branch tests.

Gate: each preset runs an edit turn; no behavioral mode fields remain.

---

## Phase C — Real RAG + context management + context inspector (ADR-0044 Phases 1–2, ADR-0049)

Engine-first: workspace/corpus state and the pipeline land before the tray UI.

1. **Indexing lifecycle** — wire `Retriever.Index` into open/save plus a vault
   bulk-ingest surface; per-workspace multi-root corpus scope (roots + globs;
   default `**/*.md`, hidden dirs excluded; the single `VAULT_ROOT` setting is
   superseded by per-workspace scope + `ALLOWED_ROOTS`); index status on
   `/health`.
2. **Heading-aware chunks** with provenance (path, heading path, block ID).
3. **Real hybrid retrieval** — FTS5 `bm25` + vec0 KNN fusion, dedupe,
   threshold; eviction deletes both vec0 and FTS rows idempotently; fix the
   re-index FTS staleness and vec0 id-collision bugs.
4. **Visible auto-RAG** — the same `rag` event shape as tool retrieval;
   labeled history/RAG truncation (no silent drops).
5. **Assembler v2 + snapshots** — per-message component/provenance, labeled
   drops, persisted turn snapshots with retention, `context` SSE,
   `GET /turns/{id}/context`, `GET /sessions/{id}/meter`.
6. **Workspaces + corpus management (ADR-0049)** — sealed `WorkspaceStore`
   (`workspaces.db`: registry + corpus roots/scope); per-workspace shard dirs
   (`workspaces/<id>/{index.db,sessions.db,meter.db}`) with lazy open +
   per-shard `sqlmigrate`; document identity/git stay global; `GET/POST
   /workspaces`; `GET/PUT /corpus` (multi-root scope + per-document status),
   `POST /corpus/index` with observable progress, `DELETE
   /corpus/documents/{id}` (idempotent eviction); `ALLOWED_ROOTS` bounds both
   `GET /directories` and corpus indexing with a typed refusal; `index`
   progress event (Phase C defines none).

Gate: open a vault → workspace registered + shard populated → multi-root scope
and idempotent eviction behave → auto-RAG provenance visible → any turn
explainable after it ends.

---

## Phase D — `/locate` chunk anchoring (ADR-0048)

Depends on Phase C for vault-wide search; open-document search can land
earlier.

1. **Command parse** engine-side: a leading `/locate` line; the pasted chunk
   follows. No `Task` change.
2. **Resolver**: normalize (markdown-stripped, whitespace-collapsed) → open
   document first, then vault index → exact normalized match, then fuzzy
   (trigram/token overlap) with a conservative threshold.
3. **`LocateResult`** (`documentId`, `path`, `blockId`, `span`, `matchType`,
   `confidence`, `context`) + a `locate` SSE event + snapshot record.
4. **Ambiguity picker** wire shape; not-found degrades to plain chat with a
   labeled warning.
5. **Anchored turn**: selection/anchor set, neighbor blocks injected,
   replacement-only instruction, guarded `edit_markdown`, diff, write-through
   (ADR-0047).
6. **Tests** per `locate-anchor.feature`: exact, normalized, fuzzy
   confirmation, duplicates, multi-paragraph, stale text, not-found.

Gate: copy a paragraph → `/locate` → approve → only that block changes in the
file; ambiguity and not-found behave as contracted.

---

## Phase E — Ratatui TUI v2 (ADR-0046)

May start after Phase B if a usable client is needed sooner; otherwise after
Phase D. The corpus tree and context tray consume Phase C's `/corpus` surface
and per-workspace shards — they must not ship against an empty index.

1. **Crate** `client/tui-rs/`: Ratatui + crossterm, plain cargo, tokio bridge
   (async generated calls + stream → UI channel).
2. **Regenerated Rust client** with its own `openapi-to-rust.toml` and
   committed `src/generated/` (fixes the stale `/fleet` gap).
3. **Rust SSE decoder** per ADR-0031 (framing + typed dispatch; label
   unknown/invalid; stop at terminal).
4. **Discovery**: `ENGINE_URL` > `ENGINE_PORT` > `127.0.0.1:9100`; `/health`
   probe and `baseUrl` adoption.
5. **UI**: chat streaming, preset tabs, meter, RAG/context panel, diff/approve,
   status line (target file, write-through/conflict state), bracketed paste,
   `@`-mention picker (engine support exists, ADR-0036), workspace
   open/resume, corpus tree (multi-root scope, per-document status,
   index/evict/rebuild, allowed-roots boundary UX), context tray (assembled
   components with pin/remove, editable retrieval query, auto-RAG toggle,
   pin-for-session), session list/resume scoped to the workspace, cancel
   generation, session titles. No editor panel, no manual save.
6. **Fleet orchestration engine-side** (ADR-0040 recorded note) before or with
   the client; the TUI renders `/fleet` only. Fold in the daemon reliability
   fixes the lifecycle verbs depend on (pass `NAME` to `serve.sh`, stop through
   `serve.sh stop`, health-probe state reconciliation, delegate log mapping).
7. **Build + retirement**: `tools/build-tui-rs.sh` (plain cargo; SSD env
   documented, never auto-set); freeze OpenTUI now, retire it after the parity
   checklist (chat, tabs, meter, RAG/context, diff/approve, write-through
   status, fleet render, paste).

Gate: the parity checklist passes against the fixed engine; approving an edit
shows the written path or a conflict.

---

## Phase F — Decision layer (Laya) + thesis validation

1. **Laya runner** in `macos-dev-config` (`serve-laya.sh`, manifest entry,
   health, stub) — the Needle-facade pattern.
2. **Global decision policy** (ADR-0045): retrieval gating + retrieve-or-not,
   second metered call, fail-open with a labeled degraded record; no per-mode
   config.
3. **Thesis-scale validation**: a real 60–80 page corpus, labeled golden
   queries, recall@k / injected tokens / latency metrics, model evaluation
   (4-bit vs 6-bit), budget tuning recorded in `status.md`.

Gate: gating reduces injected tokens without recall loss; measured defaults
documented.

---

## Cross-cutting rules

- Every new surface is OpenAPI-first; Rust codegen regenerates for the Ratatui
  TUI (ADR-0046) while the Tauri trees remain frozen (ADR-0044).
- Every model call is metered; `/locate` is deterministic and token-free.
- Failures degrade with a label; no silent drops, no silent clobbers.
- Workspace ≠ corpus: the workspace root bounds browsing/editing; the corpus
  is a multi-root set that may reach outside it (ADR-0049).
- Corpus management is index-only: scope changes and eviction touch the
  workspace shard's vec0 + FTS rows and never write document files;
  write-through stays ADR-0047. Context state is workspace-sharded (index,
  sessions, meter); document identity and git stay global (ADR-0049).
- Browsing and corpus indexing are bounded by `ALLOWED_ROOTS`; outside paths
  are typed refusals, never silent (ADR-0049).
- Data over code: presets, pipeline policy, and decision policy are JSON
  config.
