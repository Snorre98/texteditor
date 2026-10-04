# ADR-0050: TUI reader pane — rendered markdown, editor-extensible

Status: Accepted

Amends: ADR-0046 §5 (UI surface — "no editor panel"), §9 (parity checklist).

Extends: ADR-0013 §3 (dumb clients), ADR-0020 §3 / ADR-0038 / ADR-0039 (the block
tree and the manual-edit wire path), ADR-0029 (the engine owns the bytes).

Bounded by: ADR-0047 (approve remains the only write boundary).

## Context

ADR-0046 scoped the Ratatui TUI to chat, presets, meter, RAG/context, diff/approve,
and status — **no editor panel and no manual save**, with approve as the sole write
boundary (ADR-0047). That decision rests on the OpenTUI editor panel being
read-only and buggy and on the product thesis (ADR-0044: context quality, not a
richer editor). It left no way to *view* the open document beside the chat.

Two as-built facts make a read-only reader cheap and safe:

- **The document is already served as full markdown fragments.** `GET
  /documents/{id}/blocks` returns a `Block[]` whose `Text` is the complete
  markdown source fragment — "a heading keeps its `## ` marker" — and
  `serializeBlocks` is a plain `\n\n` join
  (`server/internal/document/markdown.go:20,134`). Rendering it is presentation.
- **The manual write path already exists**, unwired by the TUI: `PUT
  /documents/{id}/tree` → `SaveTree` takes a whole block-tree snapshot with a
  `writeThrough` flag (ADR-0038/0039; `contracts/interface.md` §9;
  `api/openapi.yaml` `/documents/{id}/tree`).

Forces:

- Dumb clients (ADR-0013 §3): rendering a document for display is presentation,
  not domain logic; the engine stays the source of record and the only writer.
- Write-through safety (ADR-0047): a manual editor reintroduces the two-writer
  conflict (typing vs. AI candidates) and the Tauri stale-index failure mode.
  A reader adds none of that.
- Editor extensibility: the block model is the natural seam — the reader and a
  future editor consume the *same* `Block[]` shape that `SaveTree` writes.
- Contract-first (ADR-0017): no new surface is needed here; if one were, it would
  land in `api/openapi.yaml` before client code.
- Terminal reality: markdown renders to styled `Line`s; images, math, and mermaid
  are protocol-dependent (`ratatui-image`) and are out of scope.

## Decision

1. **Add a document reader to `client/tui-rs`.** It renders the opened
   document's canonical markdown, **read-only**. It adds no save and no edit
   affordance.

2. **Source of truth is the existing block tree.** The reader calls
   `GET /documents/{id}/blocks`, joins `Block.Text` fragments with `\n\n`, and
   renders. Rendering is presentation — the client holds no document state of
   record and never writes. No new contract surface, no `api/openapi.yaml`
   change.

3. **Renderer: `tui-markdown`.** It converts markdown to styled
   `ratatui::text::Text` using pulldown-cmark, with syntect code-block
   highlighting. Image, math, and mermaid syntax render as text fallbacks in v1;
   richer rendering is future work, not a dependency of this decision.

4. **Editor-extensible seam.** The reader is built over a `DocumentView` state
   that owns the fetched `Block[]` (id / kind / text / hash) — the same shape
   `SaveTree` consumes — and renders through a single markdown function. The
   layout is a **toggleable document pane beside the chat pane**, sized so a
   future source|preview split drops in without re-architecting. The existing
   `PUT /documents/{id}/tree` write path is **left unwired**; the reader exposes
   no edit operation. This is a seam, not a plan to edit.

5. **The write boundary is unchanged.** ADR-0047 stands: approve is the only
   write boundary. The reader neither writes nor stages; it is consistent with
   ADR-0047's "the new TUI has no editor and no manual save."

6. **A future editor is a separate decision.** Extending the pane into an editor
   is not authorized here. It must revisit ADR-0047's two-writer conflict and the
   Tauri stale-index failure mode (ADR-0047 Context), add an explicit save /
   cadence model (ADR-0038/0039), and wire `SaveTree`. Recorded as a deferred
   note.

7. **Parity and docs.** The OpenTUI-retirement parity checklist (ADR-0046 §9)
   gains **reader render**. A `client-swap.feature` scenario states the reader
   renders engine-sourced blocks and holds no state of record.

## Consequences

- **+** The author can read the open document beside the chat without leaving the
  TUI or reopening the write-through question.
- **+** No new contract, no engine change: the reader rides `GET
  /documents/{id}/blocks` and the existing block model.
- **+** The reader and a future editor share one view-model (`Block[]`) and one
  layout seam; the eventual editor is an extension, not a rewrite.
- **+** ADR-0047 is untouched: no second writer, no new save path, no clobber
  surface.
- **−** One more TUI pane to lay out and keep responsive (immediate-mode repaint;
  render only the visible range, pre-wrap on width change — the ratatui
  guidance).
- **−** `tui-markdown` is explicitly a proof-of-concept library; unsupported
  markdown (images, math, mermaid, raw HTML) degrades to text.
- **−** A reader that looks like an editor invites the expectation of editing;
  the deferred note and help text must be explicit that it is read-only.
- **−** Doc churn: ADR-0046's parity list, the client-swap behavior, the plan,
  traceability, architecture, and status all gain the reader.

## Alternatives considered

- **Full in-TUI editor now** — rejected: reopens ADR-0047's two-writer conflict
  and the exact Tauri stale-index failure mode the rewrite was meant to end; a
  separate decision is required.
- **No document view (status quo)** — rejected: the reader is nearly free over the
  block route, and viewing the document is a real gap for the writing workflow.
- **New canonical-markdown read route** (e.g. `GET /documents/{id}/content`) —
  rejected: `Block[]` already carries full markdown fragments; a second read
  surface would need OpenAPI-first work for no gain.
- **Render the engine's worktree file directly from disk** — rejected: client
  file I/O violates the dumb-client rule (ADR-0013 §3); the engine serves state.
- **Full-screen reader view** — rejected: a toggleable pane beside chat is the
  layout that extends into a source|preview split; a full-screen view would be
  reworked later.
- **Richer renderer (`ratatui-markdown`/mermaid/tree-sitter) now** — rejected:
  larger, less mature dependency surface for features (images, math, mermaid)
  that v1 does not need.
- **External `$EDITOR` escape hatch (edtui `system-editor`)** — deferred: a cheap
  interim edit path, but editing is out of scope for this ADR.

## Deferred / recorded notes

- **Editor extension**: revisit ADR-0047 first (two writers, save cadence,
  guards, conflict UX); wire `PUT /documents/{id}/tree` (`SaveTree`) with
  `writeThrough` on explicit save. Not authorized here.
- **`$EDITOR` escape hatch**: may be added later as sugar without an in-TUI
  editor.
- **Rich rendering**: images/math/mermaid via `ratatui-image` (terminal
  protocol-dependent); figure/table widths; a `ratatui-markdown`-style renderer.
- **Block-level rendering / block cursor**: per-block rendering is the natural
  first step toward the editor; v1 renders the joined markdown.
- **Reader state across turns**: whether the pane follows the active document
  after an approved edit (engine-sourced refresh) is an implementation detail.
