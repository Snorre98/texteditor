<script setup lang="ts">
import { onMounted, ref } from "vue";
import { discoverEngineUrl } from "./engine";
import { capabilityAdapter } from "./capability";

// F8 shell (ADR-0013 §2): Tauri 2 + Vue 3. This is the deployment-seam shell —
// it proves the E2 handshake (the client talks to the sidecar-discovered engine
// base URL) and the E7 capability adapter. The CodeMirror editor + chat bubbles
// are the F8 UI proper (ADR-0026), built on top of this shell.

const engineUrl = ref<string | null>(null);
const error = ref<string | null>(null);
const picked = ref<string | null>(null);

onMounted(async () => {
  try {
    engineUrl.value = await discoverEngineUrl();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});

async function pickDirectory() {
  const result = await capabilityAdapter().pickDirectory();
  picked.value = result
    ? `directory: ${result.name}${result.path ? ` — ${result.path}` : ""}`
    : "cancelled";
}

async function pickFile() {
  const result = await capabilityAdapter().pickFile();
  picked.value = result
    ? `file: ${result.name}${result.path ? ` — ${result.path}` : ""}`
    : "cancelled";
}
</script>

<template>
  <main>
    <h1>texteditor</h1>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="engineUrl">
      connected to <code>{{ engineUrl }}</code>
    </p>
    <p v-else>connecting…</p>

    <section>
      <button type="button" @click="pickDirectory">Pick directory</button>
      <button type="button" @click="pickFile">Pick file</button>
      <p v-if="picked">{{ picked }}</p>
    </section>
  </main>
</template>

<style>
main {
  font-family: system-ui, sans-serif;
  max-width: 42rem;
  margin: 2rem auto;
}
.error {
  color: #b91c1c;
}
code {
  background: #f3f4f6;
  padding: 0.1rem 0.3rem;
  border-radius: 0.25rem;
}
</style>
