import { describe, expect, test } from "bun:test";
import { createAppStore, ZERO_TALLY, type SubmitTurnInput } from "../src/state/store";
import type { MeterEvent } from "../src/generated/types.gen";
import { fakeStream, fail, ok, stubApi } from "./helpers";

const METER: MeterEvent = {
  system: 10,
  tools: 0,
  rag: 4,
  history: 2,
  mentions: 0,
  user: 4,
  thinking: 0,
  thinkingApprox: false,
  completion: 5,
};

const turnOf = (store: ReturnType<typeof createAppStore>, id: string) =>
  store.state.sessionStates[id]?.turn;

// --------------------------- tests ---------------------------

describe("createAppStore", () => {
  test("refreshFleet populates models, modes, and tools", async () => {
    const { api } = stubApi();
    const store = createAppStore({ api, baseUrl: "http://x" });
    await store.refreshFleet();

    const s = store.state;
    expect(s.models.map((m) => m.name)).toEqual(["gemma4-12b", "gemma4-26b"]);
    expect(s.modes.map((m) => m.name)).toEqual(["proofreader"]);
    expect(s.tools.map((t) => t.name)).toEqual(["edit_markdown"]);
  });

  test("openDocument loads the document, block tree, and history and resets sessions", async () => {
    const { api, calls } = stubApi();
    const store = createAppStore({ api, baseUrl: "http://x" });
    await store.openDocument("/notes/thesis.md");

    const s = store.state;
    expect(calls).toContain("open:/notes/thesis.md");
    expect(s.document?.id).toBe("d1");
    expect(s.blocks[0]?.text).toBe("hello");
    expect(Object.keys(s.sessionStates)).toEqual([]);
  });

  test("submitTurn streams into the session-keyed turn slice", async () => {
    const { api } = stubApi();
    const events = [
      { name: "token", payload: { text: "Fix " } },
      { name: "token", payload: { text: "the verb." } },
      { name: "meter", payload: METER },
      { name: "candidate", payload: { ok: true, blockId: "b1" } },
      { name: "rag", payload: { ok: true, chunks: [{ blockId: "b9", text: "cited" }] } },
      { name: "done", payload: { degraded: false, usedModel: "gemma4-12b" } },
    ];
    const stream = fakeStream(events);
    const store = createAppStore({ api, baseUrl: "http://x", stream: stream.run });

    const input: SubmitTurnInput = {
      sessionId: "s1",
      modeName: "proofreader",
      documentId: "d1",
      userInput: "fix",
    };
    await store.submitTurn(input);

    const t = turnOf(store, "s1")!;
    expect(stream.tasks).toHaveLength(1);
    expect(t.tokens).toBe("Fix the verb.");
    expect(t.meter).toEqual(METER);
    expect(t.candidate).toEqual({ ok: true, blockId: "b1" });
    expect(t.rag?.chunks?.[0]?.text).toBe("cited");
    expect(t.done).toEqual({ degraded: false, usedModel: "gemma4-12b" });
    expect(t.active).toBe(false);
  });

  test("two sessions stream concurrently into isolated slices (ADR-0026 §4)", async () => {
    const { api } = stubApi();
    const streamA = fakeStream([
      { name: "token", payload: { text: "AAA" } },
      { name: "done", payload: {} },
    ]);
    const streamB = fakeStream([
      { name: "token", payload: { text: "BBB" } },
      { name: "done", payload: {} },
    ]);
    // Each session gets its own stream; a real engine would demultiplex by turn.
    const store = createAppStore({ api, baseUrl: "http://x", stream: streamA.run });

    await store.submitTurn({ sessionId: "sa", modeName: "p", documentId: "d1", userInput: "a" });
    // Swap the stream for the second session (simulating two live bubbles).
    const storeB = createAppStore({ api, baseUrl: "http://x", stream: streamB.run });
    await storeB.submitTurn({ sessionId: "sb", modeName: "p", documentId: "d1", userInput: "b" });

    expect(turnOf(store, "sa")!.tokens).toBe("AAA");
    expect(turnOf(storeB, "sb")!.tokens).toBe("BBB");
    // A session's slice does not exist until that session streams.
    expect(store.state.sessionStates["sb"]).toBeUndefined();
  });

  test("the meter cumulative tally is per-session and sums across turns", async () => {
    const { api } = stubApi();
    const stream = fakeStream([
      { name: "meter", payload: METER },
      { name: "done", payload: {} },
    ]);
    const store = createAppStore({ api, baseUrl: "http://x", stream: stream.run });
    const input: SubmitTurnInput = {
      sessionId: "s1",
      modeName: "proofreader",
      documentId: "d1",
      userInput: "fix",
    };

    await store.submitTurn(input);
    expect(turnOf(store, "s1")!.cumulative).toEqual({
      ...ZERO_TALLY,
      system: 10,
      rag: 4,
      history: 2,
      user: 4,
      completion: 5,
    });

    await store.submitTurn(input);
    expect(turnOf(store, "s1")!.cumulative.completion).toBe(10);
    expect(turnOf(store, "s1")!.cumulative.system).toBe(20);
  });

  test("a turn error event lands in the session's turn.error and deactivates it", async () => {
    const { api } = stubApi();
    const stream = fakeStream([
      { name: "error", payload: { code: "provider-unreachable", message: "down" } },
    ]);
    const store = createAppStore({ api, baseUrl: "http://x", stream: stream.run });
    await store.submitTurn({
      sessionId: "s1",
      modeName: "proofreader",
      documentId: "d1",
      userInput: "fix",
    });

    const t = turnOf(store, "s1")!;
    expect(t.error).toEqual({ code: "provider-unreachable", message: "down" });
    expect(t.active).toBe(false);
  });

  test("createSession is create-or-resume and anchors to a block", async () => {
    const { api, calls } = stubApi();
    const store = createAppStore({ api, baseUrl: "http://x" });
    await store.openDocument("/notes/thesis.md");
    const id = await store.createSession("b1", "proofreader");

    expect(id).toBe("s1");
    expect(store.state.sessions[0]?.anchorBlockId).toBe("b1");
    expect(store.state.sessionStates["s1"]).toBeDefined();
  });

  test("acceptCandidate fetches the staged candidate, then applies and commits", async () => {
    const { api, calls } = stubApi();
    const store = createAppStore({ api, baseUrl: "http://x" });
    await store.openDocument("/notes/thesis.md");
    await store.acceptCandidate("b1");

    expect(calls).toContain("applyEdit");
    expect(calls).toContain("commit");
  });

  test("saveTree sends the whole-tree snapshot (ADR-0038)", async () => {
    const { api, calls } = stubApi();
    const store = createAppStore({ api, baseUrl: "http://x" });
    await store.openDocument("/notes/thesis.md");
    await store.saveTree([
      { kind: "paragraph", text: "typed" },
      { id: "b1", kind: "paragraph", text: "kept" },
    ]);

    expect(calls).toContain("save:2");
  });

  // serving-control.feature — "The TUI switches models by starting and
  // stopping servers": the Fleet gateway calls Start(new) and Stop(old); the
  // completion is not issued until the new server reports up; a failed start
  // surfaces as an error, leaving the old model running.
  describe("switchModel (serving-control.feature 'TUI switches models')", () => {
    test("starts the new model and only then stops the old one", async () => {
      const order: string[] = [];
      const { api } = stubApi({
        startResult: async (name) => {
          order.push(`start:${name}`);
          return ok({ name, state: "up" });
        },
        stopResult: async (name) => {
          order.push(`stop:${name}`);
          return ok({ name, state: "down" });
        },
      });
      const store = createAppStore({ api, baseUrl: "http://x" });
      await store.switchModel("gemma4-12b", "gemma4-26b");

      expect(order).toEqual(["start:gemma4-26b", "stop:gemma4-12b"]);
      expect(store.state.connection.error).toBeNull();
      expect(store.state.switching).toBeNull();
    });

    test("a failed start leaves the old model running and surfaces an error", async () => {
      const { api, calls } = stubApi({
        startResult: () => fail("lanes-conflict"),
      });
      const store = createAppStore({ api, baseUrl: "http://x" });
      await store.switchModel("gemma4-12b", "gemma4-26b");

      expect(calls).not.toContain("stop:gemma4-12b");
      expect(store.state.connection.error).toContain("gemma4-12b left running");
      expect(store.state.switching).toBeNull();
    });

    test("switching to the same model is a no-op", async () => {
      const { api, calls } = stubApi();
      const store = createAppStore({ api, baseUrl: "http://x" });
      await store.switchModel("gemma4-12b", "gemma4-12b");
      expect(calls).toEqual([]);
    });
  });
});
