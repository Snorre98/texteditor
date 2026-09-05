# ADR-0008: Model provisioning via the HF API

Status: Accepted

## Context

The user wants to **download LLMs with the plain Hugging Face API**
(`huggingface-cli download`), not via runner-specific stores. Today models are
pulled three different ways (Ollama `pull`, LM Studio, HF cache) with a known
risk of downloading the same model redundantly (`inference-readme.md`'s
"avoiding duplicate downloads" note).

Forces:

- "Format follows purpose": run-format (MLX/GGUF quant) vs archival-format
  (full-precision safetensors) are genuinely different files and both are wanted.
- Duplication is only *cross-tool*, never intra-tool (HF cache is content-hash
  deduped; Ollama shares layers by blob digest).
- The `provision` verb (ADR-0007) must produce weights in the right place for
  the manifest's `source.kind`.

## Decision

1. **`provision <name>` = `huggingface-cli download`** resolving the manifest's
   `source` into the SSD layout:
   - `source.kind == "hf"` → download HF repo id into
     `$DEV_MODELS_SSD_BASE/huggingface/` (shared `HF_HOME` cache, already unified).
   - `source.kind == "gguf"` → download the named file into
     `$DEV_MODELS_SSD_BASE/gguf/`.
2. **"Lanes" discipline** (codified from `inference-readme.md`): **one runner
   per model.** A model is *either* run on MLX *or* GGUF/Ollama *or* archived —
   it is not `pull`ed into multiple runners. The manifest's `runner` field is the
   single assignment of a model to its lane.
3. **Dedup safety net:** byte-identical duplicates in the `models/` tree are
   reclaimed with APFS hardlinks (`jdupes -r -L`), not by policing downloads.
4. **Archive policy:** `huggingface/archive/<org>/<repo>` holds full-precision
   safetensors (the backup master); run-quants are re-pullable and not backed up.

## Consequences

- **+** One download path (HF API) for everything, controlled by `provision` and
  the manifest — matches the user's stated preference.
- **+** Lanes + HF unification make the same model landing in two runners the
  exception, not the default.
- **−** Runner-specific stores (Ollama/LM Studio) still copy a GGUF on import;
  the lane rule prevents the *double download*, but a deliberately Ollama-served
  model still lives in Ollama's blob store (import-then-delete of the loose file).
- **−** Gated models (Llama, etc.) require a one-time `huggingface-cli login`.

## Alternatives considered

- **Runner-native `pull` for each store** — rejected: three download paths to
  remember, and the known cross-tool duplication the user is trying to avoid.
- **Only archive full-precision, never run-quants** — rejected: the machine
  *runs* models; MLX/GGUF quants are the working copies, the archive is the
  future-GPU master. Both classes are legitimate.
- **Self-quantize instead of downloading community quants** — rejected (except
  for obscure models): downloading `mlx-community`/`bartowski` pre-quants is the
  fast path; self-quantization is compute-heavy and almost never needed.
