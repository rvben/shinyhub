import assert from "node:assert/strict";
import test from "node:test";
import { runBenchmark } from "../scripts/container-benchmark.mjs";

test("benchmark measures actual modes, verifies health, and stops after sampling", async () => {
  for (const mode of ["fresh", "snapshot"]) {
    const calls: string[] = [];
    let starts = 0;
    const result = await runBenchmark({ url: "https://shinyhub-container-preview.example.workers.dev", token: "fixture", mode, runs: 2 }, async (url: URL, init: RequestInit) => {
      assert.equal(init.headers!["authorization"], "Bearer fixture");
      assert.equal(init.redirect, "error");
      calls.push(`${init.method} ${url.pathname}`);
      if (url.pathname === "/state") return Response.json({ running: false });
      if (url.pathname === "/snapshot" && init.method === "POST") return Response.json({ id: "snapshot", size: 1000 });
      if (url.pathname === "/start") return Response.json({ mode: mode === "snapshot" && starts++ > 0 ? "snapshot" : "image", readyMs: 650 });
      return new Response(null, { status: 204 });
    });
    assert.equal(result.medianReadyMs, 650);
    assert.equal(result.samples.length, 2);
    assert.equal(calls.at(-1), "POST /stop");
    assert.equal(calls.filter((c) => c === "GET /control/healthz").length, 2);
  }
});

test("benchmark failure cleans up and does not count fallback as a snapshot success", async () => {
  const calls: string[] = [];
  let starts = 0;
  await assert.rejects(runBenchmark({ url: "http://localhost:8787", token: "fixture", mode: "snapshot", runs: 1 }, async (url: URL, init: RequestInit) => {
    calls.push(`${init.method} ${url.pathname}`);
    if (url.pathname === "/state") return Response.json({ running: false });
    if (url.pathname === "/snapshot" && init.method === "POST") return Response.json({ size: 100 });
    if (url.pathname === "/start") return Response.json({ mode: starts++ === 0 ? "image" : "image-after-snapshot-failure", readyMs: 900 });
    return new Response(null, { status: 204 });
  }), /fallback/);
  assert.equal(calls.at(-1), "POST /stop");
});

test("benchmark refuses production origins and invalid options before requests", async () => {
  for (const options of [
    { url: "https://demo.shinyhub.dev", token: "fixture" },
    { url: "http://localhost:8787", token: "" },
    { url: "http://localhost:8787", token: "fixture", runs: 0 },
  ]) {
    await assert.rejects(runBenchmark(options, async () => { assert.fail("must not make requests"); }));
  }
});

test("benchmark failures retain completed samples without reporting a successful median", async () => {
  let starts = 0;
  await assert.rejects(runBenchmark({ url: "http://localhost:8787", token: "fixture", runs: 2 }, async (url: URL) => {
    if (url.pathname === "/state") return Response.json({ running: false });
    if (url.pathname === "/start") {
      if (starts++ > 0) return new Response(null, { status: 503 });
      return Response.json({ mode: "image", readyMs: 500 });
    }
    return new Response(null, { status: 204 });
  }), (error: Error & { benchmark: { completedRuns: number; samples: unknown[]; medianReadyMs?: number } }) => {
    assert.equal(error.benchmark.completedRuns, 1);
    assert.equal(error.benchmark.samples.length, 1);
    assert.equal(error.benchmark.medianReadyMs, undefined);
    return true;
  });
});
