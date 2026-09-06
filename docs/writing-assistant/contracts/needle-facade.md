<!-- MIRROR — canonical: macos-dev-config/docs/contracts/needle-facade.md (ADR-0033 §3). Edit the canonical copy and re-sync; a test in internal/routergate checks drift when the sibling repo is present. -->
# Needle facade contract

> **Canonical copy.** This file lives in `macos-dev-config` (the machine-local
> LLM control plane, ADR-0033). `texteditor` keeps a mirror at
> `texteditor/docs/writing-assistant/contracts/needle-facade.md` with a sync
> check; edit here, not there.

The Needle router facade is the thin OpenAI-compatibility shim that serves the
Needle 2 tool-calling specialist (a `.cact` artifact on its own C++ engine) to
the texteditor engine's `ToolDecider`. It exists so the engine's `Provider` can
carry the router call **with zero Provider change** (ADR-0028 §7): the decider
issues a single non-streaming `POST /v1/chat/completions`; the facade shells the
needle binary and maps its stdout to the ADR-0028 §7/amendment-3 confidence
channel. Needle never enters the engine binary (ADR-0003); the C++ engine and
`.cact` live only behind this facade, in `macos-dev-config`.

Served as a **`delegate`** runner (ADR-0018 §3, ADR-0030): the manifest names
`serve-needle.sh` as the wrapper; the daemon's `start`/`stop`/`status` verbs thin
out to that wrapper, which in turn runs the facade binary (`cmd/serve-needle`).

## 1. Transport

- `POST /v1/chat/completions` — non-streaming only. One small response.
- `GET /health` — liveness; returns `200` when the facade is up (backs the
  daemon's `status` verb and the 60s start health-wait).
- All responses JSON, `Content-Type: application/json`.
- Request shape: a standard OpenAI `chat.completions` request. The facade reads
  only `messages[].content` (the decider's assembled router prompt) to extract
  the free-text `intent`; the `model` field is accepted and ignored (the `.cact`
  is fixed by `NEEDLE_CACT`).

## 2. The needle-stdout assumption (single ML-touch-point)

The facade's needle-*stdout* parser is the **only** place that depends on the
real `.cact` output format. Until the real format is known, this contract pins:

- A **confident** decode is three lines on stdout:

  ```
  <tool-name>
  <json-args>
  <confidence>
  ```

  where `<tool-name>` is a real `ToolDef.Name`, `<json-args>` is a compact JSON
  object (schema-valid arguments for that tool), and `<confidence>` is a float
  in `[0,1]`.

- A **refusal** / empty-call is **empty stdout** (a non-zero exit may accompany
  "empty call").

If the real `.cact` output format differs, **only the facade's stdout parser and
this document change — nothing in the engine.** This is the one assumption to
finalize at ML time.

## 3. Completion mapping (ADR-0028 §7 / amendment-3)

| Needle output | Facade response |
|---|---|
| confident (`name`/`args`/`confidence`) | `choices[0].finish_reason == "tool"`, `message.content == {"name","args","confidence"}` (compact JSON) |
| refusal / empty stdout | empty completion: `choices[0].message.content == ""`, `finish_reason == "stop"` |
| needle binary missing / spawn failure | HTTP 5xx (the decider's `Decide` surfaces it as an error → `router-unreachable` in the loop) |

The confidence threshold τ is **not applied here** — τ stays private to the
`ToolDecider` (ADR-0028 amendment-4). The facade reports the raw confidence; the
decider compares it against τ internally.

Usage counts (`prompt_tokens`/`completion_tokens`, `finish_reason`, and the
`"tool"` reason) are the standard OpenAI chat/completion fields, so the engine's
`Provider.Chat` parses them untouched (it reads `Completion.FinishReason`,
ADR-0028 §7/`interface.md §2`).

## 4. Environment

- `NEEDLE_BIN` — path to the needle C++ binary (default: `needle` on PATH).
- `NEEDLE_CACT` — path to the `.cact` artifact (default lives under
  `DEV_MODELS_SSD_BASE/needle/`).
- `NEEDLE_HOST` / `NEEDLE_PORT` — facade bind (default `127.0.0.1:8081`, driven
  by the manifest `daemons.needle` entry via the daemon + `serve-needle.sh`).
