# ADR-0010: Tool registry — tools are data with JSON schemas

Status: Accepted

## Context

The agent loop dispatches writing-specific tools (`retrieve(citation)`,
`read_note`, `edit_markdown`, `suggest_revision`, `diff`, `cite`,
`search_vault`). Tool schemas are themselves prompt overhead, and the system's
pedagogical core is showing that cost — so schemas must be first-class data.

## Decision

Tools are **declarative data**: each tool is a name + JSON Schema + a handler
binding, held in a `Tool registry`. The JSON Schema is what gets spliced into the
prompt, so its size is directly visible to the context assembler (ADR-0011).

Public API: `Register(tool)`, `List()` → schemas, `AllowlistFor(mode)` (filters
by a mode's `toolAllowlist`), `Invoke(name, args)` (dispatches to the bound Go
handler).

## Consequences

- **+** Adding a tool = adding a schema entry; no loop/context changes.
- **+** Each tool's schema cost is measurable (the token meter can report
  "tools: N tokens").
- **−** The registry must keep schema ↔ handler consistent (a schema with no
  handler, or vice versa, is a load-time error).

## Alternatives considered

- **Hard-coded tool functions** — rejected: schemas duplicated as strings in
  code, cost invisible, adding a tool is a code change.
- **Tool definitions only in the OpenAPI spec** — rejected: OpenAPI is the
  *client* contract; tool schemas are engine-internal prompt data with a
  different lifecycle (mode-scoped, frequently edited).
