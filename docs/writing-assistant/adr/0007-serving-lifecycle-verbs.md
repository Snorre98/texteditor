# ADR-0007: Serving lifecycle verbs — a defined, idempotent control contract

Status: Accepted

## Context

"Control what LLMs are served" is not only *which* models (the manifest,
ADR-0006) but *whether one is running*. Today the verbs live implicitly inside
`serve.sh` (`list/start/stop/status/log/reach`) with no formal contract, so the
engine cannot safely drive serving and humans rely on `serve.sh`'s exact spelling.

Forces:

- Lifecycle must be a **defined contract** the engine's Fleet gateway can depend
  on, without the engine shelling out to an unspecified script.
- Start/stop/status must be **idempotent** and **deterministic** so an engine or
  human can call them repeatedly without surprises.
- **The TUI actively starts and stops models.** A user switching the model in the
  model/mode switcher must cause the engine to `start` the new model and `stop`
  the old one — so the *write* side of lifecycle is a required capability, not a
  convenience.

## Decision

Formalize serving lifecycle as a **verb contract** (documented precisely in
`contracts/interface.md` §"Serving lifecycle"):

| Verb | Semantics |
|---|---|
| `list` | enumerate manifest entries + live status + what's on disk |
| `start <name\|all>` | start in background, wait for health; **refuse if port busy** (print the remap hint) |
| `stop <name\|all>` | stop; no-op + warn if not running |
| `status [name\|all]` | health-check (any of `/health`, `/v1/models`, `/api/tags`) |
| `log <name>` | tail the server log |
| `reach <name>` | base URL + exact client env/flag + a working `curl` example |
| `provision <name>` | fetch model weights via HF API (ADR-0008) |

The **executor is `serve.sh`** in `macos-dev-config` (no new daemon). The
engine's Fleet gateway is the *only* engine module that may invoke these verbs.
Discovery (`list`/`status`) reads the manifest; the **write side
(`start`/`stop`/`provision`) is shelled out to `serve.sh`** and is *required* so
the TUI can switch models at runtime. Both sides are behind the same contract,
so the TUI calls `FleetGateway.Start/Stop`, not `serve.sh` directly.

Contract invariants (normative):
- `start` is idempotent: starting a running server is an error *unless* `status`
  already reports `up`.
- `stop` is idempotent: stopping a non-running server is a no-op warning.
- Port conflicts are detected *before* launch and surfaced with the exact
  `SERVE_PORT_<NAME>` override.
- Health is checked via the runner's own endpoint; a server is `up` only when it
  answers, not merely when its process exists.

## Consequences

- **+** The engine gains a stable, testable surface for serving control without
  depending on an HTTP daemon's presence.
- **+** Humans and the engine share one vocabulary; `serve.sh` remains the CLI,
  so no new runtime is introduced.
- **−** Shelling out from Go to `serve.sh` couples the Fleet gateway to bash and
  is a process-boundary call with its own failure surface; this is accepted as
  the price of the TUI actively controlling serving (see failure contract).

## Alternatives considered

- **Always-on HTTP control daemon** — rejected: heavier; introduces a new
  always-running process to secure (Tailscale ACL already needed), when the
  manifest + `serve.sh` verbs already give the engine (and thus the TUI) full
  start/stop control. Revisit only if per-request latency of shell-out proves
  unacceptable.
- **Engine reimplements the verbs natively in Go** — rejected: duplicates
  `serve.sh`'s tuned logic (per-runner flags, health, logs) and forks authority
  with `macos-dev-config`.
- **No formalization (keep `serve.sh` as-is)** — rejected: the engine would then
  depend on an unspecified CLI surface; the "control API" would not exist.
