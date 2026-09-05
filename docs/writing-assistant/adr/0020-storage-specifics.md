# ADR-0020: Storage specifics — commit cadence, worktree, block identity, chunking

Status: Accepted

Supersedes: ADR-0004 (block-ID scheme, SQLite ownership, chunking ownership).

## Context

ADR-0004 chose SQLite + git but left four specifics open, several with internal
contradiction or hidden requirements that only surfaced once the turn loop and the
Tauri (manual editing) reality were considered:

1. **Commit cadence.** `architecture.md` says "every accepted edit / autosave"
   (two different things). The Tauri editor introduces *manual* (human) edits —
   typed keystrokes with no "accept" boundary — which differ in both property and
   action from *AI* edits (the loop proposes, the user accepts).
2. **Worktree model.** `go-git` lacks a full worktree. With autosave now in play,
   where the canonical document bytes live is load-bearing.
3. **Block identity.** `data-model.md` §1.2 says block `id` is "content-derived or
   UUID", but §4 demands IDs are *stable across edits*. A content-hash ID is not
   stable across edits — those two lines cannot both stand, and block-candidates
   ("3 rewrites of this paragraph") require a stable key.
4. **Chunking.** It's the RAG token lever (chunk size/shape), but no module owns it.

## Decision

### 1. Commit cadence — two paths

Two commit paths, both owned by the Document store; clients only send edits:

- **AI edit** — `ApplyEdit` stages a proposed block edit (a *candidate*); when the
  client accepts, `Commit` makes one atomic commit with an auto-derived message
  `mode · blockID · one-line-diff-summary`. One user decision == one commit, one
  revert unit.
- **Manual edit** — an autosave snapshot on a silence interval (e.g. 10s of
  inactivity or every N minutes) batches many manual deltas into one `autosave @ ts`
  commit. Manual edits have no accept signal to hang a commit on; committing every
  keystroke is noise.

This distinguishes "revert an AI rewrite" (one commit) from "recover to a moment"
(autosave checkpoint), which is what Q4 (word-level revertibility) and
`versioning.feature` actually need.

### 2. Worktree model — engine-owned working tree + bare-ish history

A **bare-enough** git repo plus an **engine-owned working tree** directory
alongside `app.db`. The working tree holds the *current canonical markdown* (what
editors also read); git is append-only delta history. `go-git` commits tree
snapshots; the engine writes the working tree on every accepted/manual edit
(debounced for manual). SQLite holds block metadata + block IDs, **not** duplicate
text — the canonical text is the worktree file, consistent with ADR-0004's
"git = delta storage; SQLite = metadata."

### 3. Block identity — UUID, stable across edits

A block is a **Markdown block element** (paragraph, heading, list item, code
fence, blockquote, table); a document is a **tree** of blocks (`parent_id` +
`position`). Block IDs are **UUIDs minted by the Document store at block creation,
stable for the block's lifetime**. Edits carry the block ID ("replace content of
block X"); splitting/merging mints new IDs. This satisfies `data-model.md` §4's
"stable across edits" invariant and is what makes block-candidates (keyed by one
stable ID) possible. A content-hash ID is rejected: every edit would mint a new
ID and break fine-grained versioning.

### 4. Block candidates — unaccepted edits, a side-table

Candidates *are* unaccepted AI edits: `ApplyEdit` stores the proposed text keyed
by blockID in a Document-store side-table (**not** git). Accepting a candidate →
`Commit` and the candidate becomes the block's new content; rejecting → dropped.
`Candidates(blockID)` lists open proposals diffed against the base. "3 rewrites of
this paragraph" is just 3 open candidates. One mechanism; no separate git-branch
invention.

### 5. Chunking — a separate pure leaf, size as data

A **`Chunker` pure leaf**: `Chunk(documentBlockTree, maxTokens) → []Chunk`,
paragraph-aligned + size-bounded, with chunk size a **data tunable** — this is the
RAG token lever. The Retriever's write-side `Index(documentID)` calls the Chunker
then writes `index.db` (vec + FTS). The Chunker is a separate module (not inline in
the Retriever) because the splitting algorithm is pure/deterministic and may evolve
independently (R4); it is the leaf, not the Retriever.

## Consequences

- **+** Commit cadence honestly reflects two different edit kinds (AI accept vs
  human typing), preserving Q4's atomic revert for AI edits and recovery points
  for manual ones.
- **+** Stable UUIDs make fine-grained versioning and block-candidates tractable —
  the Google-Docs-style "just this paragraph changed" guarantee.
- **+** Chunk size is a data tunable, so "change a lever and watch the meter move"
  extends to RAG.
- **−** The engine now owns a live working-tree directory (a second on-disk
  representation next to SQLite); consistency between worktree, `app.db`, and git
  history is the engine's responsibility, in one place.
- **−** The Chunker is a new module boundary; the splitting algorithm is now a
  contractual, testable unit rather than Retriever-internal code.

## Alternatives considered

- **Uniform autosave for both AI and manual edits** — rejected: loses the atomic,
  attributable AI-edit commit Q4 and `versioning.feature` promise.
- **Client-driven commit (a manual Save button)** — rejected: leaks storage policy
  into the client (R3).
- **Pure bare repo, materialize from HEAD** — rejected: `go-git` still must write a
  tree to commit, and materialize-on-read pays checkout cost on every diff/history.
- **Candidates as git branches** — rejected: fights go-git's branch story for a
  fine-grained per-block concern a keyed table handles trivially.
- **Chunker inline in the Retriever** — rejected in favor of a pure leaf; the
  splitting algorithm is deterministic and independently evolvable.
