# Runbook — edit `1-deliverable.md` with texteditor

**Target:** `/Users/snorresaether/Documents/Studies/Masters/obsidian-studies/H26/Game IT3021/deliverables/1-deliverable.md`

Covers all four modes — `drafter`, `editor`, `proofreader`, `grammar` — though the
deliverable task is line-editing, not drafting.

## 0. Verify prerequisites

```sh
go version                 # 1.26
bun --version              # 1.3.5
uv tool list               # mlx-lm 0.31.3, mlx-vlm 0.6.17
echo "$HF_HOME"            # /Volumes/Ex-SSD/models/huggingface
```

## 1. Install / update tooling

Already done on this machine — re-run only on a fresh one:

```sh
uv tool install "huggingface_hub[cli]"    # provides the `hf` CLI (huggingface-cli is now a deprecated alias)
uv tool upgrade mlx-vlm                   # already at 0.6.17; mlx-lm 0.31.3 is latest
# (zsh/00-path.zsh already puts ~/.local/bin on PATH; open a new terminal if needed)
```

Note: the daemon's `provision` verb still shells the deprecated `huggingface-cli`,
so installing `hf` does **not** fix `provision` — use Option A until
`macos-dev-config/internal/fleetdaemon/provision.go` is patched.

## 2. Download the model via the HF API

The editor uses **mode → default model** mappings (`server/config/modes/*.json`):

| Mode | Default model | HF repo (source.kind `hf`) | Size | Status |
|---|---|---|---|---|
| `editor` / `proofreader` | `gemma4-26b-moe` | `mlx-community/gemma-4-26B-A4B-it-OptiQ-4bit` | ~19 GB | **downloaded** |
| `grammar` | `llama3.1-8b` | `mlx-community/Llama-3.1-8B-Instruct-4bit` | ~5 GB | not downloaded |
| `drafter` | `mistral-24b` | `mlx-community/Mistral-Small-3.1-Text-24B-Instruct-2503-4bit` | ~14 GB | not downloaded |
| `drafter` (alt) | `qwen-35b-moe` | `mlx-community/Qwen3.6-35B-A3B-4bit` | ~20 GB | not downloaded |
| `editor` (alt) | `text` | `mlx-community/Llama-3.2-3B-Instruct-4bit` | ~2 GB | already cached |

`phi-4` (14B, **16K context**) is selectable in `editor`/`proofreader` as a light
alternative — paragraph-level passes only (ADR-0015 "short-pass polish").

`drafter` alternates also include `qwen-27b` (GGUF via `serve-qwen.sh`) — the
"writing from source" pool (ADR-0015).

**Option A — direct HF API:**

```sh
hf auth login                                           # already logged in as Snorre98 — skip unless the token expired
hf download mlx-community/gemma-4-26B-A4B-it-OptiQ-4bit    # editor + proofreader (Gemma 4 MoE, recommended)
hf download mlx-community/phi-4-4bit                       # lighter 14B alt (16K ctx → paragraph-level only)
hf download mlx-community/Mistral-Small-3.1-Text-24B-Instruct-2503-4bit   # drafter (best voice, recommended)
hf download mlx-community/Qwen3.6-35B-A3B-4bit                  # drafter alt (max quality)
# lighter editor fallback (already cached): hf download mlx-community/Llama-3.2-3B-Instruct-4bit
```

Downloads land in `$HF_HOME` (the SSD), which `mlx-lm` reads from — no `--local-dir`
needed for run-models.

### Gemma MoE (`gemma4-26b-moe`)

OptiQ sensitivity-aware mixed 4-bit (246 layers @8-bit, 79 @4-bit); beats uniform
4-bit on every benchmark. `mlx-lm` runner, port `8092`, tagged `editor` + `proofreader`.

- **Memory:** ~18 GB resident + KV (measured 17.7 GB peak at load). On 32 GB, run
  **one** big model at a time — don't load it alongside `qwen-27b`/`qwen-35b-moe`.
- **Tool-calling:** fixed upstream in Google's 2026-07-15 silent refresh; this
  quant (re-published 2026-07-20) ships the fixed canonical chat template. No
  known blocker for the agentic `editor` mode.
- **mlx-lm support:** verified — loads with stock `mlx-lm` 0.31.3 (no git build
  needed); the MoE text tower is in the PyPI release.
- **Context:** 131072 declared (256K native); raise only if memory allows.

**Option B — via the control daemon (canonical, ADR-0008):**

```sh
cd ~/Documents/Liv/Projects/macos-dev-config
go run ./cmd/fleetdaemon --manifest models.json --addr 127.0.0.1:9300
curl -X POST http://127.0.0.1:9300/provision/gemma4-26b-moe  # async, runs `huggingface-cli download`
curl http://127.0.0.1:9300/status/gemma4-26b-moe             # watch `provisioning` → `down` = done
```

> ⚠️ The daemon's `provision` verb shells `huggingface-cli`, which huggingface-hub
> v1.27.0 now deprecates in favour of `hf`. Prefer Option A (`hf download`) until
> `macos-dev-config/internal/fleetdaemon/provision.go` is updated to `hf download`.

## 3. Run the stack (three terminals)

```sh
# A — control daemon (macos-dev-config)
cd ~/Documents/Liv/Projects/macos-dev-config && go run ./cmd/fleetdaemon

# B — engine (texteditor), pinned port for a stable URL
cd ~/Documents/Liv/Projects/texteditor/server && go run ./cmd/texteditor --port 9100

# C — TUI
cd ~/Documents/Liv/Projects/texteditor/client/tui
bun install && bun run gen
bun run src/index.tsx "/Users/snorresaether/Documents/Studies/Masters/obsidian-studies/H26/Game IT3021/deliverables/1-deliverable.md"
```

## 4. Edit the document

- **Mode/model switcher** — pick `editor` (line editing, block replacement) or
  `proofreader` (grammar/clarity). Selecting `gemma4-26b-moe` makes the TUI `Start` its
  mlx-lm server on `:8092` via the daemon (auto, waits for health).
- **Chat input + `enter`** — submit a turn (`POST /turn`, live SSE stream).
- **Diff preview** — review the staged candidate; **`a`** applies + commits through the engine.
- **`esc`** — quit.

## 5. Verify / troubleshoot

```sh
curl 127.0.0.1:9300/list                 # fleet discovered through the daemon
curl 127.0.0.1:9300/status/all           # up/down/provisioning per model
curl 127.0.0.1:9100/health               # engine advertises baseUrl
```

- `model-not-found` (409) on start → you skipped step 2; run `provision`.
- First `start` can exceed the 60 s health wait if the model isn't downloaded yet — provision first.
