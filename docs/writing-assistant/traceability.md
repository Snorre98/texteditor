# Traceability matrix

Rows = ADRs. Columns = arc42 sections, behavior contracts (Gherkin), precise
contracts, and quality scenarios. Every ADR maps to at least one arc42 section;
every §10.2 quality scenario maps to at least one behavior contract.
`contracts/module-boundaries.md` (base model) is a required precise-contract
row: ADR-0001/0016 map to it and to §2/§5.2/§8.

| ADR | arc42 section(s) | Behavior contract | Precise contract | Quality scenario |
|---|---|---|---|---|
| 0001 | §2, §5.2, §8 | — | module-boundaries | Q5 |
| 0002 | §3, §4, §6 | client-swap | interface | — |
| 0003 | §2, §4 | client-swap | interface | Q2 |
| 0004 | §4, §5.2, §6, §8 | versioning | data-model, concurrency-topology | Q4 |
| 0005 | §3.2, §4, §5.2 | provider-hotswap | interface | Q3 |
| 0006 | §2, §3.2, §4, §8 | serving-control | data-model | Q2 |
| 0007 | §3.2, §4, §8 | serving-control | interface, state-machine, failure-semantics | Q2 |
| 0008 | §3.2, §8 | serving-control | data-model | — |
| 0009 | §4, §5.2 | provider-hotswap | data-model | Q2 |
| 0010 | §4, §5.2 | token-metering | data-model | — |
| 0011 | §4, §5.2, §6, §8 | token-metering | interface | Q1 |
| 0012 | §6, §8 | token-metering, client-swap | interface, concurrency-topology | — |
| 0013 | §3, §4, §7 | client-swap | module-boundaries | — |
| 0014 | §7 | — | — | — |
| 0015 | §4, §8 | provider-hotswap | data-model | Q3 |
| 0016 | §5.2, §8 | provider-hotswap, token-metering, client-swap | module-boundaries, interface, data-model, state-machine, concurrency-topology | Q1, Q3, Q5 |
| 0017 | §3.2, §4, §6 | client-swap | interface | — |
| 0018 | §3.2, §4, §8 | serving-control | data-model, state-machine | Q2 |
| 0019 | §4, §5.2 | provider-hotswap, token-metering | data-model | Q2 |
| 0020 | §4, §5.2, §8 | versioning | data-model, interface, state-machine | Q4 |
| 0021 | §7 | — | concurrency-topology | — |
| 0022 | §10 | token-metering, provider-hotswap, versioning | failure-semantics | Q1, Q2, Q3, Q4, Q5 |
| 0023 | §3, §7 | client-swap | — | — |
| 0024 | §8 | token-metering | interface, failure-semantics | Q1 |
| 0025 | §3.2, §4, §7, §8 | serving-control | interface, module-boundaries, concurrency-topology | Q2 |
| 0026 | §5.2, §8 | sessions | data-model, interface, state-machine, concurrency-topology, module-boundaries | Q1 (per-session budget), Q5 |
| 0027 | §2, §5.2, §8 | serving-control | interface, module-boundaries | Q2 |
| 0028 | §4, §5.2, §8 | tool-routing | module-boundaries, interface, data-model, failure-semantics, state-machine | Q1 (router metered), Q2 (per-mode toggle), Q5 |
| 0029 | §4, §5.2, §8 | edit-integrity | module-boundaries, interface, data-model, failure-semantics, state-machine | Q4 |
| 0030 | §2, §3.2, §4, §8 | serving-control | data-model | Q2 |
| 0032 | §5.2, §7, §8 | serving-control | module-boundaries, interface | Q2 |
| 0033 | §3.2, §5.2, §7 | serving-control | module-boundaries, interface | Q2 |
| 0034 | §5, §7 | — | — | — |
| 0035 | §3.2, §5.2, §8 | workspace | interface, module-boundaries | — |
| 0036 | §5.2, §6, §8 | workspace, token-metering | interface, data-model, failure-semantics | Q1 |
| 0037 | §7 | client-swap | — | — |

## Behavior contract ↔ quality scenario coverage

| §10.2 scenario | Behavior contract |
|---|---|
| Q1 (transparent token cost) | token-metering.feature |
| Q2 (modifiability) | serving-control.feature, provider-hotswap.feature |
| Q3 (hot-swappable serving) | provider-hotswap.feature |
| Q4 (edit integrity) | versioning.feature |
| Q5 (testability) | client-swap.feature (dumb generated clients) |

## Behavior contract ↔ ADR coverage

| Behavior contract | ADRs |
|---|---|
| serving-control.feature | 0006, 0007, 0008, 0018, 0025, 0030, 0032 |
| provider-hotswap.feature | 0005, 0009, 0015, 0016, 0019 |
| token-metering.feature | 0011, 0016, 0022, 0024, 0036 |
| versioning.feature | 0004, 0020 |
| client-swap.feature | 0002, 0013, 0016, 0017, 0023, 0037 |
| sessions.feature | 0026 |
| tool-routing.feature | 0028 |
| edit-integrity.feature | 0029 |
| workspace.feature | 0035, 0036 |

## Supersession notes

- ADR-0003 → partially superseded by ADR-0017 (codegen tool selection: ogen, Hey
  API + Zod, openapi-to-rust locked).
- ADR-0005 → superseded by ADR-0016 (Resolve moves to Fleet; Provider is pure REST).
- ADR-0006 → superseded by ADR-0018 (two-tier manifest; daemon is the sole reader).
- ADR-0007 → partially superseded by ADR-0025 (the "no HTTP daemon" conclusion);
  the verb contract itself is unchanged.
- ADR-0009/0010 → superseded by ADR-0016/0019 (mode field set; tool registry split).
- ADR-0011 → superseded by ADR-0016 (meter scale-to-total), ADR-0022 (measurable
  target), and ADR-0024 (thinking fallback).
- ADR-0013 → partially superseded by ADR-0023 (Solid renderer only; TUI-first,
  Tauri-later still stands).
- ADR-0014 → partially superseded by ADR-0021 (sidecar spawn mechanics only; the
  three-target deployment concept stands).
- ADR-0016/0017/0020 → *extended* (not reversed) by ADR-0026: the `conversation_id`
  naming and `messages.db` filename are superseded by `Session`/`sessions.db`.
- ADR-0018 §2/§4 → superseded by ADR-0025/0027 (the shared manifest loader is the
  daemon's alone; `serve.sh` no longer parses `models.json` via `jq` and receives
  the parsed manifest from the daemon, and the engine reads none).
- ADR-0002/0016 → *clarified* (not reversed) by ADR-0027 (shared-DTO ownership;
  stream-seam carve-out; REST-first scoped to process boundaries).
- ADR-0018 §1/§3 → superseded by ADR-0030 (runner enum narrowed to
  `llama.cpp`/`mlx-lm`/`mlx-vlm`/`delegate`; `ollama`/`lmstudio` demoted).
- ADR-0008 §2 → superseded by ADR-0030 (source kinds narrowed to `hf`/`gguf`/
  `needle`; `ollama`/`lmstudio` provisioning dropped; Metal made a hard
  constraint).
- ADR-0025 §3 → *clarified* (not reversed) by ADR-0032, then amended by ADR-0033:
  "lives in `macos-dev-config`" means the daemon's *deployment* (a binary) *and*
  its *source* — ADR-0032 first pinned the source in `texteditor`
  (`cmd/fleetdaemon`); ADR-0033 moved it to `macos-dev-config`, making that repo
  the machine-local LLM control plane end to end. ADR-0032 also pins the
  previously-implicit serving lifecycle mechanics (serve.sh manifest seam,
  lanes/port-enforcement home, pre-bind gate owner, ACL↔manifest projection,
  launchd shape) — those stand under ADR-0033.
- ADR-0016/0017 → *extended* (not reversed) by ADR-0035 (the `Workspace` module
  and the `GET /directories` endpoint) and ADR-0036 (`Task.mentions`, the
  `Mentions` assembler/meter component, `ContextBudget.MaxMentionTokens`).
  The `Task`/`MeterEvent` spec amendments land in `api/openapi.yaml` at
  implementation, in lockstep with codegen regeneration.

Superseded ADRs remain in the log, untouched; supersession is recorded in the
superseding ADR's header and in the §9 index.
