# ADR-0025: Serving control transport — an HTTP control daemon wrapping serve.sh

Status: Accepted

Supersedes: ADR-0007 (the "no HTTP daemon" conclusion).

## Context

ADR-0007 rejected an always-on HTTP control daemon ("revisit only if shell-out
latency proves unacceptable"), keeping the Fleet gateway shelling out to `serve.sh`.
Two forces now make the daemon the better choice, independent of any latency
concern:

1. **Remote/server-agnostic serving.** The user wants the system to work against
   machines that do not run their exact local serving setup. An HTTP control
   surface over the verb contract is the networkable seam that decouples the engine
   from "there's a `serve.sh` on this box."
2. **The Fleet gateway's write side** was pinned this session (ADR-0016) as
   blocking `Start` / async `Provision` — an HTTP daemon is a cleaner owner of
   those lifecycle semantics than a process spawn.

## Decision

Introduce an **HTTP control daemon** as the transport for the serving verb
contract. The core invariant is that the **verb contract** (ADR-0007: `list ·
start · stop · status · log · reach · provision`) survives unchanged; the daemon is
a *transport*, not a *new authority*:

1. **The daemon wraps `serve.sh`.** It is a thin networkable front-end over the
   same verbs, executing `serve.sh` (and its `models.json` parse + runner logic)
   under the hood. `serve.sh` stays authoritative for runner-specific launch
   logic; the daemon owns mapping HTTP → verbs.
2. **The daemon is the sole reader of `models.json`** (superseding ADR-0006's
   "both `serve.sh` and the engine read it"). The engine's Fleet gateway drops all
   direct manifest/file access and asks the daemon for everything — both read
   (`list`/`status`) and write (`start`/`stop`/`provision`).
3. **The daemon lives in `macos-dev-config`** (a small Go binary beside `serve.sh`),
   machine-local.
4. **Bind + auth:** the daemon binds `127.0.0.1` by default; remote use requires a
   tailnet tag (`tag:inference-client → tag:inference-server:<daemon-port>`) under
   the same deny-by-default Tailscale ACL (ADR-0021). No token layer — Tailscale is
   the sole auth, consistent with ADR-0021.

Consequence for the engine: the Fleet gateway becomes the daemon's HTTP client, and
the R3 boundary is "Fleet → daemon HTTP contract only" — the engine no longer
reaches into the manifest file or `serve.sh` at all.

## Consequences

- **+** The engine is decoupled from "a `serve.sh` is on this box" — remote
  machines only need to speak the daemon's HTTP contract, not replicate the local
  runner setup.
- **+** One authority (`models.json` → daemon) eliminates the two/three-reader
  drift ADR-0006 fought; the engine's R3 boundary is a single, clean HTTP seam.
- **+** The daemon centralizes the blocking-`Start`/async-`Provision` semantics
  this session pinned in ADR-0016.
- **−** A new always-on listener on the machine (binds localhost by default), which
  the Tailscale ACL must gate when exposed — an additional surface over `serve.sh`.
- **−** Deploys a Go binary alongside `serve.sh` in `macos-dev-config`; two live
  artifacts must be versioned/kept consistent there.

## Alternatives considered

- **Keep shell-out to `serve.sh` (ADR-0007 as-is)** — rejected in favor of remote
  agnosticism; the daemon is introduced not for latency but for a networkable,
  machine-agnostic control seam.
- **Daemon replaces `serve.sh` entirely** — rejected: moves tuned runner launch
  logic into the engine repo (blurs the two-repo R3 boundary); the daemon must wrap,
  not re-implement.
- **Daemon for writes only, engine still reads the manifest** — rejected: reintroduces
  the multi-reader drift on `models.json`.
