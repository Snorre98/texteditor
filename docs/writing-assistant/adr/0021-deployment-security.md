# ADR-0021: Deployment + security — sidecar spawn, bind policy, TLS/ACL

Status: Accepted

Supersedes: ADR-0014 (sidecar spawn mechanics), touches §11 risk #5.

## Context

ADR-0014 says the Go engine is "bundled as a Tauri sidecar, spawned on launch" but
does not define how it is spawned/stopped, on what port, or who guards the LAN
exposure. ADR-0003/0013's "clients generated against a stable base URL" collides
with the natural "just start, never collide" desire for a sidecar. And the
inference layer's only auth is Tailscale (ADR-0021 follows the `macos-dev-config`
`tailscale/acl.hujson` deny-by-default posture) — yet the writing assistant's fleet
is *on-demand* on ports the existing three-entry ACL does not list.

## Decision

### 1. Sidecar spawn/stop + port — dynamic-by-default, fixable

The Rust core spawns the engine binary as a Tauri sidecar child process on launch.
The **port is a launch parameter with two modes**:

- **Dynamic (default):** no `ENGINE_PORT` set → the engine picks a free port. The
  Rust core reads the chosen port from the engine's `/health` + `/models` reply and
  injects the actual base URL into the client. This is the "just start the app,
  never collide" path.
- **Fixed:** `ENGINE_PORT=<port>` forces a stable port — for remote/traditional
  web-server use where a client codegens against a known base URL.

**Stop** = SIGTERM, then SIGKILL on timeout.

**Port discovery for clients:** the engine advertises its actual base URL via a
"where am I" endpoint (plus optional mDNS on the LAN). Clients discover rather than
assume. This satisfies both "stable base URL" (fixed mode) and "never collide"
(dynamic mode) without threading a guessed port through every generated client.

### 2. Bind policy — localhost default, LAN opt-in

The engine binds `127.0.0.1` by default. `ENGINE_BIND=0.0.0.0` opts into LAN/remote
exposure, mirroring the serving side's `SERVE_HOST=0.0.0.0` convention
(`macos-dev-config`). This is **documented as the privacy trade-off**: binding
`0.0.0.0` exposes the API (documents + edit history) to the LAN. Localhost stays
the safe default; the web target (ADR-0014) is explicit self-hosting, not an
always-on default.

### 3. Inference + engine port auth — Tailscale deny-by-default, no token

The **single** auth layer is the Tailscale deny-by-default ACL (already the
`macos-dev-config` posture). When LAN serving is enabled, a **pre-bind gate**
refuses to `start` a model server with `SERVE_HOST=0.0.0.0` or to bind the engine
with `ENGINE_BIND=0.0.0.0` unless the port is gated by the ACL:

- The always-on ports (8081/8082/11434) are already in `acl.hujson`.
- The on-demand writing-assistant ports join the `tag:inference-server` set,
  reachable only by `tag:inference-client`.

No per-port token: the tailnet identity is the credential. This is defense
consistent with `inference-readme.md` ("none have real auth; Tailscale is the only
layer") and avoids adding a reverse-proxy + secret to runners that have no native
token support.

## Consequences

- **+** Dynamic-by-default port makes the desktop app "just start" with no
  collision; fixed mode preserves codegen-against-a-stable-URL for remote use.
- **+** Localhost-by-default keeps the local-first privacy promise; LAN is an
  explicit, documented opt-in.
- **+** Tailscale stays the single auth layer — one posture across the engine and
  the inference ports, no new secret material.
- **−** Dynamic port requires a discovery handshake (the Rust core reads
  `/health`+`/models`, then rewrites the client base URL) — extra launch-time
  machinery vs a hardcoded port.
- **−** The pre-bind gate is a behavioral coupling: serving `0.0.0.0` now requires
  the ACL to be correct, or start refuses. This is intended (fail-closed), but it
  means the tailnet tags must be maintained as the fleet grows.

## Alternatives considered

- **Always-fixed port** — rejected: loses the "never collide, just start" desktop
  path.
- **Always-dynamic, no fixable override** — rejected: kills the remote-server fixed
  codegen use.
- **Hard-require localhost (no LAN)** — rejected: kills the web/remote target and
  the fixed-port remote use in §2.
- **Token on inference ports (reverse proxy)** — rejected: runners lack token
  support natively; re-implements auth the ACL already provides; added surface.
- **No pre-bind gate (operator discipline)** — rejected: "don't be unlucky" is not
  a security posture; `inference-readme.md` itself warns against unguarded `0.0.0.0`.
