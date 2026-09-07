# Handoff — Tauri UI: fix the selection trigger + render the AI surface

The engine, the OpenAPI contract, and the TUI are complete and tested. The Tauri
editor's **engine-side** is complete too — sidecar handshake, Vue reactive store,
generated client, autosave, and the `@codemirror/merge` candidate preview — but
the Tauri **UI surface is a stub**, and as a result "leverage the AI
capabilities" is effectively unreachable from the desktop app today. This handoff
scopes exactly what is missing, why, and the plan to finish it.

## Context — the honest state

Two gaps, both confirmed in a live run (2026-09-06):

1. **The "Ask about selection" bubble does not appear** when text is
   drag-selected. The trigger is wired as a CodeMirror `StateField` provided to
   `showTooltip.from`, with a `ViewPlugin` that *self-dispatches* a transaction
   from inside `update(u)` on `u.selectionSet`, positions the tooltip at
   `pos: anchor.end` with `above: true` (strictly-above, no edge-clip) inside a
   line-wrapped, scrollable editor. During a live mouse drag this produces a
   tooltip that is shown-then-dismissed or positioned off-screen, so in practice
   **nothing renders**. This is a bug, not missing work — but it makes the *only*
   AI trigger unreachable.

2. **The Vue store supports far more than the UI renders.** `AppStore` exposes
   `switchModel`, `refreshFleet`, `acceptCandidate`, `getCandidateText`,
   per-session `{messages, turn}`, the per-component meter `cumulative` tally,
   and the `rag` queue — but `Editor.vue`/`App.vue` render none of it. There is
   **no mode selector** (the turn is hardcoded to `modes[0]`), **no free-form
   chat input** (only the selection bubble as a trigger), **no token-meter
   display**, and **no RAG-results display**. The engine is 100%; the Tauri
   *presentation layer* is the stub.

All of this is in `client/tauri/src/editor/Editor.vue` (+ a little in
`App.vue`). No engine change, no contract change, no `macos-dev-config` change.

## Locked decisions

1. **Option B** — replace the fragile inline tooltip with a deterministic,
   always-present **toolbar button** (not a CodeMirror tooltip). Rationale:
   deterministic render, works for mouse *and* keyboard (`Shift+arrows`) selection,
   and trivially unit-testable.
2. **Session model stays simple** — selection is managed *only* through the
   "Ask about selection" button (→ create-or-resume an **anchor**-block session,
   exactly today's `askAbout` semantics). **Free-form chat is doc-level only**;
   it cannot anchor to a selection (no anchor-from-chat).
3. **Mode selector** lives in the chat-section header (no placement preference).
4. **Pure presentation** — `askAbout`, `submitTurn`, `createSession`, and the
   whole store are correct and unchanged; only the `<template>`/`<script>` of
   `Editor.vue` (and possibly `App.vue`) change.

## Read first

- `client/tauri/src/editor/Editor.vue` — the surface to change (selection trigger
  at lines 37–81, `askAbout` 85–107, toolbar/paths 192–209, chat section 211–222).
- `client/tauri/src/state/store.ts` — the already-wired state + actions to render
  (`switchModel`, `refreshFleet`, `acceptCandidate`, `submitTurn`, meter
  `cumulative`, `rag`).
- `client/tauri/src/api/client.ts` + `src/api/sse.ts` — the generated, zod-validated
  boundary and the SSE stream reader (no change needed).
- `docs/writing-assistant/contracts/interface.md` §7/§8b + §11 (the SSE event
  vocabulary the meter/rag renderers consume).
- `docs/writing-assistant/behaviors/serving-control.feature` +
  `token-metering.feature` (the meter/switch semantics to surface).
- Preconditions for a working turn: `adr/0025` control daemon on `:9300`; a mode's
  `defaultModel` provisioned (see `macos-dev-config/models.json`).

## Work items — all in `client/tauri/src/`

### 1. Fix the selection trigger (Option B) — `editor/Editor.vue`

- **Delete** `setBubble`, `bubbleField`, `bubblePlugin`, `updateBubble`, the
  `showTooltip` import, and the `.texteditor-bubble` / `.texteditor-bubble button`
  styles.
- **Add** a `hasSelection` ref, driven by an `EditorView.updateListener` that sets
  `hasSelection = !view.state.selection.main.empty` (no dispatch, no tooltip).
  Mouse and keyboard selection both work.
- **Add** an "Ask about selection" button in the toolbar beside "Open file"
  (`:disabled="!hasSelection || !store.state.document"`). Its handler reads
  `view.state.selection.main.from/to` **at click time** and reuses the existing
  `askAbout` body (`createSession(blockId)` → anchor session → `submitTurn` with
  `userInput: "Improve this selection:\n\n<selected>"`), which is already correct.

### 2. Mode selector — `editor/Editor.vue`

- Add `<select v-model="selectedMode">` over `store.state.modes` (default
  `store.state.modes[0]?.name`, fallback `"proofreader"`) in the chat-section
  header. Pass `selectedMode` to `submitTurn` instead of the hardcoded
  `defaultModeName()`.

### 3. Free-form chat (doc-level) — `editor/Editor.vue`

- Add a text `<input>` + send button in the chat section. Send →
  `createSession(undefined, selectedMode)` → `submitTurn({ sessionId, modeName:
  selectedMode, documentId, userInput })`. No anchor path. Reuse/rename the
  existing `submitTurn`/`createSession` wired actions; do not reinvent them.

### 4. Meter + RAG rendering — `editor/Editor.vue`

- In the chat section, render `activeTurn.cumulative` (per-component tally the
  store already accumulates) and the latest `activeTurn.meter`.
- Render `activeTurn.rag` chunks with their provenance/source when the turn used
  `retrieve`/`read_note`.

### 5. Error surfacing — `editor/Editor.vue`

- Show `activeTurn.error.code` + `.message` prominently (the store already holds
  it). For `model-not-found` / `no-model-available` / `daemon-unreachable`, add a
  one-line provisioning hint ("provision the mode's default model — see
  macos-dev-config/models.json").

### 6. Tests — `client/tauri/tests/`

Extend the existing store fixtures (`tests/store.test.ts`) and add editor cases:

- selection → `hasSelection` true; empty selection → false.
- mode selector sends the chosen `modeName` in `submitTurn`.
- doc-level chat calls `createSession(undefined, modeName)`.
- a faked `meter` SSE event updates `turn.cumulative`; a faked `rag` event renders.
- a `model-not-found` error surfaces (not swallowed).

### 7. Verification

```sh
cd client/tauri && bun test && bun run typecheck   # must stay green
bun run tauri:dev                                    # manual smoke (daemon up + model provisioned)
```

Manual smoke: open a doc → select text → button enabled → click → anchored
session streams + candidate diff; type free-form → doc-level chat streams; pick a
mode → turn uses it; meter + RAG render; unprovisioned model shows the error
hint.

## Report back

- Confirm the trigger fix (selection → button → anchored session) works mouse and
  keyboard.
- Confirm the four rendered affordances (mode selector, doc-level chat, meter,
  RAG) and the error surfacing.
- Note any place the store's action shapes forced an adaptation (should be none —
  this is presentation-only).
