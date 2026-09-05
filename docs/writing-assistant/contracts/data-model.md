# Data model contract

Precise spec for the durable data formats the system owns. Source ADRs: ADR-0016
(per-service SQLite), ADR-0018 (two-tier manifest), ADR-0019 (mode/tool data),
ADR-0020 (block IDs, candidates, chunking).

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
| `turn_id` | TEXT | groups events into one turn (was `conversation_id`) |
| `component` | TEXT | `system` \| `tools` \| `rag` \| `history` \| `user` \| `thinking` |
| `prompt_tokens` | INTEGER | attributed prompt tokens |
| `completion_tokens` | INTEGER | attributed completion tokens |
| `approx` | INTEGER | 1 when the component is a labeled approximation (thinking, ADR-0024) |
| `model` | TEXT | logical model name actually used (`usedName`) |

### 1.4 `messages.db` — Conversation store

#### `messages` — conversation history

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | |
| `conversation_id` | TEXT | |
| `role` | TEXT | `user` \| `assistant` \| `tool` |
| `content` | TEXT | |
| `ts` | INTEGER | |

## 2. Fleet manifest (`models.json` in `macos-dev-config`) — two-tier

Source ADR-0018. Validated against a committed JSON Schema, with **semantic**
invariants (name uniqueness, lanes) enforced by the shared loader used by both the
daemon and `serve.sh`.

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
| `runner` | string | yes | `ollama` \| `mlx-lm` \| `mlx-vlm` \| `llama.cpp` \| `lmstudio` \| `delegate` |
| `delegate` | string | if `runner`==`delegate` | wrapper script name (e.g. `serve-qwen.sh`) |
| `host` | string | yes | default bind |
| `port` | integer | yes | 1–65535 |

A daemon is either a shared daemon (`ollama`/`lmstudio`, hosting many models) or a
dedicated server (each mlx/llama server is itself a daemon).

### 2.3 `Model`

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | yes | logical name, stable across runners; unique |
| `daemon` | string | yes | → `daemons[].name` serving this model |
| `source` | object | yes, except GUI-managed | how to obtain/run it |
| `source.kind` | string | yes | `hf` \| `gguf` \| `ollama` \| `lmstudio` |
| `source.repo` | string | if `hf` | HF repo id |
| `source.file` | string | if `gguf` | filename under `models/gguf/` |
| `source.tag` | string | if `ollama` | Ollama tag |
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
- Dedicated daemons (mlx/llama) bind a unique `port`; shared daemons (ollama/
  lmstudio) may host many models on one port.
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
| `contextBudget` | object | no | `maxHistoryTokens`, `maxRagTokens` |
| `maxSteps` | integer | no | per-mode dispatch/observe bound |
| `agentic` | boolean | no | multi-turn tool loop vs single-shot pass |
| `kind` | string | no | `model` \| `assistant` (reserved) |
| `preamble` | string | no | spliced before `systemPrompt` |

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
- Block IDs are stable UUIDs across edits (ADR-0020).
- Every `meter_events` row is attributable to exactly one `component`, and a
  `component` set `approx=1` is a labeled approximation, never silent.
- The fleet manifest is read only by the control daemon (`serve.sh` via the
  daemon); the engine never reads `models.json` directly (ADR-0025).
