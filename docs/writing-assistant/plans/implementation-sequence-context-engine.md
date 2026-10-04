# Implementation Sequence — Context Engine (Active)

The active roadmap after [ADR-0044](../adr/0044-context-engine-tui-first.md),
as extended by [ADR-0045](../adr/0045-prompt-presets-one-pipeline.md) (prompt
presets, one pipeline), [ADR-0046](../adr/0046-ratatui-tui-v2.md) (Ratatui TUI
v2), [ADR-0047](../adr/0047-auto-write-through-conflicts.md) (auto
write-through + conflicts), [ADR-0048](../adr/0048-locate-chunk-anchoring.md)
(`/locate`), and [ADR-0049](../adr/0049-context-management-corpus-tray.md)
(workspaces, multi-root corpus, context tray, workspace-sharded context
storage); the reader pane is added by
[ADR-0050](../adr/0050-tui-reader-pane.md) (read-only rendered markdown,
editor-extensible), and reasoning/context budgets by
[ADR-0051](../adr/0051-reasoning-policy-context-window-budgets.md) (thinking
policy + bounded escalation, per-turn window gate, session budget + compaction,
exact thinking metering); and the engine-owned context lifecycle + liveness feed
by [ADR-0052](../adr/0052-engine-owned-context-lifecycle-liveness-feed.md)
(one-verb bootstrap/resume/accept, a non-turn event feed, no client polling or
sequencing). Track 2 (deployment + Tauri editor) is **landed and frozen**
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

**Execution order (agreed 2026-10-04).** Phase D is in flight and E1 may run in
parallel with it. The sequence is **D → E1 → C5 → E2 → E3 → E4**: E1 (transport +
walking skeleton) consumes neither D nor C5; C5 (ADR-0051) is an engine phase
placed before E2 so the TUI renders its thinking/budget labels; E2/E3 consume
D's locate surfaces and C5's thinking/budget surfaces; E4 (ADR-0052) makes the
client render-only (engine-owned bootstrap/resume/accept + a liveness feed) and
is sequenced after E3 so it consumes the engine-side fleet orchestration E3
lands. OpenTUI stays frozen in-tree throughout. Handoff prompts:
[`handoff-e1.md`](handoff-e1.md), [`handoff-c5.md`](handoff-c5.md),
[`handoff-e2.md`](handoff-e2.md), [`handoff-e3.md`](handoff-e3.md).

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

Recorded follow-up (ADR-0051): `config/pipeline.json` and its schema gain the
`thinking` default, `reserveOutputTokens`, `sessionBudgetSoftRatio`, and the
`compaction` object — data-only, presets stay three-field. The behavior lands in
Phase C5.

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
7. **Reasoning policy + context budgets (ADR-0051)** — split out as
   **Phase C5** below (engine; sequenced between E1 and E2); it is not part of
   the landed C1–C4 set.

Gate (C1–C4): open a vault → workspace registered + shard populated → multi-root
scope and idempotent eviction behave → auto-RAG provenance visible → any turn
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

## Phase E1 — Ratatui transport + walking skeleton (ADR-0046)

May start immediately, in parallel with Phase D. Scope is the client's
transport and a usable chat loop — no corpus tree, no tray, no reader, and no
C5 labels; those land in E2/E3 against the fixed engine.

1. **Crate** `client/tui-rs/`: Ratatui + crossterm, plain cargo (no Tauri CLI,
   no Node/Bun), tokio bridge (async generated calls + the `/turn` byte stream
   → a UI channel; the render loop stays synchronous and render-only).
2. **Regenerated Rust client**: the crate owns its own `openapi-to-rust.toml`
   and committed `src/generated/` (fixes the stale `/fleet` gap; includes the
   C1–C4 routes). Tauri's generated tree stays untouched.
3. **Rust SSE decoder** per ADR-0031: framing, per-event serde dispatch, unknown
   or invalid events labeled and skipped, terminal stop. It must tolerate the
   future `locate` (D) and `thinking` (C5) events as unknown until E2.
4. **Discovery**: `ENGINE_URL` > numeric `ENGINE_PORT` >
   `http://127.0.0.1:9100`, `/health` probe, `baseUrl` adoption.
5. **UI v1**: chat with streaming, preset tabs (`GET /modes`), diff preview +
   approve (`POST /edits` → `/commits`), status line (connection, resolved
   model, target file, write-through/conflict), bracketed paste. No editor and
   no manual save — approve is the write boundary (ADR-0047).
6. **Bootstrap**: open a document (`POST /documents`), create/resume a session,
   `POST /turn` (`workspaceId` optional; the canonical-parent fallback covers
   pre-workspace flows).
7. **Build**: `tools/build-tui-rs.sh` (plain `cargo build`; the external-SSD
   `RUSTUP_HOME`/`CARGO_HOME` exports documented, never auto-set — ADR-0043).

Gate: `cargo test` and the build script are green; a live-model run chats,
streams, produces a candidate, approves it, and the file on disk changes (or a
conflict is labeled).

---

## Phase C5 — Reasoning policy + context-window budgets (ADR-0051)

Engine phase, sequenced **before E2** (the TUI renders its labels); it may run
after or in parallel with E1. Full scope from ADR-0051:

1. **Thinking policy** `off|auto|on`: pipeline default
   (`config/pipeline.json`), session policy + `Task.context` override, never a
   preset field. `auto` runs thinking-off first and retries **once** with
   thinking-on after a structured failure (`invalid-structure`, `guard-failed`,
   or no tool call and no answer), labeled `thinking-escalated`, bounded by the
   step cap.
2. **Runner mapping** with labeled degradation: mlx-lm
   `chat_template_kwargs: {"enable_thinking": false}`, llama.cpp where jinja
   templates are enabled, OpenAI-compatible `reasoning_effort`/vendor fields; an
   unsupported runner degrades to thinking-on labeled `thinking-unsupported`.
3. **Exact thinking accounting + progress**: the provider surfaces reasoning
   deltas as a raw event; the loop accumulates them; the meter attributes them
   exactly (ADR-0024's approximation remains only when the count is omitted); a
   `thinking` SSE event lets clients show an indicator.
4. **Thinking budget**: a reasoning cap in the pipeline; hitting it emits
   `thinking-truncated`; a `length` turn with neither tool call nor answer is a
   labeled error, never an empty `done`.
5. **Per-turn context-window gate**: priority-ordered assembly against
   `model.capabilities.contextLength` with the output reserve; drops labeled
   `context-window`; fixed + pinned + user alone over the window → typed
   `context-window-exceeded` before any provider call.
6. **Session budget live**: soft ratio warning (`session-budget-soft`), hard
   threshold refusal (`session-budget-exceeded`) unless metered compaction
   succeeds first.
7. **Metered compaction**: oldest turns → one labeled summary (its own meter
   row, thinking-off, bounded); pins and recent turns survive; the snapshot
   records `compacted` + the summarized range.
8. **Measurements**: prompt/thinking/completion tokens, wall-clock latency,
   model + quant, window utilization recorded per turn with the snapshot/meter.
9. **Data**: `config/pipeline.json` + schema gain `thinking`,
   `reserveOutputTokens`, `sessionBudgetSoftRatio`, `compaction
   {enabled,triggerHistoryTokens,keepRecentTurns}` — validated fail-fast.
10. **Contract**: additive OpenAPI-first — `thinking` event, typed budget
    errors, snapshot/meter fields. `behaviors/context-budgets.feature` is
    normative.

Suggested checkpoints: **C5a** thinking policy + runner mapping + exact
accounting + `thinking` event + measurements; **C5b** window gate + soft/hard
budgets + compaction + typed errors.

Gate: a mechanical edit runs thinking-off with exact thinking accounting; an
over-window or over-budget turn is refused or compacted with a label — never
silently truncated; `context-budgets.feature` scenarios pass.

---

## Phase E2 — Context surfaces + engine additions (ADR-0046, ADR-0049, ADR-0050, ADR-0051, ADR-0048)

Requires Phase D (locate) and Phase C5 (thinking/budget labels). Builds the
context-management half of the TUI over E1's transport.

1. **Workspace + sessions**: open/resume a workspace (`GET/POST /workspaces`),
   bounded directory browser (`GET /directories`, typed
   `path-outside-allowed-roots` UX), workspace-scoped session list/resume
   (`GET /sessions?workspaceId=`).
2. **Engine additions (OpenAPI-first)**: optional `title` on
   `CreateSessionRequest` + a rename route, and an explicit
   `POST /turns/{id}/cancel` (turn-scoped cancel registry; labeled terminal
   outcome; partial usage metered).
3. **Reader pane** (ADR-0050): `Block[]` → `\n\n` join → `tui-markdown`,
   toggleable beside chat; the `PUT /documents/{id}/tree` write path stays
   unwired.
4. **Inspector panels**: meter (`meter` event + `GET /sessions/{id}/meter`),
   RAG/context (`rag` + `context` snapshots: messages, chunks, drops, budget,
   `pinned`/`humanOverride` labels).
5. **Context tray**: pin/remove, exclude, editable retrieval query, auto-RAG
   toggle; `PUT /sessions/{id}/context` (pin-for-session) + per-turn
   `Task.context` overrides.
6. **Corpus tree**: multi-root scope, per-document status, index/evict/rebuild
   (`/corpus`, `/corpus/index`, `DELETE /corpus/documents/{id}`).
7. **Mentions + paste**: `@`-mention picker (`Task.mentions`), bracketed paste
   throughout.
8. **Locate UI** (D): `locate` event rendering, ambiguity picker
   (`POST /turns/{id}/locate`), anchored-turn status.
9. **C5 labels**: thinking indicator (`thinking` event), window/budget/
   compaction labels, latency/token readouts from the measurements.

Gate: `cargo test` green; a manual vault run exercises workspace → corpus
index/status/evict → tray pin/exclude → reader → meter/context → mentions →
locate; cancel leaves a labeled terminal state; thinking/budget labels render.

---

## Phase E3 — Orchestration, daemon reliability, parity (ADR-0046, ADR-0040)

Closes the client swap.

1. **Engine-side model orchestration** (ADR-0040 recorded note): on a degraded
   resolve, the engine attempts one flagged, bounded `Fleet.Start(default)` and
   re-resolves; the failure is labeled. The TUI renders `/fleet` and issues raw
   lifecycle verbs only — no busy/poll/switch logic in the client.
2. **Daemon reliability fixes** (`macos-dev-config`), because the lifecycle
   verbs the TUI uses must be trustworthy: pass `NAME` to `serve.sh` (correct
   `serve-<name>.log` + `/log/{name}`), label with the model name not the
   runner, stop through `serve.sh stop` (pkill pattern; detached runners must
   die), reconcile live state by health probe when unknown/after a daemon
   restart, resolve or remove the `SERVE_PORT_<NAME>` hint, and fix the
   delegate log-path mapping.
3. **Build + parity**: `tools/build-tui-rs.sh` gains the test gate; the parity
   checklist (chat, reader, tabs, meter, RAG/context, diff/approve,
   write-through status, fleet render, paste) is verified end-to-end.
4. **Retirement state**: OpenTUI stays **frozen in-tree** (decision recorded;
   it is unreferenced by builds and docs as the active client).

Gate: engine and daemon `go test ./...` green; the parity checklist passes;
approving an edit shows the written path or a conflict.

---

## Phase E4 — Engine-owned context lifecycle + liveness feed (ADR-0052)

Closes the "dumb client" gap: the TUI stops owning selection, sequencing, and
liveness. Sequenced after E3 so it consumes the engine-side fleet orchestration.
OpenAPI-first throughout (ADR-0017); Rust codegen regenerates; Tauri/TS trees
stay untouched while frozen (ADR-0044).

1. **Contract (E4.1).** Add to `api/openapi.yaml`: `POST /open` (bootstrap
   resolver, discriminated directory/document result); `POST
   /documents/{id}/session` (open-or-resume); `POST
   /documents/{id}/blocks/{bid}/accept` (atomic approve); `GET /events` (non-turn
   SSE feed + its event schema). Regenerate ogen + `openapi-to-rust`.
2. **Lifecycle verbs (E4.2).** Handlers reusing existing primitives:
   `apiserver.resolveWorkspaceFor` (`apiserver.go:559-587`),
   `loop.resolveWorkspaceID` (`loop/loop.go:202-226`), session
   create-or-resume + newest-first (`session/session.go:99-127,204-221`), and the
   stage/commit path (`document/store.go:647-848`) for `accept`. No new mutable
   state; the exact `/open` shape and the session-selection rule are pinned at
   implementation.
3. **Liveness feed (E4.3).** Expose the unfiltered bus at `GET /events`
   (`eventbus/eventbus.go:64`); add producers for corpus job progress/completion
   (`corpus/corpus.go:236-320`), `document` external-change/commit via the
   `DocHook` (`corpus/corpus.go:510-543`), and `session` create/rename; add an
   engine-side bounded fleet `ListStatus` poller that emits `fleet` on change.
   Bounded channel; drops labeled `backpressure` (Q1).
4. **Client rewrite (E4.4).** `client/tui-rs/src/bridge.rs`: replace
   `bootstrap`/`resume_or_create`/`approve` choreography with the three verbs and
   subscribe to `GET /events`; delete the corpus/fleet poll loops
   (`ui.rs:56-75`); `state.rs` sheds pointer/flow fields. The reader
   (`GET /documents/{id}/blocks`) and the turn stream are unchanged.
5. **Docs + behaviors (E4.5).** `client-swap.feature` (client sends one verb and
   renders events; accept is atomic; no client sequencing), `context-management.feature`
   (feed progress, no polling), `fleet-observability.feature` (push not poll);
   this plan's parity checklist and the traceability/architecture/status sets gain
   ADR-0052.

Gate: `cd server && CGO_ENABLED=0 go test ./... && go vet ./...` and `gofmt -l
server/` clean; `cd client/tui-rs && cargo test` + `tools/build-tui-rs.sh` green;
`client-swap.feature`/`context-management.feature`/`fleet-observability.feature`
scenarios pass; a live-model run opens a vault path (one `/open`), chats,
approves (one `accept`, file bytes change or a labeled conflict), and corpus
index progress arrives over `/events` with no client poll loop.

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
- Thinking is a policy, not a default: mechanical turns run thinking-off,
  `auto` escalates only after a structured failure, and thinking that happens
  is metered exactly (ADR-0051).
- Context is bounded per turn (model window) and per session (soft/hard
  budget + metered compaction); every drop, escalation, and truncation is
  labeled, never silent (ADR-0051).
- Data over code: presets, pipeline policy, and decision policy are JSON
  config.
- Dumb clients (ADR-0013 §3, completed by ADR-0052): the engine owns selection,
  sequencing, and liveness — bootstrap/resume/accept are single engine verbs and
  non-turn state arrives on the feed; the client sends a verb and renders events
  (overlays, selections, input, pane visibility stay client-side as
  presentation).
