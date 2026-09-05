# ADR-0024: Thinking-token attribution — bundled tokenizer as a fallback

Status: Accepted

Supersedes: ADR-0011 ("never reimplement a tokenizer").

## Context

ADR-0011 attributed tokens from provider-reported counts and explicitly rejected
"reimplementing a tokenizer," because `prompt_eval_count`/`eval_count` are exact
and free. But that rejection was about *total* attribution with a tight constraint
that does not hold for **thinking tokens**: a provider that *omits* the
reasoning/thinking count leaves the meter with no exact source for the thinking
component, while `token-metering.feature` still requires thinking tokens to be
"attributed to the thinking component, not completion."

The user's requirement is narrower and better-framed than what ADR-0011 rejected:
they want **exact thinking tokens* *when the provider reports them, and a way to
*approximate* them **only when the provider omits them** — so the tool stays
independent of any given runner's reporting quirks.

## Decision

Thinking-token attribution is two-tier:

1. **Provider reports thinking/reasoning tokens** → attribute them exactly to the
   `thinking` component; completion is the remainder. This preserves ADR-0011's
   "provider-reported counts are exact and free."
2. **Provider omits the thinking count** → the engine tokenizes the completion
   stream's reasoning prefix with a **bundled, per-model-family tokenizer**
   (pure-Go, no CGO per ADR-0003) to *count* thinking tokens. This is invoked only
   as a fallback when the provider report lacks the count.

Ownership: the **Token metering** module owns the reconciliation — it receives both
the provider counts and (when present) the engine's tokenized count, and reconciles
them with a **documented drift note** when they disagree. The tokenizer is a
Meter-internal dependency, not a public seam.

Invariant relaxation: Q1's "scaled sum equals the provider total exactly" (ADR-0022)
remains for all components **except** thinking — the thinking component is a
**labeled approximation** when the provider omits the count. It is always annotated,
never silently folded into completion.

## Consequences

- **+** The meter is exact on thinking when the runner reports it, and still
  *visible* (as a labeled estimate) when it doesn't — the independence/control the
  user wants.
- **+** The fallback is scoped: a tokenizer is carried, but only engaged for the
  reasoning prefix and only on the omitting providers.
- **−** A per-model-family tokenizer dependency enters the static binary (size +
  maintenance), and its count may not reconcile with `eval_count` when the provider
  uses a different tokenizer — hence the labeled-approximation and drift note.
- **−** ADR-0011's clean "never reimplement" is now a circumscribed exception; it
  must be documented in `contracts/failure-semantics.md` so the approximation is
  visible, never implicit.

## Alternatives considered

- **Always reimplement a tokenizer for thinking** — rejected (the broad version
  ADR-0011 originally rejected): buys precision where the provider already reports
  it.
- **No tokenizer; labeled estimate only** — rejected: loses the exact thinking
  count where the runner omits it, which is precisely the independence the user
  wants.
- **Fold omitted thinking into completion** — rejected: contradicts
  `token-metering.feature` and the user's stated reason.
