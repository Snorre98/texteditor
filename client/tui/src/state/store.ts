// Client state — solid reactive signals over the generated client (ADR-0023).
// The store is the single seam between the OpenTUI components and the engine:
// it holds no domain logic (the engine owns that), only fetch/stream lifecycle
// and UI-observable state. Every effect the TUI can cause is one action here;
// components render signals, never call the API directly.
//
// Slices: connection, fleet (control + models + liveState, ADR-0040), modes,
// tools, document (+ block tree), sessions (+ messages), and the live turn
// stream (tokens, meter, candidate/diff/rag queues, done/error, backpressure
// label).
import { batch, createSignal } from "solid-js";
import type {
  Block,
  CandidateEvent,
  DiffEvent,
  Document,
  DoneEvent,
  ErrorEvent,
  FleetModel,
  Message,
  MeterEvent,
  Mode,
  RagEvent,
  Revision,
  Session,
  Task,
  ToolDef,
  TurnOptions,
} from "../generated/types.gen";
import type { Api } from "../api/client";
import { streamTurn, type SseEventName } from "../api/sse";

// The meter component rows that accumulate across a session (everything on
// the MeterEvent wire except the thinkingApprox label).
export const METER_COMPONENTS = [
  "system",
  "tools",
  "rag",
  "history",
  "user",
  "thinking",
  "completion",
] as const;
export type MeterComponent = (typeof METER_COMPONENTS)[number];
export type MeterTally = Record<MeterComponent, number>;

export const ZERO_TALLY: MeterTally = Object.fromEntries(
  METER_COMPONENTS.map((c) => [c, 0]),
) as MeterTally;

export interface ConnectionState {
  baseUrl: string | null;
  error: string | null;
}

export interface TurnState {
  active: boolean;
  /** The answering-phase token stream (state-machine §1). */
  tokens: string;
  /** The last meter event of the active turn. */
  meter: MeterEvent | null;
  /** Per-session cumulative tally (sum of meter events). */
  cumulative: MeterTally;
  /** Latest edit candidate streamed this turn (diff preview). */
  candidate: CandidateEvent | null;
  /** Diff payloads streamed this turn. */
  diffs: DiffEvent[];
  /** Latest retrieval result streamed this turn (RAG panel). */
  rag: RagEvent | null;
  /** Terminal done payload. */
  done: DoneEvent | null;
  /** Terminal error payload. */
  error: ErrorEvent | null;
  /** A slow-client buffer overflow was labeled by the engine. */
  backpressure: boolean;
}

/** The client-side fleet slice (ADR-0040 §3/§4): the wire FleetState plus
 * action feedback the switcher renders — never synthesized state. */
export interface FleetView {
  control: "up" | "unreachable";
  models: FleetModel[];
  /** Last refresh/lifecycle error (e.g. a failed start), UI-visible. */
  error: string | null;
  /** Model name with an in-flight start/stop verb, if any. */
  busy: string | null;
}

export const FLEET_POLL_INTERVAL_MS = 10_000;

export interface EngineState {
  connection: ConnectionState;
  fleet: FleetView;
  modes: Mode[];
  tools: ToolDef[];
  document: Document | null;
  blocks: Block[];
  history: Revision[];
  sessions: Session[];
  currentSessionId: string | null;
  messages: Message[];
  turn: TurnState;
  /** Model-switch progress ("serving-control.feature: TUI switches models"). */
  switching: { from: string; to: string } | null;
}

export interface StoreDeps {
  api: Api;
  baseUrl: string;
  stream?: typeof streamTurn;
  /** Poll interval override (tests); default FLEET_POLL_INTERVAL_MS. */
  fleetPollMs?: number;
}

export interface SubmitTurnInput {
  sessionId: string;
  modeName: string;
  documentId: string;
  userInput: string;
  options?: TurnOptions;
}

export interface AppStore {
  state: ReturnType<typeof createSignal<EngineState>>[0];
  refreshFleet: () => Promise<void>;
  openDocument: (path: string) => Promise<void>;
  loadSessions: () => Promise<void>;
  createSession: () => Promise<string>;
  selectSession: (id: string) => Promise<void>;
  submitTurn: (input: SubmitTurnInput) => Promise<void>;
  /** Diff-preview "accept": fetch the staged candidate, apply, commit. */
  acceptCandidate: (blockId: string) => Promise<void>;
  /** serving-control.feature "TUI switches models": start new → up → stop old. */
  switchModel: (from: string, to: string) => Promise<void>;
  /** Fleet observability (ADR-0040): one-verb lifecycle actions + poll control. */
  startModel: (name: string) => Promise<void>;
  stopModel: (name: string) => Promise<void>;
  startFleetPoll: () => void;
  stopFleetPoll: () => void;
  refreshBlocks: () => Promise<void>;
}

const EMPTY_TURN: TurnState = {
  active: false,
  tokens: "",
  meter: null,
  cumulative: { ...ZERO_TALLY },
  candidate: null,
  diffs: [],
  rag: null,
  done: null,
  error: null,
  backpressure: false,
};

const INITIAL_STATE: EngineState = {
  connection: { baseUrl: null, error: null },
  fleet: { control: "up", models: [], error: null, busy: null },
  modes: [],
  tools: [],
  document: null,
  blocks: [],
  history: [],
  sessions: [],
  currentSessionId: null,
  messages: [],
  turn: { ...EMPTY_TURN, cumulative: { ...ZERO_TALLY } },
  switching: null,
};

// createAppStore wires the generated, zod-validated client into reactive
// signals. All API effects go through here; the UI is dumb (ADR-0013 §3).
export function createAppStore(deps: StoreDeps): AppStore {
  const [state, setState] = createSignal<EngineState>({
    ...INITIAL_STATE,
    turn: { ...EMPTY_TURN, cumulative: { ...ZERO_TALLY } },
    connection: { baseUrl: deps.baseUrl, error: null },
  });
  const stream = deps.stream ?? streamTurn;

  const patch = (p: Partial<EngineState>) =>
    batch(() => setState((s) => ({ ...s, ...p })));
  const patchTurn = (p: Partial<TurnState>) =>
    batch(() => setState((s) => ({ ...s, turn: { ...s.turn, ...p } })));

  // Unwrap the generated client's {data, error, response} result; a failed or
  // zod-invalid response surfaces as a thrown labeled error (never silent).
  async function call<T>(
    op: () => ReturnType<Api[keyof Api]>,
  ): Promise<T> {
    const res = (await op()) as { data?: T; error?: unknown };
    if (res.error) {
      throw new Error(String(res.error));
    }
    return res.data as T;
  }

  async function refreshFleet() {
    try {
      const [fleet, modes, tools] = await Promise.all([
        call<{ control: "up" | "unreachable"; models: FleetModel[] }>(() =>
          deps.api.getFleet(),
        ),
        call<Mode[]>(() => deps.api.listModes()),
        call<ToolDef[]>(() => deps.api.listTools()),
      ]);
      patch({ fleet: { ...fleet, error: null, busy: state().fleet.busy }, modes, tools });
    } catch (e) {
      // A refresh failure (engine unreachable) is UI-visible; the last-known
      // projection stays rendered (ADR-0040 §3 — labeled, never silent).
      patch({
        fleet: {
          ...state().fleet,
          error: e instanceof Error ? e.message : String(e),
        },
      });
    }
  }

  // The fleet poll (ADR-0040 §4): every FLEET_POLL_INTERVAL_MS while the app
  // runs (the TUI has no visibility events). One in-flight guard.
  let pollTimer: ReturnType<typeof setInterval> | null = null;
  let refreshInFlight = false;

  async function pollTick() {
    if (refreshInFlight) return;
    refreshInFlight = true;
    try {
      await refreshFleet();
    } finally {
      refreshInFlight = false;
    }
  }

  function startFleetPoll() {
    if (pollTimer !== null) return;
    pollTimer = setInterval(() => void pollTick(), deps.fleetPollMs ?? FLEET_POLL_INTERVAL_MS);
  }

  function stopFleetPoll() {
    if (pollTimer !== null) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  async function openDocument(path: string) {
    const document = await call<Document>(() => deps.api.openDocument(path));
    const blocks = await call<Block[]>(() => deps.api.getBlocks(document.id));
    const history = await call<Revision[]>(() => deps.api.getHistory(document.id));
    patch({ document, blocks, history, sessions: [], currentSessionId: null, messages: [] });
  }

  async function loadSessions() {
    const s = state();
    if (!s.document) throw new Error("no open document");
    const sessions = await call<Session[]>(() =>
      deps.api.listSessions(s.document!.id),
    );
    patch({ sessions });
  }

  async function createSession(): Promise<string> {
    const s = state();
    if (!s.document) throw new Error("no open document");
    const session = await call<Session>(() =>
      deps.api.createSession({ documentId: s.document!.id }),
    );
    patch({
      currentSessionId: session.id,
      sessions: [session, ...s.sessions.filter((x) => x.id !== session.id)],
      messages: [],
    });
    return session.id;
  }

  async function selectSession(id: string) {
    const messages = await call<Message[]>(() => deps.api.getSessionMessages(id));
    patch({ currentSessionId: id, messages });
  }

  async function submitTurn(input: SubmitTurnInput) {
    patchTurn({
      active: true,
      tokens: "",
      meter: null,
      candidate: null,
      diffs: [],
      rag: null,
      done: null,
      error: null,
      backpressure: false,
    });
    const task: Task = {
      sessionId: input.sessionId,
      modeName: input.modeName,
      documentId: input.documentId,
      userInput: input.userInput,
    };
    if (input.options) task.options = input.options;

    await stream(
      state().connection.baseUrl ?? deps.baseUrl,
      task,
      {
        onEvent: (name: SseEventName, payload: unknown) => {
          switch (name) {
            case "token":
              patchTurn({ tokens: state().turn.tokens + (payload as { text: string }).text });
              break;
            case "meter": {
              const meter = payload as MeterEvent;
              const cumulative = { ...state().turn.cumulative };
              for (const c of METER_COMPONENTS) {
                cumulative[c] += meter[c] ?? 0;
              }
              patchTurn({ meter, cumulative });
              break;
            }
            case "candidate":
              patchTurn({ candidate: payload as CandidateEvent });
              break;
            case "diff":
              patchTurn({ diffs: [...state().turn.diffs, payload as DiffEvent] });
              break;
            case "rag":
              patchTurn({ rag: payload as RagEvent });
              break;
            case "done":
              patchTurn({ done: payload as DoneEvent, active: false });
              break;
            case "error":
              patchTurn({ error: payload as ErrorEvent, active: false });
              break;
            case "backpressure":
              patchTurn({ backpressure: true });
              break;
          }
        },
        onValidationError: (name, err) => {
          patchTurn({
            error: {
              code: "validation-failed",
              message: `${name ?? "unknown"} event failed boundary validation: ${err.message}`,
            },
          });
        },
      },
    );

    patchTurn({ active: false });
    // The turn may have edited the document: refresh blocks + messages.
    await refreshBlocks();
    if (state().currentSessionId) {
      await selectSession(state().currentSessionId!);
    }
  }

  async function refreshBlocks() {
    const s = state();
    if (!s.document) return;
    const blocks = await call<Block[]>(() => deps.api.getBlocks(s.document!.id));
    patch({ blocks });
  }

  // acceptCandidate fetches the staged candidate for a block (the candidate
  // SSE event carries blockId + revision, not the text), then applies and
  // commits it — the diff-preview "accept" path, all through the engine
  // (ADR-0013 §3).
  async function acceptCandidate(blockId: string) {
    const s = state();
    if (!s.document) throw new Error("no open document");
    const candidates = await call<{ blockId?: string; text?: string }[]>(() =>
      deps.api.getCandidates(s.document!.id, blockId),
    );
    const latest = candidates.at(-1);
    if (!latest?.text) {
      patchTurn({
        error: { code: "no-candidate", message: `no candidate staged for ${blockId}` },
      });
      return;
    }
    await call<Revision>(() =>
      deps.api.applyEdit(s.document!.id, { blockId, text: latest.text! }),
    );
    await call<Revision>(() => deps.api.commitDocument(s.document!.id));
    await refreshBlocks();
    patchTurn({ candidate: null, diffs: [] });
  }

  // switchModel (serving-control.feature "TUI switches models"): start the new
  // model (blocking on the daemon until up — a 200 means "up"), stop the old
  // one only after the new one is up. A failed start surfaces an error and
  // leaves the old model running.
  async function switchModel(from: string, to: string) {
    if (from === to) return;
    patch({ switching: { from, to } });
    try {
      await call(() => deps.api.startModel(to));
      if (from !== "") {
        await call(() => deps.api.stopModel(from));
      }
      patch({
        switching: null,
        turn: { ...state().turn, error: null },
      });
    } catch (err) {
      patch({
        switching: null,
        turn: {
          ...state().turn,
          error: {
            code: "model-switch-failed",
            message: `failed to start ${to}: ${String(err)} — ${from} left running`,
          },
        },
      });
      return;
    }
    await refreshFleet();
  }

  // startModel / stopModel (ADR-0040 §5): the switcher's one-verb remediation
  // actions. Busy flag during the verb, immediate fleet refresh after, and the
  // action error (with the provision hint for model-not-found) in fleet.error.
  async function startModel(name: string) {
    patch({ fleet: { ...state().fleet, busy: name } });
    let actionErr: unknown = null;
    try {
      await call(() => deps.api.startModel(name));
    } catch (err) {
      actionErr = err;
    }
    patch({ fleet: { ...state().fleet, busy: null } });
    await refreshFleet();
    if (actionErr) {
      patch({ fleet: { ...state().fleet, error: startErrorDisplay(name, actionErr) } });
    }
  }

  async function stopModel(name: string) {
    patch({ fleet: { ...state().fleet, busy: name } });
    let actionErr: unknown = null;
    try {
      await call(() => deps.api.stopModel(name));
    } catch (err) {
      actionErr = err;
    }
    patch({ fleet: { ...state().fleet, busy: null } });
    await refreshFleet();
    if (actionErr) {
      patch({
        fleet: { ...state().fleet, error: `failed to stop ${name}: ${String(actionErr)}` },
      });
    }
  }

  return {
    state,
    refreshFleet,
    openDocument,
    loadSessions,
    createSession,
    selectSession,
    submitTurn,
    acceptCandidate,
    switchModel,
    startModel,
    stopModel,
    startFleetPoll,
    stopFleetPoll,
    refreshBlocks,
  };
}

// startErrorDisplay appends the provision hint for provisioning-shaped codes
// (fleet-observability.feature "Start refused with model-not-found").
const PROVISION_HINT =
  "model not provisioned — see macos-dev-config/models.json";

function startErrorDisplay(name: string, err: unknown): string {
  const message = String(err);
  if (message.includes("model-not-found")) {
    return `${message} — ${PROVISION_HINT}`;
  }
  return `failed to start ${name}: ${message}`;
}
