import { describe, expect, test } from "bun:test";
import {
  clampRect,
  defaultFreeRect,
  DOCK_INSET,
  loadWindowState,
  MIN_HEIGHT,
  MIN_WIDTH,
  resizeRect,
  sanitizeState,
  saveWindowState,
  snapZoneFor,
  STORAGE_KEY,
  windowCss,
  workspaceInset,
} from "../src/chat/windowState";

// The chat window's geometry model (ADR-0041 §2): pure functions, no DOM.
// A fake Storage double keeps persistence tests hermetic.

function memoryStorage(seed?: string) {
  const map = new Map<string, string>();
  if (seed) map.set(STORAGE_KEY, seed);
  return {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => void map.set(k, v),
  } as Pick<Storage, "getItem" | "setItem">;
}

const VW = 1280;
const VH = 800;

describe("clampRect", () => {
  test("keeps the rect inside the viewport", () => {
    const out = clampRect({ x: -50, y: 2000, width: 400, height: 560 }, VW, VH);
    expect(out.x).toBe(0);
    expect(out.y).toBe(VH - out.height);
  });

  test("clamps size to the configured minimums", () => {
    const out = clampRect({ x: 0, y: 0, width: 10, height: 10 }, VW, VH);
    expect(out.width).toBe(MIN_WIDTH);
    expect(out.height).toBe(MIN_HEIGHT);
  });

  test("never exceeds the viewport", () => {
    const out = clampRect({ x: 0, y: 0, width: 5000, height: 5000 }, VW, VH);
    expect(out.width).toBeLessThanOrEqual(VW);
    expect(out.height).toBeLessThanOrEqual(VH);
  });
});

describe("snapZoneFor", () => {
  test("detects the right edge", () => {
    const rect = { x: VW - 400 - 5, y: 10, width: 400, height: 560 };
    expect(snapZoneFor(rect, VW, VH)).toBe("right");
  });

  test("detects the left edge", () => {
    const rect = { x: 3, y: 10, width: 400, height: 560 };
    expect(snapZoneFor(rect, VW, VH)).toBe("left");
  });

  test("detects the bottom edge", () => {
    const rect = { x: 400, y: VH - 560 - 5, width: 400, height: 560 };
    expect(snapZoneFor(rect, VW, VH)).toBe("bottom");
  });

  test("is null mid-viewport", () => {
    const rect = { x: 400, y: 100, width: 400, height: 560 };
    expect(snapZoneFor(rect, VW, VH)).toBeNull();
  });
});

describe("resizeRect", () => {
  const rect = { x: 200, y: 100, width: 400, height: 560 };

  test("grows east and south", () => {
    const out = resizeRect(rect, "se", 50, 40, VW, VH);
    expect(out.width).toBe(450);
    expect(out.height).toBe(600);
    expect(out.x).toBe(200);
    expect(out.y).toBe(100);
  });

  test("west resize keeps the right edge anchored", () => {
    const out = resizeRect(rect, "w", 60, 0, VW, VH);
    expect(out.width).toBe(340);
    expect(out.x).toBe(260);
    expect(out.x + out.width).toBe(rect.x + rect.width);
  });

  test("a shrink below the minimum does not walk the anchor", () => {
    const out = resizeRect(rect, "w", 1000, 0, VW, VH);
    expect(out.width).toBe(MIN_WIDTH);
    expect(out.x + out.width).toBe(rect.x + rect.width);
  });
});

describe("windowCss / workspaceInset", () => {
  const base = { dock: "free" as const, x: 100, y: 80, width: 400, height: 560, minimized: false, sessionId: null };

  test("free-float uses absolute position", () => {
    const css = windowCss(base);
    expect(css.left).toBe("100px");
    expect(css.top).toBe("80px");
    expect(css.width).toBe("400px");
    expect(css.height).toBe("560px");
  });

  test("docked right pins to the edge with full height", () => {
    const css = windowCss({ ...base, dock: "right" });
    expect(css.right).toBe(`${DOCK_INSET}px`);
    expect(css.top).toBe(`${DOCK_INSET}px`);
    expect(css.bottom).toBe(`${DOCK_INSET}px`);
    expect(css.width).toBe("400px");
    expect(css.height).toBeUndefined();
  });

  test("workspace reflows around a right dock", () => {
    const inset = workspaceInset({ ...base, dock: "right" });
    expect(inset.marginRight).toBe(`${400 + DOCK_INSET * 2}px`);
  });

  test("workspace is untouched when floating or minimized", () => {
    expect(workspaceInset(base)).toEqual({});
    expect(workspaceInset({ ...base, minimized: true })).toEqual({});
  });
});

describe("persistence", () => {
  test("sanitizeState rejects garbage", () => {
    expect(sanitizeState(null)).toBeNull();
    expect(sanitizeState("x")).toBeNull();
    expect(sanitizeState({ x: NaN, y: 0, width: 1, height: 1 })).toBeNull();
  });

  test("sanitizeState clamps out-of-range sizes", () => {
    const out = sanitizeState({ x: 10, y: 20, width: 99999, height: 1, dock: "nope", minimized: true, sessionId: "s1" });
    expect(out).not.toBeNull();
    expect(out!.width).toBeLessThanOrEqual(800);
    expect(out!.height).toBe(MIN_HEIGHT);
    expect(out!.dock).toBe("free");
    expect(out!.minimized).toBe(true);
    expect(out!.sessionId).toBe("s1");
  });

  test("round-trips through storage", () => {
    const storage = memoryStorage();
    const state = { x: 12, y: 34, width: 400, height: 560, dock: "left" as const, minimized: false, sessionId: "s9" };
    saveWindowState(storage, state);
    const loaded = loadWindowState(storage);
    expect(loaded).toEqual(state);
  });

  test("load returns null on a corrupt blob", () => {
    expect(loadWindowState(memoryStorage("not json{"))).toBeNull();
    expect(loadWindowState(memoryStorage())).toBeNull();
  });
});

describe("defaultFreeRect", () => {
  test("sits in the upper right with sane proportions", () => {
    const rect = defaultFreeRect(VW, VH);
    expect(rect.x + rect.width).toBeLessThanOrEqual(VW);
    expect(rect.y).toBeGreaterThanOrEqual(0);
    expect(rect.width).toBeGreaterThanOrEqual(MIN_WIDTH);
    expect(rect.height).toBeGreaterThanOrEqual(MIN_HEIGHT);
  });
});
