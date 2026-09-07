<script setup lang="ts">
// App — the full-viewport workspace shell (ADR-0041 §1). On launch it discovers
// the sidecar-spawned engine (E2, ADR-0021 §1), points the generated client at
// the resolved base URL (ADR-0037), builds the reactive store (F7, ADR-0023),
// and hands it to the CodeMirror editor plus the floating chat window. The
// editor owns the full viewport; the chat floats above it (ADR-0041); a docked
// chat reflows the editor through the workspace margin. All edits + versioning
// go through the engine (ADR-0013 §3).
import { onMounted, ref, shallowRef } from "vue";
import { discoverEngineUrl } from "./engine";
import { api, setEndpoint } from "./api/client";
import { createAppStore } from "./state/store";
import { createAssistant, type EditorApi } from "./editor/useAssistant";
import { useChatWindow } from "./chat/useChatWindow";
import ChatOrb from "./chat/ChatOrb.vue";
import Editor from "./editor/Editor.vue";
import FloatingChatWindow from "./chat/FloatingChatWindow.vue";

const store = ref<ReturnType<typeof createAppStore> | null>(null);
// shallowRef: the assistant holds Vue refs (selectedMode, draft, …) that must
// NOT be deep-unwrapped when the template passes the object down to children.
const assistant = shallowRef<ReturnType<typeof createAssistant> | null>(null);
const error = ref<string | null>(null);

// The editor registers its document/selection readers here (ADR-0041 §3); the
// assistant delegates through it so the chat window can ask about selections.
// A plain holder, not a ref — children mutate `.current` directly.
const editorApi: { current: EditorApi | null } = { current: null };

const chatWindow = useChatWindow();

onMounted(async () => {
  try {
    const baseUrl = await discoverEngineUrl();
    setEndpoint(baseUrl);
    const s = createAppStore({ api, baseUrl });
    store.value = s;
    assistant.value = createAssistant(s, {
      getDocText: () => editorApi.current?.getDocText() ?? "",
      getSelection: () => editorApi.current?.getSelection() ?? null,
    });
    await s.refreshFleet();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});

function toggleChat() {
  if (chatWindow.state.minimized) chatWindow.restore();
  else chatWindow.minimize();
}
</script>

<template>
  <main class="flex h-dvh flex-col overflow-hidden">
    <p v-if="error" class="error p-2">{{ error }}</p>
    <template v-else-if="store && assistant">
      <div
        class="min-h-0 flex-1 transition-[margin] duration-150"
        :style="chatWindow.workspaceStyle.value"
      >
        <Editor
          :store="store"
          :assistant="assistant"
          :editor-api="editorApi"
          @toggle-chat="toggleChat"
        />
      </div>
      <FloatingChatWindow :store="store" :assistant="assistant" :window="chatWindow" />
      <ChatOrb v-if="chatWindow.state.minimized" @restore="chatWindow.restore()" />
    </template>
    <p v-else class="p-4">connecting…</p>
  </main>
</template>

<style>
.error {
  color: #b91c1c;
}
</style>
