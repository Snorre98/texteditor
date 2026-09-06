// The `/turn` SSE stream reader (ADR-0031 §4: clients codegen typed SSE
// decoders). The generated SDK models /turn as a plain POST (the route is
// x-ogen-raw-response, so no codegen tool can model its stream body); the
// typed part of the reader is the generated zod schemas keyed by the SSE
// `event:` name — the vocabulary locked in ADR-0017 §6 (amended: payloads are
// the wire shape, one turn per connection, no turnId repeated).
//
// The wire (hand-framed by the API server, ADR-0031) is:
//
//   event: <type>\n
//   data: <json payload>\n
//   \n
//
// Terminal events are `done` (success) and `error` (failure); the server then
// closes the stream. `backpressure` labels a dropped-buffer overflow
// (concurrency-topology.md §3) and is not terminal.
//
// EventSource cannot be used here: the route is POST with a JSON body, so the
// reader is a fetch + ReadableStream text decoder.
import { z, type ZodType } from "zod";
import type { Task } from "../generated/types.gen";
import {
  zBackpressureEvent,
  zCandidateEvent,
  zDiffEvent,
  zDoneEvent,
  zErrorEvent,
  zEvent,
  zMeterEvent,
  zRagEvent,
  zTokenEvent,
} from "../generated/zod.gen";

export const SSE_EVENT_NAMES = [
  "token",
  "meter",
  "candidate",
  "diff",
  "rag",
  "done",
  "error",
  "backpressure",
] as const;

export type SseEventName = (typeof SSE_EVENT_NAMES)[number];

export type SsePayload<T extends SseEventName> = z.infer<
  (typeof PAYLOAD_SCHEMAS)[T]
>;

// The generated zod schema per SSE event name — the event vocabulary is the
// spec's `Event.type` enum (zEvent), each payload its component schema.
export const PAYLOAD_SCHEMAS: Record<SseEventName, ZodType<unknown>> = {
  token: zTokenEvent,
  meter: zMeterEvent,
  candidate: zCandidateEvent,
  diff: zDiffEvent,
  rag: zRagEvent,
  done: zDoneEvent,
  error: zErrorEvent,
  backpressure: zBackpressureEvent,
};

export const TERMINAL_EVENTS: ReadonlySet<SseEventName> = new Set([
  "done",
  "error",
]);

export interface TurnStreamHandler {
  /** One typed, zod-validated event. The payload type follows `name`. */
  onEvent: <T extends SseEventName>(name: T, payload: SsePayload<T>) => void;
  /**
   * A payload failed boundary validation (ADR-0017 §2) or the framing was
   * malformed. The stream continues — a labeled rejection, never silent.
   */
  onValidationError?: (name: string | undefined, error: Error) => void;
}

export interface TurnStreamOptions {
  signal?: AbortSignal;
  fetchImpl?: FetchLike;
}

export type FetchLike = (
  input: string | URL | Request,
  init?: RequestInit,
) => Promise<Response>;

export interface RawSseDispatch {
  terminal: boolean;
}

// streamTurn POSTs one agent turn and consumes its SSE stream until `done` /
// `error` / disconnect. It never parses engine internals: only the generated
// zod schemas above.
export async function streamTurn(
  baseUrl: string,
  task: Task,
  handler: TurnStreamHandler,
  options: TurnStreamOptions = {},
): Promise<void> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const res = await fetchImpl(`${baseUrl}/turn`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "text/event-stream",
    },
    body: JSON.stringify(task),
    signal: options.signal,
  });
  if (!res.ok || !res.body) {
    throw new Error(`/turn stream failed: HTTP ${res.status}`);
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  const flush = (block: string): RawSseDispatch => {
    const message = parseSseMessage(block);
    if (!message) return { terminal: false };
    return dispatch(message, handler);
  };

  try {
    let terminal = false;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let sep: number;
      while ((sep = buffer.indexOf("\n\n")) !== -1) {
        const block = buffer.slice(0, sep);
        buffer = buffer.slice(sep + 2);
        terminal = flush(block).terminal;
      }
      if (terminal) break;
    }
    const tail = decoder.decode();
    if (tail) {
      buffer += tail;
      if (buffer.trim() !== "" && !terminal) flush(buffer);
    }
  } finally {
    reader.releaseLock();
  }
}

interface RawSseMessage {
  event: string;
  data: string;
}

// parseSseMessage decodes one `event:` + `data:` message block (the framing
// the API server writes — apiserver.go writeSSE). Returns null for empty or
// comment-only blocks.
export function parseSseMessage(block: string): RawSseMessage | null {
  let event = "";
  const dataLines: string[] = [];
  for (const line of block.split("\n")) {
    if (line.startsWith(":")) continue;
    if (line.startsWith("event:")) {
      event = line.slice("event:".length).trim();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice("data:".length).replace(/^\s/, ""));
    }
  }
  if (event === "") return null;
  return { event, data: dataLines.length > 0 ? dataLines.join("\n") : "{}" };
}

function dispatch(message: RawSseMessage, handler: TurnStreamHandler): RawSseDispatch {
  const name = message.event;
  if (!isSseEventName(name)) {
    handler.onValidationError?.(
      undefined,
      new Error(`unknown SSE event ${JSON.stringify(name)}`),
    );
    return { terminal: false };
  }
  let payload: unknown;
  try {
    payload = JSON.parse(message.data);
  } catch (cause) {
    handler.onValidationError?.(
      name,
      new Error(`invalid JSON in ${name} event: ${String(cause)}`),
    );
    return { terminal: false };
  }
  const schema = PAYLOAD_SCHEMAS[name];
  const parsed = schema.safeParse(payload);
  if (!parsed.success) {
    handler.onValidationError?.(name, parsed.error);
    return { terminal: false };
  }
  handler.onEvent(name, parsed.data as never);
  return { terminal: TERMINAL_EVENTS.has(name) };
}

export function isSseEventName(name: string): name is SseEventName {
  return (zEvent.shape.type.options as readonly string[]).includes(name);
}
