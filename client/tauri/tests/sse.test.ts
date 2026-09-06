import { describe, expect, test } from "bun:test";
import { parseSseMessage, streamTurn, type FetchLike, type SseEventName } from "../src/api/sse";
import type { Task } from "../src/generated/types.gen";

// The wire fixtures below are byte-exact copies of the API server's framing
// (internal/apiserver apiserver_test.go — `event: token` + `data: {...}`),
// plus the emitter payloads locked by the amended ADR-0017 §6. Ported verbatim
// from client/tui/tests/sse.test.ts (ADR-0037 §4).

describe("parseSseMessage", () => {
  test("decodes the hand-framed message block", () => {
    const msg = parseSseMessage('event: token\ndata: {"text":"hi"}');
    expect(msg).toEqual({ event: "token", data: '{"text":"hi"}' });
  });

  test("joins multi-line data payloads", () => {
    const msg = parseSseMessage('event: diff\ndata: {"ok":true,\ndata: "edits":[]}');
    expect(msg?.event).toBe("diff");
    expect(msg?.data).toContain('"edits"');
  });

  test("returns null for a block without an event line", () => {
    expect(parseSseMessage('data: {"text":"orphan"}')).toBeNull();
  });
});

describe("streamTurn", () => {
  const task: Task = {
    sessionId: "s1",
    modeName: "proofreader",
    documentId: "d1",
    userInput: "fix",
  };

  function streamingFetch(blocks: string[]): FetchLike {
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        for (const block of blocks) {
          controller.enqueue(new TextEncoder().encode(block + "\n\n"));
        }
        controller.close();
      },
    });
    return (async () =>
      new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } })) as FetchLike;
  }

  test("dispatches typed, zod-validated events until done", async () => {
    const seen: { name: string; payload: unknown }[] = [];
    const blocks = [
      'event: token\ndata: {"text":"hi"}',
      'event: token\ndata: {"text":" there"}',
      'event: meter\ndata: {"system":10,"tools":0,"rag":4,"history":2,"mentions":0,"user":4,"thinking":0,"thinkingApprox":false,"completion":5}',
      'event: done\ndata: {"degraded":false,"usedModel":"gemma4-12b"}',
      // Never dispatched: the stream ended at done.
      'event: token\ndata: {"text":"leak"}',
    ];
    await streamTurn("http://x", task, { onEvent: (name, payload) => seen.push({ name, payload }) }, { fetchImpl: streamingFetch(blocks) });

    expect(seen.map((e) => e.name)).toEqual(["token", "token", "meter", "done"]);
    expect(seen[2]?.payload).toMatchObject({ completion: 5, thinkingApprox: false });
    expect(seen[3]?.payload).toEqual({ degraded: false, usedModel: "gemma4-12b" });
  });

  test("validates a rag event against the generated schema", async () => {
    const events: [SseEventName, unknown][] = [];
    const blocks = [
      'event: rag\ndata: {"ok":true,"chunks":[{"blockId":"b9","text":"cited","score":0.8,"source":"n1"}]}',
      'event: done\ndata: {}',
    ];
    await streamTurn("http://x", task, { onEvent: (name, payload) => events.push([name, payload]) }, { fetchImpl: streamingFetch(blocks) });

    expect(events[0]?.[0]).toBe("rag");
    expect(events[0]?.[1]).toMatchObject({ ok: true });
  });

  test("labels a payload that fails zod validation instead of dispatching it", async () => {
    const errors: string[] = [];
    const dispatched: SseEventName[] = [];
    const blocks = [
      'event: meter\ndata: {"system":"not-a-number","completion":5}',
      'event: done\ndata: {}',
    ];
    await streamTurn(
      "http://x",
      task,
      {
        onEvent: (name) => dispatched.push(name),
        onValidationError: (name) => errors.push(name ?? ""),
      },
      { fetchImpl: streamingFetch(blocks) },
    );
    expect(errors).toEqual(["meter"]);
    expect(dispatched).toEqual(["done"]);
  });

  test("labels an unknown event name", async () => {
    const errors: (string | undefined)[] = [];
    const blocks = ['event: unknown_event\ndata: {}', 'event: done\ndata: {}'];
    await streamTurn(
      "http://x",
      task,
      { onEvent: () => {}, onValidationError: (name) => errors.push(name) },
      { fetchImpl: streamingFetch(blocks) },
    );
    expect(errors).toEqual([undefined]);
  });

  test("throws on a non-200 stream response", async () => {
    const badFetch = (async () => new Response("nope", { status: 500 })) as FetchLike;
    await expect(streamTurn("http://x", task, { onEvent: () => {} }, { fetchImpl: badFetch })).rejects.toThrow(/HTTP 500/);
  });
});
