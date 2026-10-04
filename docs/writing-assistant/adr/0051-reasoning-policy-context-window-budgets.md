# ADR-0051: Reasoning policy and context-window budgets

Status: Accepted

Extends: ADR-0011 (the assembler is the single metered choke point), ADR-0024
(thinking-token accounting), ADR-0044 (context inspector + snapshots), ADR-0045
(one pipeline; no per-preset behavioral fields), ADR-0049 (session context
policy + per-turn overrides; pins never bypass budgets).

Amends: ADR-0026 §5 (the per-session token budget becomes an enforced
soft/hard policy with compaction, not a bare pre-turn check).

Relates: ADR-0015 (fallback ladder — a thinking-capable model may be chosen
where a non-thinking one would do), ADR-0046 (the TUI renders the labels),
ADR-0047 (fast mechanical turns make the approve boundary feel instant),
ADR-0048 (`/locate` anchored edits are the canonical thinking-off turn).

## Context

A 2026-10-04 trial on the local serving stack (gemma-4-26B-A4B 4-bit via
mlx-lm 0.32, 18 GB weights, ~12 tok/s decode, ~1 s prefix-cached prefill)
measured where a turn's time actually goes:

- A trivial mechanical edit (`edit_markdown` for one sentence) produced **429
  reasoning tokens** before an 83-character tool call — ~40 s of wall clock,
  none of it user-visible.
- With the server's 512-token default, that reasoning was truncated
  (`finish_reason: length`) **before** the tool call; the engine emitted an
  empty `done` and no candidate.
- The engine currently ignores `reasoning` deltas entirely; thinking is only
  *approximated* when a provider omits a count (ADR-0024). The dominant latency
  of a turn is invisible and unmetered.

The same trial is the project's purpose: map the limits (latency, context
capacity, model quality) and what extends them, so hardware choices are
evidence-based. Two limits surfaced:

- **Reasoning is spent unconditionally**, even where it buys nothing: the
  model thinks before every tool call, and no policy can turn it off or
  escalate to it only when a first attempt fails.
- **Context length is unbounded per session.** `Session.tokenBudget` is checked
  pre-turn when set, but there is no per-turn window gate, no soft warning, and
  no compaction; a long session silently approaches the model's window and the
  meter records no window utilization. ADR-0049 defines the session context
  policy and labeled drops; ADR-0044/C3 landed snapshots, `BudgetUsage`, and
  labeled drop records. The enforcement half is missing.

Forces:

- Q1/Q6 discipline (ADR-0011/0022/0044): every token and every decision that
  changes the payload is visible, labeled, and metered.
- One pipeline (ADR-0045): thinking and budget policy are **not** per-preset
  fields. Session-level defaults with per-turn overrides are the accepted shape
  (ADR-0049 §8).
- Dumb clients (ADR-0013 §3): the engine decides thinking and truncation; the
  TUI renders the labels and the meter.
- Local-first, single user: prefix caching (ADR-0049 §14) is a real lever, and
  a stable front-loaded prefix must stay stable across turns.
- Failures degrade with a label, never silently (Q1/ADR-0022): a truncated
  reasoning pass, an unsupported runner, a window drop, and a session-budget
  refusal are all typed, labeled outcomes.
- Contract-first (ADR-0017): every wire addition below is intent; exact shapes
  land in `api/openapi.yaml` before client code.
- The measurement is the deliverable: per-turn token counts and latency must be
  recorded per model/quant so the limit map accumulates.

## Decision

### 1. A thinking policy with three levels

A turn's thinking is governed by a policy with levels `off`, `auto`, and `on`:

- **`off`** — the provider request asks the runner to disable the thinking
  channel (template kwargs). Mechanical edits (proofread/grammar-style turns,
  `/locate` replacements) are the target case.
- **`auto`** — start thinking-off; **escalate to thinking-on only after a
  structured failure** (see §2).
- **`on`** — always think; drafting and open-ended authoring.

The default level is pipeline policy (data, §10); a session policy may override
it, and an optional per-turn override rides the ADR-0049 `Task.context` shape.
It is never a preset field (ADR-0045).

### 2. `auto` is bounded escalation, not guessing

`auto` runs the turn thinking-off first. If the structured result is a
retryable failure — `invalid-structure`, `guard-failed`, or a turn that ends
with no tool call and no answer — the engine retries **once** with thinking-on,
labeled `thinking-escalated`, bounded by the pipeline step cap. No unbounded
retry loop. The escalation is recorded in the snapshot, so "thinking bought
nothing" is measurable per turn.

### 3. Runner mapping, with labeled degradation

The provider request gains an optional thinking/template-kwargs field; the
provider maps it per runner:

| Runner | Mechanism |
|---|---|
| mlx-lm | per-request `chat_template_kwargs: {"enable_thinking": false}` (template-declared; verified for gemma4) |
| llama.cpp | `chat_template_kwargs` where the server enables jinja templates |
| OpenAI-compatible | `reasoning_effort` / vendor `thinking` fields where supported |

A runner that cannot disable thinking **degrades to thinking-on with a labeled
`thinking-unsupported` result** — never a silent pretend that thinking was off.

### 4. Exact thinking accounting, surfaced

The provider surfaces reasoning deltas as a raw `reasoning` event; the loop
accumulates them and the meter attributes them **exactly** to `thinking`
(ADR-0024's approximation remains only as the fallback when a provider omits
the count). A `thinking` SSE event exposes progress so clients can show a
thinking indicator instead of an unexplained pause. The meter's component sum
still equals provider-reported totals (Q1).

### 5. A thinking budget, with truncation labeled

The pipeline sets a reasoning-token cap. Hitting it emits a labeled
`thinking-truncated` outcome; a turn that ends `length` with neither a tool
call nor an answer is a labeled error, never an empty `done` (the trial's
failure mode). The cap is generous enough not to truncate before a tool call.

### 6. Per-turn context-window gate

The assembler enforces, before any provider call:

```
system + tools + pinned/explicit + user + history + RAG + mentions + output reserve
    ≤ model.capabilities.contextLength
```

- The output reserve is `max_tokens` plus a safety margin (§10).
- Assembly order is priority order: system + tools + user input are fixed and
  never dropped; pins and explicit attachments (ADR-0049 §11) come next; then
  recent history, RAG chunks, and older history.
- Every drop is recorded in the existing `ContextDrop` shape with a
  `context-window` reason (labeled, counted).
- If the fixed + pinned + user components alone exceed the window, the turn is
  refused with a typed `context-window-exceeded` **before** the provider call —
  no provider error, no silent truncation.

### 7. Session cumulative budget becomes live

`Session.tokenBudget` (ADR-0026 §5) gains real semantics:

- **Soft threshold** (a ratio, §10): the snapshot/meter carries a labeled
  `session-budget-soft` warning; the turn proceeds.
- **Hard threshold**: the turn is refused with a typed
  `session-budget-exceeded`, unless compaction is enabled and succeeds first.

### 8. Compaction extends a session instead of dropping it

When history exceeds the compaction trigger (§10), the engine replaces the
oldest turns with **one metered, labeled summary**:

- The summary call is its own meter row (thinking-off, bounded), like the
  router's second row precedent (ADR-0028 §5).
- The snapshot records `compacted` plus the summarized turn range; pins and the
  most recent turns survive.
- Compaction is a labeled, reversible-by-history record — never a silent drop.
  Retention follows the snapshot-retention note (ADR-0044).

### 9. Prefix caching discipline is preserved and its cost labeled

Static context stays deterministically front-loaded (ADR-0049 §14). Compaction
and tray edits change the prefix and trade away cache reuse; that cost is
accepted and labeled in the snapshot, not hidden.

### 10. Data over code

`config/pipeline.json` (validated, fail-fast `pipeline-invalid`) gains:

- `thinking` — the default level (`off|auto|on`);
- `reserveOutputTokens` — the window-gate output reserve;
- `sessionBudgetSoftRatio` — the soft threshold ratio;
- `compaction` — `{ enabled, triggerHistoryTokens, keepRecentTurns }`.

Session/per-turn overrides live in the ADR-0049 policy shape. No per-preset
fields (ADR-0045).

### 11. Measurements are first-class

Every turn records, alongside the meter/snapshot: prompt tokens, thinking
tokens, completion tokens, wall-clock latency, model + quant, and window
utilization. This is the data the hardware map is built from; it is recorded
per turn, never approximated silently.

### 12. Contract surface (OpenAPI-first, additive)

Recorded as intent; exact names and shapes land in `api/openapi.yaml` at
implementation, OpenAPI-first (ADR-0017), with no breaking changes:

| Surface | Intent |
|---|---|
| Session context policy + `Task.context` (ADR-0049 §8) | gains the thinking level and per-turn override |
| `thinking` SSE event | reasoning progress/count, additive to the event enum |
| `context-window-exceeded` | typed error/409-style refusal before the provider call |
| `session-budget-exceeded` | typed refusal (hard threshold) |
| Snapshot / meter fields | exact thinking tokens, window utilization, `compacted` range, escalation/truncation labels |
| `config/pipeline.json` schema | the §10 fields, validated at startup |

[`behaviors/context-budgets.feature`](../behaviors/context-budgets.feature)
is normative; tests follow it.

## Consequences

- **+** Mechanical turns stop paying for thinking they do not use; the trial's
  ~40 s edit becomes seconds, with no hardware change.
- **+** Reasoning becomes a measured, opt-in retry tool (`auto`), so "does
  thinking help this task?" is answerable from the snapshot.
- **+** A session's total context is bounded and labeled: soft warnings, hard
  refusals, and metered compaction extend effective session length instead of
  silently dropping history.
- **+** The dominant latency and the context limits are now recorded per
  model/quant — the evidence base for hardware decisions.
- **+** All additions are additive; existing clients ignore the new fields.
- **−** More contract surface (thinking event, typed budget errors, snapshot
  fields) with the three-codegen lockstep (ADR-0017).
- **−** Escalation doubles the model call on failures; bounded by one retry.
- **−** Compaction spends a metered model call and changes the prefix (cache
  reuse cost).
- **−** The window gate's byte/4 estimate is conservative; provider-reported
  prompt tokens remain the authoritative post-hoc check.

## Alternatives considered

- **Always-on thinking (status quo)** — rejected: the trial shows it dominates
  latency for mechanical edits and is invisible to the meter.
- **Always-off thinking** — rejected: drafting and failure recovery benefit
  from it; `auto` captures the return instead of banning it.
- **Per-preset thinking fields** — rejected by ADR-0045 (no behavioral preset
  config); session policy + per-turn overrides is the accepted shape.
- **Client-side thinking control** — rejected: dumb clients; the engine owns
  the payload (ADR-0013 §3).
- **Client-side budget/compaction** — rejected: splits the assembler's choke
  point and the meter invariant (ADR-0011).
- **Hard-drop oldest history without compaction** — rejected: silent loss;
  compaction is metered and labeled.
- **A separate summarizer model** — deferred: one pipeline first; the summary
  call is a metered row under the same provider seam.
- **Refuse long sessions instead of compacting** — rejected: compaction is the
  limit-extender; refusal remains the hard-budget fallback.
- **Ignore reasoning deltas (status quo)** — rejected: it hides the dominant
  latency and the exact thinking count.

## Deferred / recorded notes

- **Exact runner mappings** (per server version) are pinned at implementation;
  the mapping table is intent.
- **Compaction algorithm** (extractive vs abstractive, prompt shape) is an
  implementation detail; the metered-row + labeled-snapshot contract is fixed.
- **Automatic vs user-triggered compaction** is pinned at implementation;
  `pipeline.json` carries the trigger either way.
- **Window-gate estimation accuracy**: byte/4 is the documented unit
  (ADR-0011); a tokenizer-accurate estimate is a future refinement, validated
  by provider-reported counts.
- **`thinking` event granularity** (deltas vs running count) is pinned at
  implementation; clients may render an indicator only.
- **Laya interplay** (Phase F): the decision layer may eventually choose the
  thinking level per turn; until then the static policy + escalation governs.
- **Retention/pruning of compacted ranges** follows the ADR-0044 snapshot
  retention note.
