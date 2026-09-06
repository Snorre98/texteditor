import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// Tauri 2 expects a fixed dev port and a no-clearscreen watch (ADR-0013 §2).
// Build target `esnext` — the desktop WebView is a modern WKWebView, and the
// older `safari13` default no longer works with rolldown-vite's transform.
export default defineConfig({
  plugins: [vue()],
  clearScreen: false,
  server: {
    port: 5173,
    strictPort: true,
  },
  build: {
    target: "esnext",
    minify: !process.env.TAURI_ENV_DEBUG ? "esbuild" : false,
    sourcemap: !!process.env.TAURI_ENV_DEBUG,
  },
});
