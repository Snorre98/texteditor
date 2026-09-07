# ADR-0041: Floating, draggable chat window with session list (Tauri client)

Status: Accepted

Amends: ADR-0026 §4 (the *rendering model* becomes one floating window with a
session list instead of N concurrent bubbles; the session data model is
unchanged), ADR-0013 §2 (the chat is no longer a static block under the editor).

## Context

The Tauri chat is a static `<section>` pinned under the CodeMirror editor: it
competes with the workspace for vertical space, renders messages as plain text,
and surfaces none of the session machinery ADR-0026 already provides
(`/sessions`, `/sessions/{id}/messages`, per-session `sessionStates` in the
store). The user wants the chat to hover *over* the workspace, be draggable,
and expose the full chat feature surface — including past sessions — through a
proper component library rather than hand-rolled CSS.

Forces:

- Clients stay dumb (ADR-0013 §3): window geometry, docking, and the active
  session are **client-local UI state** and must never enter the engine or the
  API contract.
- The engine is the only source of session truth (ADR-0026): the window renders
  engine-owned sessions; it never keeps its own history.
- ADR-0040 lands fleet observability the same cycle: the Tauri GUI has no model
  selector today, and the chat window is the natural home for mode **and**
  model selection (the store already carries `switchModel` + `liveState`).
- No cancel-turn route exists in the contract, so "stop generating" cannot be a
  pure client feature.

## Decision

1. **One floating chat window** overlays the workspace (`position: fixed`, own
   stacking layer). The editor owns the full viewport; the chat floats above it
   and never reflows the document.
2. **Window behaviors** (all client-side):
   - **Drag** anywhere by the header.
   - **Resize** from any edge/corner, clamped to the viewport.
   - **Minimize** to a small floating orb that restores the window.
   - **Snap/dock** to the left/right/bottom edges while dragging; docked state
     reflows the editor via CSS grid (editor column shrinks), free-float is an
     overlay. Docking is a *visual* mode — sessions and streaming are untouched.
   - **Persistence** of `{dock, x, y, width, height, minimized, sessionId}` in
     `localStorage` (key `chat-window-state`), hydrated on mount, debounce-saved.
3. **Session list + new chat** live inside the window: every session of the open
   document (anchored and free), labeled from `createdAt`/`modeType`/
   `anchorBlockId` (the API has no session titles — derived labels until one
   lands), resume with full history via `selectSession`, "new chat" via
   `createSession`, active-session highlight plus a per-session "working"
   indicator for concurrent turns (ADR-0026 §4). `Ask about selection` creates
   or resumes an anchored session *entry in this window* — the N-bubble
   rendering from ADR-0026 §4 is superseded; one window, sessions switchable.
4. **The full chat feature surface moves into the window**: markdown-rendered
   message bubbles (`marked` + `DOMPurify` sanitize), the streaming token tail,
   turn status + phase steps transcript, collapsible Meter and RAG panels, mode
   selector, **model selector with `liveState` + switch progress** (store's
   `switchModel`, ADR-0040), error/backpressure banners, per-message copy, and
   "retry last message" (resubmits the last user input over the existing
   `/turn` route — no new API).
5. **Deferred (future work)**: "Stop generating" — requires a cancel route in
   `openapi.yaml` + engine + the three-way codegen lockstep; session titles —
   an engine field on `CreateSessionRequest` + list rendering.

## Consequences

- **+** Zero engine/contract changes; the whole feature ships client-side, so
  the three-way codegen lockstep is untouched.
- **+** The chat stops competing with the editor for space; the pedagogical
  token-meter surface (meter/RAG/steps) is preserved, collapsible, and stays
  visible while writing.
- **+** Past sessions become first-class: resume, per-session concurrency, and
  anchored selection sessions all surface in one place.
- **−** One window at a time (session switching, not N visible bubbles) — a
  deliberate simplification of ADR-0026 §4's rendering; concurrent turns still
  run and report in the store.
- **−** Session labels are derived (no titles in the contract yet); the list is
  functional but not as descriptive as titled sessions will be.

## Alternatives considered

- **Nuxt UI v4 chat components** — rejected: Nuxt-first, drags in a full design
  system; heavier coupling than this client needs.
- **vue-advanced-chat** — rejected: a realtime multi-user chat-room model
  (~500 kB); wrong shape for an AI writing assistant with engine-owned sessions.
- **Tabs across the top instead of a session list** — rejected: anchored +
  doc-level sessions grow unboundedly; a list sidebar scales, tabs don't.
- **Keep the static section and only restyle it** — rejected: the whole point
  is to move the chat off the page flow and over the workspace.
