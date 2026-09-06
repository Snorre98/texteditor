// engine.ts — how the Vue app reaches the engine, per target (ADR-0021 §1).
//
// Tauri target: the Rust core spawned the engine as a sidecar and holds the
// discovered base URL; the frontend asks for it via `invoke("get_engine_base_url")`.
// Web target (E6): ENGINE_URL → ENGINE_PORT → the spec's servers[0] default
// (mirrors client/tui/src/api/discovery.ts, which is the fixed-mode TUI path).
// The resolved URL is then verified with a /health probe, so an unreachable
// engine is an explicit error, never a silent failure.

export const DEFAULT_ENGINE_URL = "http://127.0.0.1:9100"; // api/openapi.yaml servers[0]

export interface EngineEnv {
  ENGINE_URL?: string;
  ENGINE_PORT?: string;
}

export type FetchLike = (
  input: string | URL | Request,
  init?: RequestInit,
) => Promise<Response>;

export function isTauri(): boolean {
  return typeof window !== "undefined" && "__TAURI_INTERNALS__" in window;
}

export function envFromVite(): EngineEnv {
  return {
    ENGINE_URL: import.meta.env.VITE_ENGINE_URL,
    ENGINE_PORT: import.meta.env.VITE_ENGINE_PORT,
  };
}

// The web-target resolution, kept pure and testable (no `import.meta`, no
// `window`). Fixed mode: ENGINE_URL full override, else ENGINE_PORT on 127.0.0.1,
// else the spec default.
export function resolveWebEngineUrl(env: EngineEnv = {}): string {
  const engineUrl = env.ENGINE_URL?.trim();
  if (engineUrl) return stripTrailingSlash(engineUrl);
  const port = env.ENGINE_PORT?.trim();
  if (port) {
    if (!/^\d+$/.test(port)) {
      throw new Error(`ENGINE_PORT must be a numeric port, got ${JSON.stringify(port)}`);
    }
    return `http://127.0.0.1:${port}`;
  }
  return DEFAULT_ENGINE_URL;
}

export async function resolveEngineUrl(env: EngineEnv = {}): Promise<string> {
  if (isTauri()) {
    const { invoke } = await import("@tauri-apps/api/core");
    const url = await invoke<string>("get_engine_base_url");
    return stripTrailingSlash(url);
  }
  return resolveWebEngineUrl(env);
}

export async function probeHealth(
  baseUrl: string,
  fetchImpl: FetchLike = fetch,
  timeoutMs = 3000,
): Promise<boolean> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const res = await fetchImpl(`${baseUrl}/health`, {
      signal: ctrl.signal,
      headers: { Accept: "application/json" },
    });
    if (!res.ok) return false;
    const body = (await res.json()) as { status?: string };
    return body.status === "ok";
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}

export async function discoverEngineUrl(
  env: EngineEnv = envFromVite(),
  fetchImpl: FetchLike = fetch,
): Promise<string> {
  const baseUrl = await resolveEngineUrl(env);
  if (!(await probeHealth(baseUrl, fetchImpl))) {
    throw new Error(
      `engine unreachable at ${baseUrl} — is it running? ` +
        "(the engine needs the control daemon on :9300, ADR-0025; the Tauri app spawns it as a sidecar)",
    );
  }
  return baseUrl;
}

function stripTrailingSlash(url: string): string {
  return url.endsWith("/") ? url.slice(0, -1) : url;
}
