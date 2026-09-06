// capability/web.ts — E7 web branch (ADR-0014). The Web File System Access API
// (`showDirectoryPicker` / `showOpenFilePicker`) in the browser.
//
// Caveat (E6, ADR-0014 consequences): a browser FileSystemHandle cannot expose an
// absolute path, so `path` is `null` here — the web target keeps files in the
// browser sandbox and the self-hosted engine cannot see those handles. Web file
// reach is therefore browser-scoped; the engine's absolute-path document model is
// reached over `ENGINE_URL` on the user's own machine/LAN (ADR-0021 §2/§3).

import type { CapabilityAdapter } from "./adapter";

// Minimal declarations for the not-yet-widely-typed File System Access API.
interface FileSystemDirectoryHandle {
  name: string;
}
interface FileSystemFileHandle {
  name: string;
}
interface FileSystemAccessWindow {
  showDirectoryPicker?: (options?: {
    id?: string;
    mode?: "read" | "readwrite";
  }) => Promise<FileSystemDirectoryHandle>;
  showOpenFilePicker?: (options?: {
    multiple?: boolean;
  }) => Promise<FileSystemFileHandle[]>;
}

export function createWebAdapter(): CapabilityAdapter {
  const w = window as Window & FileSystemAccessWindow;
  return {
    pickDirectory: async () => {
      if (!w.showDirectoryPicker) return null;
      const handle = await w.showDirectoryPicker.call(w);
      return { name: handle.name, path: null };
    },
    pickFile: async () => {
      if (!w.showOpenFilePicker) return null;
      const handles = await w.showOpenFilePicker.call(w, { multiple: false });
      const handle = handles[0];
      return handle ? { name: handle.name, path: null } : null;
    },
  };
}
