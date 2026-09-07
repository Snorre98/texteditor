import { describe, expect, test } from "bun:test";
import type { Session } from "../src/generated/types.gen";
import type { SessionState } from "../src/state/store";
import {
  sessionLabel,
  sessionSubLabel,
  sessionWorking,
  sortSessions,
} from "../src/chat/sessionLabels";

// Derived session-list labels (ADR-0041 §3): the API has no session titles,
// so the list renders a label from the wire fields. Pure, no DOM.

function session(over: Partial<Session> = {}): Session {
  return { id: "s1", documentId: "d1", ...over };
}

describe("sessionLabel", () => {
  test("prefers the mode type", () => {
    expect(sessionLabel(session({ modeType: "proofreader" }))).toBe("proofreader");
  });

  test("labels anchored sessions as selections", () => {
    expect(sessionLabel(session({ anchorBlockId: "b1" }))).toBe("selection");
  });

  test("falls back to chat", () => {
    expect(sessionLabel(session())).toBe("chat");
  });
});

describe("sessionSubLabel", () => {
  const now = Date.UTC(2026, 8, 7, 12, 0, 0);

  test("just now", () => {
    expect(sessionSubLabel(session({ updatedAt: now - 10_000 }), now)).toBe("just now");
  });

  test("minutes and hours", () => {
    expect(sessionSubLabel(session({ updatedAt: now - 5 * 60_000 }), now)).toBe("5m ago");
    expect(sessionSubLabel(session({ updatedAt: now - 3 * 3_600_000 }), now)).toBe("3h ago");
  });

  test("days", () => {
    expect(sessionSubLabel(session({ updatedAt: now - 26 * 3_600_000 }), now)).toBe("yesterday");
    expect(sessionSubLabel(session({ updatedAt: now - 50 * 3_600_000 }), now)).toBe("2d ago");
  });

  test("empty when no timestamps", () => {
    expect(sessionSubLabel(session(), now)).toBe("");
  });
});

describe("sortSessions", () => {
  test("most recently active first", () => {
    const list = [
      session({ id: "a", updatedAt: 100 }),
      session({ id: "b", updatedAt: 300 }),
      session({ id: "c", updatedAt: 200 }),
    ];
    expect(sortSessions(list).map((s) => s.id)).toEqual(["b", "c", "a"]);
  });

  test("does not mutate the input", () => {
    const list = [session({ id: "a", updatedAt: 100 }), session({ id: "b", updatedAt: 300 })];
    sortSessions(list);
    expect(list.map((s) => s.id)).toEqual(["a", "b"]);
  });
});

describe("sessionWorking", () => {
  const states: Record<string, SessionState> = {
    s1: { messages: [], turn: { active: true } as SessionState["turn"] },
    s2: { messages: [], turn: { active: false } as SessionState["turn"] },
  };

  test("true only while a turn is streaming", () => {
    expect(sessionWorking(states, "s1")).toBe(true);
    expect(sessionWorking(states, "s2")).toBe(false);
    expect(sessionWorking(states, "missing")).toBe(false);
  });
});
