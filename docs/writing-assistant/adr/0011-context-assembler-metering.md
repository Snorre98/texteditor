# ADR-0011: Context assembler — the single metered choke point

Status: Accepted

## Context

The system's whole point is to make "what actually costs tokens" visible. The
governing levers are: system prompt/mode, tool schemas, RAG chunks, conversation
history, thinking tokens, and completion length. Without one place where the
payload is assembled, token cost is scattered and invisible.

## Decision

Introduce a **Context assembler** — a single, pure function that takes the active
mode, tool schemas, retrieved RAG chunks, conversation history, and user input,
and produces the **exact token payload** for a call, returning a **per-component
token breakdown**:

```
Assemble(ctx, {mode, tools, ragChunks, history, userInput}) →
  { payload, breakdown: { systemPrompt, tools, rag, history, user, thinking } }
```

Token counts are **read from the provider's response** (`prompt_eval_count` /
`eval_count`) and *attributed* to their sources — the engine **never reimplements
a tokenizer**. A companion **Token metering / observability** module subscribes
to the assembler's breakdown + provider counts and emits live events
(ADR-0012).

## Consequences

- **+** One observable choke point: change any lever (a mode, a tool, chunk size,
  history truncation) and watch the meter move.
- **+** Attribution is honest because it uses provider-reported counts, not an
  engine-side estimate.
- **−** Attribution of *thinking* tokens and cross-component boundaries is
  approximate (the provider reports totals, not per-component); the breakdown is
  the assembler's own accounting overlaid on real totals.

## Alternatives considered

- **Metering scattered across modules** — rejected: no single place to reason
  about cost; the pedagogical loop dies.
- **Reimplement a tokenizer to count exactly** — rejected: every model has a
  different tokenizer; provider-reported counts are exact and free.
- **Meter only totals, no attribution** — rejected: totals alone don't teach
  *which lever* to pull.
