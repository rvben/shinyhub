import { parseArgs } from "node:util";
import { fileURLToPath } from "node:url";

export async function runBenchmark({ url, token, mode = "fresh", runs = 3 }, fetcher = fetch) {
  const origin = new URL(url);
  const local = ["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname);
  const preview = /^shinyhub-container-(preview|baseline)\.[a-z0-9-]+\.workers\.dev$/.test(origin.hostname);
  if ((!local && (!preview || origin.protocol !== "https:")) || origin.username || origin.password || origin.pathname !== "/" || origin.search || origin.hash) {
    throw new Error("Use the isolated container preview/baseline workers.dev origin, or a loopback development origin");
  }
  if (!token || !["fresh", "snapshot"].includes(mode) || !Number.isInteger(runs) || runs < 1 || runs > 20) {
    throw new Error("Provide CONTAINER_BENCHMARK_TOKEN, --mode fresh|snapshot, and --runs 1..20");
  }
  async function call(path, method = "GET") {
    const response = await fetcher(new URL(path, origin), {
      method, headers: { authorization: `Bearer ${token}`, "user-agent": "shinyhub-container-benchmark" },
      redirect: "error", signal: AbortSignal.timeout(210_000),
    });
    if (!response.ok) throw new Error(`Preview ${method} ${path} returned HTTP ${response.status}`);
    return response;
  }
  const samples = [];
  let snapshotBytes;
  try {
    await call("/snapshot", "DELETE");
    await call("/stop", "POST");
    if (mode === "snapshot") {
      const setup = await (await call("/start", "POST")).json();
      if (setup.mode !== "image") throw new Error("Snapshot setup must start from a fresh image");
      snapshotBytes = (await (await call("/snapshot", "POST")).json()).size;
    }
    for (let i = 0; i < runs; i++) {
      await call("/stop", "POST");
      if ((await (await call("/state")).json()).running) throw new Error("Container is still running after stop");
      const startedAt = performance.now();
      const result = await (await call("/start", "POST")).json();
      const wallMs = Math.round(performance.now() - startedAt);
      const expected = mode === "snapshot" ? "snapshot" : "image";
      if (result.mode !== expected) throw new Error(`Expected ${expected} boot, received ${result.mode}; do not treat fallback as a snapshot measurement`);
      const health = await call("/control/healthz");
      await health.body?.cancel();
      samples.push({ ...result, wallMs, healthzStatus: health.status });
    }
  } catch (error) {
    if (error instanceof Error) {
      error.benchmark = { mode, runsRequested: runs, completedRuns: samples.length, snapshotBytes, samples };
    }
    throw error;
  } finally {
    // Always attempt cleanup, including after a failed sample. Verify /state
    // afterward if the platform rejects or stalls the stop operation.
    await call("/stop", "POST");
  }
  const sorted = samples.map((s) => s.readyMs).sort((a, b) => a - b);
  const middle = Math.floor(sorted.length / 2);
  const medianReadyMs = sorted.length % 2 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2;
  return { mode, runs, medianReadyMs, snapshotBytes, samples };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const { values } = parseArgs({ options: {
      url: { type: "string" }, mode: { type: "string", default: "fresh" }, runs: { type: "string", default: "3" },
    } });
    const result = await runBenchmark({ ...values, runs: Number(values.runs), token: process.env.CONTAINER_BENCHMARK_TOKEN });
    console.log(JSON.stringify(result, null, 2));
  } catch (error) {
    if (error.benchmark) console.log(JSON.stringify({ error: error.message, ...error.benchmark }, null, 2));
    console.error(error.message);
    process.exitCode = 1;
  }
}
