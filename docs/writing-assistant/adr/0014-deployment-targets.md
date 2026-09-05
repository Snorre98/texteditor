# ADR-0014: Deployment targets — desktop sidecar / web / TUI, one capability adapter

Status: Accepted

## Context

The same Vue+CodeMirror frontend must run in two places (desktop WebView and
web), with the TUI as a third client, and the Go engine shipped two ways
(standalone daemon or Tauri sidecar). The only per-target difference is how the
UI reaches the filesystem and OS.

## Decision

Deploy as:

```
Vue + CodeMirror (one UI codebase)
├── capability adapter: Tauri (invoke IPC) | Web (File System Access API)
            │
REST + SSE  │  (one contract, codegen'd)
            ▼
Go engine ── shipped as: standalone daemon  OR  Tauri sidecar
```

- **Desktop (Tauri)** — web UI in system WebView; Rust core does native file
  I/O/menus/dialogs; the Go engine is bundled as a **Tauri sidecar** spawned on
  launch (fully self-contained).
- **Web** — same UI served from a server, talking to the engine over the same
  REST/SSE (self-hosted on the user's own machine/LAN).
- **TUI** — the OpenTUI client, same contract, no native shell.

All app logic stays in the engine, unchanged across targets. The only per-target
work is the **capability adapter** (filesystem/OS reach).

## Consequences

- **+** Three targets, one engine, one contract.
- **−** The web target implies self-hosting the engine somewhere reachable,
  which partially trades away local-first/privacy — acceptable on the user's own
  machine or LAN, not on public infrastructure (documented caveat).

## Alternatives considered

- **Native-only desktop, no web/TUI** — rejected: the web and TUI targets are
  wanted and fall out of the same contract with minimal extra cost.
- **Separate codebases per target** — rejected: duplicates the UI; the capability
  adapter is the cheaper seam.
