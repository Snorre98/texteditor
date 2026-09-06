# Handoff — resume implementation (next session)

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are

**Track 2 E1 (deployment primitives) landed; a Track-1.5 insertion is next.**
ADR-0035 (engine-served directory listing) and ADR-0036 (file mentions as metered
turn context) are **Accepted but unimplemented**, and they are deliberately
sequenced *before* the rest of Track 2 (ADR-0035 §Context: "lands before Track 2
because both Track-2 clients inherit it from the contract"). This session
implements both. Note: **the contracts are partially pre-amended already** —
`data-model.md §1.3` lists the `mentions` meter component, `data-model.md §3.1`
lists `contextBudget.maxMentionTokens`, `failure-semantics.md §5` lists the four
mention error codes + overflow line, and `behaviors/workspace.feature` exists.
The **code, mode schema, interface.md, module-boundaries.md, and OpenAPI spec are
untouched** — reconcile them to the already-amended contracts.

### Landed + tested last session (Track 2 E1 — committed, engine primitives)

- **Dynamic port + bind policy (ADR-0021 §1/§2)** — `cmd/texteditor/main.go`:
  `--bind`/`ENGINE_BIND` (default `127.0.0.1`; `0.0.0.0` = LAN opt-in with a
  warning), `--port`/`ENGINE_PORT` (default `0` = dynamic free port); `bindListener`
  binds before the API server is built and derives `baseURL`.
- **"where am I" (ADR-0021 §1)** — `api/openapi.yaml` `Health` gains optional
  `baseUrl`; genapi regenerated; `apiserver.Deps.BaseURL` threaded into `GetHealth`.
  Recorded amendment to ADR-0017 §4.
- **Graceful shutdown** — `signal.NotifyContext` (SIGTERM/SIGINT) + `httpSrv.Shutdown`
  (the sidecar stop contract's engine half).
- **Standalone-daemon packaging (texteditor-owned)** — `tools/build.sh`,
  `tools/install-daemon.sh`, `deploy/com.texteditor.engine.plist` (KeepAlive launchd
  agent, fixed localhost port 9100 default).
- Tests: `main_test.go` (env parsing, dynamic/fixed bind), `TestHealthAdvertisesBaseURL`.

### Settled last session (do not re-ask)

1. `baseUrl` on `/health` (not a dedicated `/where` route) — recorded in ADR-0017 §4.
2. Daemon packaging lives in **texteditor** (`tools/` + `deploy/`), not macos-dev-config.
3. Rust/toolchain work (Plan E2/F) is **deferred**; this session is Go-only.

## Still open (explicit list; do not silently choose)

### Track-1.5 — ADR-0035 (Workspace) + ADR-0036 (mentions). THE central focus.

Build in this order (ADR-0036 depends on ADR-0035's `Workspace.Read`):

**W1 · `internal/workspace` module (ADR-0035 §1) — pure leaf.** Sealed
`Workspace` interface: `List(ctx, dir) ([]Entry, error)` (shallow, non-recursive,
case-insensitive name sort, hidden dotfiles *returned*) and
`Read(ctx, path, maxBytes) ([]byte, error)` (raw bytes, no versioning/indexing).
`Entry{Name, Path, IsDir}`. Typed errors: `not-found`, `not-a-directory`,
`not-regular`, `too-large`, `read-failed`. Internals = `os.ReadDir`/`os.ReadFile` +
path validation. Boundary-test (Q5).

**W2 · shared DTOs.** `dto.Mention{Path string}` + `Task.Mentions []Mention`
(`dto/loop.go`); `dto.MentionContent{Path, Text}` + `AssemblerInput.Mentions
[]MentionContent` + `Breakdown.Mentions int` + `AttributedBreakdown.Mentions int`
(`dto/assembler.go`); `ContextBudget.MaxMentionTokens int` (`dto/mode.go`).

**W3 · assembler splice (ADR-0036 §3/§4).** Splice mentions **after history,
before user input**, in mention order, each wrapped with a **path marker line**
(the RAG "Source:" discipline — record the exact marker format). Truncate
over-budget mentions **from the tail** (last mention first) per
`MaxMentionTokens`; `0` budget → all mention content truncated; overflow rendered
as a **labeled overflow line**, never folded silently (failure-semantics §4).
Add `truncateMentions` (pure); add `Mentions` to the returned `Breakdown`.

**W4 · meter carries the component (ADR-0036 §5).** `AttributedBreakdown.Mentions`;
`scalePrompt` goes 5→6 components (**mentions sits between history and user** —
match `data-model.md §1.3` enum order `system|tools|rag|history|mentions|user|thinking|completion`);
`persist` adds a `{"mentions", …}` row; `meterEvent` adds `"mentions"`.

**W5 · loop resolves mentions first, fail-fast (ADR-0036 §2).** `loop.Deps` gains
`Workspace workspace.Interface`. In `runTurn`, **before** the state machine (after
`validate`), read every `task.Mentions` via `Workspace.Read(path, 256 KiB)`,
fail-fast with typed SSE error codes pre-streaming:

| Outcome | code |
|---|---|
| `not-found` / `not-regular` | `mention-not-found` |
| `too-large` (over 256 KiB) | `mention-too-large` |
| `read-failed` | `mention-unreadable` |
| > 8 mentions | `too-many-mentions` |

Caps are **constants in the loop package** (8 mentions, 256 KiB), not mode data.
Map `workspace` sentinel errors in `codeFor` (or a dedicated emit). Pass resolved
`[]dto.MentionContent` into `AssemblerInput.Mentions`.

**W6 · composition root (`cmd/texteditor/main.go`).** Wire `workspace.New()` into
both `loop.Deps.Workspace` and `apiserver.Deps.Workspace`.

**W7 · mode schema + seed (ADR-0036 §4).** `config/schemas/mode.schema.json`:
`contextBudget` gains `maxMentionTokens` (integer ≥0). `mode/registry.go`:
parse it. `config/modes/*.json`: seed `maxMentionTokens` values. **Judgment call:
seed defaults** (ADR says `0` = no mention budget; seeding `0` disables mentions —
pick sensible per-mode values mirroring `maxRagTokens`, and record the choice).

**W8 · contract + OpenAPI (recorded amendments — ADR-0035 §2, ADR-0036 §1/§5).**
`api/openapi.yaml`: new `GET /directories?path=<abs dir>` (operationId
`listDirectory`) → `{path, entries: Entry[]}`; `Entry{name,path,isDir}` schema;
`Mention{path}` schema; `Task` gains `mentions: Mention[]`; `MeterEvent` gains
**required** `mentions` integer. Regenerate genapi (`go generate ./...`). Record
amendments: ADR-0017 §4 (add `/directories` row) + §6 (meter `mentions`, like the
earlier `rag`). `Workspace.Read` gets **no** REST surface (mentions ride through
`/turn`). Also amend `interface.md` (new Workspace section; `Mention`/`MentionContent`
in the §0 catalog; §5 `AssemblerInput.Mentions`/`Breakdown.Mentions`; §6
`AttributedBreakdown.Mentions`; §7 `Task.Mentions`; §8 `ContextBudget.MaxMentionTokens`)
and `module-boundaries.md` (Workspace module row + `Loop → Workspace` edge).

**W9 · apiserver.** `Deps.Workspace`; `ListDirectory` handler → `Workspace.List`
(errors project `not-found`/`not-a-directory`); `StartTurn` decodes `Task.mentions`
→ `dto.Task.Mentions`.

**W10 · TUI (scoped follow-up — flag before doing).** ADR-0035 §3 (`texteditor .`
directory arg) and ADR-0036's `@`-picker are **client-side presentation over the
engine contract**; ADR-0036 §Context explicitly excludes the picker ("not this
ADR's concern"). The engine+contract deliverable (W1–W9) is the mandate. Decide
whether the TUI `@`-picker + directory-arg lands this session or as a follow-up;
if landed, regen the client (`bun run gen`) and update `discovery.ts`/`store.ts`.

### Verification gates

- `CGO_ENABLED=0 go test ./...` green, plus new boundary tests: workspace
  List/Read + typed errors; assembler mention splice/truncation/overflow;
  meter mentions scaling + `mentions` event field; loop mention resolution
  (all four codes, caps, no stream starts on failure); apiserver `/directories`.
- `workspace.feature` scenarios → assert as Go unit tests (the ADR-0035/0036
  acceptance). `bun test` + `bunx tsc --noEmit` green in `client/tui/` (only if
  W10 lands; `bun run gen` required if the spec changed).

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` (build order) +
   `implementation-sequence-track2.md` (E1 landed; note the Track-1.5 insertion).
2. `docs/writing-assistant/architecture.md` — arc42.
3. `docs/writing-assistant/contracts/` — `interface.md` (to be amended per W8),
   `module-boundaries.md`, `data-model.md` (§1.3 + §3.1 already mention mentions),
   `failure-semantics.md` (§5 already lists mention codes), `state-machine.md`,
   `concurrency-topology.md`.
4. ADRs — normative. **ADR-0035, ADR-0036** govern this session; also ADR-0016
   (module inventory / sealed interfaces), ADR-0017 (OpenAPI surface), ADR-0021
   (bind policy trust boundary), ADR-0027 (shared DTOs).
5. `docs/writing-assistant/behaviors/workspace.feature` — Gherkin acceptance.

## Hard constraints (never violate)

- Single static Go binary, no CGO. SQLite via `modernc.org/sqlite`.
- Serving only via `Fleet → daemon` (ADR-0025/0027); never read `models.json` or
  call `serve.sh`.
- Sealed Go interfaces over pure DTOs (ADR-0016/0027); shared DTOs in `shared/dto`.
- Contract-first (ADR-0002/0017): amend `api/openapi.yaml` **before** touching
  genapi/clients; surface every route/shape change as a recorded contract
  amendment — no silent spec extension.
- A mentioned file is **never** a document (ADR-0036 §6): only `Workspace.Read`,
  no `DocumentStore.Open`, no git/SQLite side effects.
- The Workspace module is a **pure leaf** (ADR-0035 §1); no state, no DB, no cache.
- Test at the boundary (Q5): `CGO_ENABLED=0 go test ./...` stays green; client
  code is spec-generated, never hand-shaped.

## Then continue

ADR-0035 + ADR-0036 landed → resume Track 2 (`implementation-sequence-track2.md`):
E2 sidecar + E6/E7 web target + F6–F8 Tauri (gated on the Rust toolchain). D1
(router enablement) remains deferred unless a trigger fires.

## Report back

At each milestone: what landed, which tests pass, and any place the docs forced a
stop or a judgment call. Specifically flag: (a) the **mention splice marker
format** and **mode seed `maxMentionTokens` values** (W3/W7 — record, don't invent
silently); (b) the **TUI scope decision** (W10 — in-scope or follow-up); and (c)
the exact recorded contract amendments made to `interface.md`/ADR-0017 so the
next session can verify they match the implemented wire.
