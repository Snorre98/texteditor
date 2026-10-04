# ADR-0054: Laya decision layer — optional, experimental dependency

Status: Accepted

Amends: ADR-0053 (adds the experimental status and the decoupling guarantee it
does not record).

Extends: ADR-0044 §5 / §Alternatives (the decision layer's "start gate-only,
widen with evidence" is reframed as one sanctioned, measured experiment).

Relates: ADR-0003 (pure-Go engine, no embedded weights), ADR-0011 (the metered
choke point), ADR-0015 (one large model resident), ADR-0045 (one global policy),
ADR-0049 §11 (human overrides), ADR-0051 (thinking policy), ADR-0052 (dumb
clients).

## Context

ADR-0053 landed the Laya decision layer: a **planner** call (retrieve-or-not,
thinking, breadth) before retrieval and a **gate** call (per-chunk keep/drop)
after exclusion, under one global `config/pipeline.json` block, default off,
fail-open. It records the mechanism, the contract, and the evaluation method. It
does not record the two things that decide whether the layer is safe to keep.

**The gate is a decision model used outside its training envelope.** Laya is a
non-autoregressive encoder — `laya` (ModernBERT-large, 512 tokens),
`laya-multilingual` (mmBERT-base, 1024, up to 8192), `laya-typed-decisions`
(ModernBERT-large, 1024) — trained and benchmarked on customer-support, email,
moderation, and routing decisions. The gate asks it for *relevance* judgments
over long-form academic prose against a turn's request: out-of-distribution,
long-context, and beyond what a ~400M-parameter encoder without generation can
reason about. Laya's own typed-decisions benchmark puts the base English
checkpoint at **0.362** zero-shot against **0.766** only after in-domain
fine-tuning — and that fine-tune is a deferred note (ADR-0044). The shipped
configuration is therefore the weak one.

**The engine's independence from Laya is implied, not normative.** Every failure
degrades fail-open (ADR-0053 §8) and the policy defaults to off, but no decision
states that the engine must remain *fully* functional with the layer off or the
service absent, nor what decommissioning entails.

Forces: single user, local-first (ADR-0003); one large model resident at a time
(ADR-0015), so a second resident service is a real cost, not free; Q1 (ADR-0011)
requires every token and every failure to be visible; ADR-0049 §11 makes human
overrides authoritative; the product's hard problem is context quality
(ADR-0044).

## Decision

1. **The layer is an intentional experiment, not a quality guarantee.** The
   project keeps the gate to explore decision-model-driven retrieval gating
   ("more intelligent RAG"), knowingly at the edge of Laya's design envelope.
   The accepted limits are stated, not hidden:
   - out-of-distribution versus Laya's training domain (support/email/triage,
     not academic prose);
   - the encoder window (512 / 1024) versus the length of a candidate passage;
   - non-generative, so no query rewrite and no explanation beyond a calibrated
     probability;
   - independent per-chunk scoring, so no joint selection over the candidate set
     (redundancy, coverage, or budget coupling);
   - weak zero-shot accuracy until the ADR-0044 fine-tune path is taken.

2. **Decoupling guarantee (normative).** The engine must be fully functional
   with the decision layer disabled or the Laya service absent:
   - no startup dependency — the engine never requires a Laya service or Fleet
     entry to boot; resolution is per-call only;
   - every transport, timeout, protocol, or low-confidence failure degrades
     fail-open and labeled (`decision-degraded`, ADR-0053 §8); a degraded turn is
     never a failed turn;
   - `decision` disabled is behaviorally a no-op relative to the pre-Phase-F
     pipeline: the same retrieval depth, the same payload, no `DecisionRecord`,
     no `decision` meter rows, no gate drops;
   - when the layer is enabled and the service is missing, the turn still
     completes and the snapshot records the degradation.

3. **Removal is a contract amendment, never hidden coupling.** The layer's
   surfaces are additive and optional — `GET /decision`, the `Decision*`
   schemas, `ContextSnapshot.decision` (`omitempty`), `ContextPolicy.decision`,
   and the `decision` meter component. Decommissioning Laya is an explicit
   decision recorded against this ADR, plus a contract change and code deletion;
   nothing outside `internal/laya`, the loop's decision seams, and those typed
   surfaces may depend on it.

4. **Success is measured, not asserted.** The gate's claim — reducing injected
   tokens without recall loss — is settled by the experiment defined in
   [`research/decision-eval.md`](../research/decision-eval.md) (recall@k,
   injected tokens, gate latency, keep rate) on a thesis-scale corpus, with
   `gateThreshold` and `breadthTopK` tuned from the sweep and the shipped
   defaults set from the measurement. If the gate fails that bar, it stays off
   or the fine-tune path is pursued; it is not tuned into apparent success.

5. **No quality claims before the measurement lands.** ADRs, plans, and status
   describe the layer as an experiment until §4's sweep is recorded;
   documentation must not present gating as an established improvement.

## Consequences

- **+** The optional dependency is safe: the engine, its tests, and every
  non-decision turn run unchanged without Laya.
- **+** The experiment is honest: its limits are recorded, its exit criteria are
  measurable, and a negative result is a legitimate outcome.
- **+** Decommissioning is cheap and lawful — a contract amendment, not
  archaeology through hidden couplings.
- **−** The feature may not earn its operating cost (a second Python service, a
  second resident checkpoint, per-turn round-trips).
- **−** Docs and status carry a permanent "unproven" caveat until the sweep
  lands, and a negative sweep is churn.
- **−** Keeping the gate off by default means the experiment is exercised only
  when explicitly enabled, so the sweep — not production use — is what validates
  it.

## Alternatives considered

- **Treat the gate as a proven quality lever** — rejected: unmeasured and
  out-of-distribution; the evidence available (Laya's own benchmark) points the
  wrong way.
- **Hard-require Laya like the provider** — rejected: violates the decoupling
  guarantee and local-first resilience; a missing service would fail turns the
  engine can answer without it.
- **Remove the layer until the fine-tune exists** — rejected: the experiment
  *is* the point, and fail-open makes it safe to ship off.
- **Make the gate on by default** — rejected: it would impose the unproven
  behavior and the service dependency on every corpus turn.

## Deferred / recorded notes

- **Fine-tuning Laya** on the author's own gate decisions if zero-shot proves
  weak (ADR-0044 deferred note); this is the documented accuracy path, not part
  of the shipped experiment.
- **Turn routing** (mode/model selection) and **query rewriting** via a
  generative component — out of scope (ADR-0053).
- **Proactive (pre-retrieval) gating of pins/mentions** — never; those are human
  overrides (ADR-0049 §11).
