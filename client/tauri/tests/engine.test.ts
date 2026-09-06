import { describe, expect, test } from "bun:test";
import {
  DEFAULT_ENGINE_URL,
  probeHealth,
  resolveWebEngineUrl,
  type FetchLike,
} from "../src/engine";

describe("resolveWebEngineUrl (web fixed-mode resolution)", () => {
  test("defaults to the spec servers[0]", () => {
    expect(resolveWebEngineUrl()).toBe(DEFAULT_ENGINE_URL);
  });

  test("ENGINE_URL override wins (trailing slash stripped)", () => {
    expect(resolveWebEngineUrl({ ENGINE_URL: "http://192.168.1.10:9100/" })).toBe(
      "http://192.168.1.10:9100",
    );
  });

  test("ENGINE_PORT pins a fixed port on 127.0.0.1", () => {
    expect(resolveWebEngineUrl({ ENGINE_PORT: "9123" })).toBe(
      "http://127.0.0.1:9123",
    );
  });

  test("non-numeric ENGINE_PORT throws", () => {
    expect(() => resolveWebEngineUrl({ ENGINE_PORT: "abc" })).toThrow(
      /numeric port/,
    );
  });
});

describe("probeHealth", () => {
  const okFetch: FetchLike = async () =>
    new Response(JSON.stringify({ status: "ok" }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  const downFetch: FetchLike = async () =>
    new Response(JSON.stringify({ status: "error" }), { status: 500 });

  test("true when /health reports ok", async () => {
    expect(await probeHealth("http://engine", okFetch)).toBe(true);
  });

  test("false when /health is not ok", async () => {
    expect(await probeHealth("http://engine", downFetch)).toBe(false);
  });

  test("false on network failure", async () => {
    const boom: FetchLike = async () => {
      throw new Error("connection refused");
    };
    expect(await probeHealth("http://engine", boom)).toBe(false);
  });
});
