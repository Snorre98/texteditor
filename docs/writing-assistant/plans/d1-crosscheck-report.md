# D1 cross-check report — landed vs. remaining

A deep dive re-reading the normative docs (ADR-0028, ADR-0018/0030/0032/0033,
`implementation-sequence.md`, `parked-needle-router.md`, `interface.md`,
`daemon-http.md`) and cross-checking every build-order item against the actual
code. Three findings required fixes (one was a real bug); everything else is
verified landed.

## 1. Verified complete (item-by-item against the build order)

| Plan item | Status | Evidence |
|---|---|---|
| P1.1 `cmd/toolhash` emits `{hash, tools}` | ✅ | `server/cmd/toolhash/main.go`; `hash == routergate.ToolSetHash` (`sha256:99522f…`); `TestToolHashMatchesToolSetHash` green |
| P1.2 facade-contract mirror + drift test | ✅ | `contracts/needle-facade.md` (banner) + `internal/routergate/contract_mirror_test.go` `TestNeedleFacadeContractMirror` — passes against the sibling checkout |
| P2.3 `tools/serve-needle.sh` (serve/start/stop/status/log) | ✅ | verb-convention wrapper, mirrors `serve-qwen.sh` |
| P2.4 Go facade `cmd/serve-needle` | ✅ | non-streaming `POST /v1/chat/completions` + `/health`; confident → `finish_reason:"tool"` + `{"name","args","confidence"}`, refusal → empty `stop` completion |
| P2.5 stub needle | ✅ | `cmd/serve-needle/testdata/needle-stub.sh` (confident + refusal canned output) |
| P2.6 `tools/needle-finetune.sh` | ✅ | consumes `cmd/toolhash`, `TODO(ML)` training slot, `.cact` archive, writes `source.fingerprint` — `--dry-run` verified end-to-end |
| P2.7 manifest `needle`/`needle-router` | ✅ | `runner:"delegate"`, `source.kind:"needle"`, `source.fingerprint:""`; validates + projects in `list` |
| P2.8 canonical `docs/contracts/needle-facade.md` | ✅ | canonical in macos-dev-config, mirror byte-identical (drift test green) |
| P3 docs | ✅ | D1 row + 4 locked decisions + milestone in `implementation-sequence.md`; trigger recorded in `parked-needle-router.md` |

## 2. Gaps found and fixed during this dive

### (a) Real bug — daemon-driven `start needle` would bind the wrong port (fixed)

`serve.sh`'s delegate dispatch forwards only `QWEN_HOST`/`QWEN_PORT` to the
delegate; `serve-needle.sh` read neither `HOST`/`PORT` nor `QWEN_*`, so a
daemon-started needle would default to `8081` — colliding with `transcriber` —
while the daemon health-checked the manifest port `8091` → `start-timeout`. The
engine only reaches serving via Fleet → daemon, so this broke the real path (the
earlier curl smoke ran the facade directly, missing it).

- **Fix:** `serve-needle.sh` now honors the daemon's `HOST`/`PORT` seam first
  (`NEEDLE_HOST="${NEEDLE_HOST:-${HOST:-127.0.0.1}}"`, same for port).
- **Verified:** simulated `serve.sh start delegate` with `HOST/PORT` → facade up
  on the manifest port, `status` returned `up`, confident mapping worked through
  the delegate, `stop` clean.

### (b) Missing committed test — daemon `list` needle projection (added)

The plan's gate "daemon `list` reports `needle-router` with `runner: delegate`
and empty `fingerprint`" was only checked manually. Added
`TestListProjectsNeedleFingerprint` in `internal/fleetdaemon/daemon_test.go` —
asserts `runner:"delegate"`, `daemon:"needle"`, port, and fingerprint projection
only for `source.kind=="needle"`.

### (c) Minor — `stop` precision (fixed)

`pkill -f "serve-needle"` was broad; tightened to
`pkill -f "serve-needle .*--port $NEEDLE_PORT"`, matching the `serve-qwen.sh`
convention.

## 3. What genuinely remains

Only the deferred ML job (by design, not a gap). Concrete sequence when the time
comes:

1. Fine-tune Needle 2 over the `cmd/toolhash` vocabulary
   (`$DEV_MODELS_SSD_BASE/needle/tool-vocabulary.json`).
2. Produce `needle2.cact` → `needle-finetune.sh` archives it and writes
   `source.fingerprint`.
3. Flip one mode to `toolCalling:"router"` → `router-tools-stale` gate clears.
4. Finalize the one ML assumption: the `.cact` stdout format
   (`needle-facade.md §2`) — if it differs, only the facade's `parseStdout` and
   the contract doc change.

## 4. Notes (no action required, for awareness)

- **Port deviation:** manifest uses `8091` (ADR-0028 §7's `8081` example is taken
  by `transcriber`). Data decision, no ADR conflict.
- **`inference-readme.md`** in macos-dev-config documents the needle2 archive
  (`hf-archive.sh Cactus-Compute/needle2`) but not the new facade — optional
  doc-sync for a later session.
- **Gate re-confirmation** is covered by the existing `routergate/gate_test.go`
  (both error classes + empty-fingerprint case) — green.
- `handoff-plan-d1.md` (untracked briefing) was left untouched; the canonical
  record is `implementation-sequence.md`.

## Verification status

- texteditor: `CGO_ENABLED=0 go test -count=1 ./...` — green.
- macos-dev-config: `go test -count=1 ./...` — green (incl. new facade parser
  tests + daemon needle-projection test).
- `go run ./cmd/toolhash` hash matches `routergate.ToolSetHash`.
- Mirror drift tests pass (daemon-http, fleet-manifest schema, needle-facade).
- No `api/openapi.yaml` change and no codegen regen (router is client-transparent,
  ADR-0028).

Nothing is committed.
