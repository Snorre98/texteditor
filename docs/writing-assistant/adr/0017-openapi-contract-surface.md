# ADR-0017: The OpenAPI contract surface — endpoints, SSE, and codegen

Status: Accepted

Supersedes: ADR-0003 (codegen tool selection).

Note: the `/conversations` session endpoints listed below are renamed to
`/sessions` by ADR-0026 (the endpoint *surface* is unchanged; the resource name
and backing store change from `ConversationStore` to `SessionStore`).

## Context

The "single OpenAPI/JSON Schema spec is the contract every client codegens from"
(ADR-0003) never enumerated its endpoints or event shapes — only the SSE event
names existed (ADR-0012). Every client (Go server, TS TUI, Rust/Tauri) must be
generated from one spec, and that spec must be written *before* the server. Three
forces shape the decisions here:

1. Streaming is the primary interaction (every turn streams typed events), so the
   Go codegen tool must model SSE, not fight it.
2. The client stack is deliberately heavier this session (Zod chosen, openapi-to-rust
   locked), which raises the bar for spec tool-agnosticism.
3. The lifecycle verbs (ADR-0007), document operations (ADR-0016 §9), and the turn
   stream must all project into typed routes.

## Decision

### 1. Go codegen — `ogen`

`ogen` for the Go server. It generates typed streaming/SSE handlers and typed
event structs from the spec, so the turn stream's SSE events are codegen'd, not
hand-parsed. This is the "ogen if SSE" branch ADR-0003 left open; streaming is now
confirmed as the core interaction.

### 2. TS client — Hey API + Zod

The TS TUI is generated with **Hey API + Zod**: runtime validation of every
response and SSE event, not types-only. Rationale: the TUI targets include
`web`/LAN (ADR-0014), where the client is *not* on trusted localhost — boundary
validation is defense-in-depth there, not redundant. ADR-0003's "types-only
default" is overridden.

### 3. Rust client — `openapi-to-rust`, locked now

`openapi-to-rust` is decided now (not deferred to the Tauri phase). Consequence:
the spec models streaming in **standard OpenAPI** — component schemas plus
`text/event-stream` media type — with **no ogen-only extensions**, so all three
tools (ogen, Hey API, openapi-to-rust) consume the same spec.

### 4. Endpoint surface

| Method | Path | Backs | Notes |
|---|---|---|---|
| GET | `/health` | API server | liveness; also advertises the actual bound `baseUrl` (Track 2, ADR-0021 §1) |
| GET | `/models` | Fleet.ListModels | + live state |
| POST | `/models/{name}/start` | Fleet.Start | blocking; typed error on timeout/port/binary |
| POST | `/models/{name}/stop` | Fleet.Stop | idempotent |
| POST | `/models/{name}/provision` | Fleet.Provision | 202 Accepted → poll status |
| GET | `/models/{name}/status` | Fleet.Status | typed LiveState |
| GET | `/modes` | Mode.List | |
| GET | `/tools` | ToolRegistry.List | |
| GET | `/documents/{id}/blocks` | DocumentStore.Blocks | block tree |
| POST | `/documents/{id}/edits` | DocumentStore.ApplyEdit | model edit → newRevision |
| POST | `/documents/{id}/commits` | DocumentStore.Commit | accept; message auto-derived |
| GET | `/documents/{id}/history` | DocumentStore.History | revisions |
| GET | `/documents/{id}/diff` | DocumentStore.Diff | word edits |
| GET | `/documents/{id}/blocks/{bid}/candidates` | DocumentStore.Candidates | |
| PUT | `/documents/{id}/tree` | DocumentStore.SaveTree | manual autosave snapshot (ADR-0038) |
| POST | `/turn` → **SSE** | Loop.Run | the core interaction; returns turnID |
| GET | `/sessions` | SessionStore.ListByDocument (per document) | supersedes `/conversations` (ADR-0026) |
| POST | `/sessions` | SessionStore.Create | create-or-resume a session |
| GET | `/sessions/{id}/messages` | SessionStore.History | supersedes `/conversations/{id}/messages` |
| GET | `/sessions/{id}/meter` | (deferred) | historical per-session meter read-back — deferred |

`POST /documents {path} → {id, blocks}` opens/creates a document and returns its
**surrogate id**. All later routes use the opaque `id`.

*Amendment (Track 1.5 — recorded):* ADR-0035 adds `GET /directories?path=`
(→ `Workspace.List`, one-level listing) and ADR-0036 adds `Task.mentions` and
the `mentions` meter component. The spec amendments land in `api/openapi.yaml`
at implementation, in lockstep with codegen regeneration.

*Amendment (Track 2 — recorded):* ADR-0038 adds `PUT /documents/{id}/tree`
(→ `DocumentStore.SaveTree`) — the manual-edit autosave route, with the
`SaveTreeRequest`/`BlockWrite` schemas. Recorded contract amendment; the spec
change lands in `api/openapi.yaml` at implementation in lockstep with the three
codegens.

### 5. Lifecycle verbs project as resource-oriented routes

`POST /models/{name}/start|stop|provision` + `GET /models/{name}/status`. The CLI
verb contract (ADR-0007) stays the engine→`serve.sh` (later daemon, ADR-0025)
boundary; REST is its typed client projection. Rejected the single
`/serving/commands {verb, name}` endpoint (loses per-verb typed responses; status
must be POST).

### 6. SSE event contract (turnID-tagged)

```
event: token      data: {"text":"…"}
event: meter      data: {"system":…,"tools":…,"rag":…,"history":…,"user":…,"thinking":…,"thinkingApprox":…,"completion":…}
event: candidate  data: {"ok":true,"blockId":"…","revision":{…}}
event: diff       data: <word edits or the diff tool's structured result>
event: rag        data: {"ok":true,"chunks":[{"blockId":"…","text":"…",…}]}
event: done       data: {"degraded":false,"usedModel":"…"}
event: error      data: {"code":"…","message":"…"}
event: backpressure data: {}
```

**Framing:** each SSE message is `event: <type>` + `data: <payload>` (the type
is the SSE event name; the payload is the JSON object). `turnID` is **not**
repeated in payloads: the API server demultiplexes one turn's stream to exactly
one client connection (ADR-0016 §3/§11), so correlation is per-connection. The
component schemas in `api/openapi.yaml` model the *payloads* above, not an
envelope. The `done` event folds in the degradation label (`degraded`,
`usedModel`) so the substitution is never silent (Q3, ADR-0022).

*Amendment (Track 1, TUI session — recorded):* this section originally showed
an envelope (`{turnId, type, data}`) with `promptTokens`/`completionTokens`
meter fields that the hand-framed server (ADR-0031) never emitted. It is
rewritten to the committed wire above, and the vocabulary gains `rag` (loop →
`retrieve`/`read_note` observation) so the ADR-0013 RAG-results panel has a
stream data source. `interface.md §11` and `api/openapi.yaml` were amended
in lockstep.

*Amendment (Track 2, E1 session — recorded):* the `Health` schema gains an
optional `baseUrl` field — the engine's actual bound base URL — so a
dynamic-port client can discover the engine rather than assume a port
(ADR-0021 §1). In fixed mode it equals the configured address; in dynamic mode
it is the OS-assigned port. `servers[0]` remains the *fixed-mode* default.

*Amendment (Track 1.5 — recorded):* the meter payload gains a required
`mentions` field — the attributed tokens of the turn's file mentions
(ADR-0036), summing into the scaled totals like every other component.

## Consequences

- **+** One spec, three clients, no drift; typed SSE handlers in Go; runtime
  validation in TS.
- **+** Streaming is a first-class, tool-agnostic OpenAPI concern — no ogen-only
  extensions to lock out the Rust client later.
- **−** Zod validation adds TS build+runtime work; openapi-to-rust is committed
  before the Tauri phase, so its streaming conventions must reconcile with ogen's
  at spec-authoring time.
- **−** The turn stream must be modeled as `text/event-stream` with an event
  vocabulary codified as schema components (a spec-authoring discipline, not a
  single codegen feature).

## Alternatives considered

- **oapi-codegen** — rejected for the core: models `text/event-stream` as a raw
  body, leaving SSE handlers hand-written (fights the system's primary interaction).
- **Types-only TS (no Zod)** — rejected: client runs on LAN where boundary
  validation is not redundant.
- **Defer Rust codegen** — rejected: the spec must be authored tool-agnostically
  from day one; deferring invites ogen-specific modeling that the Rust tool later
  can't consume.
- **Single `/serving/commands` endpoint** — rejected: loses typed per-verb responses.
