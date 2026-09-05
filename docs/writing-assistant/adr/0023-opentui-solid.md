# ADR-0023: OpenTUI renderer — Solid

Status: Accepted

Supersedes: ADR-0013 (renderer-choice dangling note).

## Context

ADR-0013 deferred the OpenTUI renderer choice ("TS directly vs Solid") to TUI
build time, leaving it as §11 risk #6. The deferral cost more than it saved: the
renderer choice shapes how the whole client-side reactivity is authored, and it
should be consistent with the heavier client stack settled in ADR-0017 (Zod +
openapi-to-rust).

## Decision

The OpenTUI TUI is written with **Solid** (reactive signals), matching OpenCode
itself, whose official bindings are Solid-first. The panels (markdown editor, chat,
live token meter, model/mode switcher, RAG results, diff preview) are Solid
components over the generated Hey API + Zod client.

## Consequences

- **+** Reactive programming model transfers toward the future Vue 3 + CodeMirror
  editor (ADR-0013), so the TUI patterns aren't dead-end.
- **+** Solid is OpenCode's renderer, so ecosystem/tooling alignment is high.
- **−** A runtime + setup cost vs raw TS; a small dependency surface the "dumb
  client" must carry (dumb in *domain logic*, not in rendering).

## Alternatives considered

- **TS directly against the Zig/TS core** — rejected: manual re-render discipline,
  and the reactive patterns don't transfer to the Vue editor.
