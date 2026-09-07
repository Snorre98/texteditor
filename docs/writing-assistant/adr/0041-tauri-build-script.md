# ADR-0041: One-script Tauri build — `tools/build-tauri.sh` owns sidecar + client

Status: Accepted

Extends: ADR-0034 §6 (layout-sensitive tooling resolves the repo root from its
own path and owns the repo-root/module-root boundary), ADR-0021 §1 (sidecar
bundling + `texteditor-<target-triple>` naming), ADR-0003 (single static Go
engine binary, `CGO_ENABLED=0`).

## Context

The desktop editor has **three disjoint build paths** today:

1. The Go engine sidecar — built by hand (`cd server && CGO_ENABLED=0 go build
   -o ../client/tauri/src-tauri/binaries/texteditor-<triple> ./cmd/texteditor`),
   a step the README carries verbatim.
2. Frontend deps + typecheck + vite build (`bun install` / `bun run build`).
3. The Tauri bundle (`bun run tauri:build`, which runs its own
   `beforeBuildCommand` frontend build and bundles `externalBin`).

Step 1 being manual is the root of the README's documented **stale-binary
trap**: engine changes don't reach the app unless the sidecar is rebuilt by
hand — the app UI looks fresh while the engine behind it is not. It is also the
only build path with no gate: nothing checks that the engine or client tests
pass before a bundle ships. And it encodes machine specifics (the Rust
toolchain lives on the external SSD, `RUSTUP_HOME`/`CARGO_HOME` exported by
hand) in prose rather than in tooling.

Forces:

- ADR-0034 §6: layout-sensitive code is fixed, not guessed at — `tools/`
  scripts already resolve `REPO_ROOT` from `BASH_SOURCE` (`build.sh`,
  `install-daemon.sh`) and every Go command runs from `server/`; CI and dev
  scripts must know the repo-root/module-root boundary.
- The sidecar is bundled, not installed (ADR-0021 §1); its name is
  `texteditor-<target-triple>` and Tauri appends the triple from
  `externalBin: ["binaries/texteditor"]`.
- The standalone daemon deploy target keeps its own path (`tools/build.sh` +
  `tools/install-daemon.sh`, ADR-0014 §2) — the desktop build must not disturb
  it.
- `cargo test` (sidecar handshake) spawns the real engine, which fails fast
  when the control daemon is unreachable (ADR-0025) — a build gate cannot
  assume the daemon is up.
- This is build tooling, not a contract change: no `api/openapi.yaml`
  amendment, no codegen lockstep (the ADR-0037 §3 distinction).
- There is no CI yet (status.md TODO #4) — the script is the seam CI will
  adopt, so nothing machine-specific may be hardcoded.

## Decision

### 1. `tools/build-tauri.sh` is the one desktop-build entry point

The script owns the whole pipeline in this order: **toolchain check →
verification gates → sidecar build → deps → `tauri build` → report**. It is
run from the repo root; the docs point at it and drop the manual step. Scope is
the Tauri app only; the standalone daemon stays `tools/build.sh`.

### 2. The sidecar is always rebuilt from source — never trusted as present

`HOST_TRIPLE` is read from `rustc -vV` (the host field), then
`CGO_ENABLED=0 go build -o client/tauri/src-tauri/binaries/texteditor-$HOST_TRIPLE`
runs from `server/` before every bundle. Always-rebuild is the guarantee
against the stale-binary trap: drift is the trap, and a pure-Go build (no CGO,
ADR-0003) makes freshness cheap. A `--sidecar-only` mode rebuilds the sidecar
and prints the `tauri:dev` hint — the dev-refresh path, replacing the README's
manual step.

### 3. Verification gates, default on, skippable

`--skip-gates` bypasses: `CGO_ENABLED=0 go test ./...` (server),
`bun test` + `bun run typecheck` (client/tauri), and `cargo test`
(src-tauri). `cargo test` is additionally gated on daemon reachability — a
fast `curl` of `$DAEMON_URL/list`; unreachable ⇒ warn + skip (not fail), since
the handshake test cannot pass without the daemon and a build must not require
it. `--sidecar-only` skips the gates too.

### 4. Toolchain policy — require, warn, never auto-set

`go`, `bun`, and `cargo` must be on `PATH`. If `cargo` is missing the script
prints the exact `RUSTUP_HOME`/`CARGO_HOME` SSD export lines and exits 1. It
never auto-exports machine-specific paths — the script stays CI-ready, and the
prose stays in the docs where this machine's SSD convention is recorded.

### 5. Deps + bundle are delegated, not duplicated

`bun install --frozen-lockfile` (deterministic; the `bun.lock` is committed),
then `bun run tauri:build` — the Tauri CLI's `beforeBuildCommand` already owns
the frontend build (`vue-tsc` + `vite build`), so the script does not run it a
second time. Artifacts stay in Tauri's canonical dir
(`src-tauri/target/release/bundle/`); no copying to a second location.

## Consequences

- **+** One documented entry point replaces the three-path dance; the
  stale-binary trap is impossible by construction (fresh sidecar on every
  build, and a one-command dev refresh).
- **+** A bundle now implies green engine + client tests (or an explicit
  `--skip-gates`), closing the one ungated build path.
- **+** CI-ready: no hardcoded machine paths, no daemon requirement for a
  build, frozen-lockfile installs — the script is the seam status.md TODO #4
  will adopt.
- **−** Shell, not make/just — accepted: `tools/*.sh` is the established
  convention (ADR-0034 §6, `build.sh` precedent), and the pipeline is linear.
- **−** The cargo-test gate warns-and-skips when the daemon is down — a
  degraded gate, not a hard failure. Accepted: a build must not require the
  control plane; the warning names the missing daemon.
- **−** Host-triple only: a second platform means extending the sidecar build
  (Go cross-compiles trivially; the Tauri/Rust side is the real cost — deferred
  until ADR-0014's targets demand it).

## Alternatives considered

- **Drift-check rebuild (like macos-dev-config's `build-fleetdaemon.sh`,
  ADR-0033 §5)** — rejected: drift-rebuild suits a version-pinned daemon whose
  binary is verified against a pin; the sidecar's whole failure class *is*
  drift, and a fresh pure-Go build is cheap enough to always pay.
- **Build the sidecar inside Tauri's `beforeBuildCommand` hook** — rejected:
  buries a cross-language step inside the Tauri CLI, hides its failure in
  cargo output, and is untestable standalone.
- **cargo xtask** — rejected: a Rust-only task runner is the wrong host for a
  Go-first build step; adds a dev-dependency for what a linear shell script
  does.
- **Makefile / justfile** — rejected: introduces a new toolchain convention
  for one target; the repo already standardizes on `tools/*.sh`.
- **Build both macOS arches now** — rejected (YAGNI): only the host triple
  ships today (ADR-0014); the Go cross-compile is trivial to add when a second
  target exists, and the ADR above records that.
- **Copy bundles to a top-level `dist/`** — rejected: two artifact locations
  drift; `src-tauri/target/release/bundle/` is Tauri's canonical, self-documenting
  output.

## Future work

- Stamp the engine version into the sidecar (`-ldflags "-X main.version=..."`,
  the ADR-0033 §5 pattern) once the engine advertises a version — it has no
  `--version` flag today.
