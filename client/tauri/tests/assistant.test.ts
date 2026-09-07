import { describe, expect, test } from "bun:test";
import {
  createAssistant,
  errorDisplay,
  hasSelectionFor,
  totalTokens,
  turnStatus,
  turnSummary,
  type SelectionRange,
} from "../src/editor/useAssistant";
import { createAppStore, type TurnState } from "../src/state/store";
import type { MeterEvent } from "../src/generated/types.gen";
import { fakeStream, stubApi } from "./helpers";

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

const DOC = "hello";

function makeAssistant(events: { name: string; payload: unknown }[] = []) {
  const { api } = stubApi();
  const stream = fakeStream(events);
  const store = createAppStore({ api, baseUrl: "http://x", stream: stream.run });
  return {
    store,
    stream,
    run: () =>
      createAssistant(store, {
        getDocText: () => DOC,
        getSelection: (): SelectionRange | null => ({ from: 0, to: 3 }),
      }),
  };
}

describe("hasSelectionFor", () => {
  test("non-empty selection is true", () => {
    expect(hasSelectionFor({ empty: false })).toBe(true);
  });
  test("empty selection is false", () => {
    expect(hasSelectionFor({ empty: true })).toBe(false);
  });
});

describe("errorDisplay", () => {
  test("provisioning-shaped codes get a hint appended", () => {
    expect(errorDisplay({ code: "model-not-found", message: "nope" })).toContain(
      "macos-dev-config/models.json",
    );
    expect(errorDisplay({ code: "no-model-available", message: "x" })).toContain(
      "macos-dev-config/models.json",
    );
    expect(errorDisplay({ code: "daemon-unreachable", message: "x" })).toContain(
      "macos-dev-config/models.json",
    );
  });
  test("unrelated codes pass the message through unchanged", () => {
    expect(errorDisplay({ code: "provider-unreachable", message: "down" })).toBe("down");
  });
});

describe("turn lifecycle helpers", () => {
  const base = (): TurnState => ({
    active: false,
    tokens: "",
    meter: null,
    cumulative: {
      system: 0,
      tools: 0,
      rag: 0,
      history: 0,
      mentions: 0,
      user: 0,
      thinking: 0,
      completion: 0,
    },
    candidate: null,
    diffs: [],
    rag: null,
    done: null,
    error: null,
    backpressure: false,
    steps: [],
  });

  test("turnStatus reflects the turn lifecycle", () => {
    expect(turnStatus(undefined)).toBe("idle");
    expect(turnStatus({ ...base(), active: true })).toBe("working");
    expect(turnStatus({ ...base(), done: {} })).toBe("done");
    expect(turnStatus({ ...base(), error: { code: "x" } })).toBe("error");
    expect(turnStatus(base())).toBe("idle");
  });

  test("turnSummary renders model and degraded state", () => {
    expect(turnSummary(null)).toBe("");
    expect(turnSummary({ usedModel: "gemma4-26b-moe", degraded: false })).toBe(
      "answered with gemma4-26b-moe",
    );
    expect(turnSummary({ usedModel: "gemma4-26b-moe", degraded: true })).toBe(
      "answered with gemma4-26b-moe (degraded)",
    );
    expect(turnSummary({})).toBe("answered");
  });

  test("totalTokens sums the cumulative tally", () => {
    expect(totalTokens(undefined)).toBe(0);
    expect(
      totalTokens({ system: 10, tools: 0, rag: 4, history: 2, mentions: 0, user: 4, thinking: 0, completion: 5 }),
    ).toBe(25);
  });
});

describe("createAssistant", () => {
  test("askAbout anchors to the selected block and submits the selection", async () => {
    const { store, stream, run } = makeAssistant([
      { name: "done", payload: {} },
    ]);
    const assistant = run();
    await store.openDocument("/notes/thesis.md");
    await assistant.askAbout();

    expect(store.state.sessions[0]?.anchorBlockId).toBe("b1");
    expect(stream.tasks).toHaveLength(1);
    const task = stream.tasks[0] as {
      modeName: string;
      userInput: string;
      sessionId: string;
    };
    expect(task.userInput).toBe("Improve this selection:\n\nhel");
    expect(store.state.sessionStates[task.sessionId]).toBeDefined();
  });

  test("sendDocChat creates a doc-level session (no anchor) with the chosen mode", async () => {
    const { store, stream, run } = makeAssistant([
      { name: "done", payload: {} },
    ]);
    const assistant = run();
    await store.openDocument("/notes/thesis.md");
    assistant.selectedMode.value = "deep-review";
    assistant.draft.value = "summarize";
    await assistant.sendDocChat();

    expect(store.state.sessions[0]?.anchorBlockId).toBeUndefined();
    expect(assistant.draft.value).toBe("");
    expect(stream.tasks).toHaveLength(1);
    expect((stream.tasks[0] as { modeName: string }).modeName).toBe("deep-review");
  });

  test("sendDocChat ignores blank input", async () => {
    const { store, stream, run } = makeAssistant();
    const assistant = run();
    await store.openDocument("/notes/thesis.md");
    assistant.draft.value = "   ";
    await assistant.sendDocChat();
    expect(stream.tasks).toHaveLength(0);
  });

  test("a faked meter event updates the composable's activeTurn.cumulative", async () => {
    const { store, run } = makeAssistant([
      { name: "meter", payload: METER },
      { name: "done", payload: {} },
    ]);
    const assistant = run();
    await store.openDocument("/notes/thesis.md");
    assistant.draft.value = "x";
    await assistant.sendDocChat();

    const turn = assistant.activeTurn.value!;
    expect(turn.cumulative.system).toBe(10);
    expect(turn.cumulative.completion).toBe(5);
    expect(turn.meter).toEqual(METER);
  });

  test("a faked rag event exposes chunks on the active turn", async () => {
    const { store, run } = makeAssistant([
      {
        name: "rag",
        payload: {
          ok: true,
          chunks: [{ blockId: "b9", text: "cited", source: "notes/thesis.md" }],
        },
      },
      { name: "done", payload: {} },
    ]);
    const assistant = run();
    await store.openDocument("/notes/thesis.md");
    assistant.draft.value = "x";
    await assistant.sendDocChat();

    const rag = assistant.activeTurn.value!.rag!;
    expect(rag.chunks).toHaveLength(1);
    expect(rag.chunks[0]?.source).toBe("notes/thesis.md");
    expect(rag.chunks[0]?.text).toBe("cited");
  });

  test("a model-not-found error surfaces on the active turn (not swallowed)", async () => {
    const { store, run } = makeAssistant([
      { name: "error", payload: { code: "model-not-found", message: "no gemma" } },
    ]);
    const assistant = run();
    await store.openDocument("/notes/thesis.md");
    assistant.draft.value = "x";
    await assistant.sendDocChat();

    const turn = assistant.activeTurn.value!;
    expect(turn.active).toBe(false);
    expect(turn.error?.code).toBe("model-not-found");
    expect(turn.error?.message).toBe("no gemma");
  });
});
