# ADR-0012: Streaming — SSE with typed events, NDJSON fallback

Status: Accepted

## Context

Clients need live, typed feedback: token stream, token-meter updates, rewrite
candidates, diffs, completion, and errors. Ollama already speaks NDJSON for
streaming; the system needs a richer, self-describing stream for its own events.

## Decision

Streaming uses **SSE with typed events**:

```
event: token      data: {"text":"…"}
event: meter      data: {"promptTokens":{...},"completionTokens":{...}}
event: candidate  data: {"blockId":"…","text":"…"}
event: diff       data: {"blockId":"…","insertions":[…],"deletions":[…]}
event: done       data: {"usage":{...}}
event: error      data: {"code":"…","message":"…"}
```

A **drop-in NDJSON path mirrors Ollama** if a raw passthrough is ever needed
(e.g. the provider gateway forwarding Ollama's own stream verbatim). The OpenAPI
spec defines the SSE event shapes so clients codegen them (ADR-0003).

## Consequences

- **+** Self-describing, versioned events; clients just switch on `event:`.
- **+** The token meter is a first-class streamed event, not a polling concern.
- **−** SSE requires an HTTP connection per stream; fine locally, and the NDJSON
  fallback covers providers that prefer it.

## Alternatives considered

- **WebSockets** — rejected: SSE is simpler, one-directional (which is all
  streaming needs), and drops into existing HTTP/OpenAPI tooling.
- **NDJSON only** — rejected: no event typing/metadata channel; but kept as the
  passthrough fallback.
