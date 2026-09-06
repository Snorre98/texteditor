// Client state — Vue 3 reactive store over the generated client (ADR-0023).
// The store is the single seam between the CodeMirror/Vue components and the
// engine: it holds no domain logic (the engine owns that), only fetch/stream
// lifecycle and UI-observable state. Every effect the editor can cause is one
// action here; components render the reactive state, never call the API directly.
//
// Slices: connection, models (+liveState), modes, tools, document (+block tree
// +history), sessions, and the live turn stream — tokens, meter, candidate/diff/
// rag queues, done/error, backpressure. Session scoping (ADR-0026 §1/§4): multiple
// selection-anchored bubbles plus one doc-level chat stream simultaneously, so
// `{messages, turn}` is keyed PER SESSION (`sessionStates: Record<sessionId, …>`),
// generalizing the TUI's single `turn`/`messages` slice to a map. One turn per
// session, sessions in parallel.
import { reactive } from "vue";
import type {
  Block,
  BlockWrite,
  CandidateEvent,
  DiffEvent,
  Document,
  DoneEvent,
  ErrorEvent,
  Message,
  MeterEvent,
  Mode,
  Model,
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

/** Per-session state: message history + the session's live turn (ADR-0026 §4). */
export interface SessionState {
  messages: Message[];
  turn: TurnState;
}

export interface EngineState {
  connection: ConnectionState;
  models: Model[];
  modes: Mode[];
  tools: ToolDef[];
  document: Document | null;
  blocks: Block[];
  history: Revision[];
  sessions: Session[];
  /** `{messages, turn}` keyed per session — concurrent bubbles (ADR-0026 §1/§4). */
  sessionStates: Record<string, SessionState>;
  /** Model-switch progress ("serving-control.feature: TUI switches models"). */
  switching: { from: string; to: string } | null;
}

export interface StoreDeps {
  api: Api;
  baseUrl: string;
  stream?: typeof streamTurn;
}

export interface SubmitTurnInput {
  sessionId: string;
  modeName: string;
  documentId: string;
  userInput: string;
  options?: TurnOptions;
}

export interface AppStore {
  state: EngineState;
  refreshFleet: () => Promise<void>;
  refreshModels: () => Promise<void>;
  openDocument: (path: string) => Promise<void>;
  loadSessions: () => Promise<void>;
  /** create-or-resume; anchorBlockId set = selection/bubble anchor (ADR-0026 §1). */
  createSession: (anchorBlockId?: string, modeType?: string) => Promise<string>;
  /** Restores a session's message history (reopening a bubble, ADR-0026 §2). */
  selectSession: (id: string) => Promise<void>;
  submitTurn: (input: SubmitTurnInput) => Promise<void>;
  /** Diff-preview "accept": fetch the staged candidate, apply, commit. */
  acceptCandidate: (blockId: string) => Promise<void>;
  /** Fetch the latest staged candidate text for a block (merge preview). */
  getCandidateText: (blockId: string) => Promise<string | null>;
  /** Manual autosave: send the whole block tree (ADR-0038). */
  saveTree: (tree: BlockWrite[]) => Promise<void>;
  /** serving-control.feature "TUI switches models": start new → up → stop old. */
  switchModel: (from: string, to: string) => Promise<void>;
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

function freshSessionState(): SessionState {
  return {
    messages: [],
    turn: { ...EMPTY_TURN, cumulative: { ...ZERO_TALLY } },
  };
}

function initialState(baseUrl: string): EngineState {
  return {
    connection: { baseUrl, error: null },
    models: [],
    modes: [],
    tools: [],
    document: null,
    blocks: [],
    history: [],
    sessions: [],
    sessionStates: {},
    switching: null,
  };
}

// createAppStore wires the generated, zod-validated client into Vue reactive
// state. All API effects go through here; the UI is dumb (ADR-0013 §3).
export function createAppStore(deps: StoreDeps): AppStore {
  const state = reactive<EngineState>(initialState(deps.baseUrl));
  const stream = deps.stream ?? streamTurn;

  // Ensure a session's `{messages, turn}` slice exists and return it.
  const sessionState = (id: string): SessionState => {
    let s = state.sessionStates[id];
    if (!s) {
      s = freshSessionState();
      state.sessionStates[id] = s;
    }
    return s;
  };

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

  async function refreshModels() {
    state.models = await call<Model[]>(() => deps.api.listModels());
  }

  async function refreshFleet() {
    const [models, modes, tools] = await Promise.all([
      call<Model[]>(() => deps.api.listModels()),
      call<Mode[]>(() => deps.api.listModes()),
      call<ToolDef[]>(() => deps.api.listTools()),
    ]);
    state.models = models;
    state.modes = modes;
    state.tools = tools;
  }

  async function openDocument(path: string) {
    const document = await call<Document>(() => deps.api.openDocument(path));
    const blocks = await call<Block[]>(() => deps.api.getBlocks(document.id));
    const history = await call<Revision[]>(() => deps.api.getHistory(document.id));
    state.document = document;
    state.blocks = blocks;
    state.history = history;
    state.sessions = [];
    state.sessionStates = {};
  }

  async function loadSessions() {
    if (!state.document) throw new Error("no open document");
    state.sessions = await call<Session[]>(() =>
      deps.api.listSessions(state.document!.id),
    );
  }

  async function createSession(anchorBlockId?: string, modeType?: string): Promise<string> {
    if (!state.document) throw new Error("no open document");
    const body: { documentId: string; anchorBlockId?: string; modeType?: string } = {
      documentId: state.document.id,
    };
    if (anchorBlockId) body.anchorBlockId = anchorBlockId;
    if (modeType) body.modeType = modeType;
    const session = await call<Session>(() => deps.api.createSession(body));
    state.sessions = [session, ...state.sessions.filter((x) => x.id !== session.id)];
    sessionState(session.id);
    return session.id;
  }

  async function selectSession(id: string) {
    const messages = await call<Message[]>(() => deps.api.getSessionMessages(id));
    sessionState(id).messages = messages;
  }

  async function submitTurn(input: SubmitTurnInput) {
    const ss = sessionState(input.sessionId);
    Object.assign(ss.turn, {
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

    const turn = ss.turn;
    await stream(
      state.connection.baseUrl ?? deps.baseUrl,
      task,
      {
        onEvent: (name: SseEventName, payload: unknown) => {
          switch (name) {
            case "token":
              turn.tokens += (payload as { text: string }).text;
              break;
            case "meter": {
              const meter = payload as MeterEvent;
              const cumulative = turn.cumulative;
              for (const c of METER_COMPONENTS) {
                cumulative[c] += meter[c] ?? 0;
              }
              turn.meter = meter;
              break;
            }
            case "candidate":
              turn.candidate = payload as CandidateEvent;
              break;
            case "diff":
              turn.diffs = [...turn.diffs, payload as DiffEvent];
              break;
            case "rag":
              turn.rag = payload as RagEvent;
              break;
            case "done":
              turn.done = payload as DoneEvent;
              turn.active = false;
              break;
            case "error":
              turn.error = payload as ErrorEvent;
              turn.active = false;
              break;
            case "backpressure":
              turn.backpressure = true;
              break;
          }
        },
        onValidationError: (name, err) => {
          turn.error = {
            code: "validation-failed",
            message: `${name ?? "unknown"} event failed boundary validation: ${err.message}`,
          };
        },
      },
    );

    turn.active = false;
    // The turn may have edited the document: refresh blocks + messages.
    await refreshBlocks();
    await selectSession(input.sessionId);
  }

  async function refreshBlocks() {
    if (!state.document) return;
    state.blocks = await call<Block[]>(() => deps.api.getBlocks(state.document!.id));
  }

  // acceptCandidate fetches the staged candidate for a block (the candidate
  // SSE event carries blockId + revision, not the text), then applies and
  // commits it — the diff-preview "accept" path, all through the engine
  // (ADR-0013 §3).
  async function acceptCandidate(blockId: string) {
    if (!state.document) throw new Error("no open document");
    const candidates = await call<{ blockId?: string; text?: string }[]>(() =>
      deps.api.getCandidates(state.document!.id, blockId),
    );
    const latest = candidates.at(-1);
    if (!latest?.text) {
      for (const id of Object.keys(state.sessionStates)) {
        state.sessionStates[id].turn.error = {
          code: "no-candidate",
          message: `no candidate staged for ${blockId}`,
        };
      }
      return;
    }
    await call<Revision>(() =>
      deps.api.applyEdit(state.document!.id, { blockId, text: latest.text! }),
    );
    await call<Revision>(() => deps.api.commitDocument(state.document!.id));
    await refreshBlocks();
  }

  // getCandidateText fetches the latest staged candidate for a block (the merge
  // preview's "right" side) without applying it — the candidate SSE event carries
  // blockId + revision, not the text.
  async function getCandidateText(blockId: string): Promise<string | null> {
    if (!state.document) throw new Error("no open document");
    const candidates = await call<{ blockId?: string; text?: string }[]>(() =>
      deps.api.getCandidates(state.document!.id, blockId),
    );
    return candidates.at(-1)?.text ?? null;
  }

  // saveTree sends the manual-edit whole-tree snapshot (ADR-0038). The engine
  // reconciles, mints IDs for new blocks, formats, and commits iff changed.
  async function saveTree(tree: BlockWrite[]) {
    if (!state.document) throw new Error("no open document");
    await call<Revision>(() => deps.api.saveDocument(state.document!.id, { blocks: tree }));
    await refreshBlocks();
  }

  // switchModel (serving-control.feature "TUI switches models"): start the new
  // model (blocking on the daemon until up — a 200 means "up"), stop the old
  // one only after the new one is up. A failed start surfaces an error and
  // leaves the old model running.
  async function switchModel(from: string, to: string) {
    if (from === to) return;
    state.switching = { from, to };
    try {
      await call(() => deps.api.startModel(to));
      if (from !== "") {
        await call(() => deps.api.stopModel(from));
      }
      state.switching = null;
    } catch (err) {
      state.switching = null;
      state.connection.error =
        `failed to start ${to}: ${String(err)} — ${from} left running`;
      return;
    }
    await refreshModels();
  }

  return {
    state,
    refreshFleet,
    refreshModels,
    openDocument,
    loadSessions,
    createSession,
    selectSession,
    submitTurn,
    acceptCandidate,
    getCandidateText,
    saveTree,
    switchModel,
    refreshBlocks,
  };
}
