// Port discovery (ADR-0021 §1, fixed mode — the TUI target).
//
// The engine's base URL is resolved as:
//   1. ENGINE_URL  — a full base URL override (web/LAN target, ADR-0014).
//   2. ENGINE_PORT — a fixed port on 127.0.0.1 (ADR-0021 §1 fixed mode).
//   3. the spec's servers[0] default (http://127.0.0.1:9100, api/openapi.yaml).
//
// The resolved URL is then verified with a /health probe, so a stale port
// surfaces as an explicit "engine unreachable" screen instead of a silent
// fetch failure. Dynamic port discovery (no ENGINE_PORT → engine picks a free
// port and advertises it) is Plan E (Track 2, ADR-0021 §1 + E3/E4): the
// engine has no dynamic-port mode or base-URL advertisement yet. This module
// is the seam where that resolver will slot in — callers only ever consume
// the resolved baseUrl.

export interface EngineEnv {
  ENGINE_URL?: string;
  ENGINE_PORT?: string;
}

// The slimmest fetch shape we need — avoids `typeof fetch` (bun's fetch
// carries extra statics like preconnect) in tests and the runtime seam.
export type FetchLike = (
  input: string | URL | Request,
  init?: RequestInit,
) => Promise<Response>;

export const DEFAULT_BASE_URL = "http://127.0.0.1:9100"; // api/openapi.yaml servers[0]

// envFrom picks the two relevant keys out of a process-env-shaped record so
// callers can pass process.env directly without index-signature friction.
export function envFrom(env: { [k: string]: string | undefined }): EngineEnv {
  return {
    ENGINE_URL: env.ENGINE_URL,
    ENGINE_PORT: env.ENGINE_PORT,
  };
}

export function resolveBaseUrl(env: EngineEnv = {}): string {
  const engineUrl = env.ENGINE_URL?.trim();
  if (engineUrl) {
    return stripTrailingSlash(engineUrl);
  }
  const port = env.ENGINE_PORT?.trim();
  if (port) {
    if (!/^\d+$/.test(port)) {
      throw new Error(
        `ENGINE_PORT must be a numeric port, got ${JSON.stringify(port)}`,
      );
    }
    return `http://127.0.0.1:${port}`;
  }
  return DEFAULT_BASE_URL;
}

export async function probeHealth(
  baseUrl: string,
  fetchImpl: FetchLike = fetch,
  timeoutMs = 1500,
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

// discover resolves the base URL and verifies the engine answers /health.
// It throws with a user-facing hint when the engine is unreachable, so the UI
// can render an explicit connection screen rather than failing downstream.
// Pass no env to read ENGINE_URL/ENGINE_PORT from process.env.
export async function discover(
  env: EngineEnv = envFrom(process.env),
  fetchImpl: FetchLike = fetch,
): Promise<string> {
  const baseUrl = resolveBaseUrl(env);
  if (!(await probeHealth(baseUrl, fetchImpl))) {
    throw new Error(
      `engine unreachable at ${baseUrl} — is it running? ` +
        "(start it with `go run ./cmd/texteditor`, or set ENGINE_PORT/ENGINE_URL to the actual endpoint)",
    );
  }
  return baseUrl;
}

function stripTrailingSlash(url: string): string {
  return url.endsWith("/") ? url.slice(0, -1) : url;
}
