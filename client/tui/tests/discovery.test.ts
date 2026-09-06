import { describe, expect, test } from "bun:test";
import { DEFAULT_BASE_URL, discover, probeHealth, resolveBaseUrl, type FetchLike } from "../src/api/discovery";

describe("resolveBaseUrl (ADR-0021 §1 fixed mode)", () => {
  test("falls back to the spec servers[0] default", () => {
    expect(resolveBaseUrl({})).toBe(DEFAULT_BASE_URL);
  });

  test("ENGINE_PORT forces a fixed localhost port", () => {
    expect(resolveBaseUrl({ ENGINE_PORT: "9123" })).toBe("http://127.0.0.1:9123");
  });

  test("ENGINE_URL overrides everything (web/LAN target)", () => {
    expect(resolveBaseUrl({ ENGINE_URL: "http://192.168.1.9:9100/", ENGINE_PORT: "9123" })).toBe(
      "http://192.168.1.9:9100",
    );
  });

  test("rejects a non-numeric ENGINE_PORT", () => {
    expect(() => resolveBaseUrl({ ENGINE_PORT: "not-a-port" })).toThrow(/ENGINE_PORT/);
  });
});

describe("probeHealth", () => {
  const okFetch = (async () => new Response(JSON.stringify({ status: "ok" }))) as FetchLike;
  const badFetch = (async () => new Response(JSON.stringify({ status: "down" }), { status: 500 })) as FetchLike;
  const throwFetch = (async () => {
    throw new Error("connection refused");
  }) as FetchLike;

  test("accepts an ok /health reply", async () => {
    expect(await probeHealth("http://x", okFetch)).toBe(true);
  });

  test("rejects a non-2xx reply", async () => {
    expect(await probeHealth("http://x", badFetch)).toBe(false);
  });

  test("rejects an unreachable engine", async () => {
    expect(await probeHealth("http://x", throwFetch)).toBe(false);
  });
});

describe("discover", () => {
  test("returns the resolved URL when the engine answers", async () => {
    const okFetch = (async () => new Response(JSON.stringify({ status: "ok" }))) as FetchLike;
    const base = await discover({ ENGINE_PORT: "9100" }, okFetch);
    expect(base).toBe("http://127.0.0.1:9100");
  });

  test("throws a user-facing hint when unreachable", async () => {
    const throwFetch = (async () => {
      throw new Error("connection refused");
    }) as FetchLike;
    await expect(discover({}, throwFetch)).rejects.toThrow(/engine unreachable/);
  });
});
