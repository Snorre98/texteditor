# Data model contract

Precise spec for the durable data formats the system owns. Source ADRs: ADR-0016
(per-service SQLite), ADR-0018 (two-tier manifest), ADR-0019 (mode/tool data),
ADR-0020 (block IDs, candidates, chunking), ADR-0036 (mentions meter component),
ADR-0047 (canonical paths + write-through state), ADR-0049 (workspaces,
multi-root corpus, workspace-sharded context storage).

## 1. SQLite app databases — one file per service *instance*

Per ADR-0016 as amended by ADR-0049 §5, SQLite state is partitioned by service
**instance**: global files carry document identity and the workspace registry;
each workspace shard carries only its context state. No SQLite file is shared
across modules, and no cross-workspace aggregation exists.

```
<data>/                         # --data, default ~/.local/share/texteditor
  app.db                        # GLOBAL: documents, blocks, candidates
  git/<docID>.git               # GLOBAL: per-document history
  worktree/<docID>/             # GLOBAL: canonical file per document
  workspaces.db                 # GLOBAL registry: workspace records + corpus scope
  workspaces/<workspaceID>/     # per-workspace shard (context state only)
    index.db                    #   corpus vec0 + FTS + chunks + status
    sessions.db                 #   sessions + messages
    meter.db                    #   meter events
```

Shards open lazily on first use, migrate per shard, and close on LRU eviction
(open-handle cap 4) under a reference-counted lease; a leased shard is never
closed underneath a running turn or corpus job.

### 1.1 `app.db` — Document store

#### `documents` — document metadata

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | surrogate id (UUID) — the REST identity |
| `path` | TEXT UNIQUE | canonical absolute path; the Document store's open resolver |
| `path_key` | TEXT | case-folded canonical path; alias/symlink identity (ADR-0047 §2) |
| `root_block_id` | TEXT | id of the root block (document = tree of blocks) |
| `updated_at` | INTEGER | unix epoch seconds |
| `content_hash` | TEXT | last-known disk content hash (external-change anchor, ADR-0047) |

#### `blocks` — stable block IDs (paragraphs/headings/tables, UUID)

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | **UUID**, minted at creation, stable across edits |
| `document_id` | TEXT FK | → `documents.id` |
| `parent_id` | TEXT NULL | tree structure |
| `kind` | TEXT | `paragraph` \| `heading` \| `list_item` \| `code_fence` \| `blockquote` \| `table` |
| `position` | INTEGER | sibling order |

Block identity (ADR-0020): stable UUID, minted by the Document store; edits carry
the block ID ("replace content of block X"); split/merge mints new IDs. Content
hashes are rejected (not stable across edits).

#### `candidates` — block rewrites (unaccepted edits)

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `block_id` | TEXT → `blocks.id` | the block being rewritten |
| `base_rev` | TEXT | the revision the candidate diffs against |
| `text` | TEXT | proposed replacement text |
| `mode` | TEXT | the mode that proposed it |
| `ts` | INTEGER | |

Candidates are *unaccepted AI edits* (ADR-0020): accepting commits the block's new
content and clears the row; rejecting drops it. They are **not** stored in git.

### 1.2 `index.db` — Retriever (rebuildable projection, per workspace shard)

Lives at `<data>/workspaces/<workspaceID>/index.db` (ADR-0049 §5). A **derived,
rebuildable projection** of versioned documents and corpus files (the Chunker
produces it; `Index`/`IndexPath` rebuild it); it may denormalize chunk text.
Identity (ADR-0049 §4):

- `file_key` keys `indexed_files`: a `documentID` for versioned documents, the
  case-folded canonical path for path-keyed corpus files.
- `chunk_key` keys chunks/vec/FTS: `documentID#index` for versioned documents,
  `path#index` for corpus files.

#### `index_meta` — learned properties

| Column | Type | Notes |
|---|---|---|
| `key` | TEXT PK | e.g. `dim` |
| `value` | TEXT | the embedding dimension the vec table was created with |

#### `indexed_files` — one status row per indexed file

| Column | Type | Notes |
|---|---|---|
| `file_key` | TEXT PK | documentID or canonical path key |
| `path_key` | TEXT | case-folded canonical path |
| `path` | TEXT | canonical absolute path |
| `document_id` | TEXT | empty for corpus files |
| `content_hash` | TEXT | last-indexed content hash (stale detection) |
| `chunk_count` | INTEGER | indexed chunk count |
| `indexed_at` | INTEGER | unix epoch seconds |

#### `chunks` — chunk corpus + provenance

| Column | Type | Notes |
|---|---|---|
| `chunk_key` | TEXT PK | `documentID#index` \| `path#index` |
| `file_key` | TEXT | owning identity (delete/rebuild unit) |
| `path_key` | TEXT | canonical path key |
| `path` | TEXT | canonical absolute path |
| `heading` | TEXT | heading stack / nearest heading |
| `block_id` | TEXT | versioned block id (empty for corpus) |
| `content` | TEXT | chunk text |

#### `blocks_ft` — FTS5 full-text index

| Column | Type | Notes |
|---|---|---|
| `chunk_key` | TEXT | unindexed, → `chunks.chunk_key` |
| `content` | TEXT | indexed text of the chunk |

#### `vec_chunks` — embeddings (`sqlite-vec` `vec0` table)

| Column | Type | Notes |
|---|---|---|
| `chunk_key` | TEXT | metadata column, → `chunks.chunk_key` |
| `embedding` | vec0 | float32 vector, KNN-indexed |

No explicit integer id: vec0 assigns rowids, so per-document index counters
cannot collide across files (the Phase C id-collision fix). The vec table is
created when the embedding dimension is first learned (`index_meta.dim`), after
the base schema; `Query`/`Index`/`IndexPath` ensure it, `Status`/`Evict` do not
need it. Eviction deletes both vec0 and FTS rows by chunk key and is idempotent.

### 1.3 `meter.db` — Token metering (per workspace shard)

Lives at `<data>/workspaces/<workspaceID>/meter.db`; no global meter total
(ADR-0049 §5).

#### `meter_events` — token-metering events

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `ts` | INTEGER | unix epoch ms |
| `session_id` | TEXT | → `sessions.id` (the owning session) |
| `turn_id` | TEXT | groups events into one turn |
| `component` | TEXT | `system` \| `tools` \| `rag` \| `history` \| `mentions` \| `user` \| `thinking` \| `completion` \| `compaction` \| `decision` |
| `prompt_tokens` | INTEGER | attributed prompt tokens |
| `completion_tokens` | INTEGER | attributed completion tokens |
| `approx` | INTEGER | 1 when the component is a labeled approximation (thinking, ADR-0024) |
| `model` | TEXT | logical model name actually used (`usedName`) |

#### `meter_measurements` — per-turn measurements (ADR-0051 §11)

One row per turn (the `compaction` summary call uses `<turnID>:compaction`):
prompt/thinking/completion tokens, wall-clock latency, model + quant, and window
utilization, so the hardware map accumulates per model/quant. Upserted by
`turn_id`.

| Column | Type | Notes |
|---|---|---|
| `turn_id` | TEXT PK | the turn (or `<turnID>:compaction` / `<turnID>:decision:<role>`) |
| `session_id` | TEXT | → `sessions.id` |
| `model` | TEXT | logical model name actually used |
| `prompt_tokens` | INTEGER | provider-reported prompt tokens |
| `thinking_tokens` | INTEGER | exact reasoning count (or the labeled estimate) |
| `completion_tokens` | INTEGER | provider-reported output tokens |
| `latency_ms` | INTEGER | wall-clock turn latency |
| `window_utilization` | REAL | `promptTokens / model.contextLength` |
| `ts` | INTEGER | unix epoch ms |

### 1.4 `sessions.db` — Session store (per workspace shard)

Source ADR-0026 as amended by ADR-0049 §5: lives at
`<data>/workspaces/<workspaceID>/sessions.db`; sessions are workspace-scoped by
shard and `workspaces.db` routes session/turn ids to the shard. Owned by the
Session store only.

#### `sessions` — session entity

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | UUID, client-facing identity |
| `document_id` | TEXT | → `documents.id` |
| `anchor_block_id` | TEXT NULL | → block id; nil = doc-level chat, set = selection/bubble anchor |
| `mode_type` | TEXT | persisted per-session persona |
| `title` | TEXT | human label, auto-derived or user-edited |
| `token_budget` | INTEGER NULL | optional per-session cumulative-token cap |
| `context_policy` | TEXT NOT NULL DEFAULT '' | persisted session context policy JSON (ADR-0049 §8); `''` = none |
| `created_at` | INTEGER | unix epoch seconds |
| `updated_at` | INTEGER | unix epoch seconds |

Many `sessions` rows may share one `document_id`. A `(document_id,
anchor_block_id)` pair is create-or-resume: re-anchoring to the same block
reopens the same session. `context_policy` holds the opaque
`ContextPolicy` JSON (pins/excludes/autoRag/retrievalQuery); the Session store
validates it on set and never interprets it. It is added append-only as the
last migration in `sessionsSchema` (`ALTER TABLE sessions ADD COLUMN`), so an
existing `sessions.db` upgrades in place.

#### `messages` — conversation history (many per session)

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `session_id` | TEXT | → `sessions.id` (was `conversation_id`) |
| `role` | TEXT | `user` \| `assistant` \| `tool` |
| `content` | TEXT | |
| `ts` | INTEGER | |

#### `turn_context` — persisted per-turn context snapshots (many per session)

Source ADR-0044 §4 (Phase C3). One row per turn; the snapshot is opaque JSON
(the Session store never parses it) and the payload of the `context` SSE event
and `GET /turns/{id}/context`.

| Column | Type | Notes |
|---|---|---|
| `turn_id` | TEXT PK | the turn the snapshot belongs to |
| `session_id` | TEXT | → `sessions.id` |
| `snapshot` | TEXT | the serialized `ContextSnapshot` (JSON passthrough) |
| `created_at` | INTEGER | unix epoch seconds |

**Retention:** `SaveContext` keeps only the newest 100 rows per `session_id`
(ordered by `created_at DESC, rowid DESC`) in the same transaction as the
insert — snapshots grow with usage, so the bound is required (ADR-0044 §4).

### 1.5 `workspaces.db` — Workspace store (global registry)

Source ADR-0049 §2/§3/§5. The only global index; owned by the Workspace store.
It never stores document identity (that stays in `app.db`) or per-workspace
context state (that lives in the shard).

#### `workspaces` — workspace records

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | UUID, client-facing identity |
| `root` | TEXT | canonical absolute directory (EvalSymlinks + case-fold) |
| `root_key` | TEXT UNIQUE | case-folded canonical root (alias identity) |
| `name` | TEXT | display label (default: basename of root) |
| `created_at` / `updated_at` | INTEGER | unix epoch seconds |

#### `corpus_roots` — multi-root corpus scope

| Column | Type | Notes |
|---|---|---|
| `workspace_id` | TEXT | → `workspaces.id` |
| `path` | TEXT | canonical absolute root (file or directory) |
| `path_key` | TEXT | case-folded canonical path |
| `position` | INTEGER | order; the workspace root seeds position 0 |

PK `(workspace_id, path_key)`.

#### `corpus_scope` — include/exclude globs

| Column | Type | Notes |
|---|---|---|
| `workspace_id` | TEXT | → `workspaces.id` |
| `kind` | TEXT | `include` \| `exclude` |
| `pattern` | TEXT | glob (`**/*.md` default; hidden dirs excluded by the walker) |

PK `(workspace_id, kind, pattern)`.

#### `corpus_jobs` — observable indexing progress

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | job id |
| `workspace_id` | TEXT | → `workspaces.id` |
| `kind` | TEXT | `reconcile` \| `index` \| `path` |
| `state` | TEXT | `running` \| `done` \| `error` |
| `total` / `completed` | INTEGER | file counts |
| `error` | TEXT | failure label |
| `started_at` / `finished_at` | INTEGER | unix epoch seconds |

Progress is polled through `GET /corpus` (Phase C pin; no SSE stream).

#### `corpus_evicted` / `corpus_errors` — per-path status

Eviction tombstones (status `evicted` until re-indexed) and the last index
error per path (status `error`); PK `(workspace_id, path_key)`.

#### `session_routes` / `turn_routes` — routing index

`session_routes(session_id PK, workspace_id, created_at)` and
`turn_routes(turn_id PK, workspace_id, session_id, created_at)` let
`GET /sessions/{id}/meter` and `GET /turns/{id}/context` resolve a shard with no
cross-workspace scan; `turn_routes` rows are pruned after 30 days.

## 2. Fleet manifest (`models.json` in `macos-dev-config`) — two-tier

Source ADR-0018. Validated against a committed JSON Schema, with **semantic**
invariants (name uniqueness, lanes) enforced by the shared loader — owned by and
invoked only by the control daemon, which hands the parsed manifest to `serve.sh`
(ADR-0025, ADR-0027).

### 2.1 Top level

| Field | Type | Required | Notes |
|---|---|---|---|
| `$schema` | string (uri) | yes | the committed schema |
| `daemons` | `Daemon[]` | yes | the lifecycle units |
| `models` | `Model[]` | yes | the resolve/provision units |

### 2.2 `Daemon`

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | unique; the lifecycle unit `start`/`stop` operate on |
| `runner` | string | yes | `llama.cpp` \| `mlx-lm` \| `mlx-vlm` \| `delegate` |
| `delegate` | string | if `runner`==`delegate` | wrapper script name (e.g. `serve-qwen.sh`) |
| `host` | string | yes | default bind |
| `port` | integer | yes | 1–65535 |

A daemon is always a **dedicated server** over a direct model file (llama.cpp or
MLX); the shared-daemon concept (one port hosting many models) is dropped, and
every runner must use the **Metal** GPU backend (ADR-0030).

### 2.3 `Model`

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | logical name, stable across runners; unique |
| `daemon` | string | yes | → `daemons[].name` serving this model |
| `source` | object | yes, except GUI-managed | how to obtain/run it |
| `source.kind` | string | yes | `hf` \| `gguf` \| `needle` |
| `source.repo` | string | if `hf` | HF repo id (MLX quant, e.g. `mlx-community/…`) |
| `source.file` | string | if `gguf` | filename under `models/gguf/` |
| `source.fingerprint` | string | if `needle` | tool-set hash the `.cact` was fine-tuned against (ADR-0028) |
| `capabilities.contextLength` | integer | yes | tokens |
| `capabilities.thinkingMode` | boolean | yes | emits reasoning tokens |
| `capabilities.supportsSystemPrompt` | boolean | yes | native `system` role |
| `defaults.temperature` | number | no | 0.0–2.0 |
| `defaults.maxTokens` | integer | no | output cap |
| `modeTags` | string[] | no | which modes may select this model |

### 2.4 Invariants

- `daemons[].name` and `models[].name` are each unique.
- `models[].daemon` references an existing `daemons[].name` (or the model is
  invalid).
- Every daemon binds a unique `port`; there are no shared daemons (ADR-0030).
  Every local runner uses the Metal GPU backend; CPU-only/CUDA paths are not
  supported.
- A `modeTag` must name a mode in the Mode registry, or the manifest is invalid.
- **Lanes rule** (semantic, enforced by the shared loader): no two models resolve
  to the same `source` (hf repo or gguf file) on **different** daemons. A conflict
  fails load with `lanes-conflict` naming both entries.

## 3. Mode & tool definitions (data files, engine repo)

Source ADR-0019 as amended by ADR-0045. Live at `config/modes/*.json` and
`config/tools/*.json` in the engine repo, versioned + `go:embed`'d, validated at
startup. The pipeline policy lives in `config/pipeline.json` (§3.3).

### 3.1 Mode (prompt preset)

Since ADR-0045 a mode is exactly three fields; the behavioral fields
(`toolAllowlist`, `params`, `contextBudget`, `maxSteps`, `agentic`, `kind`,
`preamble`, `toolCalling`) are removed from the schema and the shipped files, and
a stale file carrying one fails startup with `schema-invalid`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | unique; also the fallback `modeTag` |
| `systemPrompt` | string | yes | fixed cost per turn |
| `defaultModel` | string | yes | must resolve via the manifest |

### 3.2 Tool

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | unique; the executor's handler key |
| `description` | string | yes | goes into the prompt |
| `parameters` | JSON Schema | yes | the prompt-spliced function schema |

Tools are global (ADR-0045): every registered tool is advertised on every turn;
no mode restricts them.

### 3.3 Pipeline policy

Source ADR-0045 §3. Live at `config/pipeline.json` (embedded, schema-validated at
startup). One policy for every preset.

| Field | Type | Required | Notes |
|---|---|---|---|
| `maxSteps` | integer ≥ 1 | yes | global dispatch/observe bound |
| `maxHistoryTokens` | integer ≥ 0 | yes | history budget; 0 drops all |
| `maxRagTokens` | integer ≥ 0 | yes | auto-RAG budget; 0 drops all |
| `maxMentionTokens` | integer ≥ 0 | yes | mention budget; 0 truncates all (labeled) |
| `autoRagTopK` | integer ≥ 1 | yes | auto-RAG retrieval depth, every turn |
| `thinking` | enum `off`\|`auto`\|`on` | yes | default thinking policy (ADR-0051 §1) |
| `maxThinkingTokens` | integer ≥ 0 | yes | reasoning-token cap; hitting it labels `thinking-truncated` (§5) |
| `reserveOutputTokens` | integer ≥ 0 | yes | window-gate output reserve (§6) |
| `sessionBudgetSoftRatio` | number 0–1 | yes | soft session-budget threshold ratio (§7) |
| `compaction` | object | yes | `{enabled, triggerHistoryTokens, keepRecentTurns}` (§8) |
| `decision` | object | yes | `{mode: off\|planner\|planner+gate, model, gateThreshold, maxCandidates, maxHistoryTurns, breadthTopK{none,few,many}, maxChunkTokens, maxPlannerTokens, timeoutMs}` — global Laya policy, default `off` (ADR-0053, ADR-0055) |

Startup validation failures (typed errors): `mode-refs-unknown-model`,
`mode-unreachable-no-tag`, `tool-has-no-handler`, `schema-invalid` (ADR-0019),
and `pipeline-invalid` (ADR-0045). The mode-level `mode-refs-unknown-tool` gate
is gone with the tool field; the tool registry's `tool-has-no-handler` gate
remains.

## 4. Invariants (cross-store)

- Each SQLite file is owned by exactly one service *instance*; no module
  reads/writes another's file (ADR-0016 as amended by ADR-0049 §5). Global:
  `app.db`, `workspaces.db`, git, worktree. Per workspace shard: `index.db`,
  `sessions.db`, `meter.db`. Supersedes the prior "Document store owns all SQLite."
- Block IDs are stable UUIDs across edits (ADR-0020). Content *hashes* are used
  only as transient guard anchors, never as identity.
- **Canonical-content invariant (ADR-0029):** block content is stored canonical —
  normalized on `ApplyEdit`, opinionated-formatted on `Commit`/autosave — so the
  engine owns formatting and a block's content hash is stable per revision.
- Every `meter_events` row is attributable to exactly one `component`, and a
  `component` set `approx=1` is a labeled approximation, never silent.
- `sessions.db` is the Session store's single-writer file; `messages.session_id`
  references a `sessions.id`, and `meter_events.session_id` groups token cost per
  session (ADR-0026).
- The fleet manifest is read only by the control daemon; the engine never reads
  `models.json` directly, and `serve.sh` receives the parsed manifest from the
  daemon rather than parsing the file itself (ADR-0025, ADR-0027).
