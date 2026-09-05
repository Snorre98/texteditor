# Vision: native, local LLM-powered text editing

A reflection on what the machine affords that a cloud inference provider cannot.

Status: **Vision / reflection — not an ADR.** This document is deliberately
*outside* the ADR log, `architecture.md` §9, and `traceability.md`. It records
forward-looking possibilities plus one working decision (the decoupling model,
below); the decision's authoritative record lives in `architecture.md` §8/§11.
Promote any specific idea to an ADR only when you actually intend to build it.

---

## The framing

A hosted inference provider hands the model a **snapshot** (a frozen prompt
string) over a **stateless** request. The whole lifecycle of this app — document,
retriever, formatter, git history, sessions, token meter — lives on the one
machine the app runs on. So the model is not given a document; it is given a
**live, executable, self-correcting environment**. The document is something the
model can *interrogate, mutate, and verify*, and the environment reports back the
true result of every mutation.

Everything below is a consequence of that shift. Each idea is tagged by how it
relates to the current serving contract:

- **[engine]** — lives entirely in the Go engine; works today, no contract change.
- **[in-contract]** — available through the existing OpenAI-compatible Provider
  surface (likely needs a `capabilities` flag).
- **[extension]** — requires growing the serving contract beyond OpenAI-compatible
  (an ADR-scale fork).

## Tier A — live ground-truth context  [engine]

1. **Structural map, not a text blob.** Feed the model the block *tree* (IDs,
   kinds, parent/child, position) plus canonical content per block. It navigates
   by ID, never by searching prose.
2. **Re-read after every edit.** Re-inject the true canonical state after each
   `ApplyEdit`, so the model edits against reality, never its memory. Kills the
   "model hallucinated the file contents" failure class.
3. **Verifiable citations.** Attach `{chunk, source, citationID}` to every fact;
   a deterministic `cite_check` tool answers "does this exact quote exist in chunk
   X, and is it about what you claim?" Providers cannot verify because they don't
   hold the corpus.
4. **Temporal/revision context.** Git history + revision IDs let the model ask
   "diff R12→R14" and get a word diff, not a recollection. No revision graph in the
   cloud.
5. **Live budget self-awareness.** Feed the meter back into the prompt ("14k tokens
   left this turn, 3 edits made, session 40%"). The model paces itself; providers
   hide usage.
6. **Provenance-tagged retrieval.** Chunks return `{score, source,
   why-it-matched: FTS vs vector vs rerank}` so the model weights weak evidence
   differently.

## Tier B — uncertainty & grounding  [in-contract]

7. **Confidence from logprobs.** `logprobs` is available in the OpenAI surface.
   Detect low-entropy spans — citations, numbers, names, dates — and trigger
   verification only on the uncertain parts.
8. **Citation-hallucination detection.** An invented citation almost always has
   high entropy on the invented string. Flag it, route to `cite_check`/retriever.
   The local analog of a router's confidence, applied to the writer's *output*.

## Tier C — constrained / structured decoding  [partly extension]

9. **Grammar-constrained markdown.** Force the writer's edit content to be
   structurally valid (balanced fences, valid tables) via a GBNF grammar at the
   serving layer. Extends the "engine owns the bytes" philosophy: prevent malformed
   *before*, not just normalize *after*.
10. **Vocabulary whitelisting (logit bias).** When the model must emit a block ID
    or a tool name, restrict the allowed tokens to the known set — a wrong ID or
    tool name becomes mechanically impossible.
11. **Schema-constrained tool args.** Already the shape of Needle's byte-level
    grammar and OpenAI's `response_format=json_schema`; the open question is whether
    to also constrain the *writer's* native tool calls, not just the router's.

## Tier D — serving / training level  [extension]

12. **KV-cache prefix pinning.** Pin the system prompt + document prefix so every
    turn in a multi-edit loop reuses the KV cache — make *the document* the pinned
    prefix.
13. **Per-user fine-tuning (LoRA).** Fine-tune on the user's own notes, voice, and
    citation style. No provider fine-tunes per user.
14. **Speculative decoding.** A tiny draft model (Needle-class, 45M) drafts, the
    big writer verifies. Not routing — drafting. Reuses the `delegate`+facade
    serving already specced.
15. **Retrieval-as-a-tool, model-driven.** The model drives `search_vault`/`retrieve`
    mid-thought, getting ranked hits with provenance it can weigh — instead of a
    pre-filled context the assembler chose for it.

## The contract fork

The one decision hiding behind all of this: the Provider gateway is currently
**OpenAI-compatible only** (`Chat`/`Stream`/`Embed`, ADR-0016 §2). Tiers A and most
of B work *inside* that contract today; they are engine-side and are the biggest
wins. Tiers C–D require growing the serving contract beyond OpenAI (logit bias,
grammars, KV-cache control, speculative decoding) — new `capabilities` in the
manifest plus a Provider contract extension.

Two plausible positions, left undecided here:

- **Stay OpenAI-compatible forever** — clean, portable, caps the system at A+B.
- **Grow a non-OpenAI serving surface** — unlocks C–D, at the cost of the
  portability and simplicity the OpenAI contract buys.

## The in-process question

A deeper look at the fork above, and at "no LLM in process" (ADR-0003). Two
things that are easy to conflate should be kept separate.

**Cognition vs. control.** Running the model in-process does not change its
cognition — the weights, the sampling math, and the reasoning quality are
identical whether the model is a linked library or an HTTP endpoint. What
in-process changes is the *control surface*: what the host process can observe
and manipulate around the token stream and the model's hidden state. So:

- No, in-process does not make the model smarter.
- Yes, in-process grants levers that a stateless REST boundary cannot — and some
  of those levers enable *system-level* reasoning (search, backtracking,
  self-correction).

**Two classes of lever.**

1. *State control (in-process only).* The KV cache is state, not output:
   - **Branching / backtracking** — generate an edit, see the diff is bad, rewind
     the model to before that token span, regenerate from a corrected prompt
     without re-paying the prompt tokens (try → reject → retry).
   - **Prefix pinning** — pin the document + system prompt once and reuse the
     cache across every step of a multi-edit loop.
   - **Best-of-N / self-consistency** — generate N candidates from one pinned
     prefix, keep the one that validates best.

   These are where the "lesser LLM reliability" wins actually live: the system
   need not depend on the model being good if it can *search and verify* cheaply.

2. *Token/sampler control (available without going in-process).* Logit bias,
   grammar-constrained decoding, logprobs, mid-stream temperature/stop, exact
   tokenizer access. These guarantee structure (valid markdown, valid block IDs,
   valid tool args). They do **not** require linking the library — llama.cpp/MLX
   expose grammar and `logit_bias` on their server, just not through the strict
   OpenAI facade.

**Three steps, not two.** "Bound to OpenAI REST" and "in-process" are separate
decisions:

1. External server + OpenAI facade (current) — text in/out only.
2. External server + richer protocol — the daemon exposes native features
   (grammars, logit bias, logprobs, KV-cache hints); gains most of tier C without
   CGO or bundling weights.
3. In-process (link libllama/MLX) — adds true state manipulation (branching,
   backtracking, speculative decoding) and distribution (one artifact ships model
   + engine).

Most of the "cognitive" control is step 2, not step 3. Step 3 is specifically for
*state search* and *bundling the model*.

**The honest costs of step 3.**

- **No-CGO breaks.** The mature path (llama.cpp, MLX) needs CGO. A pure-Go port
  (`llama.go`) keeps the single static binary but is slower and less complete.
  In-process therefore puts ADR-0003 on the table.
- **Crash isolation is gone.** A segfault in native inference kills the engine,
  not a separately respawnable process.
- **Memory/lifecycle is yours.** Weights live in the process (on Apple Silicon,
  shared unified memory — a plus), but load/unload is engine-owned and the
  daemon's `start`/`stop` verbs no longer apply.
- **Distribution gets heavy.** "Download texteditor = contains the LLM" is a real
  product advantage, but it means shipping weights + engine and re-litigating
  ADR-0015 per-machine.

**The Provider seam survives either way.** The "talk to cloud cheaply" property is
not threatened by in-process — it is threatened by *abandoning the Provider seam*.
The right shape keeps the Provider as a **pluggable inference backend** — HTTP
(local/cloud) *and* in-process — so cloud stays a one-line swap for simple text
gen, and in-process is an additional backend, not a replacement.

**Synthesis.** The question is not "should we go in-process someday," but "which
layer do we grow, and in what order":

1. Grow the serving protocol first (step 2) — grammar + logit bias + logprobs
   through the daemon; unlocks tier C and most of the reliability wins without
   touching no-CGO.
2. Go in-process only when state search is needed (step 3) — branching /
   backtracking / speculative decoding, or "the app bundles its own default
   model." That is a real ADR, because it reverses ADR-0003.

## The decoupling model — knobs as a separate surface

Working decision (recorded centrally in `architecture.md` §8/§11): **the MVP
engine is Go**, and the native-AI control surface is deliberately *decoupled*, not
folded into the MVP.

The Provider gateway is already a process seam. The "knobs" — logprobs,
grammar-constrained decoding, KV-cache manipulation, speculative decoding — are a
richer inference-control surface to build *later*, behind that seam:

- a future `InferenceControl` interface — a *sibling* of `ProviderGateway`, not a
  change to it;
- reached over a richer protocol (llama.cpp/MLX native endpoints) or a separate
  native process (e.g. a Rust `mistral.rs` service) — not by extending the
  OpenAI-compatible contract;
- in-process only if the engine is ever rewritten or a native backend is embedded.

This decouples the MVP's language from the native-AI ambition: the knobs are a
door that stays open regardless of what the engine is written in. Go wins the MVP
because its goroutine/channel model maps directly onto the streaming core (SSE
event bus, per-session concurrent turns, backpressure) — the part that matters
most, and the part no Rust framework abstracts away.

Consequence: the Provider stays frozen at OpenAI-compatible for the MVP. The knobs
are a *separate surface*, not an extension of the same contract.

## A suggested ordering

1. **Tier A first** — it is the moat, and it is mostly free given what is already
   built (block IDs, canonical content, git, meter, retriever).
2. **Tier B next** — `logprobs` is one `capabilities` flag.
3. **Tiers C–D later** — only when ready to stop being OpenAI-compatible.

## Hooks into the accepted architecture (non-normative)

- The **block tree + canonical content** (A1–A2) lean on block IDs (ADR-0020) and
  the `TextFormatter` canonical invariant (ADR-0029).
- **Citation verification** (A3, B8) is the mechanical answer to the citation floor
  in ADR-0015.
- **Constrained decoding** (C9–C11) is the same "guarantee structure, don't hope
  for it" instinct behind Needle (ADR-0028) and `TextFormatter` (ADR-0029).
- **Speculative decoding / drafting** (D14) reuses the `delegate`+facade serving
  from ADR-0028.
- **Logprobs / token-level uncertainty** (B7) sits beside the bundled tokenizer
  already accepted for thinking attribution (ADR-0024).

## Open questions to revisit

- Which tier is worth an ADR first, and when?
- *Resolved by the decoupling model:* the Provider stays OpenAI-compatible for the
  MVP; the knobs are a separate `InferenceControl` surface behind the seam, not an
  extension of the contract.
- Should the writer's *output* be grammar-constrained, or is post-hoc
  `TextFormatter` normalization sufficient for the writing use case?
- Is the long-term moat *reliability* (grammars / logit-bias, reachable at step 2)
  or *search* (branching / backtracking, only step 3)? That answer determines
  whether in-process is ever worth the CGO cost.
- When "cognitive" is invoked, is the goal for the *system* to get better at
  reasoning (search + verification) or the *model* to get better (fine-tuning,
  personal LoRA)? These point at different investments — step 3 vs tier D.
