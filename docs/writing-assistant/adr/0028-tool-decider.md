# ADR-0028: Tool decider — optional router ("writer signals, specialist decides")

Status: Accepted

Extends: ADR-0016 (module inventory), ADR-0019 (modes/tools as data), ADR-0027
(shared-DTO catalog — gains `Decision`, `RouterContext`, `RouterUsage`,
`RouterResult`).
Relates: ADR-0018 (delegate runner), ADR-0024 (metering), ADR-0025 (control daemon).

## Context

The turn loop resolves tool intent *in the writing model itself*: the assembler
splices the mode's tool schemas, and a small or weak writer model can fall into a
thinking-loop (malformed call, hallucinated tool name, retries) that burns tokens
and derails the turn. The proposal is not to make routing faster but to make it
*correct*: separate "I need a tool" (the writer's job) from "which tool, what
arguments" (a specialist's job).

Needle 2 (45M params, its own C++ engine, a `.cact` artifact) is grammar-constrained
— it *cannot* emit schema-invalid JSON — so the malformed-call half of the loop is
eliminated by construction, and its empty-call/refusal maps onto the loop's existing
"plan needs no tool → answering" transition.

The requirement is a **strictly additive, toggleable module**: when off, the writer
decides the exact tool via native tool-calling and the app behaves exactly as the
accepted baseline; when on, the writer emits a coarse signal and the router decides.

## Decision

### 1. `ToolDecider` — an optional module, wired only when a mode requests it

```go
// shared/dto (owner-free pure DTOs, ADR-0027)

type Decision struct {
    Name       string          // real tool name (== a ToolDef.Name)
    Args       json.RawMessage // schema-valid arguments for that tool
    Confidence float32         // 0..1; < τ ⇒ "no tool, answer now"
}

type RouterContext struct {    // argument-binding context the loop re-bundles
    ToolDefs  []ToolDef        // the mode's allowlisted tools (candidate set)
    Chunks    []Chunk          // retrieved chunks (citation/note provenance for args)
    Selection *Selection       // the anchored block, when the session is block-scoped
    History   []Message        // recent conversation (arg context)
    UserInput string           // the turn's original request
}

type RouterUsage struct {      // the router call's own metering inputs
    Breakdown Breakdown        // router prompt's per-component split (reuses ADR-0016 §6)
    Counts    ProviderCounts   // router provider's exact counts
}

type RouterResult struct {
    Decision Decision
    Usage    RouterUsage
}

type ToolDecider interface {
    SignalTool() ToolDef   // the single request_tool definition spliced into the writer's payload
    Decide(ctx context.Context, intent string, c RouterContext) (RouterResult, error)
}
```

Two methods. The decider is a self-contained service in the Retriever style
(ADR-0016 §8): it resolves `needle-router` via Fleet and calls Provider internally;
its prompt layout and confidence threshold `τ` (default `0.7`, a hidden internal) are
private. It does **not** touch the Tool executor — it emits `(name, args)`; the loop
invokes.

### 2. `request_tool` — the writer's wire format (a synthetic tool, not a registered one)

`SignalTool()` returns the single tool the writer sees in router mode:

```jsonc
{
  "name": "request_tool",
  "description": "Request an external action (retrieval, note access, editing, citation, vault search). Describe what you need in free text.",
  "parameters": { "type": "object", "properties": { "intent": { "type": "string" } }, "required": ["intent"] }
}
```

- The writer does *native* tool-calling but trivially: one tool, one free string.
  `finish_reason=tool_calls` reaches the loop exactly as today; the loop sees
  `request_tool`, intercepts, and routes `intent` to `Decide`.
- `request_tool` is **not** in `config/tools/*.json` and has no handler — it must not
  enter the Tool registry (it would trip `tool-has-no-handler`). It is the router's
  protocol, owned by `ToolDecider`.
- Reserved name: the Tool registry rejects any real tool named `request_tool`
  (name-collision, fail-fast).

### 3. Per-mode toggle: `toolCalling`

`Mode` gains one optional field:

```jsonc
"toolCalling": "native"   // or "router"; default "native"
```

- `native` (default) → the loop uses native tool-calling; **no `ToolDecider` is
  wired**; the writer decides the exact tool. Byte-identical to the accepted baseline.
- `router` → the loop wires a `ToolDecider`; the assembler splices `SignalTool()`
  instead of `ToolRegistry.AllowlistFor(mode)`.
- The assembler is **unchanged**: it already takes `[]JSONSchema` (ADR-0016 §6); the
  loop selects which schema slice to pass. The `request_tool` description carries the
  "you may request a tool" instruction, so no system-prompt change either.
- `toolCalling: "router"` is only meaningful with `agentic: true`; a router on a
  `maxSteps=0` single-shot mode is allowed but pointless (no hard rule).

### 4. Fail-fast startup gates (same discipline as ADR-0019 §2)

Two new startup errors, owned by the Mode registry's cross-reference validation
(it already checks `mode-refs-unknown-model` / `-unknown-tool`):

| Error | Condition |
|---|---|
| `mode-refs-router-unavailable` | a mode has `toolCalling: "router"` but no resolvable `needle-router` model in the manifest |
| `router-tools-stale` | the manifest's `needle-router` `source.fingerprint` ≠ the engine's tool-set hash |

The tool-set hash is a deterministic fingerprint over the canonical sorted set of
`(name, description, parameters)` of `ToolRegistry.List()`. It is computed at startup
and compared against the manifest fingerprint recorded at `needle finetune` time. A
mismatch means the `.cact` was trained on a different tool vocabulary — a derived
artifact that drifted — and the router refuses to serve, never silently degrades.

### 5. Metering — a second `Meter.Attribute`, no schema change

The router call is a distinct model call with its own prompt/response. The loop, on
receiving `RouterResult`, calls `Meter.Attribute(turnID, result.Usage.Breakdown,
result.Usage.Counts)` — a **second** attribution row. The `meter_events.model` column
(`needle-router`) plus `turn_id`/`session_id` grouping already disambiguate router
rows from writer rows (data-model §1.3), so **no `meter_events` schema change**.
`Breakdown` is reused with the mapping: `SystemPrompt`=router instruction,
`Tools`=candidate schemas, `Rag`/`History`=arg context, `User`=intent, `Thinking`=0
always. ADR-0024's thinking reconciliation does not apply (Needle is not a thinking
model). Q1's scale-to-total now holds *per call*; the per-session budget (ADR-0026 §5)
counts both calls.

### 6. Fallback ladder

1. `Confidence ≥ τ` → dispatch `Invoke(decision.Name, decision.Args)`.
2. `Confidence < τ` / empty-call / refusal → **`answering`**, graceful "no tool" (the
   existing `planning → answering` transition; no derail, no extra call).
3. `Decide` returns an `error` (Needle up at startup but crashed mid-turn) → labeled
   `error` event (`router-unreachable`), then `answering` — **no retry loop**. Native
   tool-calling is *retained* as the permanent OFF state; a per-step native re-call
   (tier-3) is explicitly deferred as YAGNI.

### 7. Deployment — `delegate` + facade, ADR-0003 preserved

Needle is not OpenAI-compatible. It is served as a **`delegate` runner** (ADR-0018 §3)
behind a thin OpenAI facade, resident in `macos-dev-config`:

```jsonc
"daemons": [ { "name": "needle", "runner": "delegate", "delegate": "serve-needle.sh", "host": "127.0.0.1", "port": 8081 } ],
"models": [ {
    "name": "needle-router",
    "daemon": "needle",
    "source": { "kind": "needle", "file": "needle2.cact", "fingerprint": "<tool-set hash>" },
    "capabilities": { "contextLength": 2048, "thinkingMode": false, "supportsSystemPrompt": false },
    "defaults": { "temperature": 0.0 },
    "modeTags": []
} ]
```

- `serve-needle.sh` wraps the C++ engine and exposes `/v1/chat/completions` so
  `Provider.Chat` (non-streaming — one small response) carries the call with **zero
  Provider change**. Refusal/empty is encoded as an empty completion; confidence as a
  stop-reason/header inside the facade.
- Needle never enters the engine binary: **ADR-0003 (single static Go, no CGO) and
  ADR-0025 (engine→daemon only) are untouched.**
- Resolution is by **name**, the `nomic-embed` special-purpose pattern (ADR-0016 §8) —
  not a mode's `defaultModel`, so ADR-0015's fleet policy does not select it.

## Consequences

- **+** The malformed-call failure mode is eliminated by grammar, and refusal
  degrades gracefully — a correctness win for weak writers, not a speed optimization.
- **+** Strictly additive: `native` is the byte-identical baseline; the router is a
  sealed, optional service on its own seam (`ToolDecider`), with the Tool executor,
  Provider, and assembler all unchanged.
- **−** A derived artifact (`.cact`) coupled to the tool vocabulary re-enters the
  system — contained only by the `router-tools-stale` sync gate; this strains the
  zero-drift ethos of "tools are data" (ADR-0019).
- **−** Two metered choke points per router turn (ADR-0011's "single choke point" is
  now two); the router is a *visible* added cost, not a token savings.

## Alternatives considered

- **Native-only (writer decides)** — the baseline; retained as the OFF state,
  rejected as the *only* state because it keeps the thinking-loop failure mode for
  weak writers.
- **Literal marker + stream scan (`⟦TOOL⟧`)** — rejected vs `request_tool`: needs a
  custom token scanner and a mid-stream stop, and foregoes native
  `finish_reason=tool_calls`.
- **In-process (CGO embed of the C++ engine)** — rejected: violates ADR-0003.
- **Router as a mode `defaultModel`** — rejected: category error; it's a
  special-purpose decision service, not a writing persona (the `nomic-embed`
  pattern).
- **Always fine-tune, no sync gate** — rejected: silent staleness on tool edits (the
  failure class `failure-semantics.md` forbids).
