<script setup lang="ts">
import { onMounted, ref } from "vue";
import { discoverEngineUrl } from "./engine";
import { api, setEndpoint } from "./api/client";
import { createAppStore } from "./state/store";
import Editor from "./editor/Editor.vue";

// F8 (ADR-0013 §2): the Tauri editor. On launch it discovers the sidecar-spawned
// engine (E2, ADR-0021 §1), points the generated client at the resolved base URL
// (ADR-0037), builds the reactive store (F7, ADR-0023), and hands it to the
// CodeMirror editor. All edits + versioning go through the engine (ADR-0013 §3).

const store = ref<ReturnType<typeof createAppStore> | null>(null);
const error = ref<string | null>(null);

onMounted(async () => {
  try {
    const baseUrl = await discoverEngineUrl();
    setEndpoint(baseUrl);
    store.value = createAppStore({ api, baseUrl });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});
</script>

<template>
  <main>
    <h1>texteditor</h1>
    <p v-if="error" class="error">{{ error }}</p>
    <Editor v-else-if="store" :store="store" />
    <p v-else>connecting…</p>
  </main>
</template>

<style>
main {
  font-family: system-ui, sans-serif;
  max-width: 64rem;
  margin: 2rem auto;
}
.error {
  color: #b91c1c;
}
</style>
