# ADR-0018: Fleet manifest (two-tier) + serving control — the actual contract

Status: Accepted

Supersedes: ADR-0006 (single `models` array shape), ADR-0007 (provision signature
and daemon-free lifecycle).

## Context

ADR-0006 declared `models.json` the single source of truth "replacing
`servers.conf`" and "read by both `serve.sh` and the engine." That migration was
never implemented: `serve.sh` still parses `servers.conf`, and the two registries
have diverged. Three concrete problems surfaced when the files were reconciled:

1. `models.json` conflates two things: *servable models* (`gemma4-12b`,
   `gemma4-26b`, `nomic-embed`) and *daemons* (`lmstudio` as a bare entry). Ollama
   is implicit (three entries share port 11434); `lmstudio` is an explicit bare
   daemon. The `runner` field is doing double duty.
2. `servers.conf`'s `qwen` entry uses runner `delegate` → `serve-qwen.sh` (the
   tuned 128K/q8_0-KV/mlock wrapper). `models.json` flattened it to plain
   `llama.cpp`, silently dropping the verified tuning.
3. `serving-control.feature` asserts the lanes rule ("manifest rejected at
   validation with a lanes conflict"), but `fleet-manifest.schema.json` cannot
   express cross-entry identity checks — JSON Schema draft-07 has no such facility.

## Decision

### 1. Two-tier manifest: `daemons` + `models`

```jsonc
{
  "$schema": "…/fleet-manifest.schema.json",
  "daemons": [
    { "name": "ollama",    "runner": "ollama",    "host": "127.0.0.1", "port": 11434 },
    { "name": "lmstudio",  "runner": "lmstudio",  "host": "127.0.0.1", "port": 1234 },
    { "name": "qwen",      "runner": "delegate",  "delegate": "serve-qwen.sh", "host": "127.0.0.1", "port": 8080 }
    // dedicated mlx-lm/mlx-vlm servers are themselves daemons, one per model
  ],
  "models": [
    {
      "name": "mistral-24b",
      "daemon": "mistral-24b",          // the daemon that serves this model
      "source": { "kind": "hf", "repo": "mlx-community/Mistral-Small-3.1-24B-Instruct-4bit" },
      "capabilities": { "contextLength": 131072, "thinkingMode": false, "supportsSystemPrompt": true },
      "defaults": { "temperature": 0.5 },
      "modeTags": ["drafter"]
    }
  ]
}
```

- **`daemons`** are the lifecycle units (what `start`/`stop` operate on). A
  daemon is either a shared daemon (ollama/lmstudio hosting many models) or a
  dedicated one (each mlx/llama server). `runner` names which binary; a
  `delegate` field names a wrapper script when one exists.
- **`models`** are the resolve/provision units; each references a `daemon` by
  name. The model carries `source`, `capabilities`, `defaults`, `modeTags`.

### 2. `serve.sh` migrates to parse `models.json` now

`serve.sh` is rewritten to parse `models.json` via `jq`; `servers.conf` is
deleted. This implements ADR-0006 as written — one source of truth, no generated
artifact to drift. The `qwen` wrapper survives via the `delegate` runner (below),
so nothing is lost in migration.

### 3. Runner enum gains `delegate`

The `runner` enum expands with `delegate`. `qwen` returns as a daemon entry
`{runner: delegate, delegate: "serve-qwen.sh"}`, with its tunables (`QWEN_CTX`,
`QWEN_KV`, `QWEN_NP`, `QWEN_SPEC`) still in env vars consumed by `serve-qwen.sh`.
The manifest names the wrapper; the wrapper owns the runner-side controls.

### 4. Lanes rule: schema enforces name-uniqueness; a semantic validator enforces lanes

- `fleet-manifest.schema.json` enforces `models[].name` uniqueness (JSON Schema
  can do this via `uniqueItems`-over-a-projection is *not* expressible, so name
  uniqueness and port non-collision are enforced by the validator, with the schema
  carrying structural typing only).
- A **shared semantic validator** — the `models.json` loader, invoked by *both*
  the engine and `serve.sh` — enforces the lanes rule (ADR-0008): **no two models
  resolve to the same HF repo/source on different daemons.** A lanes conflict
  fails manifest load with `lanes-conflict` naming both entries. This is a semantic
  invariant, correctly outside JSON Schema's reach.

### 5. Provision is async and observable

```go
Provision(ctx context.Context, name string) (provisionID string, err error)
```

`Provision` kicks off the HF download in the background (shelling `serve.sh
provision` → `huggingface-cli download`, resumable) and returns a `provisionID`.
Progress is observed via `Status(name)` — which now returns `provisioning` with
bytes-done/total — and completion via a terminal lifecycle event. The
`POST /models/{name}/provision` endpoint is `202 Accepted`, and the client polls
status. This matches the `provisioning` phase in `state-machine.md` §2.

## Consequences

- **+** `models.json` is now a strict superset of what `servers.conf` described; the
  migration loses nothing (delegate preserved).
- **+** Two tiers separate "what runs" (daemons) from "what's servable" (models),
  which is the honest shape the old flat file couldn't express.
- **+** The lanes rule is enforced at load by a validator shared by engine and
  `serve.sh`, closing the contract/schema gap.
- **+** Provision is non-blocking and observable — a 20 GB download no longer
  stalls the request handler.
- **−** A schema rewrite (substantial) superseding ADR-0006's single-array shape,
  and a bash `jq` parsing layer in `serve.sh` (regression risk against the
  previously tuned bash).
- **−** The `delegate` runner is a machine-specific escape hatch; it names a
  wrapper script that only exists in `macos-dev-config`, so the manifest's `runner`
  enum is not fully provider-generic.

## Alternatives considered

- **Keep the single `models` array with daemons implied by `runner`** — rejected:
  the `lmstudio`/`ollama` inconsistency shows `runner` is doing two jobs; two tiers
  express the lifecycle-vs-servability distinction directly.
- **Flatten `qwen` to llama.cpp + `extraArgs`** — rejected: loses the verified
  128K/q8_0/mlock tuning, and free-form arg blobs leak runner flags into a data
  contract (R3 smell).
- **Encode lanes purely in JSON Schema** — rejected: draft-07 cannot cross-check
  two entries' identity.
- **Keep `servers.conf` (or generate it from `models.json`)** — rejected: dual
  source of truth reintroduces the drift ADR-0006 exists to prevent.
