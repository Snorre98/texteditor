# ADR-0002: Layered, REST-first process architecture

Status: Accepted

## Context

The system spans four logical layers: clients (TUI, markdown editor), an OpenAPI
contract, a Go engine owning all logic and state, and external model serving.
Two forces pull in opposite directions: the clients must be easy to swap
("clients are dumb"), and every layer must be testable in isolation.

The frontend-swap guarantee from `architecture.md` is the load-bearing rule:
clients render and send commands only; they hold no RAG, prompt assembly, mode
definitions, or versioning.

## Decision

Adopt a **layered, REST-first** topology:

1. **Clients (Layer 3)** — dumb, swappable. They render, send commands, stream
   responses, show the token meter and diffs. No domain logic.
2. **API contract** — one versioned OpenAPI/JSON Schema spec is the source of
   truth; every client is *generated* from it (ADR-0003).
3. **Engine (Layer 2)** — a single Go process owning all logic + state.
4. **Serving (Layer 0)** — external, swappable; reached over OpenAI-compatible
   REST, controlled via the fleet manifest + lifecycle verbs (ADR-0006/0007).

Layer boundaries are **process boundaries**: REST + SSE (ADR-0012) between them.
"Everything over REST" means each layer runs as its own process and can be
tested against a stubbed HTTP endpoint.

## Consequences

- **+** Any client can be generated from the spec; adding one is a codegen step.
- **+** The engine is testable headless: drive it with `curl`/a generated client.
- **+** Serving is hot-swappable because the engine only ever sees a REST URL.
- **−** Serialization/SSE framing overhead at each boundary (acceptable locally).
- **−** Contract-first requires the spec to be written *before* the server —
  a mild inversion that pays off in generated, consistent types.

## Alternatives considered

- **In-process library calls between engine and client** — rejected: forces a
  shared language/runtime, kills the TUI↔Tauri swap.
- **gRPC / typed RPC everywhere** — rejected: OpenAI-compatible REST is the
  ecosystem standard for inference, and SSE gives clean streaming; gRPC adds
  ceremony without a local-scale payoff.
- **Event-sourced bus as the only seam** — rejected as primary: REST is simpler
  for request/response; SSE covers the streaming need.
