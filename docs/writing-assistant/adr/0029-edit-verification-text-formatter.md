# ADR-0029: Edit verification + TextFormatter — "the engine owns the bytes"

Status: Accepted

Extends: ADR-0016 (module inventory — `BlockEdit` shape, `DocumentStore`
dependencies), ADR-0019 (`edit_markdown` schema), ADR-0020 (block identity, commit
cadence), ADR-0027 (shared-DTO catalog gains `BlockKind`, `TextFormatterIssue`,
`Guard`).
Relates: ADR-0028 (tool decider) — orthogonal: ADR-0028 fixes *well-formed calls*;
this ADR fixes *well-formed content*.

## Context

LLM edit tools fail on simple markdown for two reasons that are impossible in
token space: the model must (a) reproduce existing bytes to *anchor* an edit, and
(b) emit correct whitespace, indentation, and table alignment. The model
reconstructs rather than reads, and whitespace is invisible to the tokenizer, so
its "search string" is a paraphrase and its "replacement" is mis-formatted. When
the tool then fails with an uninformative "string not found," the model retries by
guessing — the edit thinking-loop.

The accepted architecture already removes the anchor problem: edits target a block
by **stable UUID** (ADR-0020), not a search string. What remains is the byte
problem — and the fix is to make the engine, not the model, own formatting, so the
model never has to reproduce bytes. A block-level guard additionally detects stale
or hallucinated context. The formatting style is deliberately hardcoded: a fixed,
opinionated style is *what the model can be expected to match*, which is how the
system helps weaker LLMs by enforcing structure and reliability.

## Decision

### 1. Whole-block replacement is the only edit primitive

`ApplyEdit` replaces the content of block X; it cannot touch neighbors by
construction. No substring search exists anywhere in the edit path. The model
supplies *content only* — never whitespace, alignment, or structure.

### 2. `TextFormatter` — a pure leaf that owns formatting

```go
type BlockKind string // paragraph | heading | list_item | code_fence | blockquote | table

type TextFormatterIssue struct {
    Line    int
    Message string
}

type TextFormatter interface {
    Normalize(kind BlockKind, text string) (canonical string, changes []string) // semantic-preserving
    Validate(kind BlockKind, text string) []TextFormatterIssue                   // structural integrity
    Format(kind BlockKind, text string) (formatted string, changes []string)     // opinionated style
}
```

- **`Normalize`** — canonical whitespace per block kind (indentation, list markers,
  table pipe alignment, line endings, trailing whitespace). Semantic-preserving.
- **`Validate`** — structural integrity (table separator vs header column count,
  balanced fences, list depth consistency). Returns issues, not a pass/fail.
- **`Format`** — the hardcoded opinionated style (heading case, list marker
  preference, citation spacing, wrap width). Code, not data — deliberately *not* a
  config lever (ADR-0019's "data" applies to modes/tools, not to the fixed format).

The three ops hook three boundaries:

| Op | Boundary | When |
|---|---|---|
| `Normalize` | `ApplyEdit` (model boundary) | always; the candidate is always canonical |
| `Validate` | the edit-tool handler, pre-flight | before staging; issues reach the model |
| `Format` | `Commit` (accept) + autosave (save) | the persisted document is always formatted |

### 3. Canonical-content invariant

Blocks are always stored in canonical form — normalized on `ApplyEdit`, formatted
on `Commit`/autosave. Consequence: content hashes are cheap and stable per
revision, which is what makes the guard (below) trivially correct.

### 4. Block-level guard — a content-hash echo

```go
type Guard struct {
    BlockID string // a sibling/context block the edit relies on
    Hash    string // short hash of its canonical content
}
```

`BlockEdit` gains `Guards []Guard`. The engine surfaces a short content hash
alongside each block in the edit read path (`{blockID, kind, content, hash}`); the
model **echoes** the hash — it never reproduces the neighbor's text. `ApplyEdit`
verifies the guards *atomically* with staging: if a guarded block's current
canonical content no longer matches its echoed hash, the edit is rejected with a
typed `guard-failed` error naming the changed blocks. The revision-level `BaseID`
anchor (already in `Candidate`) remains the coarse always-on guard; block-level
guards add fine-grained "this neighbor changed" detection.

### 5. Structured edit result

The `edit_markdown` tool returns a structured result (fed to the loop's `observing`
phase, informing retries):

```jsonc
// success
{ "ok": true, "blockId": "…", "diff": {…}, "normalized": ["…"] }
// stale context
{ "ok": false, "error": "guard-failed",      "details": [ { "blockId": "…", "reason": "content changed" } ] }
// structural failure
{ "ok": false, "error": "invalid-structure", "issues":  [ { "line": 4, "message": "separator row has 2 columns, header has 3" } ] }
```

### 6. Ownership

The **Document store** depends on `TextFormatter`: it normalizes on `ApplyEdit` and
formats on `Commit`/`Save`, so the canonical invariant is enforced at the single
write boundary with no orchestration race (the Retriever precedent, ADR-0016 §8).
The edit-tool handler does the pre-flight `Validate` and formats the structured
result for the model.

## Consequences

- **+** Anchoring, whitespace, and structure failures become structurally
  impossible or fail fast with a specific hint — the edit thinking-loop is closed.
- **+** Stable canonical content → cheap, correct block hashes → the guard is free.
- **+** A hardcoded style gives weaker LLMs a fixed target, not a moving one.
- **+** Fits the base model: `TextFormatter` is a pure leaf; DTOs in/out.
- **−** `DocumentStore` gains a `TextFormatter` dependency and is no longer a pure leaf
  (Retriever-style, ADR-0016 §8).
- **−** The edit read path must surface `{blockID, kind, content, hash}`, which
  touches the Retriever/assembler document view.

## Alternatives considered

- **String-match `old`/`new` edits** — rejected: re-imports the anchoring failure.
- **Model owns formatting** — rejected: the entire premise.
- **Formatting as config-as-data** — rejected: hardcoded per decision (a fixed
  style is what lesser LLMs are expected to match).
- **Full-document format on every model edit** — rejected: noisy diffs, undermines
  Q4's word-level reverts.
- **Guard as content-echo (model reproduces neighbor text)** — rejected: reproduces
  the exact byte-failure this ADR exists to remove; a short hash is copyable, text
  is not.
