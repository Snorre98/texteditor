# ADR-0009: Mode registry — modes are declarative data, not code

Status: Accepted

## Context

The system must support distinct behaviors (edit, draft, proofread, review
literature) each with its own system prompt, default model, tool set, sampling
params, and context budget — and these must be changeable quickly ("change
model/persona" in the spirit of OpenCode's modes and agents) without rebuilding.

## Decision

Modes are **declarative configurations** stored as data (JSON, sibling to the
fleet manifest), interpreted by a `Mode registry` module. Each mode:

```jsonc
{
  "name": "proofreader",
  "systemPrompt": "…",
  "defaultModel": "gemma4-12b",       // logical name → provider gateway (ADR-0005)
  "toolAllowlist": ["read_note", "edit_markdown", "diff"],
  "params": { "temperature": 0.3 },
  "contextBudget": { "maxHistoryTokens": 32000, "maxRagTokens": 16000 }
}
```

Switching mode is a config change, not a rebuild. The mode registry's public API
is `List()`, `Get(name)`, and `ResolveModel(mode)` (which consults the fleet
manifest via the Fleet gateway to confirm the default model is servable).

## Consequences

- **+** Adding/editing a mode is a data edit; experiments (temperature, model,
  tool set) are cheap and visible.
- **+** The token cost of a mode's fixed parts (system prompt, tool schemas) is
  measurable per mode by the context assembler (ADR-0011).
- **−** Unvalidated mode data could reference a missing model or tool; the
  registry must validate against the manifest and tool registry at load and
  surface the error (failure contract).

## Alternatives considered

- **Modes as Go code (structs/switch)** — rejected: persona changes require a
  rebuild; contradicts "data, not code."
- **Modes as database rows** — rejected as *primary*: files are versioned with
  git, diffable, and easier to hand-edit than DB rows; SQLite can cache them if
  needed later.
