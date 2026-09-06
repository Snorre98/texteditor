# texteditor-tui

The OpenTUI + Solid client for the writing-assistant engine (Plan C, C6–C8).
A **dumb client** (ADR-0013 §3): the whole surface is generated from the locked
contract `../../api/openapi.yaml` (ADR-0017), every response is zod-validated at
the boundary, and all domain logic lives in the engine.

## Layout

```
src/
  generated/       Hey API + Zod output (bun run gen — committed, like server/internal/genapi)
  api/
    client.ts      validated calls over the generated sdk (responseValidator + baseUrl rewrite)
    discovery.ts   port discovery, fixed mode (ADR-0021 §1): ENGINE_URL > ENGINE_PORT > 9100 + /health probe
    sse.ts         the /turn stream reader over the generated zod payload schemas (ADR-0031 §4)
  state/
    store.ts       Solid signals store (ADR-0023): models/modes/tools/document/sessions/turn/meter
  ui/              OpenTUI Solid panels: editor, chat, meter, switcher, RAG, diff
```

## Run

```sh
bun install
bun run gen       # regenerate src/generated from ../../api/openapi.yaml
bun test          # discovery/decoder/store/component tests (28)
bun run typecheck

# engine must be up (it binds 127.0.0.1:9100 by default):
#   go run ../server/cmd/texteditor           (engine — needs the control daemon)
#   cd ../../../macos-dev-config && go run ./cmd/fleetdaemon   (control daemon, ADR-0033)

ENGINE_PORT=9100 TEXTEDITOR_DOC=/path/to/note.md bun run src/index.tsx
# or: bun run src/index.tsx /path/to/note.md
```

Port discovery (fixed mode): `ENGINE_URL` (full base) > `ENGINE_PORT` (port on
127.0.0.1) > the spec's `servers[0]` default 9100, verified with a `/health`
probe — an unreachable engine is an explicit screen, never a silent failure.
Dynamic discovery is Plan E (Track 2).

## Keys

- `esc` quit · `a` accept the staged candidate (diff preview)
- chat input `enter` submits a turn (`POST /turn`, SSE)
- model `<select>` starts the chosen model and — once it reports up — stops
  the previous one (serving-control.feature "TUI switches models")
- mode `<select>` picks the turn's mode

## Codegen notes (recorded, not silent)

- openapi-ts is pinned to **0.62.0**: v0.99.0's `sdk` plugin references an
  unpublished `@hey-api/sdk` package. client-fetch is pinned to **0.7.2** —
  the last version whose `Options` carries `client` (removed in 0.7.3), which
  the generated sdk.gen.ts depends on.
- `/turn` is `x-ogen-raw-response`; the generated `startTurn` is not used for
  streaming. `sse.ts` reads the stream and decodes each payload with the
  generated zod schema keyed by the SSE `event:` name.
- The SSE vocabulary was amended to the committed wire (see ADR-0017 §6
  amendment note): payloads carry no `turnId` (one turn per connection) and
  the vocabulary gained `rag`.
