import { expect, test } from "bun:test";
import { testRender } from "@opentui/solid";
import { createAppStore } from "../src/state/store";
import { App } from "../src/ui/app";
import { stubApi } from "./helpers";

// Component smoke tests (C8): the panels mount on the OpenTUI test renderer
// and render their titles/content from store signals. No engine internals —
// the store under test is fed by the stub api (spec-shaped, not hand-shaped).
test("App renders the meter, chat, editor, diff, and RAG panels", async () => {
  const { api } = stubApi();
  const store = createAppStore({ api, baseUrl: "http://x" });
  const setup = await testRender(() => (<App store={store} docPath={null} />), {
    width: 90,
    height: 32,
  });

  try {
    await setup.waitForFrame((frame) => frame.includes("token meter"));
    const frame = setup.captureCharFrame();
    expect(frame).toContain("token meter");
    expect(frame).toContain("chat");
    expect(frame).toContain("diff preview");
    expect(frame).toContain("rag results");
    expect(frame).toContain("document — none open");
  } finally {
    setup.renderer.destroy();
  }
});

test("App renders a document's blocks and a seeded meter state", async () => {
  const { api } = stubApi();
  const store = createAppStore({ api, baseUrl: "http://x" });
  await store.openDocument("/notes/thesis.md");

  const setup = await testRender(() => (<App store={store} docPath={null} />), {
    width: 90,
    height: 32,
  });

  try {
    await setup.waitForFrame((frame) => frame.includes("document — /notes/thesis.md"));
    // The markdown renderable parses asynchronously (parser worker); pump
    // frames until the block text is rendered. The worker's result can land
    // after the renderer reports idle, so one waitForFrame call can bail
    // early — retry the wait until the worker round-trip completes.
    await retryFrames(setup, (frame) => frame.includes("hello"));
    expect(setup.captureCharFrame()).toContain("document — /notes/thesis.md");
    expect(setup.captureCharFrame()).toContain("hello");
  } finally {
    setup.renderer.destroy();
  }
});

// retryFrames re-issues waitForFrame until the predicate holds. The test
// renderer's waitForFrame bails out as soon as the scheduler looks idle, which
// races the markdown parser worker's asynchronous result — retrying gives the
// worker another scheduler cycle to land.
async function retryFrames(
  setup: Awaited<ReturnType<typeof testRender>>,
  predicate: (frame: string) => boolean,
  attempts = 40,
) {
  for (let i = 0; i < attempts; i++) {
    try {
      await setup.waitForFrame(predicate, { maxPasses: 20 });
      return;
    } catch {
      await new Promise((r) => setTimeout(r, 5));
    }
  }
  throw new Error("worker-backed markdown content never rendered");
}
