// windowState — the floating chat window's geometry and persistence model
// (ADR-0041 §2). Pure functions only: no DOM, no Vue — unit-testable under
// `bun test`. The window is always positioned by the composable from these
// primitives; the engine and the API contract know nothing about any of it
// (ADR-0013 §3: client-local UI state).

export type DockZone = "free" | "left" | "right" | "bottom";

export interface ChatWindowRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface ChatWindowState extends ChatWindowRect {
  dock: DockZone;
  minimized: boolean;
  /** Last active session id, restored on relaunch with the same document. */
  sessionId: string | null;
}

export const MIN_WIDTH = 320;
export const MIN_HEIGHT = 240;
export const MAX_WIDTH = 800;
export const MAX_HEIGHT = 1200;
/** Distance from a viewport edge that triggers the snap-to-dock preview. */
export const SNAP_PX = 24;
/** Free-float inset when docked (gap between the docked window and edges). */
export const DOCK_INSET = 8;
export const STORAGE_KEY = "chat-window-state";

export const DEFAULT_STATE: ChatWindowState = {
  x: 0,
  y: 0,
  width: 400,
  height: 560,
  dock: "free",
  minimized: false,
  sessionId: null,
};

/** The default free-float rect: upper-right of the viewport, sane size. */
export function defaultFreeRect(vw: number, vh: number): ChatWindowRect {
  const width = Math.min(420, Math.max(MIN_WIDTH, Math.floor(vw * 0.32)));
  const height = Math.min(640, Math.max(MIN_HEIGHT, Math.floor(vh * 0.7)));
  return {
    x: Math.max(0, vw - width - 24),
    y: Math.max(0, Math.min(96, vh - height - 24)),
    width,
    height,
  };
}

export function clampNum(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

/** Clamp a rect to the viewport (width/height within min..max and the frame). */
export function clampRect(
  rect: ChatWindowRect,
  vw: number,
  vh: number,
): ChatWindowRect {
  const width = clampNum(rect.width, MIN_WIDTH, Math.min(MAX_WIDTH, vw));
  const height = clampNum(rect.height, MIN_HEIGHT, Math.min(MAX_HEIGHT, vh));
  return {
    x: clampNum(rect.x, 0, Math.max(0, vw - width)),
    y: clampNum(rect.y, 0, Math.max(0, vh - height)),
    width,
    height,
  };
}

/** Which edge the rect is snapped against, or null. */
export function snapZoneFor(
  rect: ChatWindowRect,
  vw: number,
  vh: number,
  snap = SNAP_PX,
): "left" | "right" | "bottom" | null {
  if (rect.x <= snap) return "left";
  if (vw - (rect.x + rect.width) <= snap) return "right";
  if (vh - (rect.y + rect.height) <= snap) return "bottom";
  return null;
}

export type ResizeDir =
  | "n" | "s" | "e" | "w"
  | "ne" | "nw" | "se" | "sw";

/** Apply a pointer delta to a rect for one resize handle, clamped. */
export function resizeRect(
  rect: ChatWindowRect,
  dir: ResizeDir,
  dx: number,
  dy: number,
  vw: number,
  vh: number,
): ChatWindowRect {
  let { x, y, width, height } = rect;
  if (dir.includes("e")) width = width + dx;
  if (dir.includes("s")) height = height + dy;
  if (dir.includes("w")) {
    width = width - dx;
    x = x + dx;
  }
  if (dir.includes("n")) {
    height = height - dy;
    y = y + dy;
  }
  // A west/north shrink below the minimum must not walk the edge off-screen:
  // clamp size first, then re-anchor the fixed edge.
  const nw = clampNum(width, MIN_WIDTH, Math.min(MAX_WIDTH, vw));
  const nh = clampNum(height, MIN_HEIGHT, Math.min(MAX_HEIGHT, vh));
  if (dir.includes("w")) x = rect.x + (rect.width - nw);
  if (dir.includes("n")) y = rect.y + (rect.height - nh);
  return clampRect({ x, y, width: nw, height: nh }, vw, vh);
}

/** The absolute CSS geometry for a state, for position:fixed rendering. */
export function windowCss(
  state: ChatWindowState,
): Record<string, string> {
  if (state.dock === "left" || state.dock === "right") {
    return {
      top: `${DOCK_INSET}px`,
      bottom: `${DOCK_INSET}px`,
      [state.dock]: `${DOCK_INSET}px`,
      width: `${state.width}px`,
    };
  }
  if (state.dock === "bottom") {
    return {
      left: `${DOCK_INSET}px`,
      right: `${DOCK_INSET}px`,
      bottom: `${DOCK_INSET}px`,
      height: `${state.height}px`,
    };
  }
  return {
    left: `${state.x}px`,
    top: `${state.y}px`,
    width: `${state.width}px`,
    height: `${state.height}px`,
  };
}

/** The workspace margin so the editor reflows around a docked window. */
export function workspaceInset(state: ChatWindowState): Record<string, string> {
  if (state.minimized) return {};
  switch (state.dock) {
    case "left":
      return { marginLeft: `${state.width + DOCK_INSET * 2}px` };
    case "right":
      return { marginRight: `${state.width + DOCK_INSET * 2}px` };
    case "bottom":
      return { marginBottom: `${state.height + DOCK_INSET * 2}px` };
    default:
      return {};
  }
}

function isFiniteNum(v: unknown): v is number {
  return typeof v === "number" && Number.isFinite(v);
}

/** Validate + normalize a persisted blob; null when unusable. */
export function sanitizeState(raw: unknown): ChatWindowState | null {
  if (typeof raw !== "object" || raw === null) return null;
  const r = raw as Record<string, unknown>;
  if (
    !isFiniteNum(r.x) || !isFiniteNum(r.y) ||
    !isFiniteNum(r.width) || !isFiniteNum(r.height)
  ) {
    return null;
  }
  const dock =
    r.dock === "left" || r.dock === "right" || r.dock === "bottom" || r.dock === "free"
      ? r.dock
      : "free";
  return {
    x: r.x,
    y: r.y,
    width: clampNum(r.width, MIN_WIDTH, MAX_WIDTH),
    height: clampNum(r.height, MIN_HEIGHT, MAX_HEIGHT),
    dock,
    minimized: r.minimized === true,
    sessionId: typeof r.sessionId === "string" ? r.sessionId : null,
  };
}

export function loadWindowState(
  storage: Pick<Storage, "getItem">,
  key = STORAGE_KEY,
): ChatWindowState | null {
  try {
    const raw = storage.getItem(key);
    if (raw === null) return null;
    return sanitizeState(JSON.parse(raw));
  } catch {
    return null;
  }
}

export function saveWindowState(
  storage: Pick<Storage, "setItem">,
  state: ChatWindowState,
  key = STORAGE_KEY,
): void {
  try {
    storage.setItem(key, JSON.stringify(state));
  } catch {
    // Storage can throw (quota/private mode) — window state is best-effort.
  }
}
