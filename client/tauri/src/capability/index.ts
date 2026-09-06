// capability/index.ts — the per-target dispatch (ADR-0014): Tauri `invoke` vs
// the web File System Access API. The rest of the app only ever calls
// `capabilityAdapter()` and is blind to the target.

import type { CapabilityAdapter } from "./adapter";
import { createTauriAdapter } from "./tauri";
import { createWebAdapter } from "./web";
import { isTauri } from "../engine";

export type { CapabilityAdapter, PickResult } from "./adapter";

let cached: CapabilityAdapter | null = null;

export function capabilityAdapter(): CapabilityAdapter {
  if (!cached) {
    cached = isTauri() ? createTauriAdapter() : createWebAdapter();
  }
  return cached;
}
