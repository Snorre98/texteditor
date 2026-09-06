// The manual-edit autosave cadence (ADR-0020 §1, ADR-0038 §4). The client holds
// the silence-interval timer and sends one whole-tree snapshot per interval; the
// engine commits on receipt. Keystrokes batch into one autosave: each edit resets
// the silence timer, and only a period of inactivity (default 10 s) triggers a
// save. The timer is a *sync cadence*, not a commit decision — reconciliation,
// formatting, and commit stay engine-side.
//
// Timers are injected so the cadence is deterministically testable.
export interface AutosaveOptions {
  /** Silence interval after which an edit is autosaved. */
  intervalMs: number;
  /** Send the current whole-tree snapshot (the store's saveTree). */
  onSave: () => void | Promise<void>;
  setTimeout?: (fn: () => void, ms: number) => unknown;
  clearTimeout?: (handle: unknown) => void;
}

export interface Autosave {
  /** Record a keystroke: reset the silence timer. */
  noteEdit: () => void;
  /** Save immediately and cancel any pending timer. */
  flush: () => Promise<void>;
  /** Cancel any pending timer (no save). */
  dispose: () => void;
}

export const DEFAULT_AUTOSAVE_INTERVAL_MS = 10_000;

export function createAutosave(opts: AutosaveOptions): Autosave {
  const intervalMs = opts.intervalMs;
  const setT: (fn: () => void, ms: number) => unknown =
    opts.setTimeout ?? ((fn, ms) => setTimeout(fn, ms));
  const clearT: (handle: unknown) => void =
    opts.clearTimeout ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));

  let timer: unknown = null;

  const cancel = () => {
    if (timer !== null) {
      clearT(timer);
      timer = null;
    }
  };

  return {
    noteEdit() {
      cancel();
      timer = setT(() => {
        timer = null;
        void opts.onSave();
      }, intervalMs);
    },
    async flush() {
      cancel();
      await opts.onSave();
    },
    dispose() {
      cancel();
    },
  };
}
