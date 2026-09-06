import { describe, expect, test } from "bun:test";
import { createAutosave } from "../src/editor/autosave";

// A deterministic fake timer: captures scheduled callbacks, fires them manually.
function fakeClock() {
  const scheduled = new Map<number, { fn: () => void; ms: number }>();
  let next = 0;
  const clock = {
    setTimeout: (fn: () => void, ms: number) => {
      const h = next++;
      scheduled.set(h, { fn, ms });
      return h;
    },
    clearTimeout: (h: unknown) => {
      scheduled.delete(h as number);
    },
    fireAll: async () => {
      const pending = [...scheduled.values()];
      scheduled.clear();
      for (const p of pending) await p.fn();
    },
    pending: () => scheduled.size,
  };
  return clock;
}

describe("createAutosave (manual-edit silence cadence, ADR-0020 §1)", () => {
  test("keystrokes reset the timer; only silence triggers a save", async () => {
    const clock = fakeClock();
    const saves: number[] = [];
    const a = createAutosave({
      intervalMs: 10_000,
      onSave: () => saves.push(saves.length),
      setTimeout: clock.setTimeout,
      clearTimeout: clock.clearTimeout,
    });

    a.noteEdit();
    a.noteEdit();
    a.noteEdit();
    expect(clock.pending()).toBe(1); // debounced to a single timer
    expect(saves).toEqual([]); // not yet saved

    await clock.fireAll();
    expect(saves).toEqual([0]);
    expect(clock.pending()).toBe(0);
  });

  test("flush saves immediately and cancels the pending timer", async () => {
    const clock = fakeClock();
    const saves: number[] = [];
    const a = createAutosave({
      intervalMs: 10_000,
      onSave: () => saves.push(saves.length),
      setTimeout: clock.setTimeout,
      clearTimeout: clock.clearTimeout,
    });

    a.noteEdit();
    await a.flush();
    expect(saves).toEqual([0]);
    expect(clock.pending()).toBe(0);
  });

  test("dispose cancels without saving", () => {
    const clock = fakeClock();
    const saves: number[] = [];
    const a = createAutosave({
      intervalMs: 10_000,
      onSave: () => saves.push(saves.length),
      setTimeout: clock.setTimeout,
      clearTimeout: clock.clearTimeout,
    });

    a.noteEdit();
    a.dispose();
    expect(saves).toEqual([]);
    expect(clock.pending()).toBe(0);
  });
});
