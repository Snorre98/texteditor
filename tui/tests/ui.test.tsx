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
    // frames until the block text is rendered.
    await setup.waitForFrame(
      (frame) => frame.includes("hello"),
      { maxPasses: 60 },
    );
    expect(setup.captureCharFrame()).toContain("document — /notes/thesis.md");
    expect(setup.captureCharFrame()).toContain("hello");
  } finally {
    setup.renderer.destroy();
  }
});
