# ADR-0048: `/locate` — anchor a pasted chunk to its vault location

Status: Accepted

Extends: ADR-0036 (turn-scoped, metered context attachments), ADR-0020 (stable
block IDs), ADR-0011 (assembler choke point), ADR-0029/ADR-0047 (guarded edits,
write-through).

Depends on: the vault index of ADR-0044 Phase C for vault-wide search
(open-document search works without it).

## Context

The dominant real workflow for a thesis-sized vault is not "rewrite the
document": it is copying a paragraph or sentence from Obsidian, pasting it into
the assistant, and asking for an improvement. Today the engine has no idea
where that text lives. The model can only return a replacement blob the user
must place by hand, and the pasted chunk plus surrounding context consumes
prompt budget that a located edit would not need.

The primitives exist but are unwired:

- Documents have stable block IDs (ADR-0020); blocks are paragraphs, headings,
  and tables with text and a content hash.
- `Task.selection{blockId}` and session `anchorBlockId` already model a
  block-scoped turn — no client ever sets `selection`.
- `edit_markdown` applies whole-block replacements by ID, with guards
  (ADR-0029; enforcement per ADR-0047).

Missing: a vault search surface, a normalization/fuzzy matcher, and a command
that turns pasted text into an anchor. Markdown already uses `#` (headings),
`!` (images), and `$` (math) as leading symbols, so those are not available as
command prefixes.

Forces:

- Dumb clients (ADR-0013 §3): the engine parses and resolves; clients send the
  text unchanged.
- Locating must be deterministic and cheap; using an LLM to find the chunk
  would spend exactly the tokens the feature is meant to save.
- Never fail silently: ambiguity and not-found are labeled outcomes (Q1/
  ADR-0022 discipline).
- A wrong anchor is worse than no anchor: fuzzy hits need conservative
  thresholds and user confirmation.
- The located edit must ride the guarded, write-through path
  (ADR-0029/ADR-0047).

## Decision

1. **Command syntax.** A leading line `/locate` in `userInput` marks the rest
   of the message as the chunk to locate; the pasted text follows on the
   subsequent lines. Parsing happens engine-side; `Task` needs no new required
   field and clients send the text unchanged. `/` is free of markdown
   collisions.

2. **Deterministic resolver, no LLM.** Normalize the pasted text
   (markdown-stripped, whitespace-collapsed) and search in order: the currently
   open document's blocks, then the vault index. Match exact normalized
   equality (with a hash fast path) first; only then fall back to a fuzzy
   score (trigram/token overlap) above a conservative threshold.

3. **Typed result.** The resolver returns `LocateResult`:
   `{documentId, path, blockId, span?, matchType: exact|fuzzy, confidence,
   context?}`. It is recorded in the context snapshot (ADR-0044 Phase C) and
   emitted as a `locate` SSE event so clients can render it and the meter can
   account for the turn's actual context.

4. **Ambiguity and not-found degrade, never fail silently.**
   - Multiple candidates above threshold → return the ranked list; the TUI
     shows a picker; the turn waits for a choice or the user cancels to plain
     chat.
   - No candidate → typed `locate-not-found`; the turn proceeds as normal chat
     with a labeled warning (fail-open).

5. **Anchored turn.** On success the turn is scoped to the block: the internal
   selection/anchor is set, neighboring blocks are injected as context, and the
   prompt instructs the model to return only the replacement text for the
   anchored block. The edit path is `edit_markdown` with the block's base-hash
   guard (ADR-0047); the diff is shown; approve writes through.

6. **No new tool registration.** `/locate` is deterministic preprocessing, not
   model tool-calling. A future `search_vault` tool may reuse the resolver, but
   the command path must not depend on the model choosing to call anything.

7. **Behavior contract and tests.** A `locate-anchor.feature` contract is
   normative. Tests cover normalization, exact and fuzzy matches, duplicate
   text across files, multi-paragraph chunks, stale text (file changed since
   the copy), not-found, and the ambiguous-picker flow.

## Consequences

- **+** Paste → targeted rewrite: the edit lands in the right block, context
  stays small, and the user approves a diff instead of placing a blob.
- **+** Deterministic and testable; no token cost for locating; the locate
  outcome is visible in the snapshot and the event stream.
- **+** Reuses existing block IDs, guards, and the write-through path — no new
  storage model.
- **−** Vault-wide search depends on the Phase C index; until then only the
  open document can be searched (still the common case).
- **−** Fuzzy matching is heuristic; thresholds need tuning and fuzzy hits
  should be confirmed. A conservative default favors not-found over a wrong
  anchor.
- **−** Adds a client-facing command convention that must be documented in TUI
  help and handled by the paste path (ADR-0046 bracketed paste).

## Alternatives considered

- **Auto-detect every pasted chunk** — rejected: surprising (a normal quote
  becomes an edit anchor), and vault search on every turn is wasteful; an
  explicit command keeps intent clear.
- **Prefix symbols (`#`, `!`, `$`)** — rejected: all three collide with
  markdown syntax; no suitable single symbol remains that is not worse than a
  word.
- **Keybinding-only (e.g. Ctrl+L)** — deferred: no control for typed flows and
  harder to document; can be added later as sugar over the same resolver.
- **LLM-based matching** — rejected: nondeterministic, spends the tokens the
  feature saves, and can hallucinate an anchor.
- **New `locate` tool instead of a command** — deferred: preprocessing must not
  depend on the model's tool choice; the resolver is shared if a tool is added.
