// capability/tauri.ts — E7 Tauri branch. `invoke` IPC into the Rust core for
// native file dialogs (`pick_directory`/`pick_file` in src-tauri/src/capability.rs).
// The returned absolute path is then handed to the engine (ADR-0013 §3: native
// I/O in the Rust core, all edits + versioning through the engine).

import type { CapabilityAdapter, PickResult } from "./adapter";
import { invoke } from "@tauri-apps/api/core";

export function createTauriAdapter(): CapabilityAdapter {
  return {
    pickDirectory: () =>
      invoke<string | null>("pick_directory").then(toResult),
    pickFile: () => invoke<string | null>("pick_file").then(toResult),
  };
}

function toResult(path: string | null): PickResult | null {
  if (!path) return null;
  const name = path.split("/").pop() ?? path;
  return { name, path };
}
