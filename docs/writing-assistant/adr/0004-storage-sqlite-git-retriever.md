# ADR-0004: Storage — SQLite for metadata/search/embeddings, git for versioning

Status: Accepted

## Context

The system needs: document metadata, stable block IDs, embeddings (KNN),
full-text keyword search, token-metering events, and conversation history —
plus Google-Docs-style version history (coarse full-document + fine paragraph
candidates). Two storage engines are each the best tool for part of this.

## Decision

1. **SQLite** (via `modernc.org/sqlite`, pure Go) is the single local file for
   everything *non-git*:
   - `document metadata` + `stable block IDs` (paragraphs/headings/tables)
   - `embeddings` via `sqlite-vec` (`vec0` table, KNN)
   - `FTS5` full-text index (lexical retrieval)
   - `token-metering events` and `conversation history`
2. **git** (via `go-git`, pure Go) owns version history: coarse history
   (commits) + block candidates (block-ID-keyed alternatives diffed against a base).
   "Nicely formatted versions" = word-level diff between two commits.
3. **The `Retriever` sits behind a Go interface** — the rest of the engine
   depends only on `Retriever`, never on sqlite-vec. Hybrid retrieval
   (semantic + lexical) is exposed through that interface.

## Consequences

- **+** Storage backend (sqlite-vec today) is swappable without touching the
  agent loop, modes, or context assembler.
- **+** git gives revert/branching/history "for free" with cheap deltas.
- **+** Stable block IDs enable paragraph-level versioning ("just this paragraph
  changed"), which is what CodeMirror/ProseMirror document models already do.
- **−** Two storage engines must be kept consistent; the engine owns both, so
  consistency is enforced in one place, but it is still a design responsibility.
- **−** `go-git` is less feature-complete than libgit2 (no full worktree model);
  acceptable for an engine-owned repo with no external git CLI.

## Alternatives considered

- **git for everything** — rejected: no structured query/vector/FTS layer; SQLite
  is the query/metadata/vector layer git cannot be.
- **CGO sqlite + libgit2** — rejected: breaks the no-CGO single-binary goal
  (ADR-0003).
- **Dedicated vector DB (Qdrant/Weaviate)** — rejected: another daemon + network
  hop for a single-user local app; `sqlite-vec` is sufficient and in-process.
