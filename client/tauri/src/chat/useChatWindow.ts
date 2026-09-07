// useChatWindow — the floating window's reactive geometry + pointer glue
// (ADR-0041 §2). All math lives in windowState.ts (pure, tested); this
// composable only wires pointer events to it and persists on change.
// Everything here is client-local UI state — the engine never sees it.
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue";
import type { Ref } from "vue";
import {
  clampRect,
  defaultFreeRect,
  DEFAULT_STATE,
  loadWindowState,
  resizeRect,
  saveWindowState,
  snapZoneFor,
  STORAGE_KEY,
  windowCss,
  workspaceInset,
} from "./windowState";
import type {
  ChatWindowState,
  DockZone,
  ResizeDir,
} from "./windowState";

export interface ChatWindow {
  state: ChatWindowState;
  /** Which edge is being previewed for a snap during a drag (null = none). */
  snapPreview: Ref<DockZone | null>;
  /** CSS for the fixed-position window element. */
  windowStyle: Ref<Record<string, string>>;
  /** CSS margin for the workspace so the editor reflows around a dock. */
  workspaceStyle: Ref<Record<string, string>>;
  onHeaderPointerDown: (e: PointerEvent) => void;
  onResizePointerDown: (e: PointerEvent, dir: ResizeDir) => void;
  minimize: () => void;
  restore: () => void;
  releaseDock: () => void;
  setSession: (id: string | null) => void;
  dispose: () => void;
}

const PERSIST_DEBOUNCE_MS = 300;

interface DragState {
  kind: "move" | "resize";
  dir: ResizeDir;
  startX: number;
  startY: number;
  startRect: ChatWindowState;
  moved: boolean;
}

function viewport(): { vw: number; vh: number } {
  return {
    vw: typeof window !== "undefined" ? window.innerWidth : 1280,
    vh: typeof window !== "undefined" ? window.innerHeight : 800,
  };
}

export function useChatWindow(opts?: {
  storageKey?: string;
  storage?: Pick<Storage, "getItem" | "setItem">;
}): ChatWindow {
  const storageKey = opts?.storageKey ?? STORAGE_KEY;
  const storage = opts?.storage ?? window.localStorage;
  const { vw, vh } = viewport();

  const persisted = loadWindowState(storage, storageKey);
  const base: ChatWindowState = persisted ?? {
    ...DEFAULT_STATE,
    ...defaultFreeRect(vw, vh),
  };
  const state = reactive<ChatWindowState>({ ...base, ...clampRect(base, vw, vh) });
  const snapPreview = ref<DockZone | null>(null);

  const windowStyle = computed(() => windowCss(state));
  const workspaceStyle = computed(() => workspaceInset(state));

  let drag: DragState | null = null;
  let persistTimer: ReturnType<typeof setTimeout> | null = null;

  function persist() {
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(() => {
      saveWindowState(storage, { ...state }, storageKey);
      persistTimer = null;
    }, PERSIST_DEBOUNCE_MS);
  }

  watch(
    () => [state.dock, state.x, state.y, state.width, state.height, state.minimized, state.sessionId],
    () => persist(),
  );

  function onPointerMove(e: PointerEvent) {
    if (!drag) return;
    const { vw: w, vh: h } = viewport();
    if (drag.kind === "move") {
      const next = clampRect(
        {
          ...state,
          x: drag.startRect.x + (e.clientX - drag.startX),
          y: drag.startRect.y + (e.clientY - drag.startY),
        },
        w,
        h,
      );
      state.x = next.x;
      state.y = next.y;
      state.width = next.width;
      state.height = next.height;
      snapPreview.value = snapZoneFor(next, w, h);
      drag.moved = true;
    } else {
      const next = resizeRect(
        drag.startRect,
        drag.dir,
        e.clientX - drag.startX,
        e.clientY - drag.startY,
        w,
        h,
      );
      state.x = next.x;
      state.y = next.y;
      state.width = next.width;
      state.height = next.height;
      drag.moved = true;
    }
  }

  function onPointerUp() {
    if (!drag) return;
    const wasDrag = drag;
    if (wasDrag.kind === "move" && snapPreview.value) {
      state.dock = snapPreview.value;
    }
    snapPreview.value = null;
    drag = null;
    window.removeEventListener("pointermove", onPointerMove);
    window.removeEventListener("pointerup", onPointerUp);
    if (wasDrag.moved) persist();
  }

  function beginDrag(e: PointerEvent, kind: DragState["kind"], dir: ResizeDir) {
    // Dragging a docked window undocks it into free-float at its current spot.
    if (kind === "move" && state.dock !== "free") {
      state.dock = "free";
    }
    drag = {
      kind,
      dir,
      startX: e.clientX,
      startY: e.clientY,
      startRect: { ...state },
      moved: false,
    };
    window.addEventListener("pointermove", onPointerMove);
    window.addEventListener("pointerup", onPointerUp);
  }

  function onHeaderPointerDown(e: PointerEvent) {
    if (e.button !== 0) return;
    beginDrag(e, "move", "e");
  }

  function onResizePointerDown(e: PointerEvent, dir: ResizeDir) {
    if (e.button !== 0) return;
    e.stopPropagation();
    beginDrag(e, "resize", dir);
  }

  function minimize() {
    state.minimized = true;
  }

  function restore() {
    state.minimized = false;
  }

  function releaseDock() {
    if (state.dock === "free") return;
    state.dock = "free";
    const rect = defaultFreeRect(viewport().vw, viewport().vh);
    state.x = rect.x;
    state.y = rect.y;
    state.width = rect.width;
    state.height = rect.height;
  }

  function setSession(id: string | null) {
    state.sessionId = id;
  }

  function dispose() {
    if (persistTimer) {
      clearTimeout(persistTimer);
      persistTimer = null;
    }
    if (drag) {
      window.removeEventListener("pointermove", onPointerMove);
      window.removeEventListener("pointerup", onPointerUp);
      drag = null;
    }
  }

  onBeforeUnmount(dispose);

  return {
    state,
    snapPreview,
    windowStyle,
    workspaceStyle,
    onHeaderPointerDown,
    onResizePointerDown,
    minimize,
    restore,
    releaseDock,
    setSession,
    dispose,
  };
}
