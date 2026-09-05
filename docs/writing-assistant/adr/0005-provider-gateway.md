# ADR-0005: Provider gateway — model name → endpoint + capabilities

Status: Accepted

## Context

Model serving is deliberately external (Layer 0): Ollama, MLX-LM, MLX-VLM, and
llama.cpp all serve different models on different local ports today (see
`macos-dev-config/servers.conf`), and a hosted API may be added later. The rest
of the engine must never care *which* runtime serves a model.

## Decision

Introduce a **Provider gateway** module whose single responsibility is mapping a
logical model *name* to a concrete OpenAI-compatible endpoint plus its
capabilities and sampling params:

```
Resolve(name) → {
  baseURL,          // http://host:port/v1
  capabilities: { contextLength, thinkingMode, supportsSystemPrompt },
  defaults: { temperature, maxTokens }
}
Chat(ctx, name, params)          // → completion
Stream(ctx, name, params)        // → SSE events
```

The gateway **never loads weights**; it only talks REST. Swapping Ollama ↔
MLX ↔ llama.cpp ↔ a hosted API changes only this module. Discovery of what
`name` means comes from the fleet manifest via the Fleet gateway (ADR-0006).

## Consequences

- **+** The entire model fleet is a uniform, hot-swappable resource to the agent
  loop and context assembler.
- **+** Provider-specific quirks (auth, request shape differences) are contained
  in one module.
- **−** One more indirection layer; a model name that is missing from both the
  manifest and any running server must be a clear, surfaced error (ADR-0007).

## Alternatives considered

- **Call provider REST directly from the agent loop** — rejected: leaks provider
  details into the loop; defeats hot-swap.
- **A full provider SDK per runtime** — rejected: overkill; local runtimes all
  expose OpenAI-compatible `/v1` already, so one thin adapter suffices.
- **Gateway owns model *downloading*** — rejected: provisioning is a separate
  concern owned by the Fleet gateway / `serve.sh` (ADR-0008).
