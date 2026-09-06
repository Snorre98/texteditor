# ADR-0037: API server CORS policy — the webview/web targets reach the engine directly

Status: Accepted

Extends: ADR-0014 (deployment targets), ADR-0017 (OpenAPI surface), ADR-0021
(bind policy). Records the F7 transport decision (handoff-plan-f).

## Context

ADR-0014 pins the shape: the Vue + CodeMirror frontend talks **REST + SSE
directly** to the Go engine, and the capability adapter is the *only* per-target
seam. That holds for the Tauri desktop webview and for the web target alike.

The F7 decision (handoff-plan-f, "which client the webview uses") chose **(a)
direct HTTP from the webview** — `fetch` + `ReadableStream` SSE against the
sidecar-discovered base URL. But the engine currently serves **no
`Access-Control-Allow-Origin` header** (ogen's OPTIONS handler sets only
`Allow-Methods`/`Allow-Headers`, never `Allow-Origin`), and the Tauri webview on
macOS/Linux **cannot disable CORS** (only Windows' `--disable-web-security`, which
itself breaks Tauri IPC). A plain `fetch` from the webview origin
(`tauri://localhost` / `http://tauri.localhost`, or the dev server
`http://localhost:5173`) to `http://127.0.0.1:<dynamic-port>` is cross-origin and
is blocked. The F8 shell was only compile-green; the end-to-end handshake has not
exercised this yet.

The web target (E6, ADR-0014) has the same requirement: it self-hosts the same UI
on the user's machine/LAN and must `fetch` the engine cross-origin. So CORS is
latent, committed work — not new scope invented by (a).

## Decision

### 1. The API server serves CORS with an explicit origin allowlist, opt-in

`--cors-origins` / `ENGINE_CORS_ORIGINS` — a comma-separated list of allowed
origins. **Empty (the default) = CORS disabled**, preserving today's behavior for
the standalone daemon and the TUI. The consumer that needs CORS sets it
explicitly:

- **Tauri sidecar** — the Rust core passes `--cors-origins` for the known local
  webview origins when it spawns the engine (alongside `-bind`/`-port`). The
  candidate origins are `tauri://localhost`, `http://tauri.localhost`,
  `https://tauri.localhost`, and the Vite dev origin `http://localhost:5173`; the
  exact origin string is platform-dependent and is confirmed against the shipped
  webview at implementation (left open here).
- **Web target** — the operator sets `ENGINE_CORS_ORIGINS` to the serving origin
  when self-hosting (alongside `ENGINE_BIND=0.0.0.0`, ADR-0021 §2).
- **No `*`.** The web target runs on LAN where clients are untrusted (the ADR-0017
  §2 Zod rationale); a wildcard would export documents + history to any origin.

### 2. Implementation: one ogen `WithMiddleware` global middleware

- Sets `Access-Control-Allow-Origin` (matched against the allowlist) and
  `Vary: Origin` on **every** response — including the `/turn` raw SSE stream
  (`x-ogen-raw-response`, ADR-0031 §3) and the OPTIONS preflight.
- Short-circuits `OPTIONS` with `204` + `Access-Control-Allow-Methods` +
  `Access-Control-Allow-Headers: content-type, accept`. This is required because
  `POST /turn` carries `Content-Type: application/json` and
  `Accept: text/event-stream` — both CORS-non-safelisted, so the browser preflights.
- The middleware runs around `handleStartTurnRequest`, so the header is set before
  the first SSE flush; this is asserted in the implementation's acceptance test.
- With an empty allowlist the middleware is a no-op (or is not installed), so the
  standalone-daemon/TUI paths are bit-for-bit unchanged.

### 3. Transport-level only — no contract shape change, no codegen

CORS is a transport header, not an OpenAPI schema concern. This ADR does **not**
amend `api/openapi.yaml`, and does **not** trigger `go generate ./...`,
`bun run gen`, or `openapi-to-rust` regen. It is an engine implementation change
behind a single middleware, with a config knob.

### 4. F7 transport decision (recorded)

The webview is the client: direct `fetch` + `ReadableStream` SSE over a regenerated
Hey API + Zod client, with `sse.ts` ported verbatim (ADR-0017 §2, ADR-0031 §4). The
F6 Rust generated client's shipped role is `/health` discovery only
(`sidecar.rs::confirm_base_url`). See `client/tauri/README.md` "F7 transport".

## Consequences

- **+** The ADR-0014 diagram holds as written: the Vue frontend does REST+SSE to
  the engine; the capability adapter stays the only per-target seam.
- **+** CORS is shared by the desktop webview and the web target — one policy, no
  per-client hack; the web target becomes feasible without inventing a transport.
- **+** Explicit allowlist, no wildcard, opt-in by the consumer — consistent with
  ADR-0021's fail-closed posture; the standalone daemon/TUI keep today's behavior.
- **−** The engine gains a transport concern it previously did not carry, and an
  operator knob; the OPTIONS/preflight path must be tested (a browser-only path the
  Go tests won't exercise without an explicit CORS test).
- **−** The Rust generated client is largely dormant (committed, whole, never
  hand-shaped); its only shipped consumer is the health probe.

## Alternatives considered

- **Route all engine calls through the Rust core (`invoke`/Tauri events)** —
  rejected: violates ADR-0014's "only the capability adapter differs"; the web
  target cannot reproduce an IPC/event transport, splitting the frontend seam.
- **`@tauri-apps/plugin-http` (fetch through Rust, no engine change)** — rejected
  for now: avoids the engine change but adds a plugin, and SSE streaming over the
  plugin is an unspiked risk; a browser-compatible `fetch` over CORS serves both
  targets with one code path. Kept as a fallback if the CORS change proves costly.
- **`Access-Control-Allow-Origin: *`** — rejected: unsafe once the engine binds
  LAN (`ENGINE_BIND=0.0.0.0`) for the web target; a wildcard would expose document
  contents and edit history to any origin.
- **Hand-write a thin TS client without codegen** — rejected: ADR-0017 §2 mandates
  Hey API + Zod for TS clients.
