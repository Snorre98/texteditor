# ADR-0030: Fleet substrate — pure llama.cpp + MLX on Metal

Status: Accepted

Supersedes: ADR-0018 §1/§3 (runner enum — `ollama`/`lmstudio` demoted), ADR-0008 §2
(source kinds — `ollama`/`lmstudio` dropped). Narrows the runner-agnosticism stance
of ADR-0005/0016 (now spans only the pure runners + `delegate`).

## Context

The accepted architecture treats serving as runner-agnostic: ADR-0005 declares
"swapping Ollama ↔ MLX ↔ llama.cpp ↔ a hosted API changes only this module," and
ADR-0016 §1 hides the runner binary from the engine entirely. The manifest
`runner` enum (ADR-0018) is therefore a mixed bag: two *shared daemons* — `ollama`
and `lmstudio`, high-level apps with their own model stores and registries — plus
three *dedicated* servers (`mlx-lm`, `mlx-vlm`, `llama.cpp`) and a `delegate`
escape hatch.

Two forces now argue for narrowing this:

1. **Ollama and LM Studio are the "edge-AI" layer the vision explicitly wants to
   leave behind.** A model behind Ollama's or LM Studio's OpenAI facade hides the
   knobs — grammar, `logit_bias`, logprobs — that the decoupling model
   (`research/vision-native-local-llm-text-editing.md`; `architecture.md` §8/§11)
   commits to reaching later through a future `InferenceControl` surface. The
   non-native knobs are only reachable through a *pure* runner's native protocol.
2. **Metal is assumed but never declared.** ADR-0015's bandwidth-bound sizing
   (`tok/s ≈ 120 GB/s ÷ file-size-per-token`) is the Mac mini's Metal GPU
   unified-memory bandwidth, yet "Metal" appears nowhere in the accepted docs,
   leaving the sizing's premise implicit and permitting CPU-only fallbacks that
   would silently violate it.

## Decision

1. **The `runner` enum narrows to `llama.cpp | mlx-lm | mlx-vlm | delegate`.**
   `ollama` and `lmstudio` are demoted from first-class runners. A daemon is now
   always a dedicated server over a direct model file; the shared-daemon concept
   (one port hosting many models) is dropped.

2. **Metal is a hard constraint.** Every local runner must use the Metal GPU
   backend (llama.cpp's Metal build; MLX *is* Apple's Metal-native framework).
   CPU-only and CUDA paths are not supported deployments. This makes explicit the
   premise ADR-0015's sizing already rests on.

3. **Provisioning is direct quants only.** `source.kind` narrows to
   `hf | gguf | needle`: models land as MLX quants (an `hf` repo, e.g.
   `mlx-community/…`) or a GGUF file directly. The `ollama` and `lmstudio` source
   kinds — and their import-then-delete copies (ADR-0008) — are dropped.

4. **llama.cpp and MLX both stay; llama.cpp is the control default.** Both are
   Metal-native pure runners. llama.cpp has the richest control surface (grammar,
   `logit_bias`, logprobs, and the KV library) and is the default where knob
   control matters — the future `InferenceControl` path. mlx-lm/mlx-vlm is the
   Apple-native default for plain text-gen. This preference is a fleet-policy
   default (ADR-0015), not a code change.

## Consequences

- **+** Serving is closer to the model: no Ollama/LM Studio abstraction between
  the engine and Metal, and the non-native knobs become reachable via a richer
  protocol — the cheap half of `InferenceControl`.
- **+** Metal is declared, so ADR-0015's bandwidth sizing is grounded, and a
  CPU-only fallback can no longer silently undercut it.
- **+** Provisioning simplifies: one download path (HF) into MLX/GGUF quants, with
  no Ollama blob store or LM Studio import copy.
- **−** Loses Ollama/LM Studio convenience (one shared daemon, `ollama pull`, a
  GUI); any model previously served by them must be re-provisioned as a direct
  MLX/GGUF quant.
- **−** Narrows ADR-0005's "swapping is free": runner-agnosticism now spans only
  the pure runners plus `delegate`, not the hosted/Ollama/LM-Studio surface.

## Alternatives considered

- **Keep `ollama`/`lmstudio` first-class** — rejected: keeps the edge-AI layer that
  hides the knobs, and re-opens the import-copy waste ADR-0008 already flagged.
- **Metal as a soft preference, not a constraint** — rejected: leaves ADR-0015's
  sizing premise implicit and permits CPU-only fallbacks that violate it.
- **MLX-only (drop llama.cpp)** — rejected: llama.cpp holds the richest control
  surface (grammar/logit_bias/KV), which the `InferenceControl` ambition needs.
- **llama.cpp-only (drop MLX)** — rejected: MLX is Apple's Metal-native framework
  and the cleanest path for plain text-gen; there is no reason to drop it.
