import { describe, expect, test } from "bun:test";
import { createAppStore, ZERO_TALLY, type SubmitTurnInput } from "../src/state/store";
import type { MeterEvent } from "../src/generated/types.gen";
import { fakeStream, fail, ok, stubApi } from "./helpers";

const METER: MeterEvent = {
  system: 10,
  tools: 0,
  rag: 4,
  history: 2,
  user: 4,
  thinking: 0,
  thinkingApprox: false,
  completion: 5,
};

// --------------------------- tests ---------------------------

describe("createAppStore", () => {
  test("refreshFleet populates models, modes, and tools", async () => {
    const { api } = stubApi();
    const store = createAppStore({ api, baseUrl: "http://x" });
    await store.refreshFleet();

    const s = store.state();
    expect(s.models.map((m) => m.name)).toEqual(["gemma4-12b", "gemma4-26b"]);
    expect(s.modes.map((m) => m.name)).toEqual(["proofreader"]);
    expect(s.tools.map((t) => t.name)).toEqual(["edit_markdown"]);
  });

  test("openDocument loads the document, block tree, and history", async () => {
    const { api, calls } = stubApi();
    const store = createAppStore({ api, baseUrl: "http://x" });
    await store.openDocument("/notes/thesis.md");

    const s = store.state();
    expect(calls).toContain("open:/notes/thesis.md");
    expect(s.document?.id).toBe("d1");
    expect(s.blocks[0]?.text).toBe("hello");
  });

  test("submitTurn streams tokens/meter/candidate/rag/done into signals", async () => {
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

    const s = store.state();
    expect(stream.tasks).toHaveLength(1);
    expect(s.turn.tokens).toBe("Fix the verb.");
    expect(s.turn.meter).toEqual(METER);
    expect(s.turn.candidate).toEqual({ ok: true, blockId: "b1" });
    expect(s.turn.rag?.chunks?.[0]?.text).toBe("cited");
    expect(s.turn.done).toEqual({ degraded: false, usedModel: "gemma4-12b" });
    expect(s.turn.active).toBe(false);
  });

  test("the meter cumulative tally sums across turns", async () => {
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
    expect(store.state().turn.cumulative.completion).toBe(5);
    expect(store.state().turn.cumulative).toEqual({
      ...ZERO_TALLY,
      system: 10,
      rag: 4,
      history: 2,
      user: 4,
      completion: 5,
    });

    await store.submitTurn(input);
    expect(store.state().turn.cumulative.completion).toBe(10);
    expect(store.state().turn.cumulative.system).toBe(20);
  });

  test("a turn error event lands in turn.error and deactivates the turn", async () => {
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

    const s = store.state();
    expect(s.turn.error).toEqual({ code: "provider-unreachable", message: "down" });
    expect(s.turn.active).toBe(false);
  });

  test("acceptCandidate fetches the staged candidate, then applies and commits", async () => {
    const { api, calls } = stubApi();
    const store = createAppStore({ api, baseUrl: "http://x" });
    await store.openDocument("/notes/thesis.md");
    await store.acceptCandidate("b1");

    expect(calls).toContain("applyEdit");
    expect(calls).toContain("commit");
  });

  // serving-control.feature — "The TUI switches models by starting and
  // stopping servers": the Fleet gateway calls Start(new) and Stop(old); the
  // completion is not issued until the new server reports up; a failed start
  // surfaces as an error event, leaving the old model running.
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
      expect(store.state().turn.error).toBeNull();
      expect(store.state().switching).toBeNull();
    });

    test("a failed start leaves the old model running and surfaces an error", async () => {
      const { api, calls } = stubApi({
        startResult: () => fail("lanes-conflict"),
      });
      const store = createAppStore({ api, baseUrl: "http://x" });
      await store.switchModel("gemma4-12b", "gemma4-26b");

      expect(calls).not.toContain("stop:gemma4-12b");
      const err = store.state().turn.error;
      expect(err?.code).toBe("model-switch-failed");
      expect(err?.message).toContain("gemma4-12b left running");
      expect(store.state().switching).toBeNull();
    });

    test("switching to the same model is a no-op", async () => {
      const { api, calls } = stubApi();
      const store = createAppStore({ api, baseUrl: "http://x" });
      await store.switchModel("gemma4-12b", "gemma4-12b");
      expect(calls).toEqual([]);
    });
  });
});
