# ADR-0032: Control daemon is authored in texteditor; lifecycle decision-gaps closed

Status: Accepted

Clarifies: ADR-0025 §3 ("the daemon lives in `macos-dev-config` … a small Go
binary beside `serve.sh`") — that sentence pins the daemon's *deployment*, not
its *source*. This ADR does not reverse any prior decision.

## Context

ADR-0025 established the control daemon as the HTTP transport over the lifecycle
verb contract and, with ADR-0027, made it the sole reader of `models.json`, which
hands the parsed manifest to `serve.sh`. But three things were left unspecified,
and they are exactly the decisions that must be pinned before the daemon can be
built without re-litigating them later:

1. **Where the daemon's *source* lives.** ADR-0025 §3 says "the daemon lives in
   `macos-dev-config` (a small Go binary beside `serve.sh`)." That is a
   *deployment* fact — the artifact that runs on the Mac. It says nothing about
   which repository authors the source. The engine repo (`texteditor`) already
   holds the daemon's entire wire vocabulary: the **client** side
   (`internal/fleet`, the `fleet.NewDaemon` implementation), the **contract**
   (`contracts/daemon-http.md`), and the fleet-manifest schema. `macos-dev-config`
   is currently script-and-data only (bash, `models.json`, `acl.hujson`,
   `launchd/`) with no Go toolchain.
2. **How the daemon binary is produced and updated.** A drop-shipped binary in a
   second repo needs a pin + a fetch + a way to tell the running binary's version
   from the desired one, or the two repos drift the way ADR-0027 already banned
   for `models.json` readers.
3. **Mechanics that the earlier serving ADRs left as implicit prose**, which
   surfaced in a decision-gap audit:
   - `serve.sh` "receives the parsed manifest from the daemon" (ADR-0027 §4) —
     *how* is the parsed manifest crossed over (what exact seam)?
   - the lanes rule exists and fails load with `lanes-conflict` (ADR-0018 §4,
     `data-model.md §2.4`) — *where* is it enforced (which binary, at which
     boundary)?
   - the pre-bind gate and Tailscale ACL (ADR-0021 §3) — *who* refuses a
     non-`127.0.0.1` bind, and how does the on-demand fleet stay in sync with a
     hand-maintained `acl.hujson`?
   - always-on agents (`launchd/`, `module-boundaries.md §1`) — the ADR set names
     an always-on named agent but pins none of the plist/lifecycle mechanics.

These are not new architectural positions; they are the parts of ADR-0018/
0021/0025/0027 that were left to be worked out "at build time." Recording them
here removes the ambiguity before implementation, consistent with the base model
(R6: every public API is a precise contract; deviating requires a recorded
rationale).

## Decision

### 1. Source in `texteditor`; binary in `macos-dev-config`

The daemon's **source** lives in the engine repo as a second binary:
`texteditor/cmd/fleetdaemon/` (same Go module `texteditor`, built
`CGO_ENABLED=0` → single static binary, ADR-0003). It is the mirror of
`internal/fleet`: the client and server of one contract are authored, compiled,
and CI-tested together in one module.

The daemon's **binary** is drop-shipped into `macos-dev-config` (git-ignored) and
run there via `launchd`. `macos-dev-config` therefore keeps script + data + the
deployed artifact only; **no Go module, no `go.mod`, no toolchain dependency
enters that repository.**

The two-repo boundary stays a **contract** — `contracts/daemon-http.md` plus the
fleet-manifest schema — never shared source (ADR-0001 R3). `macos-dev-config`
owns `models.json` (the machine-local instance of that contract), `serve.sh`, the
Tailscale `acl.hujson`, and `launchd/`; `texteditor` owns the contract and both
halves of the wire. Shipping a binary across the boundary is a deploy artifact,
not a code coupling.

### 2. Release, drop-ship, and programmatic tracking

- The daemon binary is fetched with **`go install texteditor/cmd/fleetdaemon@<tag>`**
  — pinning a semver Git tag, not an ad-hoc revision.
- `macos-dev-config` pins the desired release in a small **version file**
  (`fleetdaemon.version`, committed; the binary itself stays git-ignored under
  `bin/`). The pin — not the binary — is what is versioned.
- An installer step (in `install.sh`, or a dedicated `tools/fetch-fleetdaemon.sh`)
  resolves the pin to a tag, runs `go install`, and **compares the installed
  binary's `--version` output to the pin**, refetching only on drift. The daemon
  exposes its built-from version (`--version` / a `version` subcommand) so
  "am I current" is scriptable on any machine, rather than a manual memory.

This makes the released daemon version *checkable* end to end: the engine repo's
tag is the source of truth, and the deployed binary self-reports the tag it was
built from.

### 3. `serve.sh` manifest seam — per-invocation env vars

The parsed manifest crosses the daemon → `serve.sh` boundary as **per-invocation
environment variables** (`RUNNER`, `MODEL`, `HOST`, `PORT`, and the remap
`SERVE_PORT_<NAME>`), not as a JSON blob re-parsed by `serve.sh`. `serve.sh` stays
stateless and does **not** re-parse `models.json` (no `jq` parse — the regression
surface ADR-0027 removed), nor read `servers.conf`. This keeps `serve.sh` a thin,
runner-specific executor that the daemon wraps (ADR-0025 §1: wrap, not
re-implement), while the daemon owns manifest parse, lanes, provision, and live
state.

### 4. Lanes + port-uniqueness enforced in the daemon, at load

Semantic manifest invariants — unique `daemons[].name`/`models[].name`, unique
per-daemon `port` non-collision, `models[].daemon` referencing an existing daemon,
and the **lanes rule** (no two models resolve to the same `source` on different
daemons) — are enforced by the **daemon** at manifest load (ADR-0018 §4,
`data-model.md §2.4`). A lanes conflict fails load with the typed `lanes-conflict`
naming both entries; port/name collisions surface as the already-contracted
typed errors. JSON Schema validates structure only; these cross-entry identity
checks are semantic and live in the daemon's loader (the single reader ADR-0027
established). This resolves the "shared semantic loader" ghost definitively: it
is the daemon's, and the daemon's alone.

### 5. Pre-bind gate + ACL — the daemon owns the gate; the manifest drives the ACL

- The **daemon** enforces the pre-bind gate (ADR-0021 §3): a `start` whose
  resolved bind host is not `127.0.0.1` is rejected unless that host/port is
  gated by the Tailscale ACL. The check happens *before* shelling out to
  `serve.sh`, fail-closed (no process is spawned on an ungated LAN bind).
- The on-demand fleet's ports are **derived from the manifest** into a generated
  Tailscale ACL view (a template `acl.hujson` that a startup check reconciles
  against the manifest's daemon list), rather than being a hand-maintained list
  that silently grows stale as the fleet does. The manifest stays the single
  source of truth for what *can* be served; the ACL is a projection of it,
  checked at daemon startup so a model added to `models.json` but missing from
  the ACL is a visible, surfaced mismatch — never a silent LAN exposure.

### 6. `launchd` mechanics

- The daemon runs as **one** always-on LaunchAgent (`launchd/*.plist`, kept-alive).
- Model runners are **not** always-on launchd agents; they are started on demand
  by the daemon's `start` verb (matching the on-demand fleet, ADR-0015/0021), so
  the always-on footprint is exactly the daemon.
- Plists are templated (bind address + manifest path + version pin plumbed in);
  `launchctl load` remains idempotent through `install.sh`; server logs follow the
  existing `var/` convention, with the daemon's own log separate from runner logs.

## Consequences

- **+** One Go module, one CI: the daemon client (`internal/fleet`) and server
  (`cmd/fleetdaemon`) are cross-checked against the same `daemon-http.md`
  contract, eliminating the two-repo drift class ADR-0027 fought for `models.json`.
- **+** `macos-dev-config` stays script + data only; its deployment role and the
  R3 contract boundary are unchanged, and it gains a versioned, checkable binary
  rather than an unversioned blob.
- **+** The serving lifecycle's implicit mechanics (manifest seam, lanes home,
  pre-bind gate owner, ACL↔manifest sync, launchd shape) are now pinned, so the
  daemon can be built against decisions instead of assumptions.
- **−** A packaging step (build in `texteditor` → fetch into
  `macos-dev-config`) that did not previously exist; `go install` requires the Go
  toolchain present on the install path (acceptable for a dev machine; a release
  asset is the escalation path if a non-developer host is ever targeted).
- **−** A generated-ACL projection is new reconciliation surface; it must be
  checked at startup or a fleet edit could leave the ACL stale relative to the
  manifest.

## Alternatives considered

- **Author the daemon as a separate Go module inside `macos-dev-config`** —
  rejected: duplicates the fleet-manifest schema, the verb contract, and the
  JSON-Schema validation deps into a second repo, and introduces a new artifact
  class (a `go.mod`) into a machine-config repo that is otherwise script-only.
- **Keep `serve.sh` parsing `models.json` via `jq` and re-derive lanes there** —
  rejected: two parsers of one file is the drift ADR-0006/0018/0025/0027 all
  exist to prevent; the daemon is the sole reader.
- **Hand-maintained ACL list, reconciled by operator discipline** — rejected:
  "don't be unlucky" is not a security posture (ADR-0021); the manifest must
  drive the projected ACL so a fleet edit cannot silently outrun the gate.
- **Always-on launchd agents for every runner, not just the daemon** — rejected:
  contravenes the on-demand fleet and the memory discipline of ADR-0015/0021;
  runners start/stop under daemon control.
- **Release asset hosting (GitHub Releases) as the primary fetch** — deferred;
  `go install @tag` is the developer-machine default and the one required now.
  A release asset becomes the fallback only if a non-developer install path is
  required later.
