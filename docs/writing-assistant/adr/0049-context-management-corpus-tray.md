# ADR-0049: Context management — workspaces, multi-root corpus, and the context tray

Status: Accepted

Extends: ADR-0011 (assembler as the single metered choke point), ADR-0036
(turn-scoped, metered context attachments), ADR-0044 (context inspector;
TUI-first), ADR-0048 (`/locate` chunk anchoring).

Amends: ADR-0016 §SQLite partition (one SQLite file per service *instance*:
global document/registry files plus per-workspace retriever/session/meter
shards), ADR-0026 (the Session store is workspace-scoped; sessions persist in
the workspace shard), ADR-0035 §3 (workspaces become engine entities; the
stateless filesystem leaf is renamed `Filesystem` at implementation).

Bounded by: ADR-0045 (no per-preset behavioral config).

Depends on: ADR-0044 Phase C (production indexing and per-turn snapshots).

## Context

ADR-0044 made the engine a context engine and made the assembled turn
**visible** (the Q6 snapshot). The missing half is **control**: the author
cannot say which documents are in the retrievable corpus, cannot include a
sibling or parent directory of sources, cannot evict a stale or mistaken file
from the index, cannot see per-document index status, and cannot curate a
turn's context before it is sent. The as-built index has no production caller,
no eviction, and two latent bugs — FTS rows survive a re-index, and vec0 chunk
ids collide across documents (`internal/retriever/retriever.go:157-178`) — so
"what is retrievable" is currently uncontrolled as well as invisible.

Two structural gaps surface with it:

- **Workspaces.** ADR-0035 §3 deliberately kept the engine stateless about
  workspaces: the client held the directory as presentation state. That stance
  cannot carry a persistent corpus scope or a resumable per-project session
  list. The author needs a first-class workspace that groups a browsable
  directory and its subdirectories.
- **Storage.** Today four global SQLite files (`app.db`, `index.db`,
  `sessions.db`, `meter.db`) carry all state. Mixing every project's corpus,
  sessions, and meter rows into one file works, but gives up isolation:
  no independent rebuild/backup of a project's context state, no bounded vec0
  scan, no per-project lifecycle.

Forces:

- Q1/Q6 discipline (ADR-0011/0022/0044): every token that enters a model call
  must be visible; control actions that change the payload must be metered and
  recorded, never silent.
- Dumb clients (ADR-0013 §3): workspace, corpus, indexing, and assembly state
  are engine-side; the TUI renders and toggles.
- One pipeline (ADR-0045): controls must not reintroduce per-preset behavioral
  fields; defaults are session-level with per-turn overrides.
- Write-through safety (ADR-0047): one canonical document identity per
  canonical path (hash-checked, symlink-safe) is load-bearing; storage
  isolation must not duplicate versioned identity.
- Local-first, single user: corpus scope is the author's own data, not a
  policy file shipped with the engine; one machine, a handful of workspaces.
- Prefix caching: a stable, front-loaded pinned prefix is cheaper across
  turns; a heavily edited tray reduces reuse.
- Security (ADR-0021): `ENGINE_BIND=0.0.0.0` exposes the API to the LAN. A
  directory browser and a corpus indexer with unrestricted reach would turn
  that exposure into whole-filesystem access.
- Contract-first (ADR-0017): the surfaces below are intent; exact shapes land
  in `api/openapi.yaml` before client code.

The order lives in Phases C and E of
[`plans/implementation-sequence-context-engine.md`](../plans/implementation-sequence-context-engine.md);
[`behaviors/context-management.feature`](../behaviors/context-management.feature)
is normative.

## Decision

### 1. Three layers, explicitly separated

Context management has three distinct layers, and the docs keep them apart:

- **Workspace** — persistent engine entity: a root directory plus its
  subdirectories. It governs **browsing and editing** only.
- **Corpus** — persistent engine entity: a workspace's set of corpus roots
  plus include/exclude globs. It governs **retrievability** only.
- **Context tray** — ephemeral per-turn view of the assembled context
  components; its edits are turn-scoped unless pinned for the session.

**Workspace ≠ Corpus**: a corpus root may live outside the workspace root
(siblings, parents, shared literature directories). The workspace root never
bounds what is retrievable.

### 2. Workspace is first-class

```go
type Workspace struct {
    ID        string // UUID, client-facing identity
    Root      string // canonical absolute directory (EvalSymlinks + case-fold)
    Name      string // display label (default: basename of Root)
    CreatedAt int64
    UpdatedAt int64
}
```

- Create-or-resume by directory: opening a directory resolves to the most
  specific existing workspace whose root contains it; otherwise one is
  created. The exact nested-root rule is an implementation detail.
- The root is canonicalized (ADR-0047 discipline) so aliases and symlinks
  resolve to one workspace.
- A workspace owns a session list, a corpus, and a storage shard (§5); it is
  the unit the TUI opens, resumes, and switches.

### 3. Corpus is multi-root

A corpus is **a set of corpus roots** (each a file or a directory) plus
include/exclude globs — not a single root:

- Retrieval spans the union of the corpus roots; a chunk is returned if it
  matches an include pattern and no exclude pattern under any root.
- Paths are canonicalized (`EvalSymlinks` + case-fold, ADR-0047) and deduped,
  so a file reachable via two roots is indexed once per workspace.
- Roots may lie outside the workspace root; browsing/editing is unaffected.
- Defaults: the workspace root is the corpus's first root; include `**/*.md`;
  hidden directories (`.obsidian/`, dotfiles) are excluded.

### 4. Corpus layer — persistent, engine-owned, index-only

- **Index / remove / rebuild** are engine operations; the client sends
  decisions, never file manipulation.
- **Per-document status** is visible: indexed, chunk count, stale (the disk
  file changed since indexing), pending, evicted, or error.
- **Scope changes are idempotent and index-only.** They read file content to
  chunk/embed, but never open, version, edit, or write document files —
  write-through remains ADR-0047's business. Excluding a document reconciles
  the index by evicting it.
- **Eviction deletes both the vec0 rows and the FTS rows, and is idempotent**
  (evicting an already-evicted document succeeds). This is where the current
  re-index FTS-staleness and vec0 id-collision bugs are fixed, as part of
  Phase C.
- **Indexing is not opening.** Corpus files that were never explicitly opened
  are indexed path-keyed and are never versioned documents: no `documents`
  row, no block IDs, no git history (the ADR-0036 §6 discipline generalized
  to the corpus). Only the active/opened document is a versioned entity.

### 5. Storage is workspace-sharded by concern

Context state is sharded per workspace; document identity stays global:

```
<data>/                         # --data, default ~/.local/share/texteditor
  app.db                        # GLOBAL: documents, blocks, candidates (Document store)
  git/<docID>.git               # GLOBAL: per-document history
  worktree/<docID>/             # GLOBAL: canonical file per document
  workspaces.db                 # GLOBAL registry: workspace records + corpus roots/scope
  workspaces/<workspaceID>/     # per-workspace shard (context state only)
    index.db                    #   corpus vec0 + FTS
    sessions.db                 #   sessions + messages
    meter.db                    #   meter events
```

- **Context state only is sharded.** The global Document store keeps one
  canonical path → one document → one git history, so ADR-0047's
  write-through, hash checks, and symlink safety are untouched. Sharding
  documents/git per workspace is rejected: the same file opened from two
  workspaces would get two identities and two writers over one disk file.
- **One sealed `WorkspaceStore`** owns `workspaces.db` (workspace records +
  corpus roots/scope). It is engine-side per ADR-0016 (sealed interface, pure
  DTOs) and migrated via `internal/sqlmigrate`.
- **Workspace-scoped service instances**: the Retriever, Session store, and
  Token meter are instantiated per workspace, opened lazily on first use,
  migrated per shard, and closable on workspace switch. `app.db` stays
  Document-store-only, so no SQLite file is shared across modules.
- ADR-0016's "one file per locked service" generalizes to **one file per
  service instance**: a workspace shard is the unit of isolation, rebuild,
  backup, and deletion.
- **No cross-workspace aggregation**: sessions, meter, and search are
  workspace-scoped. The registry is the only global index; there is no global
  meter total or cross-workspace session list.
- Sessions are workspace-scoped by shard; `Session.workspaceId` remains a
  client-facing scope for routing and the API, but needs no cross-workspace
  column.

### 6. ALLOWED_ROOTS — a bounded filesystem selector

A config/env allowlist, `ALLOWED_ROOTS` (default: `$HOME` or the configured
projects root), bounds **both**:

- `GET /directories` browsing, and
- corpus root selection and indexing.

A path outside the boundary is refused with a typed error
(`path-outside-allowed-roots`) — never silently ignored. The selector cannot
escape the boundary even with `ENGINE_BIND=0.0.0.0`. Browsing is an existing
route (ADR-0035); only the boundary and its typed refusal are new. Exact
setting names, defaults, and error shape are pinned OpenAPI-first at
implementation.

### 7. Turn/session context layer — the context tray

The TUI renders a **context tray** of the assembled components — system,
history, RAG chunks, mentions, the `/locate` anchor, and user input — with:

- per-item **pin/remove**,
- an editable **retrieval query**,
- an **auto-RAG on/off** toggle.

After any edit the engine re-assembles and re-meters; the final set is what
the Phase C context snapshot records. Items are per-turn by default; **"pin
for session"** persists an item in the session store so it survives turns,
reconnects, and session resume.

### 8. Contract shape — session defaults plus per-turn overrides

The engine owns a persisted session-level context policy (pinned refs,
excluded refs, auto-RAG flag, retrieval query). `Task` gains an **optional,
additive `context` object** carrying the same fields as per-turn overrides;
the engine merges overrides over the session policy.

Chosen over a turn-only `Task.context` because:

- "Pin for session" needs a persistent home; a turn-only field cannot survive
  disconnect/resume (ADR-0026).
- Per-turn deltas stay ephemeral and small instead of re-sending full state.
- Session scope is not a preset/mode field (ADR-0045).

The change is additive: `Task.context` is optional and existing clients ignore
it. Exact field names and ChunkRef shapes land in `api/openapi.yaml` first.

### 9. The assembler remains the single metered choke point

The UI sends decisions (include/exclude/pin, corpus scope); the engine builds
the payload. The assembler (ADR-0011) is still the only place the payload and
its per-component breakdown are produced; the tray never assembles, counts, or
attributes. Metering applies to the post-override set exactly as before.

### 10. Dumb client

Corpus state, workspace registry, indexing, and assembly are engine-side; the
TUI renders the engine's data and sends toggles. No traversal, no scope
resolution, no payload construction, no provenance computation client-side
(ADR-0013 §3).

### 11. Pins are a human override — of the gate, never of budgets

- Pinned items and explicit attachments (mentions, the `/locate` anchor)
  **bypass the decision gate**: the author's explicit inclusion wins over the
  Laya gate's drop.
- Auto-retrieved chunks **remain gated** as before.
- **Pins never bypass budgets.** After overrides, the assembler still applies
  the context budget and truncation; every dropped item is labeled, and the
  meter's component sum still equals provider-reported totals (Q1). The
  snapshot labels pinned items as human overrides.

### 12. Control ≠ influence

Including text does not make it relevant: included-but-irrelevant context
still competes for attention (lost-in-the-middle). The context inspector, the
Laya decision gate, and `/locate` remain the quality levers; the tray is a
control surface, not a relevance guarantee.

### 13. No per-preset config sprawl

Tray and corpus controls are session-level defaults plus per-turn overrides —
never mode/preset fields (ADR-0045). Presets stay `name` + `systemPrompt` +
`defaultModel`.

### 14. Prefix caching discipline

Static pinned context is serialized deterministically and front-loaded, so the
cacheable prefix is stable across turns. A heavily edited per-turn tray
changes the prefix and trades away cache reuse; this is an accepted cost, not
a bug. Exact placement within the assembly order is an implementation detail.

### 15. Session start flow

Opening a directory resolves or creates its workspace (registry row + shard),
then the author opens a markdown file and creates/resumes a session **under
that workspace**. The corpus is seeded with the workspace root as its first
corpus root but is independently editable and multi-root; corpus scope and the
session list restore from the workspace on resume.

### 16. Intended contract surface

Recorded as intent; exact shapes land in `api/openapi.yaml` at implementation,
OpenAPI-first (ADR-0017), with no breaking changes.

| Route | Intent |
|---|---|
| `GET /workspaces`, `POST /workspaces` (create-or-resume by absolute root), `GET /workspaces/{id}` | Workspace registry |
| `GET /corpus?workspaceId=` | Multi-root scope + per-document status (indexed/stale/chunk count) + job progress |
| `PUT /corpus` | Set scope (`workspaceId`, `roots[]`, `include[]`, `exclude[]`); idempotent; reconciles the index |
| `POST /corpus/index` | Bulk ingest/rebuild of the current scope; observable progress; idempotent per unchanged content |
| `DELETE /corpus/documents/{id}` | Evict one document (vec0 + FTS rows); idempotent |
| `GET /sessions?workspaceId=` (existing `documentId` filter kept), `CreateSessionRequest.workspaceId` | Workspace-scoped session list/create |
| `Task.context` (optional) | Per-turn overrides merged over the persisted session policy |
| `GET /directories`, corpus mutations | Bounded by `ALLOWED_ROOTS`; outside paths → typed `path-outside-allowed-roots` |

- SSE: reuse the `rag` (retrieved chunks with provenance) and `context`
  (Phase C snapshot) events. Add an **`index` progress event**, because Phase C
  defines no index-progress event; exact transport (stream vs. polling
  `GET /corpus`) is pinned at implementation.
- Corpus scope changes, eviction, and indexing emit observable status; nothing
  is silent.

## Consequences

- **+** The author controls what is retrievable (multi-root corpus, scope,
  eviction, status) and what enters a turn (the tray), not just observes it
  afterward.
- **+** The latent index bugs are fixed structurally: eviction removes both
  vec0 and FTS rows idempotently, and re-index no longer leaves stale FTS rows
  or colliding vec0 ids.
- **+** Per-workspace shards isolate context state: independent rebuild,
  backup, delete, and bounded vec0 scans; documents/versioning stay global, so
  write-through safety (ADR-0047) is untouched.
- **+** `workspaces.db` keeps `app.db` Document-store-only and avoids a shared
  file's single `PRAGMA user_version` migration collision.
- **+** `ALLOWED_ROOTS` is defense-in-depth under ADR-0021: a LAN-bound engine
  can no longer browse or index outside the boundary.
- **−** One more root file (`workspaces.db`) plus N workspace shards to open,
  migrate, and close; lazy lifecycle policy is required.
- **−** No cross-workspace views: global meter totals, all-sessions lists, and
  cross-workspace search are deliberately out of scope.
- **−** Corpus roots shared by two workspaces are embedded once per workspace
  (duplicate storage/embedding cost) — accepted for isolation.
- **−** `ALLOWED_ROOTS` adds UX friction: legitimate paths outside the default
  home/projects root are refused until configured, and clients must render the
  typed refusal.
- **−** Multi-root canonicalization/dedupe is real per-index work, and
  overlapping globs make status attribution subtler.
- **−** Persisted session context policy grows `sessions.db`; retention and
  pruning are Phase C concerns (the snapshot retention note in ADR-0044).
- **−** Amending ADR-0016/0026/0035 shifts the module/storage docs (contracts,
  module graph, the `Workspace` leaf rename) — recorded here, landed with
  implementation.

## Alternatives considered

- **Single-root corpus tied to the workspace root** — rejected: the thesis
  corpus lives in sibling/parent directories and shared literature folders;
  the workspace root is a browsing scope, not a retrieval boundary.
- **Path-prefix-derived corpus** (corpus ≡ workspace root, no explicit roots)
  — rejected: same reason, plus explicit canonical roots make membership
  deterministic and dedupe-able.
- **Workspace = Corpus** — rejected: browsing/editing boundaries and
  retrieval boundaries are different concerns with different lifecycles.
- **Full project shard** (documents + git + index + sessions + meter per
  workspace) — rejected: the same file opened from two workspaces would get
  duplicate identities and histories, and two writers would hash-fight over
  one disk file, defeating ADR-0047.
- **Single global DBs + `workspace_id` columns** — rejected: no isolation,
  monolithic vec0 scan, all projects share session/meter growth; the shard is
  the natural rebuild/backup unit.
- **`app.db` as a shared central registry (previous draft)** — rejected:
  two modules over one file collide on `sqlmigrate`'s single
  `PRAGMA user_version`, and it breaks app.db's single-owner rule.
- **Workspace shards inside the vault (`<root>/.texteditor/`)** — rejected:
  writes engine data into the user's vault, churns Obsidian sync/backup, and
  complicates hidden-directory scope handling.
- **Turn-only `Task.context`, no session policy** — rejected: pin-for-session
  needs persistence across turns and reconnects.
- **Session policy only, no per-turn overrides** — rejected: per-turn
  removal/inclusion is the point of the tray.
- **Pins bypass budgets too** — rejected: inclusion override is not a cost
  override; Q1 requires metered totals and labeled drops.
- **Preset-level corpus/tray fields** — rejected by ADR-0045 (one pipeline, no
  behavioral mode fields).
- **Unbounded filesystem selector** — rejected: a whole-filesystem browser and
  indexer behind `ENGINE_BIND=0.0.0.0` is unacceptable; the allowlist is the
  containment.
- **Client-side allowlist / client-side corpus walk** — rejected: dumb client;
  the boundary and traversal are engine policy.

## Deferred / recorded notes

- **Nested workspace resolution**: the exact rule when a directory lies inside
  an existing workspace root (most-specific root wins vs. explicit create) is
  pinned at implementation.
- **Leaf rename mechanics**: renaming ADR-0035's stateless `Workspace` leaf to
  `Filesystem` (and the corresponding `contracts/interface.md` /
  `module-boundaries.md` updates) lands with implementation.
- **Shard lifecycle policy**: lazy open, close-on-switch/LRU, and open-handle
  bounds are implementation details; the shard layout is decided here.
- **Corpus identity schema**: how `index.db` keys path-only corpus files
  (canonical path, synthetic key, manifest table) is pinned at implementation.
- **`ALLOWED_ROOTS` setting shape**: env var vs. config file, the configured
  projects root, and whether per-workspace root grants are allowed later.
- **Index-progress transport**: `index` event on a stream vs. polling
  `GET /corpus`; recorded as an intent here.
- **Chunk-ref and session-policy field names**: exact wire shapes are pinned
  OpenAPI-first at implementation.
- **Cross-workspace views** (meter totals, all-sessions, global search): out
  of scope; a future ADR would own them.
- **PDF/other-format ingest**: out of scope; the corpus is markdown-first
  (ADR-0044).
