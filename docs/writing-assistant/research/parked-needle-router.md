# Parked research: Needle-2 as a tool-calling router

Status: **Parked — no architectural decision made.** This is a research card, not
an ADR. It is deliberately *not* entered in `architecture.md` §9 or
`traceability.md`. Revisit only when a promotion trigger (below) fires.

## The idea

Introduce a fast, dedicated tool-calling specialist — [Cactus Needle
2](https://cactuscompute.com/needle) (45M params, 14 MB CQ2-bit, its own C++
engine, ~500 tok/s decode) — as a **ToolRouter**: a single "which tool and what
arguments" endpoint the writing agent consults, offloading native tool-calling
from the writing/editing LLM. Needle turns a natural-language request into a
typed tool call (name + arguments), with a byte-level grammar that guarantees
schema-valid JSON, plus a learned confidence score and an empty-call/refusal when
off-topic — so it can also act as a cheap "is this a prose request, or does it
need a tool?" pre-flight.

## Integration seams (the already-accepted fit)

This maps cleanly onto two seams that already exist in the accepted architecture:

1. **`ToolExecutor.Invoke(name, args)`** (ADR-0016 §5). The Tool-registry/Tool-
   executor split means "decide tool + args" is already a distinct step from
   "execute". Needle would sit *in front of* `Invoke` — same downstream, different
   decider. The loop's dispatch/observe state machine is unchanged.

2. **The `delegate` runner pattern** (ADR-0018 §3, the `qwen`→`serve-qwen.sh`
   precedent). Needle is *not* OpenAI-compatible (it ships its own dependency-free
   C++ engine rather than a `/v1` REST server). Serving it therefore requires
   either a thin OpenAI-facade wrapper or a delegated daemon entry — exactly the
   pattern the fleet manifest already has for `qwen`.

## Open design questions (unresolved, by design)

- **Metering.** Tool-routing would burn tokens in a second model. The breakdown
  needs a new `router` component beside `thinking` (assembler `Breakdown` +
  `meter_events.component`), with the same scale-to-total attribution.
- **Fine-tuning workflow.** Needle's reported wins come *after* per-tool
  fine-tuning (`needle finetune`). That implies a fine-tune-and-re-export step each
  time the tool set changes — a new operational loop for a "tools are data" system.
- **Single choke point vs dual callers.** Tool selection leaving the writing LLM
  means the assembler/meter must attribute across two models and the loop must
  route to two Providers.

## Honest assessment — why premature now

The current system has ~7 writing tools, one user, and a 27B-MoE writing model
that already performs native tool-calling, accurately, for a tool list this small.
Three reasons this is not worth pursuing yet:

1. **Accuracy.** Needle 2's *base* (un-fine-tuned) numbers are modest for genuine
   function calling — ~40–62% Simple / 17% DroidCall on BFCL. It only pulls ahead
   of a generalist *after* fine-tuning on your exact vocabulary. At 7 tools, the
   routing problem is trivial enough that the writing LLM is already accurate.
2. **Latency is not the bottleneck.** Tool-call decision is milliseconds at this
   scale; the writing model's 10–25 tok/s stream dominates turn time. Needle's
   500 tok/s edge matters at fleet/embedded scale, not single-machine academic use.
3. **Complexity.** It adds a second served model, a fine-tune loop, a Provider
   adapter (non-OpenAI engine), and a new metering component — for an optimization
   the current design doesn't need.

The genuinely interesting property is **not** routing speed but the
**empty-call/refusal + confidence** behavior as a pre-flight "does this need a tool
at all?" filter — worth watching independently of adopting Needle as a full router.

## Promotion triggers

Reopen as an ADR only if one or more of these fires:

1. The tool set grows past ~15 tools, making selection a real accuracy/recall
   concern for the writing LLM.
2. A measured accuracy or spurious-tool-call/refusal problem appears with native
   tool-calling on the writing model.
3. An always-on or edge/second-device target is added (e.g. a wake-word or
   embedded device) where 14 MB-at-500-tok/s and bounded 28 MB RAM genuinely matter.
