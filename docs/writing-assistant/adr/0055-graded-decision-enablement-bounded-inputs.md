# ADR-0055: Graded decision enablement and bounded Laya inputs

Status: Accepted

Amends: ADR-0053 §4 (outputs) and §6 (policy) — the single `enabled` boolean
becomes a three-value `mode`, and the planner/gate inputs gain engine-side
bounds.

Extends: ADR-0045 §7 (one global policy; still no per-preset fields), ADR-0051
(the deterministic token estimator, reused for decision-input truncation).

Relates: ADR-0054 (the layer is an optional experiment; this ADR is how the
experiment is isolated and kept from silently truncating), ADR-0011 (Q1: no
silent truncation), ADR-0049 §11 (human overrides).

## Context

ADR-0053 enables the layer with one boolean. With `enabled`, the planner and the
gate run or do not run together. The planner cannot be evaluated without the
gate, and the gate — the novel behavior ADR-0054 sanctions as an experiment —
cannot be enabled without also changing turn routing and thinking. The policy
must be able to isolate the gate.

The call inputs are also unbounded **in tokens**. The planner state is built from
the request, a bounded history window, the selected passage, and the preset; the
gate sends one state per candidate chunk. A selected block or a candidate
passage can exceed Laya's encoder window (512 for `laya`, 1024 for the others),
and Laya truncates silently to fit. Silent truncation violates Q1 (ADR-0011) and
means a relevance judgment can be computed on a passage tail the model never saw.

Forces: one global policy, never per-preset (ADR-0045); deterministic,
engine-owned budgeting already exists (ADR-0051); the policy schema is fail-fast
with `additionalProperties: false`, so renames surface loudly; human overrides
already outrank the model (ADR-0053 §7).

## Decision

1. **Graded global policy.** Replace `decision.enabled` with
   `decision.mode: off | planner | planner+gate`:
   - `off` (default) — no Laya calls; retrieval runs at `autoRagTopK`; no gate;
     no record; no meter rows (ADR-0054 §2).
   - `planner` — the planner call only: retrieve-or-not, breadth→topK, and
     thinking. The gate is skipped and every post-exclude candidate chunk enters
     the prompt corpus (still subject to the ordinary budgets).
   - `planner+gate` — the planner and the gate; the gate filters candidates at
     `gateThreshold` (ADR-0053 behavior).

2. **Graded override.** `ContextPolicy.decision` carries the same three values
   (absent = inherit). Precedence is per-turn > session > global; `off` at any
   layer guarantees zero Laya calls; an explicit `autoRag=false` still disables
   retrieval and Laya cannot re-enable it (ADR-0053 §7). The TUI tray toggle
   cycles the three values.

3. **Bounded inputs.** Add `maxChunkTokens` and `maxPlannerTokens` to the
   policy. Before any Laya call the engine truncates deterministically using the
   ADR-0051 estimator: each gate passage to `maxChunkTokens`, the planner state
   (request + history + selection) to `maxPlannerTokens`. Truncation is labeled
   — `truncated` on the affected `DecisionChunk` / `DecisionPlanner` in the
   snapshot — so a relevance score is never attributed to a passage the engine
   silently cut. The engine does not rely on Laya's own window truncation.
   Defaults are conservative for the 512-token English checkpoint (proposed: 384
   per chunk, 1024 for the planner), leaving room for the question/instruction
   prefix.

4. **Contract (additive plus one breaking rename).** `DecisionPolicy.mode`
   replaces `enabled`; `maxChunkTokens` / `maxPlannerTokens` are added;
   `ContextPolicy.decision` becomes the three-value enum; `DecisionPlanner` and
   `DecisionChunk` gain `truncated`. A config that still carries `enabled` fails
   startup (`schema-invalid`) — the schema is `additionalProperties: false`, so
   the rename surfaces loudly rather than being ignored (ADR-0045 precedent).
   Codegen (`go generate ./...` and the TUI Rust regen) runs in lockstep.

5. **Metering is unchanged.** `planner` emits only the planner row;
   `planner+gate` emits both (ADR-0053 §9).

## Consequences

- **+** The gate experiment is isolated from turn routing, and the planner is
  measurable on its own.
- **+** No silent truncation: every clamped input is visible in the snapshot
  (Q1 holds).
- **+** A session can run planner-only, and `off` remains a strict no-op.
- **−** A breaking config/contract rename (`enabled` → `mode`) and two more
  tunables.
- **−** Head-window truncation can still drop a passage tail that carried the
  relevance signal; the label makes it visible, not harmless. Tail-preserving or
  semantic chunking is left to retrieval.
- **−** `planner` mode keeps every candidate, so a turn in that mode pays the
  planner round-trip without the token savings the gate provides.

## Alternatives considered

- **Keep `enabled` and add a separate `gate` boolean** — rejected: two booleans
  encode three reachable states plus one nonsense state (`gate` on while the
  layer is off); a single enum is the honest shape.
- **Separate planner and gate services** — rejected: Laya routes checkpoints by
  language, not by role; planner and gate are two question sets on one service
  (ADR-0053).
- **Bound inputs only via Laya's `max_len`** — rejected: that is server-side and
  silent; the engine must own and label the bound (Q1).
- **No bound; assume the chunker keeps passages small** — rejected: the selected
  passage and the history are not chunker-sized, and the failure is silent.

## Deferred / recorded notes

- **Per-role checkpoints** (a dedicated planner vs gate checkpoint) if one
  question set measurably dominates the other.
- **Joint (set-level) chunk selection** — cover the candidate set as a whole
  rather than scoring each chunk independently (ADR-0054 known limit).
- **Tail-preserving or overlap truncation** if head-window clamping is shown to
  cost recall.
