# ADR-0015: Fleet sizing policy — MoE over dense, 14B+ floor for citation work

Status: Accepted

## Context

`local-llms-for-writing.md` models the hardware reality (Mac mini M4, 32 GB
unified, 120 GB/s) and the failure modes of small models in academic work.
These conclusions must be *policy*, not prose, so the fleet manifest (ADR-0006)
and mode defaults (ADR-0009) encode them consistently.

## Decision

Adopt the following normative policy, encoded as manifest `capabilities` and
mode `defaultModel` choices:

1. **Bandwidth-bound sizing.** Treat tok/s ≈ 120 GB/s ÷ file-size-per-token; the
   27B dense model (~5–6 tok/s) is overkill; **MoE is preferred** (26B-A4B /
   35B-A3B read far fewer bytes/token than their size suggests).
2. **Citation floor: 14B+.** 7–8B models fabricate citations at >20%; they are
   **grammar passes only**, never drafters of citation-heavy text. 14B+ is the
   practical floor for drafting/editing with sources.
3. **Role assignments** (default fleet):
   - Editing (primary): Gemma 4 12B (speed) or Gemma 4 26B-A4B (quality).
   - Writing from source/prose: Mistral Small 3.1 24B or Qwen 35B-A3B MoE.
   - Short-pass polish: Phi-4 14B (16K context limit → paragraph/section only).
   - Grammar pass: Llama 3.1 8B.
   - One-model compromise: Gemma 4 26B-A4B.
4. **Temperature cheat sheet** as mode defaults: editing/proofreading 0.3–0.4;
   drafting 0.4–0.6; brainstorming/outlining 0.6–0.7.
5. **Always pass sources in context** (never ask a local model to "find papers");
   system-prompt line: *"Use only sources provided. If unsure, write '[citation
   needed]'. APA 7 or Chicago author-date, as specified."* Separate drafting from
   reviewing; don't trust generated statistics/quotes — re-extract by hand.

## Consequences

- **+** The manifest and modes are pre-seeded with defensible, hardware-grounded
  choices instead of arbitrary defaults.
- **+** The citation-fabrication rule is enforced by *which model a mode may
  select* (a `modeTags`/capability gate), not by hoping the prompt is enough.
- **−** Fleet composition is hardware-specific; moving to other hardware (future
  GPU box) re-runs this ADR. The archive (ADR-0008) exists for that future.

## Alternatives considered

- **One big dense 27B for everything** — rejected: ~5–6 tok/s is not interactive
  for editing; memory-tight.
- **7–8B as the daily driver** — rejected: citation fabrication rate makes it
  unsafe for the primary academic use case.
- **No policy (pick ad hoc per call)** — rejected: the manifest/mode data model
  needs consistent, defensible defaults to be trustworthy.
