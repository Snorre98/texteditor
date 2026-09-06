import type { Api } from "../src/api/client";
import type {
  Block,
  Document,
  Mode,
  Model,
  Revision,
  Session,
  ToolDef,
} from "../src/generated/types.gen";
import type { streamTurn } from "../src/api/sse";

// Shared test doubles: a stub of the generated api (all methods return canned
// {data, error} results) and a recorded fake /turn stream.

export type CallResult<T> =
  | { data: T; error?: undefined }
  | { data?: undefined; error: unknown };

export function ok<T>(data: T): CallResult<T> {
  return { data };
}

export function fail(error: string): { data?: undefined; error: unknown } {
  return { error: new Error(error) };
}

export interface StubApiOptions {
  startResult?: (name: string) => CallResult<unknown> | Promise<CallResult<unknown>>;
  stopResult?: (name: string) => CallResult<unknown> | Promise<CallResult<unknown>>;
  getCandidatesResult?: () => CallResult<{ blockId?: string; text?: string }[]>;
}

export function stubApi(opts: StubApiOptions = {}) {
  const calls: string[] = [];
  const api = {
    health: async () => ok({ status: "ok" as const }),
    listModels: async () => {
      calls.push("listModels");
      return ok([
        { name: "gemma4-12b", baseUrl: "http://127.0.0.1:8087/v1", liveState: "down" },
        { name: "gemma4-26b", baseUrl: "http://127.0.0.1:8089/v1", liveState: "up" },
      ] satisfies Model[]);
    },
    startModel: async (name: string) => {
      calls.push(`start:${name}`);
      const r = opts.startResult?.(name) ?? ok({ name, state: "up" });
      return r;
    },
    stopModel: async (name: string) => {
      calls.push(`stop:${name}`);
      return opts.stopResult?.(name) ?? ok({ name, state: "down" });
    },
    provisionModel: async () => ok({ provisionID: "p1" }),
    getModelStatus: async (name: string) => ok({ name, state: "up" }),
    listModes: async () => ok([{ name: "proofreader" } satisfies Mode]),
    listTools: async () => ok([{ name: "edit_markdown" } satisfies ToolDef]),
    openDocument: async (path: string) => {
      calls.push(`open:${path}`);
      return ok({ id: "d1", path, rootBlockId: "b1" } satisfies Document);
    },
    getBlocks: async () =>
      ok([{ id: "b1", kind: "paragraph", position: 0, text: "hello" } satisfies Block]),
    applyEdit: async () => {
      calls.push("applyEdit");
      return ok({ id: "r1", message: "candidate" } satisfies Revision);
    },
    commitDocument: async () => {
      calls.push("commit");
      return ok({ id: "r2", message: "accepted" } satisfies Revision);
    },
    getHistory: async () => ok([] satisfies Revision[]),
    getDiff: async () => ok([]),
    getCandidates: async () =>
      opts.getCandidatesResult?.() ?? ok([{ blockId: "b1", text: "edited" }]),
    listSessions: async () => {
      calls.push("listSessions");
      return ok([{ id: "s1", documentId: "d1" } satisfies Session]);
    },
    createSession: async () => ok({ id: "s1", documentId: "d1" } satisfies Session),
    getSessionMessages: async () => {
      calls.push("messages");
      return ok([]);
    },
  };
  return { api: api as unknown as Api, calls };
}

// A recorded fake stream that replays a canned event sequence on demand.
export function fakeStream(events: { name: string; payload: unknown }[]) {
  const tasks: unknown[] = [];
  const run: typeof streamTurn = async (_baseUrl, task, handler) => {
    tasks.push(task);
    for (const ev of events) {
      handler.onEvent(ev.name as Parameters<typeof handler.onEvent>[0], ev.payload as never);
    }
  };
  return { run, tasks };
}
