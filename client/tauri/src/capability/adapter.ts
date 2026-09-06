// capability/adapter.ts — E7, ADR-0014. The single per-target seam.
//
// The one thing that differs per deployment target is how the UI reaches the
// filesystem/OS. The adapter abstracts "pick a file/directory, get its path"
// behind one interface; the Vue UI is written once against it (ADR-0014's
// frontend-swap guarantee). All app logic — opening, reading, editing,
// versioning — stays in the engine, reached over the OpenAPI contract.

export interface PickResult {
  /** Bare file/dir name (display only). */
  name: string;
  /**
   * Absolute path usable by the engine (`POST /documents {path}`,
   * `GET /directories?path=`). `null` on the web target, where the File System
   * Access API keeps files in the browser sandbox and cannot expose absolute
   * paths (the E6 caveat, ADR-0014 consequences).
   */
  path: string | null;
}

export interface CapabilityAdapter {
  /** Open a native directory picker; null on cancel/unsupported. */
  pickDirectory(): Promise<PickResult | null>;
  /** Open a native file picker; null on cancel/unsupported. */
  pickFile(): Promise<PickResult | null>;
}
