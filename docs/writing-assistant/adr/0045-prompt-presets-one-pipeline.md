# ADR-0045: Prompt presets — one turn pipeline, behavioral mode fields removed

Status: Accepted

Amends: ADR-0019 (the mode field set and its justifications), ADR-0028 (the
router toggle is parked; no mode may enable it), ADR-0044 §5/Phase 3 (the
decision layer gets one global policy, not per-mode `decision` config).

## Context

ADR-0019 gave modes behavioral levers — `agentic`, `maxSteps`, `toolAllowlist`,
`toolCalling`, per-mode `contextBudget`, `params`, `preamble`, `kind` — so each
persona could shape its own turn. As built, most of those levers are dead,
ignored, or traps:

- `params` never reach the provider when the fleet manifest supplies defaults:
  the loop passes no `Overrides` to `Fleet.Resolve`, and the assembler's
  mode-params fallback only fires when both merged values are zero.
- `kind` has no behavior anywhere (ADR-0019 itself calls it reserved).
- `preamble` duplicates the citation sentence already inside three of the four
  `systemPrompt`s.
- `toolCalling: "router"` is enabled by no shipped mode; the entire
  `ToolDecider` seam is dormant in production.
- `proofreader` and `grammar` advertise `edit_markdown` but `agentic: false`
  forces `maxSteps = 0`, so tool calls are silently discarded — they cannot
  write, despite offering the tool.
- The TUI defaults to the alphabetically-first mode, `drafter`, which has **no**
  `edit_markdown` at all. The first turn a new user runs cannot produce a file
  edit; this is the concrete reason "the LLM writing to a file" has never been
  tested successfully.
- Mode names are load-bearing in two more places — the `modeTags` fallback
  ladder and session `modeType` labels — multiplying paths that must be kept
  consistent.

The product's hard problem is the pre-processing and context layer (retrieval,
gating, budgets, explanation) feeding one generate → approve → apply loop. Four
divergent turn shapes multiply the paths to test without improving that loop.

Forces:

- Contract-first and rename-averse: `mode` is already on the wire (`/modes`,
  `Task.modeName`, `session.modeType`); renaming it to `preset` buys terminology
  at the cost of three codegens and every client.
- Fail-fast validation (ADR-0019) is good and stays: unknown model, unknown
  tool, unreachable-no-tag must still fail startup.
- Fleet fallback groups by `modeTags`; presets remain named, so the tags and
  the ladder keep working unchanged.
- ADR-0044 froze Tauri/web and made the TUI the active client; a mode collapse
  is engine-side and does not touch that decision.

## Decision

1. **Modes collapse to prompt presets.** The mode schema is exactly
   `name`, `systemPrompt`, `defaultModel` (all required; no other fields).
   Removed from the schema and the shipped files: `toolAllowlist`, `params`,
   `contextBudget`, `maxSteps`, `agentic`, `kind`, `preamble`, `toolCalling`.
   The wire/registry term remains `mode`; "preset" is the conceptual name.

2. **One fixed turn pipeline.** Every turn runs the same shape: mentions →
   history → retrieval (auto-RAG; later gated by the decision layer) → assemble
   → agentic stream with tool dispatch → observe → candidate/diff. All
   registered tools are available on every turn; there is one global step cap
   and one global context budget.

3. **Pipeline policy is one data file.** `config/pipeline.json` (embedded,
   schema-validated at startup) holds `maxSteps`, `maxHistoryTokens`,
   `maxRagTokens`, `maxMentionTokens`, and `autoRagTopK`. Editing policy is one
   edit, not four.

4. **Presets are a UI concern.** The TUI presents them as tabs; selection sends
   `Task.modeName` exactly as today. `GET /modes` keeps serving
   `{name, systemPrompt, defaultModel}`; no contract rename.

5. **`modeTags` stay.** Fleet fallback still groups by preset name; the
   `mode-unreachable-no-tag` startup gate is unchanged.

6. **The router seam is parked.** The loop no longer reads `toolCalling`; the
   `ToolDecider` package and `routergate` stay in the tree but unwired. ADR-0028
   enablement is deferred indefinitely; the ADR-0044 decision layer is the
   sanctioned route for model-driven decisions.

7. **One global decision policy.** ADR-0044 Phase 3's per-mode
   `decision: off|gate|route` config is replaced by a single pipeline-level
   policy; the decision layer does not reintroduce mode fields.

## Consequences

- **+** Every preset can edit: no more drafter trap, no advertised-but-dropped
  tools, no silent single-shot branch.
- **+** One pipeline to test and meter; the dead `params` path, the
  `kind`/`preamble` fields, the dormant router branches, and per-mode budget
  matrices are deleted.
- **+** Preset switching stays declarative data; fail-fast validation and
  `modeTags` fallback survive unchanged.
- **−** Per-preset tool restriction is lost — e.g. `grammar` can now call
  `edit_markdown`. Accepted: the prompt text steers behavior, and the approve
  step (ADR-0047) is the real guard.
- **−** Terminology mismatch: the product says "presets", the contract says
  `mode`. Accepted to avoid a breaking rename; a future rename needs its own
  ADR and codegen lockstep.
- **−** Removing fields rewrites all mode files and the JSON Schema; stale
  files fail startup with `schema-invalid` (intended).
- **−** Parked router code remains as dead weight until a cleanup ADR removes
  it.

## Alternatives considered

- **Freeze modes as-is (moratorium only)** — rejected: the dead fields and the
  drafter/grammar traps remain, and every new feature (decision layer, locate)
  would have to decide whether it is per-mode.
- **Keep data, ignore behavioral fields in the loop** — rejected: the schema
  would lie about what matters; fail-fast validation should reject fields that
  do nothing.
- **Remove modes entirely (single hardcoded prompt)** — rejected: presets are
  genuinely useful (prompt + model pairing), sessions already carry
  `modeType`, and `modeTags` is the fleet fallback grouping.
- **Rename to `/presets` and `presetName`** — rejected for now: a breaking
  contract change across three codegens for a vocabulary fix; not worth the
  lockstep.
