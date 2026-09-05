# Control-daemon HTTP contract

The engine's Fleet gateway reaches serving **only** through the control daemon's
HTTP contract (ADR-0025, ADR-0027). This page pins the transport so the engine
client (`fleet.NewDaemon`) and the daemon binary (in `macos-dev-config`, Plan B)
can be built independently against one shape. It projects the lifecycle **verbs**
(ADR-0007, `contracts/interface.md §12`) onto REST, mirroring the engine's own
OpenAPI route style (ADR-0017 §5). The daemon is the **sole reader** of
`models.json` (ADR-0027); the engine never reads the manifest or invokes `serve.sh`.

Source ADRs: ADR-0007, ADR-0018, ADR-0025, ADR-0027, ADR-0030.

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
manifest), each with the `daemon`'s host/port and its `defaults`/`capabilities`/
`modeTags`. The engine maps this to `Fleet.ListModels()` and uses `defaults` in
`Resolve`'s merge.

```json
{
  "models": [
    {
      "name": "mistral-24b",
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

## 3. The engine client (`fleet.NewDaemon`)

The Fleet gateway consumes the daemon contract as follows:

| `FleetGateway` op | Daemon verb | Notes |
|---|---|---|
| `ListModels()` | `list` | maps to `[]dto.Model` (drops daemon-owned `defaults` into an internal entry) |
| `Status(name)` | `status` | maps `state` → `dto.LiveState` |
| `Start(name)` | `start` | blocking; typed errors `port-in-use`/`binary-missing`/`model-not-found`/`start-timeout` |
| `Stop(name)` | `stop` | idempotent |
| `Provision(ctx, name)` | `provision` | returns `provisionID` |
| `Resolve(name, opts)` | `list` + `status` | merge `defaults ← mode.params ← overrides`; enforce capability gates; fold the fallback ladder (ADR-0015) — engine-side, over daemon-returned data |

The engine never sees `runner`, `source`, or `provisioning` fields beyond what the
contract exposes; those remain daemon-owned (ADR-0016 §1).

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
