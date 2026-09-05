# ADR-0019: Modes and tools as data — schemas, location, validation, handler binding

Status: Accepted

Supersedes: ADR-0009 (mode field set), ADR-0010 (tool registry as one module).

## Context

ADR-0009/0010 made modes and tools "data, not code" but left three specifics
vague: where the data files live, how they're validated (and the exact failure
when they reference a missing model or tool), and — the hard seam — how a tool's
JSON definition binds to its Go handler without a code/data leak.

A session tenet (locked services over pure DTOs) and R2 ("narrow, stable") sharpen
the constraints: modes/tools are app logic (not machine-local), so they belong
with the engine; and any binder must be a sealed name-based seam, never reflection
or data-reaching-into-code.

## Decision

### 1. Location — engine repo, versioned + embedded

Mode and tool definitions live in the engine repo at `config/modes/*.json` and
`config/tools/*.json`, versioned in git and shipped via `go:embed`. They are not
machine-specific; the machine-specific part is *which models* exist, and that
lives in the fleet manifest (ADR-0018). The registries load-and-validate all files
at startup.

### 2. Validation — fail-fast at startup, typed errors

The mode/tool registries validate against committed JSON Schemas and the fleet
manifest/tool registry at load. A violation fails engine startup (not a turn)
with typed errors:

| Error | Condition |
|---|---|
| `mode-refs-unknown-model` | `defaultModel` not in the fleet manifest |
| `mode-unreachable-no-tag` | `mode.name` appears in no model's `modeTags` |
| `mode-refs-unknown-tool` | a `toolAllowlist` entry is not a registered tool |
| `tool-has-no-handler` | a registered tool's name has no bound Go handler |
| `schema-invalid` | a mode/tool file fails its JSON Schema |

Fail-fast beats lazy: a broken mode is a config bug, surfaced at startup, not a
mid-turn `no-model-available` surprise. Files are re-validated on read, so a
later edit is caught the same way.

### 3. Tool → handler binding — name-keyed, in the executor

The Tool registry (definitions) and Tool executor (execution) are split (ADR-0016
§5). The executor owns a private `map[name]→handler func` in its package; the
registry owns schemas. **The `name` is the seam.** At `Invoke(name, args)` the
executor looks up the name in its handler map. Startup cross-checks registry names
against executor handlers and fails with `tool-has-no-handler`. No reflection, no
data naming a Go symbol, no plugins.

### 4. Concrete schemas

**Mode:**

```jsonc
{
  "name": "proofreader",                  // required, unique
  "systemPrompt": "…",                    // required
  "defaultModel": "gemma4-12b",           // required; must resolve via manifest
  "toolAllowlist": ["read_note", "diff"], // subset of registered tools
  "params": { "temperature": 0.3, "maxTokens": 2048 },
  "contextBudget": { "maxHistoryTokens": 32000, "maxRagTokens": 16000 },
  "maxSteps": 4,                          // per-mode dispatch/observe bound
  "agentic": false,                       // multi-turn tool loop vs single-shot pass
  "kind": "model",                        // "model" | "assistant" (reserved)
  "preamble": ""                          // spliced before systemPrompt (e.g. citation line)
}
```

Required: `name`, `systemPrompt`, `defaultModel`. Everything else optional with
documented defaults. The extra fields (beyond ADR-0009's set) are deliberate, not
speculative:

- `maxSteps` + `agentic` — a mode's turn shape varies: `proofreader` is a
  single-shot pass; `literature-reviewer` is a multi-turn tool loop. Encoding this
  as data lets the loop honor per-mode bounds without code.
- `preamble` — per-mode citation discipline (ADR-0015's "use only sources
  provided" line) varies by mode; some want it, some don't.
- `kind` — reserved now (OpenCode's `model`/`assistant` distinction) rather than
  guessed; it carries no current behavior.

**Tool:**

```jsonc
{
  "name": "retrieve",                    // required, unique → handler key
  "description": "retrieve a passage by citation",
  "parameters": {                        // OpenAI-function schema, spliced into the prompt
    "type": "object",
    "properties": { "citation": { "type": "string" } },
    "required": ["citation"]
  }
}
```

`parameters` is the exact JSON Schema the context assembler splices into the
payload, so its size is directly visible to the meter (ADR-0011).

## Consequences

- **+** Modes/tools are versioned app logic, shipped in the static binary —
  consistent with the single-binary, no-runtime-deps goal (ADR-0003).
- **+** Fail-fast validation with exact typed errors; a broken reference can never
  reach a turn.
- **+** The tool binder is a sealed name seam — no reflection, no code/data leak,
  R1/R3 clean.
- **−** Adding a tool requires *both* a data entry (registry) and a Go handler
  (executor); the `tool-has-no-handler` startup check is what keeps them in sync
  (a discipline surface, but a small one).

## Alternatives considered

- **Modes/tools in `macos-dev-config` (machine-local)** — rejected: they're app
  logic, not machine-specific, and runtime edits would bypass the engine's
  validation and the locked-service boundary.
- **Store in SQLite** — rejected: contradicts ADR-0009's explicit rejection of
  DB-rows-as-primary (git-diffability + hand-editability).
- **Data names a Go symbol + reflection** — rejected: data reaching into the
  binary's symbol table, fragile to renames, R1/R3 violation.
- **Plugins/dynamic-load** — rejected: breaks the single-static-binary + no-CGO
  goal for a 7-tool single-user app.
