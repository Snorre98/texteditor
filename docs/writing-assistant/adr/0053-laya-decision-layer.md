# ADR-0053: Laya decision layer — typed retrieval gating, retrieve-or-not, thinking level

Status: Accepted

Extends: ADR-0044 §5 (the decision layer), ADR-0028 (optional second metered
call), ADR-0049 §8/§11 (context-policy seam; pins bypass the gate), ADR-0051
(thinking policy). Amends: ADR-0044 §5 (scope narrowed; Laya is a typed-question
engine, not a chat model; no per-mode config), ADR-0028 §7 (Laya is a native
HTTP API — no OpenAI facade is built).

Relates: ADR-0003 (single static Go, no CGO), ADR-0011 (metered choke point),
ADR-0016 §8 (resolve-by-name), ADR-0017 (OpenAPI-first), ADR-0024 (metering),
ADR-0045 (one global policy), ADR-0052 (dumb clients).

## Context

ADR-0044 §5 named a typed-decision model (Laya) for "turn routing
(mode/model/retrieve) and per-chunk retrieval gating" as a second metered call,
off by default; its §Alternatives said to start gate-only and widen with
evidence. ADR-0045 then replaced per-mode decision config with one global
policy and parked the tool router.

The actual Laya artifact (github.com/NandhaKishorM/laya, `pip install laya`) is
a **non-autoregressive System-1 decision engine**: it answers typed questions
(`choice`, `score`, `noul`) over a "state" in a single forward pass, exposes its
own HTTP server (`laya-serve`, `POST /v1/systemone` and `/v1/systemone/batch`,
Jev-compatible JSON), and a `Router` that selects a checkpoint by script/
language (`english`, `multilingual`, `typed-decisions`). It does not generate
text; `answer_confidence` is calibrated and `min_confidence` is an abstention
gate. This diverges from the ADR-0044 assumption of a chat-shaped sidecar.

Forces: one global policy (ADR-0045); dumb clients render engine data
(ADR-0013 §3, ADR-0052); pins are a human override that bypass the gate but
never budgets (ADR-0049 §11); thinking is a policy with a post-failure auto
escalation (ADR-0051); every model call is metered (ADR-0011/Q1); local-first,
single user (ADR-0003/0015).

## Decision

1. **Scope.** Laya decides three things in this phase: (a) **retrieve-or-not**,
   (b) **per-chunk gating** of the auto-retrieved set, and (c) the **thinking
   level** for the turn. Turn routing (mode/model) is deferred. Mentions,
   history, and the `/locate` anchor are never gated.

2. **Two calls per decided turn.** A **planner** call runs *before* retrieval
   (it decides retrieve-or-not and breadth, which cannot be decided after
   retrieval); a **gate** call runs *after* retrieval and exclusion. Both are
   native Laya `POST /v1/systemone[/batch]` calls.

3. **Inputs.** Planner state = `{request, history (bounded), selection, mode}`.
   Gate = one batch state per candidate chunk, `{request, passage}` with
   provenance. Laya never sees mention bodies, the system prompt, or the full
   history.

4. **Outputs.** Planner: `retrieve` = `noul`, `thinking` = `choice[off|auto|on]`,
   `breadth` = `choice[none|few|many]` (mapped by config to a retrieval topK).
   Gate: one `noul` per chunk ("is this passage directly relevant to the
   request"); the **engine applies `gateThreshold`** to P(relevant). A
   low-confidence/abstained answer is kept (fail-open) and labeled. Laya cannot
   rewrite the query (non-generative) — rewriting is out of scope.

5. **Placement.** `retrieve(planned topK) → exclude → gate(auto set only) →
   resolve thinking → assemble → provider`. Pins/mentions/anchor bypass the
   gate (ADR-0049 §11). The `rag` SSE event stays the post-exclude **pre-gate**
   candidate set; the snapshot records the decisions and the surviving chunks.
   The assembler remains the single metered choke point for the payload.

6. **Policy.** One global `decision` block in `config/pipeline.json`
   (`enabled` default **false**, `model`, `gateThreshold`, `maxCandidates`,
   `maxHistoryTurns`, `breadthTopK`, `timeoutMs`), schema-validated fail-fast.
   `ContextPolicy` gains `decision: off|on` for session/per-turn overrides; the
   TUI tray gets a toggle. No per-preset decision fields (ADR-0045).

7. **Precedence.** Human/policy outranks Laya: `decision=off` runs no Laya call;
   session `autoRag=false` disables retrieval (Laya cannot re-enable it; the
   planner runs only to choose thinking, its retrieve answer ignored); a
   non-nil `ContextPolicy.thinking` overrides Laya; pins bypass the gate. Laya
   chooses only inside the space policy leaves open.

8. **Failure.** Any Laya transport/timeout/protocol failure degrades fail-open:
   retrieval uses policy defaults, the gate keeps all chunks, the turn proceeds,
   and the snapshot labels `decision-degraded` with a reason
   (`unreachable|timeout|protocol|low-confidence`). A degraded turn is never a
   failed turn.

9. **Metering.** A new `Meter.AttributeDecision` (mirroring
   `AttributeCompaction`) writes one row per Laya call with component `decision`
   and `model` = the routed checkpoint; the planner-vs-gate role and checkpoint
   ride in the snapshot record. No `meter_events` schema change.

10. **Deployment.** One `laya` delegate daemon: `serve-laya.sh` wraps
    `laya-serve` (no facade binary) in `macos-dev-config`; the engine resolves
    it by name via Fleet and calls its native API with a small HTTP client
    (`internal/laya`). The engine embeds no weights and stays pure Go
    (ADR-0003).

11. **Contract (OpenAPI-first, additive).** Typed `DecisionPolicy`,
    `DecisionRecord`, `DecisionPlanner`, `DecisionGate`, `DecisionChunk`;
    `ContextSnapshot.decision` becomes typed; `ContextPolicy.decision` is added;
    `SessionMeter` gains the `decision` component; a read-only `GET /decision`
    returns the global policy. No new SSE event (the snapshot suffices).

12. **Evaluation.** Phase F closes with a recorded experiment: a golden query
    set with relevance judgments over a thesis-scale corpus, measuring recall@k,
    injected tokens, and latency, tuning `gateThreshold` and `breadthTopK` and
    comparing checkpoints/exports. Defaults ship from the measurement. The
    claim — gating reduces injected tokens without recall loss — is measured,
    not a CI gate.

## Consequences

- **+** Smarter RAG: the gate selects subheader-level chunks by typed relevance,
  and `retrieve-or-not` skips retrieval on non-corpus turns.
- **+** Thinking becomes a per-turn model decision within the ADR-0051 policy,
  while human overrides and post-failure escalation stay authoritative.
- **+** Fail-open + labeled, metered, snapshot-recorded: Q1/Q6 hold; the engine
  never depends on Laya for a turn to complete.
- **−** A second Python service and per-turn latency; corpus turns now pay up
  to two Laya round-trips (planner + gate).
- **−** A new derived dependency (checkpoints/`laya-serve`); a language-routed
  checkpoint makes the deciding model vary by input, which the snapshot must
  surface.
- **−** Scope beyond ADR-0044's "gate-only" start (retrieve-or-not + thinking),
  accepted with the typed API as justification.

## Alternatives considered

- **OpenAI chat facade for Laya (ADR-0028 §7 literal)** — rejected: it discards
  the typed-question API and reintroduces stringly-typed parsing.
- **Two pinned planner/gate checkpoints** — rejected: Laya routes checkpoints by
  language, not by task; planner/gate are two question sets on one service.
- **Single post-retrieval call** — rejected: retrieve-or-not cannot be decided
  after retrieval without paying retrieval anyway.
- **In-process Go heuristic or CGO-embedded model** — rejected: ADR-0003, and it
  is not a typed-decision model.
- **Full turn routing (mode/model) up front** — rejected: ADR-0044 §Alternatives;
  widen with evidence.
- **Laya query rewriting** — impossible: Laya is non-generative.

## Deferred / recorded notes

- Turn routing (mode/model selection) and mode-name-driven behavior.
- Fine-tuning Laya on the author's own gate decisions if zero-shot proves weak
  (ADR-0044 deferred note).
- Query rewriting via a separate generative component.
- Proactive (pre-retrieval) gating of pins/mentions — never; human override.
