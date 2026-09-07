<script setup lang="ts">
// FloatingChatWindow — the floating, draggable chat window (ADR-0041). A dumb
// renderer: geometry comes from the useChatWindow composable (pure math in
// windowState.ts), content from the store via the shared assistant, and all
// effects route through the store. Header drag, 8-way resize, snap-to-dock
// preview, session list, meter/RAG panels, composer — everything here is
// presentation over the already-correct engine surface.
import { computed, onMounted, onBeforeUnmount, ref, watch } from "vue";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
  GripHorizontal,
  MessagesSquare,
  Minus,
  PanelLeft,
  PanelLeftClose,
  PinOff,
} from "lucide-vue-next";
import type { AppStore } from "../state/store";
import type { createAssistant } from "../editor/useAssistant";
import type { ChatWindow } from "./useChatWindow";
import type { DockZone, ResizeDir } from "./windowState";
import ChatComposer from "./ChatComposer.vue";
import MessageList from "./MessageList.vue";
import MeterPanel from "./MeterPanel.vue";
import ModelSelector from "./ModelSelector.vue";
import RagPanel from "./RagPanel.vue";
import SessionList from "./SessionList.vue";
import TurnStatusBar from "./TurnStatusBar.vue";

type Assistant = ReturnType<typeof createAssistant>;

const props = defineProps<{
  store: AppStore;
  assistant: Assistant;
  window: ChatWindow;
}>();

const showSessions = ref(true);

const dock = computed(() => props.window.state.dock);
const minimized = computed(() => props.window.state.minimized);
const snapPreview = props.window.snapPreview;

const canChat = computed(() => !!props.store.state.document);

/** Which resize handles exist for the current dock mode. */
const resizeDirs = computed<ResizeDir[]>(() => {
  switch (dock.value) {
    case "left":
      return ["e"];
    case "right":
      return ["w"];
    case "bottom":
      return ["n"];
    default:
      return ["n", "s", "e", "w", "ne", "nw", "se", "sw"];
  }
});

const resizeCursor: Record<ResizeDir, string> = {
  n: "cursor-ns-resize",
  s: "cursor-ns-resize",
  e: "cursor-ew-resize",
  w: "cursor-ew-resize",
  ne: "cursor-nesw-resize",
  sw: "cursor-nesw-resize",
  nw: "cursor-nwse-resize",
  se: "cursor-nwse-resize",
};

const resizeClass: Record<ResizeDir, string> = {
  n: "top-0 left-3 right-3 h-1.5",
  s: "bottom-0 left-3 right-3 h-1.5",
  e: "right-0 top-3 bottom-3 w-1.5",
  w: "left-0 top-3 bottom-3 w-1.5",
  ne: "top-0 right-0 size-3",
  nw: "top-0 left-0 size-3",
  se: "bottom-0 right-0 size-3",
  sw: "bottom-0 left-0 size-3",
};

const snapHint: Record<Exclude<DockZone, "free">, string> = {
  left: "absolute inset-y-0 left-0 w-1/3 rounded-lg border border-primary/40 bg-primary/10",
  right: "absolute inset-y-0 right-0 w-1/3 rounded-lg border border-primary/40 bg-primary/10",
  bottom: "absolute inset-x-0 bottom-0 h-1/3 rounded-lg border border-primary/40 bg-primary/10",
};

const snapClass = computed(() => {
  const zone = snapPreview.value;
  if (zone === null || zone === "free") return null;
  return snapHint[zone];
});

function onHeaderPointerDown(e: PointerEvent) {
  const target = e.target as HTMLElement;
  if (target.closest("button, [data-no-drag]")) return;
  props.window.onHeaderPointerDown(e);
}

function selectSession(id: string) {
  props.window.setSession(id);
  void props.assistant.openSession(id);
}

function newChat() {
  props.window.setSession(null);
  props.assistant.newChat();
}

// Session restoration (ADR-0041 §2/§3): when a document opens, load its
// sessions, then resume the persisted session if it exists, else the most
// recently active one (mirroring the TUI's resume-first behavior).
watch(
  () => props.store.state.document?.id,
  async (id) => {
    if (!id) return;
    try {
      await props.store.loadSessions();
    } catch {
      return;
    }
    const sessions = props.store.state.sessions;
    if (sessions.length === 0) return;
    const stored = sessions.find((s) => s.id === props.window.state.sessionId);
    const target = stored ?? sessions[0];
    if (target && !props.assistant.activeSessionId.value) {
      selectSession(target.id);
    }
  },
  { immediate: true },
);

// Any active-session change (resume, new chat, ask-about) persists with the
// window state so a relaunch restores the same conversation.
watch(
  () => props.assistant.activeSessionId.value,
  (id) => props.window.setSession(id),
);

onMounted(() => props.store.startFleetPoll());
onBeforeUnmount(() => props.store.stopFleetPoll());
</script>

<template>
  <section
    class="fixed z-50 flex min-h-0 flex-col overflow-hidden rounded-xl border bg-card text-card-foreground shadow-xl"
    :class="snapPreview ? 'pointer-events-none' : ''"
    :style="window.windowStyle.value"
    v-show="!minimized"
  >
    <!-- header: drag handle + mode/model selects + window actions -->
    <header
      class="flex shrink-0 items-center gap-2 border-b px-2 py-1.5"
      :class="dock === 'free' ? 'cursor-grab active:cursor-grabbing' : ''"
      @pointerdown="onHeaderPointerDown"
    >
      <GripHorizontal class="size-4 shrink-0 text-muted-foreground" />
      <MessagesSquare class="size-4 shrink-0 text-muted-foreground" />
      <span class="shrink-0 text-sm font-semibold">Chat</span>

      <div data-no-drag class="flex min-w-0 flex-1 items-center gap-1.5">
        <Select v-model="assistant.selectedMode.value" aria-label="Mode">
          <SelectTrigger class="h-7 min-w-0 max-w-32">
            <SelectValue placeholder="mode…" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="m in store.state.modes" :key="m.name" :value="m.name">
              {{ m.name }}
            </SelectItem>
          </SelectContent>
        </Select>
        <ModelSelector :store="store" />
      </div>

      <Button
        v-if="showSessions"
        variant="ghost"
        size="icon-xs"
        aria-label="Hide sessions"
        @click="showSessions = false"
      >
        <PanelLeftClose />
      </Button>
      <Button
        v-else
        variant="ghost"
        size="icon-xs"
        aria-label="Show sessions"
        @click="showSessions = true"
      >
        <PanelLeft />
      </Button>
      <Button
        v-if="dock !== 'free'"
        variant="ghost"
        size="icon-xs"
        aria-label="Undock"
        @click="window.releaseDock()"
      >
        <PinOff />
      </Button>
      <Button variant="ghost" size="icon-xs" aria-label="Minimize" @click="window.minimize()">
        <Minus />
      </Button>
    </header>

    <div class="flex min-h-0 flex-1">
      <!-- session list sidebar -->
      <div v-if="showSessions" class="w-44 shrink-0 border-r">
        <SessionList
          :store="store"
          :active-session-id="assistant.activeSessionId.value"
          @select="selectSession"
          @new-chat="newChat"
        />
      </div>

      <!-- chat body -->
      <div class="flex min-w-0 flex-1 flex-col">
        <MessageList
          :session-id="assistant.activeSessionId.value"
          :messages="assistant.messages.value"
          :turn="assistant.activeTurn.value"
        />
        <MeterPanel :cumulative="assistant.activeTurn.value?.cumulative" />
        <RagPanel :rag="assistant.activeTurn.value?.rag" />
        <TurnStatusBar
          :turn="assistant.activeTurn.value"
          :can-retry="assistant.messages.value.some((m) => m.role === 'user')"
          @retry="() => void assistant.retryLast()"
        />
        <ChatComposer
          :draft="assistant.draft.value"
          :busy="assistant.busy.value"
          :can-chat="canChat"
          @update-draft="(v) => (assistant.draft.value = v)"
          @send="() => void assistant.sendDocChat()"
        />
      </div>
    </div>

    <!-- resize handles -->
    <div
      v-for="dir in resizeDirs"
      :key="`r-${dir}`"
      class="absolute z-10 touch-none"
      :class="[resizeClass[dir], resizeCursor[dir]]"
      @pointerdown="(e) => window.onResizePointerDown(e, dir)"
    />
  </section>

  <!-- snap preview overlay (viewport-level: the dock zone is viewport-relative) -->
  <div
    v-if="snapClass"
    class="pointer-events-none fixed inset-0 z-40"
    aria-hidden="true"
  >
    <div :class="snapClass" />
  </div>
</template>
