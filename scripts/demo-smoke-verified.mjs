import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

const require = createRequire(new URL("../deploy/cloudflare-demo/package.json", import.meta.url));
const WebSocket = require("ws");

export async function waitForTelemetry(probe, observed, signal, { timeoutMS = 30_000, intervalMS = 1_000 } = {}) {
  const deadline = Date.now() + timeoutMS;
  while (!observed()) {
    if (Date.now() >= deadline) throw new Error("Timed out waiting for correlated server telemetry");
    await probe();
    if (!observed()) await delay(intervalMS, undefined, { signal });
  }
}

export const websocketPaths = [
  "/app/operations-dashboard/websocket/",
  "/app/bookmarking-demo/websocket/",
  "/app/streamlit-demo/_stcore/stream",
];

const diagnosticPaths = new Set(["/", "/login", "/healthz", "/__demo/start", "/__demo/ready",
  "/__demo/status", "/__demo/session", "/api/server-info", "/api/auth/me", ...websocketPaths]);

// Consume only this smoke run's records. Never print/store raw tail payloads:
// unrelated visitors' request headers can include session cookies.
export function smokeInspector(id) {
  const closed = new Set();
  const connections = new Set();
  let observed = false;
  let failure;
  return {
    ingest(record) {
      if (record.type === "tail-info" || (record.event?.type && record.event?.message)) { failure = "Tail stream reported incomplete telemetry"; return; }
      const request = record.event?.request;
      const headers = request?.headers ?? {};
      const logMessages = (record.logs ?? []).flatMap(log => log.message ?? []).flatMap(message => {
        if (typeof message !== "string") return [message];
        try { return [JSON.parse(message)]; } catch { return []; }
      });
      let url;
      try { url = request?.url ? new URL(request.url) : undefined; } catch { return; }
      const correlated = Object.entries(headers).some(([key, value]) =>
        key.toLowerCase() === "x-shinyhub-demo-smoke" && value === id)
        || url?.searchParams.get("__demo_smoke") === id
        || logMessages.some(message => message?.smoke_id === id);
      if (!correlated) return;
      observed = true;
      const path = diagnosticPaths.has(url?.pathname) ? url.pathname : "<other>";
      // Cloudflare can mark a streamed HTTP invocation canceled after the
      // client has consumed its successful response. The smoke suite checks
      // that response separately; upgrades still require clean server closes.
      const completedHTTP = record.outcome === "canceled"
        && record.event?.response?.status >= 200 && record.event.response.status < 400;
      // A transparent upgrade can end with either disconnect outcome even when
      // its client and container relay both completed normal close handshakes.
      // It cannot pass check() until independent DO evidence covers every app.
      const disconnectedUpgrade = ["responseStreamDisconnected", "canceled"].includes(record.outcome)
        && request?.method === "GET"
        && websocketPaths.includes(url?.pathname)
        && Object.entries(headers).some(([key, value]) => key.toLowerCase() === "upgrade" && value?.toLowerCase() === "websocket");
      if ((record.exceptions?.length ?? 0) > 0 || (record.outcome && record.outcome !== "ok" && !completedHTTP && !disconnectedUpgrade)) {
        // Do not expose URLs, headers, exception messages or unrelated logs.
        const outcome = /^[a-zA-Z]+$/.test(record.outcome ?? "") ? record.outcome : "unknown";
        failure = `Smoke request produced a server-side exception or unsuccessful invocation (path=${path}, outcome=${outcome}, exceptions=${record.exceptions?.length ?? 0})`;
      }
      if (record.event?.response?.status >= 500) {
        failure = `Smoke request returned HTTP 5xx (path=${path}, status=${record.event.response.status})`;
      }
      for (const log of record.logs ?? []) {
        if (["error", "fatal"].includes(log.level)) failure ??= `Smoke request emitted a server-side error (path=${path}, level=${log.level})`;
        for (let message of log.message ?? []) {
          if (typeof message === "string") {
            try { message = JSON.parse(message); } catch { continue; }
          }
          if (message?.event === "demo_websocket_closed" && message.smoke_id === id
            && message.client_code === 1000 && message.backend_code === 1000
            && typeof message.connection_id === "string") {
            closed.add(message.smoke_path ?? url?.pathname);
            connections.add(message.connection_id);
          }
        }
      }
    },
    check() {
      if (failure) throw new Error(failure);
      return observed && websocketPaths.every(path => closed.has(path)) && connections.size >= websocketPaths.length;
    },
    get observed() { return observed; },
  };
}

async function until(check, timeoutMS, signal) {
  const end = Date.now() + timeoutMS;
  while (!check()) {
    if (Date.now() >= end) throw new Error("Timed out waiting for correlated server telemetry");
    await delay(100, undefined, { signal });
  }
}

export async function verifiedSmoke(env = process.env) {
  const { CLOUDFLARE_ACCOUNT_ID: account, CLOUDFLARE_API_TOKEN: token } = env;
  if (!/^[0-9a-f]{32}$/.test(account ?? "") || !token) throw new Error("Cloudflare deployment credentials are required for server verification");
  const worker = env.SHINYHUB_DEMO_WORKER ?? "shinyhub-demo";
  const endpoint = `https://api.cloudflare.com/client/v4/accounts/${account}/workers/scripts/${encodeURIComponent(worker)}/tails`;
  const api = async (url, method, body) => {
    const response = await fetch(url, { method, body: body === undefined ? undefined : JSON.stringify(body),
      headers: { authorization: `Bearer ${token}`, "content-type": "application/json", "user-agent": "ShinyHub-Demo-Verification/1.0" },
      signal: AbortSignal.timeout(15_000),
    });
    if (method === "DELETE" && response.status === 404) return;
    if (!response.ok) throw new Error(`Cloudflare tail ${method} failed (HTTP ${response.status})`);
    const data = await response.json();
    if (!data.success) throw new Error(`Cloudflare tail ${method} was unsuccessful`);
    return data.result;
  };
  const id = randomUUID();
  const inspector = smokeInspector(id);
  const controller = new AbortController();
  const stop = () => controller.abort(new Error("Verification interrupted"));
  process.once("SIGTERM", stop);
  process.once("SIGINT", stop);
  let session, socket, child, streamFailure, primaryFailure, cleanupFailure;
  const runSmoke = (phase, marker) => new Promise((resolve, reject) => {
    child = spawn(fileURLToPath(new URL("./demo-smoke.sh", import.meta.url)), [phase], {
      env: { ...env, SHINYHUB_DEMO_SMOKE_ID: marker }, stdio: "inherit", signal: controller.signal,
    });
    child.once("error", reject);
    child.once("exit", code => code === 0 ? resolve() : reject(new Error(`Demo smoke ${phase} exited with status ${code}`)));
  });
  try {
    await runSmoke("wait-for-rollout", "");
    session = await api(endpoint, "POST", { filters: [] });
    socket = new WebSocket(session.url, "trace-v1");
    let opened = false;
    socket.addEventListener("open", () => { opened = true; socket.send(JSON.stringify({ debug: false })); });
    socket.addEventListener("error", () => { streamFailure = new Error("Cloudflare tail connection failed"); });
    socket.addEventListener("close", () => { streamFailure = new Error("Cloudflare tail disconnected before verification finished"); });
    let messages = Promise.resolve();
    socket.addEventListener("message", event => {
      messages = messages.then(async () => {
        const text = typeof event.data === "string" ? event.data : event.data.toString();
        inspector.ingest(JSON.parse(text));
      }).catch(() => { streamFailure = new Error("Could not decode Cloudflare tail telemetry"); });
    });
    const check = () => { if (streamFailure) throw streamFailure; return inspector.check(); };
    await until(() => { if (streamFailure) throw streamFailure; return opened; }, 15_000, controller.signal);
    // Prove the listener is receiving events before starting the smoke suite.
    const status = new URL("/__demo/status", env.SHINYHUB_DEMO_URL ?? "https://demo.shinyhub.dev");
    // Tail registration can propagate after the socket opens. Keep probing a
    // passive endpoint so a missed first request cannot block verification.
    await waitForTelemetry(() => fetch(status, {
      headers: { "x-shinyhub-demo-smoke": id, "user-agent": "ShinyHub-Demo-Verification/1.0" },
      signal: AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)]),
    }).then(r => r.arrayBuffer()), () => { check(); return inspector.observed; }, controller.signal);
    await runSmoke("verify", id);
    // Read only synthetic close records through the passive status probe.
    // This proves the inner relay completed, without another WebSocket proxy.
    await waitForTelemetry(() => fetch(status, {
      headers: { "x-shinyhub-demo-smoke": id, "user-agent": "ShinyHub-Demo-Verification/1.0" },
      signal: AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)]),
    }).then(r => r.arrayBuffer()), check, controller.signal);
    // Include late transport exceptions following the final close callback.
    await delay(2_000, undefined, { signal: controller.signal });
    await messages;
    check();
    console.log("Demo smoke passed: all three WebSockets closed cleanly at both proxy ends; correlated server telemetry contains no errors.");
  } catch (error) {
    primaryFailure = error;
    throw error;
  } finally {
    child?.kill("SIGTERM");
    socket?.terminate();
    process.removeListener("SIGTERM", stop);
    process.removeListener("SIGINT", stop);
    if (session) {
      // A failed tail cleanup is the run's error only when the smoke itself
      // passed; otherwise the primary failure propagates and this is logged.
      try { await api(`${endpoint}/${encodeURIComponent(session.id)}`, "DELETE"); }
      catch (error) {
        if (primaryFailure) console.error(error.message);
        else cleanupFailure = error;
      }
    }
  }
  if (cleanupFailure) throw cleanupFailure;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  verifiedSmoke().catch(error => { console.error(error.message); process.exitCode = 1; });
}
