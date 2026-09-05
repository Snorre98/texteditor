# ADR-0013: Clients — OpenTUI first, Tauri later, both dumb

Status: Accepted

## Context

Two clients are planned: a terminal TUI (today) and a richer Tauri markdown
editor (later). Both must be thin over the same API, and the first one must be
the fastest path to a working system with a live token meter.

## Decision

1. **TUI first — OpenTUI** (Zig core, TS bindings; write TS directly or via
   Solid, matching OpenCode). Panels: markdown editor, chat, live token meter,
   model/mode switcher, RAG results, diff preview (OpenTUI ships native
   `Markdown`, `Diff`, `TextTable` renderables).
2. **Markdown editor later — Tauri 2** (Rust core + system WebView) + Vue 3 +
   CodeMirror 6. **No Node at runtime** (Bun/Node is build-time only). Assistant
   affordances: selection popover chat bubble (CodeMirror tooltip API) and
   side-by-side candidates (`@codemirror/merge`).
3. **Both are dumb clients** — generated from the spec (ADR-0003), containing no
   domain logic. The Tauri app may keep native file browsing/OS integration, but
   all edits and versioning go through the engine so history is consistent.

## Consequences

- **+** The frontend-swap guarantee is realized: build the engine once, ship
  both clients from one spec.
- **+** TUI is the fastest path to the live token-metering loop.
- **−** OpenTUI renderer choice (TS directly vs Solid) is a minor unresolved
  decision to lock at TUI build time.

## Alternatives considered

- **Tauri first** — rejected: slower to first working system; the token-meter
  loop is the priority and the TUI reaches it fastest.
- **Electron** — rejected: Node at runtime, heavier; Tauri is the stated target.
- **Single native CLI only** — rejected: the richer markdown editing experience
  is a stated later goal; the architecture supports both without rework.
