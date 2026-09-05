# ADR-0006: Fleet manifest — JSON in macos-dev-config is the single source of truth

Status: Accepted

## Context

The user must be able to **control what LLMs are served through the system from
the `macos-dev-config` directory**. Today that authority is `servers.conf`, a
pipe-delimited flat file that only `serve.sh` understands — not machine-readable
by the Go engine, not rich enough to carry capabilities or mode tags.

Forces:

- "Control what's servable" must be a *data* change, not an engine rebuild
  ("modes and tools are data, not code").
- The definition must live in `macos-dev-config` (the machine-local authority),
  but be consumed by two independent programs: `serve.sh` (shell) and the Go
  engine's Fleet gateway.
- The engine needs more per model than a port: context length, thinking support,
  sampling defaults, and which *modes* (ADR-0009) a model can serve.

## Decision

Replace `servers.conf` with a **JSON fleet manifest** — `models.json` in
`macos-dev-config` — as the **single source of truth** for what models can be
served on the machine. Both `serve.sh` and the engine's Fleet gateway parse it.

Manifest schema (one entry per model):

```jsonc
{
  "$schema": "…/fleet-manifest.schema.json",
  "models": [
    {
      "name": "gemma4-26b",                 // logical name (stable across runners)
      "runner": "ollama",                    // ollama | mlx-lm | mlx-vlm | llama.cpp | lmstudio
      "source": {                            // how to obtain/run it (ADR-0008)
        "kind": "hf",                        // hf | gguf | ollama | lmstudio
        "repo": "mlx-community/Gemma-4-26B-A4B-4bit",   // hf: repo id
        "file": "Qwen3.8-27B-UD-Q5_K_XL.gguf"           // gguf: filename under models/gguf
      },
      "host": "127.0.0.1",
      "port": 11434,
      "capabilities": { "contextLength": 262144, "thinkingMode": false, "supportsSystemPrompt": true },
      "defaults": { "temperature": 0.4 },
      "modeTags": ["editor", "drafter"]       // which modes may use it
    }
  ]
}
```

The manifest is validated against a committed JSON Schema (the schema is the
*contract*; see `contracts/data-model.md` §2). The Go engine imports the same
schema for validation and generated types.

## Consequences

- **+** One human-editable, machine-parseable file is the whole "control panel"
  for what runs on the machine — the user's stated requirement, met as data.
- **+** The engine's Fleet gateway can discover and map models with zero
  provider-specific code (it reads the manifest).
- **+** `serve.sh` and the engine can never disagree about a port/model/capability.
- **−** Migration cost: `servers.conf`'s six rows move to JSON once (small).
- **−** JSON has no comments — documentation/justification per entry moves to the
  `capabilities`/`defaults` fields and `inference-readme.md`, not inline prose.

## Alternatives considered

- **Keep `servers.conf` and derive a JSON view** — rejected: two files to keep in
  sync, and the pipe format is the wrong thing to be authoritative.
- **Extend `servers.conf` columns** — rejected: escaping/quoting in a
  pipe-delimited file is fragile once capabilities/mode tags are added; JSON
  is parseable by both bash (`jq`) and Go without ambiguity.
- **Manifest defined in the engine repo instead of macos-dev-config** — rejected:
  the machine must own what runs on it; the engine must treat it as external data.
