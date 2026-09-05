# ADR-0031: SSE server transport is hand-framed; ogen scope clarified

Status: Accepted

Clarifies: ADR-0017 §1 (the ogen "typed streaming handlers" rationale) and §3
("no ogen-only extensions").

## Context

ADR-0017 §1 chose `ogen` for the Go server on the ground that it "generates typed
streaming/SSE handlers and typed event structs from the spec, so the turn stream's
SSE events are codegen'd, not hand-parsed." A technical spike (ogen v1.24.0)
showed that claim is only half-true today: ogen generates typed SSE **clients**,
but **server-side** `text/event-stream` response encoding is unimplemented —
generating a server handler for an SSE endpoint fails with
`Feature "sse server response encoding" is not implemented yet` (open issue #1742).

Two observations follow:

1. The hard part of SSE is **server-side state**, not types: stream lifecycle,
   per-event flush, client disconnect, `turnID` correlation/demultiplexing,
   bounded-queue backpressure, and the error-before-headers vs error-after-stream
   split. No OpenAPI codegen tool generates that state machine — `oapi-codegen`
   and `openapi-generator` model `text/event-stream` as a raw body and stop there.
   So the server-side stream was going to be hand-written under any tool; ogen's
   ADR-0017 §1 rationale overstated what it could deliver on the server.
2. ogen's genuine value to this system is (a) the typed, validated **non-streaming
   REST surface** (models, modes, tools, documents, sessions, health) and (b) the
   event-vocabulary component schemas that the **clients** (Hey API + Zod,
   `openapi-to-rust`) codegen from. The Go server already holds typed `Event` DTOs
   in `shared/dto`; it never needed codegen to know event shapes.

## Decision

1. **ogen stays the Go codegen tool**, but its scope is stated honestly: it owns
   the typed non-streaming REST routes (routing, request decode, validation, otel)
   and the OpenAPI event-vocabulary contract. It is **not** relied on for
   server-side SSE framing.
2. **The `/turn` SSE endpoint is hand-framed in the API server.** It is a thin
   `net/http` handler that subscribes to the SSE event bus with a `turnID` filter
   and writes the stream (`Content-Type: text/event-stream`, per-event flush,
   `r.Context().Done()` cancellation, drop + `backpressure` event on a full
   subscriber queue). It consumes the typed `Event` DTO from `shared/dto`; the
   state machine it implements is `contracts/concurrency-topology.md` §2–§3.
3. **`x-ogen-raw-response: true`** marks the `/turn` SSE response in the spec so
   ogen hands the handler the typed decoded request plus the raw
   `http.ResponseWriter` (`RawHandler.StartTurn(ctx, req, w) error`) and does not
   attempt to encode the stream itself. This keeps the route inside ogen's router
   (request decode + middleware) while the handler owns the response.
4. The event vocabulary remains in the spec as component schemas
   (`token`/`meter`/`candidate`/`diff`/`done`/`error`/`backpressure`, each
   `turnId`-tagged) so the TS and Rust clients still codegen typed SSE decoders.
   The `x-ogen-raw-response` extension is additive and ignored by Hey API and
   `openapi-to-rust`, which continue to consume the standard `text/event-stream`
   media type + event schema.

## Consequences

- **+** The record matches the tool: ogen's streaming limitation is acknowledged
  rather than hand-waved, and the server-side SSE state — the actual hard part —
  is owned where it has to be owned, in the API server.
- **+** The contract-first promise is intact: one spec, typed event schemas, typed
  client SSE; the Go server's stream is a thin, testable adapter, not a second
  source of event truth.
- **−** The server-side SSE framing is hand-written code (a bounded state machine
  with the flush/cancel/backpressure concerns enumerated above); it must be
  boundary-tested like any other module.
- **−** `x-ogen-raw-response` is an ogen-only `x-` extension in the spec. It is
  the narrowest ogen-specific surface and is ignored by the other two tools, but
  it technically relaxes ADR-0017 §3's "no ogen-only extensions" discipline to a
  single, server-side, additive marker.

## Alternatives considered

- **Hand-mount `/turn` outside ogen entirely** — rejected: loses ogen's request
  decode and routing for the turn request, and splits the contract surface across
  two routers for no state-machine benefit.
- **Switch Go codegen tools** — rejected: no Go OpenAPI tool generates server-side
  SSE state; the alternatives model `text/event-stream` as a raw body at best.
  Reverses ADR-0017's explicit choice for zero gain on the hard part.
- **Block on ogen server-SSE support (issue #1742)** — rejected: cannot sequence
  the build on an open upstream feature.
- **Reopen the transport (WebSockets / NDJSON-only)** — rejected: out of scope of
  this ADR; ADR-0012's SSE-with-typed-events decision stands, and the client
  codegen (Hey API, `openapi-to-rust`) is built around it.
