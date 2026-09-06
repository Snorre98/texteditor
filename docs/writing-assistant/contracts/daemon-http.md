<!-- MIRROR — canonical: macos-dev-config/docs/contracts/daemon-http.md (ADR-0033 §3). Edit the canonical copy and re-sync; a test in internal/fleet checks drift when the sibling repo is present. -->
# Control-daemon HTTP contract

> **Canonical copy.** This file lives in `macos-dev-config` (the machine-local
> LLM control plane, ADR-0033). `texteditor` keeps a mirror at
> `texteditor/docs/writing-assistant/contracts/daemon-http.md` with a sync check;
> edit here, not there.

Every local-LLM app reaches serving **only** through the control daemon's HTTP
contract: the texteditor engine via its Fleet gateway client (`fleet.NewDaemon`),
PresentationToMarkdown via its own client (its ADR-0034). This page pins the
transport so the daemon binary (this repo, `cmd/fleetdaemon`) and its consumers
can be built independently against one shape. It projects the lifecycle **verbs**
(ADR-0007, `texteditor/docs/writing-assistant/contracts/interface.md §12`) onto
REST. The daemon is the **sole reader** of `models.json` (ADR-0027); consumers
never read the manifest or invoke `serve.sh`.

Source ADRs: ADR-0007, ADR-0018, ADR-0025, ADR-0027, ADR-0030, ADR-0033
(all in `texteditor/docs/writing-assistant/adr/`).

## 1. Transport conventions

- All requests/responses are JSON (`Content-Type: application/json`), UTF-8.
- The daemon binds `127.0.0.1:<port>` by default; remote access is gated by a
  Tailscale ACL tag (ADR-0021 §3), not a token.
- Errors are `application/json` with a **stable `code`** (the verb-contract error
  codes, `interface.md §12.1`) and a human `message`:

```json
{ "code": "port-in-use", "message": "port 8083 already bound (SERVE_PORT_TEXT=8093)" }
```

| HTTP status | Meaning |
|---|---|
| 200 | success (read verbs) |
| 202 | accepted, async (provision) |
| 400 | bad request |
| 404 | `unknown-server` (name not in manifest) |
| 409 | `port-in-use`, `model-not-found`, `lanes-conflict` |
| 409/400 | `binary-missing`, `not-running` |
| 500 | daemon-internal failure |

## 2. Verbs

### `list` — `GET /list`

Returns every servable model (the resolve/provision units of the two-tier
manifest), each with the `daemon`'s host/port/runner and its
`defaults`/`capabilities`/`modeTags`. The engine maps this to
`Fleet.ListModels()` and uses `defaults` in `Resolve`'s merge. `runner` and
`daemon` were added by ADR-0033 §4 so consumers can do runner-specific memory
management and target `start`/`stop`; the engine client ignores them
(daemon-owned fields).

```json
{
  "models": [
    {
      "name": "mistral-24b",
      "runner": "mlx-lm",
      "daemon": "mistral-24b",
      "host": "127.0.0.1",
      "port": 8085,
      "capabilities": { "contextLength": 131072, "thinkingMode": false, "supportsSystemPrompt": true },
      "defaults": { "temperature": 0.5 },
      "modeTags": ["drafter"]
    }
  ]
}
```

### `status` — `GET /status/{name}`

Health of one server (or `all`):

```json
{ "name": "mistral-24b", "state": "up" }
```

`state ∈ up | down | starting | stopping | provisioning | unknown`. When
`provisioning`, the response adds `bytes`/`total`:

```json
{ "name": "gemma4-26b", "state": "provisioning", "bytes": 1048576, "total": 20971520 }
```

### `start` — `POST /start/{name}`

**Blocking** (waits for health, 60s bound). On success returns 200 with the up
state; refuses a busy port (409 `port-in-use` with the `SERVE_PORT_<NAME>` hint),
a missing runner binary (409/400 `binary-missing`), or a not-yet-provisioned model
(409 `model-not-found`). Starting a server already `up` is an error unless
`status` reports `up` (idempotency, `state-machine.md §2.3`).

```json
{ "name": "mistral-24b", "state": "up" }
```

### `stop` — `POST /stop/{name}`

Idempotent; no-op (200 with a warning message) when already `down`.

```json
{ "name": "mistral-24b", "state": "down" }
```

### `provision` — `POST /provision/{name}`

Async HF download. Returns `202` immediately with a `provisionID`; progress is
observed via `GET /status/{name}` (state `provisioning`, bytes/total). Re-running
skips already-present files (ADR-0008).

```json
{ "provisionID": "prov-mistral-24b" }
```

### `log` — `GET /log/{name}`

Tail of the server log (read-only).

```json
{ "name": "mistral-24b", "lines": ["…", "…"] }
```

### `reach` — `GET /reach/{name}`

Base URL + a client `curl` example (read-only).

```json
{ "name": "mistral-24b", "baseURL": "http://127.0.0.1:8085/v1", "curl": "curl http://127.0.0.1:8085/v1/models" }
```

## 3. Consumers

### The engine client (`fleet.NewDaemon`, texteditor)

| `FleetGateway` op | Daemon verb | Notes |
|---|---|---|
| `ListModels()` | `list` | maps to `[]dto.Model` (drops daemon-owned `defaults`/`runner`/`daemon` into an internal entry) |
| `Status(name)` | `status` | maps `state` → `dto.LiveState` |
| `Start(name)` | `start` | blocking; typed errors `port-in-use`/`binary-missing`/`model-not-found`/`start-timeout` |
| `Stop(name)` | `stop` | idempotent |
| `Provision(ctx, name)` | `provision` | returns `provisionID` |
| `Resolve(name, opts)` | `list` + `status` | merge `defaults ← mode.params ← overrides`; enforce capability gates; fold the fallback ladder (ADR-0015) — engine-side, over daemon-returned data |

### PresentationToMarkdown (its ADR-0034)

PtM resolves feature endpoints from `list` (no hardcoded ports), reports health
via `status`, starts on-demand servers via `start`, and picks runner-specific
memory-release paths from `runner` (mlx-vlm `/unload`, ollama
`keep_alive:0`-style hooks). It never shells out to `serve.sh` and never reads
`models.json`.

The engine never sees `runner`, `source`, or `provisioning` fields beyond what the
contract exposes; those remain daemon-owned (ADR-0016 §1) — with the single
ADR-0033 §4 exception that `runner`/`daemon` are exposed for consumer-side memory
management.

## 4. Error-code table (`interface.md §12.1`, confirmed)

| Code | Meaning |
|---|---|
| `unknown-server` | name not in manifest |
| `port-in-use` | target port already bound; includes the remap hint |
| `model-not-found` | `source` file/repo missing and not yet provisioned |
| `binary-missing` | runner binary not on PATH |
| `not-running` | `stop`/`status`/`log` on a server that isn't up |
| `lanes-conflict` | two models resolve to the same source on different daemons (ADR-0018) |

Transport-level failure — the daemon is unreachable at all — is the engine's
`daemon-unreachable` (`failure-semantics.md §6`), distinct from any single-model
`provider-unreachable`.
