# Needle-2 as a tool-calling router (research → ADR-0028)

Status: **Promoted.** The `ToolDecider` seam is adopted as an optional,
off-by-default module — ADR-0028. The Needle-specific *enablement* (fine-tune,
serve, flip a mode to `router`) remains deferred, gated by the triggers below.
This card stays in `research/` (not in `architecture.md` §9 or `traceability.md`);
ADR-0028 is the normative record.

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

## How it landed in the architecture (ADR-0028)

Two seams, with one correction from the original card:

1. **A new `ToolDecider` module — not `ToolExecutor.Invoke`.** The original card
   said Needle sits "in front of `Invoke`" (ADR-0016 §5). ADR-0028 corrected this:
   `Invoke(name, args)` is the *execution* slot — the decision has already happened
   by the time it's called. The decider had no seam, so ADR-0028 adds a narrow
   optional `ToolDecider` in the loop's dispatch step, wired only when a mode sets
   `toolCalling: "router"`. The Tool executor, Provider, and assembler are unchanged.

2. **The `delegate` runner pattern** (ADR-0018 §3, the `qwen`→`serve-qwen.sh`
   precedent), extended. Needle is *not* OpenAI-compatible (own dependency-free C++
   engine, `.cact` file). ADR-0028 serves it as a `delegate` daemon behind a thin
   OpenAI facade (`serve-needle.sh`), resolved **by name** like `nomic-embed`
   (ADR-0016 §8) — not as a mode's `defaultModel`. Refusal/confidence are encoded in
   the facade's OpenAI response so the Provider stays untouched.

## Open design questions — resolved by ADR-0028

- **Metering.** Resolved: a second `Meter.Attribute` row tagged
  `model=needle-router`; the existing `model` column + `turn_id` grouping
  disambiguate. No `meter_events` schema change. (`Breakdown` is reused;
  `Thinking=0` always.)
- **Fine-tuning workflow.** Resolved with a fail-fast sync gate: `needle finetune`
  records a tool-set fingerprint in the manifest; startup compares it to the engine's
  tool-set hash and fails with `router-tools-stale` on drift. This is the one piece
  that strains "tools are data" (ADR-0019) — a derived artifact that can drift — and
  the gate is what contains it.
- **Dual callers.** Resolved: `ToolDecider` is a self-contained service (Retriever
  style), depending on Fleet + Provider internally; the loop only consumes its
  `RouterResult`.

## Why enablement is still deferred

The current system has ~7 writing tools, one user, and writing models that already
perform native tool-calling accurately for a tool list this small. Three reasons to
keep the router *off by default* (not to remove the seam — that's built):

1. **Accuracy.** Needle 2's *base* (un-fine-tuned) numbers are modest for genuine
   function calling — ~40–62% Simple / 17% DroidCall on BFCL. It only pulls ahead of
   a generalist *after* fine-tuning on your exact vocabulary. So enabling it means
   standing up the fine-tune loop first.
2. **Latency is not the goal.** The value here is **correctness** (grammar kills the
   malformed call; empty-call degrades gracefully), not speed — Needle's 500 tok/s
   edge is irrelevant at single-machine academic scale.
3. **Cost is visible, not hidden.** The router is a second metered model call. It's
   a correctness trade, not a token savings; the meter shows it as such.

The genuinely interesting property is the **empty-call/refusal + confidence**
behavior — the "does this need a tool at all?" filter — which is exactly what makes
a weak writer degrade gracefully rather than derail.

## Enablement triggers

Flip a mode to `toolCalling: "router"` (and stand up the fine-tune + `delegate`
serving) only if one or more of these fires:

1. The tool set grows past ~15 tools, making selection a real accuracy/recall
   concern for the writing LLM.
2. A measured accuracy or spurious-tool-call/refusal problem appears with native
   tool-calling on the writing model.
3. A mode is assigned a writer model measurably weak at tool-calling for its tool
   set (e.g. an 8B writer doing agentic retrieval).
4. An always-on or edge/second-device target is added (e.g. a wake-word or embedded
   device) where 14 MB-at-500-tok/s and bounded 28 MB RAM genuinely matter.
