# ADR-0033: Control daemon source moves to macos-dev-config (the machine-local LLM control plane)

Status: Accepted

Supersedes: ADR-0032 §1 ("Source in `texteditor`; binary in `macos-dev-config`")
and ADR-0032's consequence "no Go module … enters `macos-dev-config`" (§1, last
paragraph), and amends ADR-0032 §2 (release/fetch mechanics). ADR-0032 §3–§6
(serve.sh manifest seam, lanes/port enforcement home, pre-bind gate owner,
launchd shape) are unchanged.

## Context

ADR-0032 was implemented: the daemon server was authored in `texteditor`
(`cmd/fleetdaemon` + `internal/fleetdaemon`), and its binary is drop-shipped into
`macos-dev-config`. Two facts surfaced after the build that ADR-0032's split does
not fit:

1. **The daemon is the machine's LLM control plane, not the engine's.** It is the
   sole reader of `models.json` (ADR-0027), owns lifecycle, lanes, provision, the
   pre-bind gate, and the ACL projection — all machine-local authorities that
   ADR-0006 pinned in `macos-dev-config` ("the machine must own what runs on it").
   But its *source* — the authority behind that authority — lives in one consumer
   of it (`texteditor`). Any other app that serves or consumes local LLMs on this
   machine (PresentationToMarkdown is the first; it already hardcodes ports and
   shells out to `serve.sh` from its own `SERVERS` catalog, drifting from
   `models.json`) must look to the engine repo for how the machine's LLMs are
   actually controlled. The daemon is serving-*side* infrastructure, and the
   serving side is a separate module cluster (ADR-0001) with its own public API.
2. **The engine's coupling to the daemon server is one-way and contract-shaped.**
   `internal/fleet` consumes only `daemon-http.md`'s JSON shapes. The only
   texteditor coupling the daemon server has is `shared/dto` (a handful of wire
   DTOs) and a test importing `internal/fleet` for a client↔server cross-parse.
   Both are replaceable with contract-pinned types — the R3 discipline ADR-0001
   already mandates for the two-repo boundary ("a *contract*, not shared types").

Keeping the source in `texteditor` would preserve the cross-parse CI benefit
(ADR-0032's "+" for one module), but it costs what ADR-0032 itself fought for
elsewhere: a second consumer (PtM) cannot evolve the control plane without a
round-trip through the engine repo. That is the same "two masters of one
authority" drift class as the `models.json` readers ban.

## Decision

### 1. Daemon source is authored in `macos-dev-config`

`texteditor/cmd/fleetdaemon/` and `internal/fleetdaemon/` move to
`macos-dev-config` (`cmd/fleetdaemon/` + `internal/fleetdaemon/` under a new
`go.mod`). `macos-dev-config` is now, end to end, the machine-local source of
truth for interacting with local LLMs: `models.json`, `serve.sh`, the daemon that
reads one and wraps the other, `launchd/`, and the Tailscale ACL. The daemon's
HTTP contract is the machine's public API that every local-LLM app consumes —
texteditor's engine via `internal/fleet`, PresentationToMarkdown via its own
client (ADR-0034 in that repo).

The explicit reversal: ADR-0032 §1's "no Go module enters `macos-dev-config`"
consequence is void. The repo gains a Go toolchain dependency; the binary it
ships is now built from its own source, not fetched from another repo's tag.

### 2. Wire types are duplicated, contract-pinned — no shared Go code

Each repo defines its own copies of the daemon wire types (the daemon's
`Capabilities`/`LiveState`; the engine's `shared/dto` fleet types remain
texteditor-owned and unchanged). The single source of truth for their shape is
the contract (`daemon-http.md` §2 examples), the R3 boundary ADR-0001 defines.
The cross-parse test dies with the move; the contract examples and a mirrored
contract check (below) replace it. Duplication is the accepted cost of
"contract, not shared types" and is small (~6 structs).

### 3. Contract and schema ownership move; texteditor keeps mirrors

- **Canonical** `daemon-http.md` moves to `macos-dev-config` (`docs/contracts/`),
  alongside the fleet-manifest JSON Schema (`internal/fleetdaemon/`, embedded in
  the binary and now owned where the sole reader lives).
- `texteditor` keeps **mirror copies** (`contracts/daemon-http.md`,
  `contracts/assets/fleet-manifest.schema.json`) marked as mirrors of the
  canonical files, with a sync check that fails when the sibling
  `macos-dev-config` checkout is present and the copies differ.

### 4. `list` exposes `runner` and `daemon`

`daemon-http.md` §2 `list` gains two daemon-owned fields per entry: `runner`
(the enum: `llama.cpp | mlx-lm | mlx-vlm | delegate`) and `daemon` (the lifecycle
unit name). Consumers like PtM need the runner to do runner-specific memory
management (mlx-vlm `/unload`, ollama `keep_alive:0`) and the daemon name to
start/stop. The engine client keeps ignoring both (daemon-owned fields,
ADR-0016 §1); the addition is backward-compatible.

### 5. Build + pin: local build, repo tag

- `tools/fetch-fleetdaemon.sh` becomes `tools/build-fleetdaemon.sh`: it runs
  `go build -ldflags "-X main.version=<pin>" -o bin/fleetdaemon ./cmd/fleetdaemon`
  from this repo's source and verifies `bin/fleetdaemon --version` equals the
  pin, rebuilding only on drift.
- `fleetdaemon.version` pins a **tag of this repo** (e.g. `fleetdaemon/v0.1.0`),
  or `dev` — which resolves to `git describe --tags --always` (the working tree;
  rebuilt whenever the tree changes). The pin — not the binary — remains the only
  versioned thing (ADR-0032 §2's principle, now against this repo's own tags).

## Consequences

- **+** `macos-dev-config` is one coherent control plane: manifest, executor,
  daemon, launchd, ACL — and every local-LLM app (engine, PtM, future) consumes
  one machine-local public API. A serving change no longer round-trips through
  the engine repo.
- **+** The daemon server loses its only in-module coupling (`texteditor/shared/dto`);
  the two-repo boundary is purely the HTTP contract + schema, exactly R3.
- **+** The `list` enrichment makes the contract usable by non-engine consumers
  that must know *how* a model is served, not just where.
- **−** Duplicated wire DTOs and a mirrored contract introduce a new sync surface;
  the mirror check (and the contract's own examples) is the guard. Smaller than
  the removed cross-repo *source* coupling it replaces.
- **−** The fleet↔fleetdaemon cross-parse CI test is lost (client and server no
  longer compile in one module); the contract is the single cross-repo check now.
- **−** `macos-dev-config` gains a `go.mod` and a build step, reversing ADR-0032's
  toolchain-free stance — accepted as the price of owning the daemon's source.

## Alternatives considered

- **Keep source in `texteditor` (ADR-0032 §1 as-is)** — rejected: the machine's
  control plane stays authored in one of its consumers; PtM and future apps must
  couple to the engine repo to evolve serving. This ADR exists because the user
  observed exactly that after implementing ADR-0032.
- **Extract a shared Go module (wire DTOs) consumed by both repos** — rejected:
  reintroduces a shared code package across the two-repo boundary, reversing
  ADR-0001 R3's "contract, not shared types" and adding versioning machinery for
  ~6 structs.
- **Duplicate only, but keep the contract canonical in `texteditor`** — rejected:
  the serving side's public API belongs with the serving side (ADR-0001: the
  serving cluster's public API is the verb contract). Mirrors keep the engine's
  R6 docs home readable without making it canonical.
- **Release-asset fetch from GitHub Releases instead of local build** — rejected
  for now: the machine IS the build host; `go build` from the checkout is simpler
  than publish-and-fetch for a single dev machine.
