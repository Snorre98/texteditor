# Concurrency topology contract

The bounded graph: which queue feeds what, capacities, backpressure, and
single-writer serialization. Source ADRs: ADR-0012 (event bus), ADR-0016 (turn
async + turnID correlation), ADR-0020 (storage), ADR-0025 (daemon).

## 1. The graph

```mermaid
flowchart LR
    Prov[Provider gateway] -->|raw token/done/error| Loop[Agent loop]
    Loop -->|token/candidate/diff/done| Bus[SSE event bus]
    Meter[Token metering] -->|meter events| Bus
    Bus -->|bounded chan| API[API server]
    API -->|SSE| Client[Client]
    Fleet[Fleet gateway] -->|HTTP| Daemon[Control daemon]
    Daemon --> Exec[serve.sh]
    Doc[Document store] --> Git[(git repo)]:::sw
    Doc --> DB1[(app.db)]:::sw
    Meter --> DB2[(meter.db)]:::sw
    Conv[Conversation store] --> DB3[(messages.db)]:::sw
    Retriever[Retriever] --> DB4[(index.db)]:::sw
    Loop --> Doc
    Loop --> Prov
    Loop --> Fleet
    Loop --> Retriever
    classDef sw fill:#f0f0f0,stroke:#999
```

Every queue is bounded. Producers block (or drop with a labeled backpressure
event) when a queue is full — never unbounded buffering.

## 2. Queues and capacities

| Queue | Feeds | Capacity | On overflow |
|---|---|---|---|
| `EventBus` per-subscriber chan | API server → client | 256 events | drop oldest + emit `backpressure` event |
| agent-loop dispatch queue | tool calls | 1 in flight | single-turn, single-tool-at-a-time |
| provider stream | SSE events | unbounded at source, bounded at subscriber | backpressure via `Subscribe` chan |
| daemon request queue | lifecycle verbs | 1 in flight per daemon | serialized (start/stop/provision are serial) |

## 3. Backpressure behavior

- A slow client does **not** slow the provider or the engine: the bus drops the
  client's oldest buffered events and emits one `backpressure` event so the UI can
  show "stream dropped."
- Tool dispatch is serial (one in flight per turn); a second tool call waits
  until the first observes (state-machine §1.2).
- Lifecycle verbs through the daemon are serialized per daemon (one `start`/
  `stop`/`provision` at a time); concurrent verbs queue.

## 4. Single-writer serialization

- Each SQLite file (`app.db`, `index.db`, `meter.db`, `messages.db`) and the git
  repo are **single-writer**, owned by exactly one service (ADR-0016). No module
  reads/writes another's file.
- **Meter events** are append-only, single-writer (the Token metering module).
- **Provider calls** are serialized per model server: the provider gateway does
  not fan out concurrent chats to the same `-np 1` slot server; it queues them.
- **The manifest** is read solely by the control daemon; the engine's Fleet reads
  and writes serving state only through the daemon's HTTP contract (ADR-0025).

## 5. Invariants

- No unbounded buffer anywhere; every queue has a stated capacity.
- At most one tool call is in flight per agent-loop turn.
- Single-writer stores (SQLite files, git) are never written concurrently.
- Backpressure propagates to the source only as a *labeled* drop, never silently.
- Per-turn events are correlated by `turnID`; the API server demultiplexes one
  turn's stream to exactly one client connection (ADR-0016).
