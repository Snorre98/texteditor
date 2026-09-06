# Data model contract

Precise spec for the durable data formats the system owns. Source ADRs: ADR-0016
(per-service SQLite), ADR-0018 (two-tier manifest), ADR-0019 (mode/tool data),
ADR-0020 (block IDs, candidates, chunking), ADR-0036 (mentions meter component).

## 1. SQLite app databases — per-service files

Per ADR-0016, SQLite state is **partitioned by service**, one file per locked
service. No SQLite file is shared across modules.

### 1.1 `app.db` — Document store

#### `documents` — document metadata

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | surrogate id (UUID) — the REST identity |
| `path` | TEXT UNIQUE | absolute path; the Document store's open resolver |
| `root_block_id` | TEXT | id of the root block (document = tree of blocks) |
| `updated_at` | INTEGER | unix epoch seconds |

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

### 1.2 `index.db` — Retriever (rebuildable projection)

#### `blocks_ft` — FTS5 full-text index

| Column | Type | Notes |
|---|---|---|
| `block_id` | TEXT | unindexed, → block id |
| `content` | TEXT | indexed text of the block/chunk |

#### `vec_chunks` — embeddings (`sqlite-vec` `vec0` table)

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | chunk id |
| `document_id` | TEXT | → documents.id |
| `block_id` | TEXT NULL | → block id if the chunk aligns to a block |
| `embedding` | vec0 | float32 vector, KNN-indexed |

`index.db` is a **derived, rebuildable projection** of documents (the Chunker
produces it; `Index` rebuilds it). It may denormalize block text.

### 1.3 `meter.db` — Token metering

#### `meter_events` — token-metering events

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `ts` | INTEGER | unix epoch ms |
| `session_id` | TEXT | → `sessions.id` (the owning session) |
| `turn_id` | TEXT | groups events into one turn |
| `component` | TEXT | `system` \| `tools` \| `rag` \| `history` \| `mentions` \| `user` \| `thinking` \| `completion` |
| `prompt_tokens` | INTEGER | attributed prompt tokens |
| `completion_tokens` | INTEGER | attributed completion tokens |
| `approx` | INTEGER | 1 when the component is a labeled approximation (thinking, ADR-0024) |
| `model` | TEXT | logical model name actually used (`usedName`) |

### 1.4 `sessions.db` — Session store

Source ADR-0026. Dedicated file (renamed from `messages.db`). Owned by the
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
| `created_at` | INTEGER | unix epoch seconds |
| `updated_at` | INTEGER | unix epoch seconds |

Many `sessions` rows may share one `document_id`. A `(document_id,
anchor_block_id)` pair is create-or-resume: re-anchoring to the same block
reopens the same session.

#### `messages` — conversation history (many per session)

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `session_id` | TEXT | → `sessions.id` (was `conversation_id`) |
| `role` | TEXT | `user` \| `assistant` \| `tool` |
| `content` | TEXT | |
| `ts` | INTEGER | |

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

Source ADR-0019. Live at `config/modes/*.json` and `config/tools/*.json` in the
engine repo, versioned + `go:embed`'d, validated at startup.

### 3.1 Mode

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | unique; also the fallback `modeTag` |
| `systemPrompt` | string | yes | fixed cost per turn |
| `defaultModel` | string | yes | must resolve via the manifest |
| `toolAllowlist` | string[] | no | subset of registered tool names |
| `params` | object | no | `temperature`, `maxTokens` |
| `contextBudget` | object | no | `maxHistoryTokens`, `maxRagTokens`, `maxMentionTokens` (ADR-0036) |
| `maxSteps` | integer | no | per-mode dispatch/observe bound |
| `agentic` | boolean | no | multi-turn tool loop vs single-shot pass |
| `kind` | string | no | `model` \| `assistant` (reserved) |
| `preamble` | string | no | spliced before `systemPrompt` |
| `toolCalling` | string | no | `native` \| `router` (default `native`) |

### 3.2 Tool

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | unique; the executor's handler key |
| `description` | string | yes | goes into the prompt |
| `parameters` | JSON Schema | yes | the prompt-spliced function schema |

Startup validation failures (typed errors): `mode-refs-unknown-model`,
`mode-unreachable-no-tag`, `mode-refs-unknown-tool`, `tool-has-no-handler`,
`schema-invalid` (ADR-0019).

## 4. Invariants (cross-store)

- Each SQLite file is owned by exactly one service; no module reads/writes
  another's file (ADR-0016). Supersedes the prior "Document store owns all SQLite."
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
