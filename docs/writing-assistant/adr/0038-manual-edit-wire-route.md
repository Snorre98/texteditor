# ADR-0038: Manual-edit wire route — the `PUT /documents/{id}/tree` autosave path

Status: Accepted

Extends: ADR-0020 (commit cadence: the manual/autosave path), ADR-0029 (edit
verification: the engine owns the bytes), ADR-0017 (OpenAPI surface). Records the
F8 manual-edit contract amendment (handoff-plan-f).

## Context

ADR-0020 §1 splits commit cadence into two paths: **AI edit** (`ApplyEdit` → stage
candidate → `Commit` on accept) and **manual edit** (autosave snapshot on a silence
interval, batching many deltas, no accept signal). ADR-0029 §1 makes whole-block
replacement the only edit primitive, and §6 pins `Format` to `Commit`/`Save` with
the Document store owning the canonical-content invariant. `versioning.feature`
already asserts the manual path ("Manual edits autosave on a silence interval").

The module surface has carried `Save(doc Document)` since ADR-0016/0020, but the
**wire** never projected it: the OpenAPI surface has `POST /documents/{id}/edits`
(ApplyEdit → candidate) and `POST /documents/{id}/commits` (accept), and no route
for "here is the human-typed block tree." The TUI never needed it (it drives AI
edits + accepts candidates); the Tauri editor is the first client that types block
text directly.

Two constraints make this a new route rather than an overload of the candidate path:

1. `ApplyEdit` **always stages a candidate** (ADR-0029 §1, ADR-0020 §4) — a
   git-excluded side-table row. Routing manual keystrokes through it would
   misrepresent human typing as an AI proposal, pollute `GET /candidates`, and
   violate ADR-0020 §1's "no accept boundary."
2. Block IDs are **engine-minted UUIDs** (ADR-0020 §3). Human editing is structural
   (Enter splits a block, backspace merges), so the client must be able to send
   *new* blocks without IDs; the client never mints IDs.

## Decision

### 1. A new route projects the existing `Save`/autosave op

`PUT /documents/{id}/tree` (operationId `saveDocument`) replaces the document's
block tree with the client's current tree and, if anything changed, commits an
`autosave @ ts` snapshot. Request `SaveTreeRequest`, response `Revision`.

### 2. Request/response shapes

```yaml
SaveTreeRequest:
  type: object
  required: [blocks]
  properties:
    blocks:
      type: array
      items:
        $ref: '#/components/schemas/BlockWrite'

BlockWrite:
  type: object
  required: [kind, text]
  properties:
    id:            # absent = new block; the engine mints a UUID (ADR-0020 §3)
      type: string
    parentId:
      type: string
    kind:
      type: string
      enum: [paragraph, heading, list_item, code_fence, blockquote, table]
    text:
      type: string
```

- **Array order = position** (the client does not maintain integer positions).
- `id` absent → new block (engine mints); `id` present → existing block, matched by
  stable UUID. A block in the current tree but absent from the request is deleted.
  A block whose `kind`/`parentId` changed is retyped/moved.
- No `hash`/`guards` on the write path — those are AI-edit concerns (ADR-0029 §4).

Response: the resulting `Revision` (message `autosave @ <ts>`); if the incoming tree
is unchanged, the engine returns the current HEAD without a new commit (a no-op).

### 3. Semantics — reconciliation, formatting, commit (all engine-side)

The engine reconciles the incoming tree against its current tree, then for the
changed set: `Normalize` on write, `Format` on the commit (ADR-0029 §6), write the
working tree, and commit iff changed. Structural reconciliation mints/drops/retypes
blocks (ADR-0020 §3 "splitting/merging mints new IDs"). Manual saves join the
Document store's existing per-document edit queue (ADR-0026 §4) — never interleaved
mid-block.

### 4. Cadence — client sync trigger, engine commit-on-receipt

The client holds the silence-interval timer (10 s / N min) and sends one tree per
interval; the engine commits on receipt. The timer is a *sync cadence* (an editor
UX concern), not a commit decision — the engine still owns reconciliation,
formatting, and commit. (The literal ADR-0020 §2 "engine debounces" reading would
require the client to stream changes continuously and the engine to commit
asynchronously; rejected for contract determinism — see Alternatives.)

### 5. Open-candidate interaction

A manual save of a block **drops that block's open candidates** — human keystrokes
supersede the AI proposal (the candidate's `BaseID` is now stale). `GET /candidates`
reflects this immediately.

### 6. Recorded contract amendment — codegen in lockstep

This **is** a spec change (new route + schemas), unlike the CORS policy (ADR-0037,
transport-only). The amendment lands in `api/openapi.yaml` and re-runs all three
codegens in lockstep: `go generate ./...` (ogen), `bun run gen` (Hey API + Zod),
`openapi-to-rust generate`. ADR-0017 §4's endpoint table gains the route. The
module-level `Save(doc Document)` signature in `interface.md §9` is clarified to a
tree-taking form (`SaveTree(documentID string, tree []BlockWrite) error`) — the
prior `Document`-taking signature carried no block content and could not express
this op.

## Consequences

- **+** Manual editing is a first-class wire path, distinct from the AI candidate
  path — no candidate pollution, no accept signal, exactly ADR-0020 §1.
- **+** The client stays dumb: it serializes its block-tree state; all
  reconciliation, ID minting, formatting, and diffing stay engine-side (ADR-0013 §3).
- **+** Fine-grained versioning is preserved: stable UUIDs mean a full-tree save
  still diffs per-block (versioning.feature "fine-grained, not whole-document").
- **−** A new route + two schemas: a recorded amendment with three-codegen lockstep
  (the first spec change of Track 2, unlike the transport-only CORS work).
- **−** The engine's `Save` reconciliation is a real piece of state (tree diff,
  mint/drop/retype) that was previously only sketched; it must be boundary-tested.

## Alternatives considered

- **Ride manual edits through `ApplyEdit` + a manual/AI flag** — rejected: `ApplyEdit`
  stages a candidate; the flag would leak manual edits into the AI proposal view,
  contradicting ADR-0020 §1.
- **Per-block delta batch** — rejected: the client would compute a structural diff
  (split/merge/insert/delete) — domain logic ADR-0013 §3 keeps engine-side; a
  full-tree snapshot reuses the existing `Save` semantics with the dumbest client.
- **Raw markdown text** — rejected: re-parsing mints new UUIDs and destroys stable
  block identity/versioning (ADR-0020 §3).
- **Engine-debounced (async) autosave commit** — rejected for now: faithful to
  ADR-0020 §2's literal "engine debounces" but forces the route to return "staged"
  and the commit to land later (observed via `GET /history`), breaking the
  request/response determinism the client needs; the client-side silence trigger
  achieves the same batching with a synchronous commit.
