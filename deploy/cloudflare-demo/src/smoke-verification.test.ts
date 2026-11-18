import assert from "node:assert/strict";
import test from "node:test";
import { smokeInspector, websocketPaths, waitForTelemetry } from "../../../scripts/demo-smoke-verified.mjs";
const id = "27a1ae5d-8dce-4e3e-a717-d27d87bc7544";

test("tail registration retries passive probes until telemetry arrives", async () => {
  let probes = 0;
  await waitForTelemetry(async () => { probes++; }, () => probes >= 3,
    new AbortController().signal, { timeoutMS: 1000, intervalMS: 1 });
  assert.equal(probes, 3);
});

test("tail registration honors cancellation instead of launching the smoke suite", async () => {
  const controller = new AbortController();
  await assert.rejects(waitForTelemetry(async () => { controller.abort(); }, () => false,
    controller.signal, { intervalMS: 1 }), /abort/i);
});
const trace = (path: string, extra = {}) => ({
  outcome: "ok", exceptions: [],
  event: { request: { url: `https://demo.test${path}?__demo_smoke=${id}`, headers: {} }, response: { status: 101 } },
  logs: [{ level: "info", message: [{ event: "demo_websocket_closed", smoke_id: id, connection_id: path, client_code: 1000, backend_code: 1000 }] }],
  ...extra,
});

test("transient rollout telemetry stays outside the verified smoke run", () => {
  const inspector = smokeInspector(id);
  const rollout = {
    outcome: "exception",
    event: { request: { url: "https://demo.test/api/server-info", method: "GET" }, response: { status: 503 } },
    logs: [{ level: "error", message: ["Runtime signalled the container to exit due to a new version rollout: 143"] }],
  };
  inspector.ingest(rollout);
  assert.equal(inspector.observed, false);

  for (const path of websocketPaths) inspector.ingest(trace(path));
  assert.equal(inspector.check(), true);
  inspector.ingest({ ...rollout, event: {
    ...rollout.event,
    request: { ...rollout.event.request, headers: { "x-shinyhub-demo-smoke": id } },
  } });
  assert.throws(() => inspector.check(), /path=\/api\/server-info, status=503/);
});

test("invocation diagnostics identify safe paths and outcomes without exposing request or exception data", () => {
  const inspector = smokeInspector(id);
  inspector.ingest({ outcome: "exception", exceptions: [{ message: "private exception" }],
    event: { request: { url: `https://demo.test/__demo/ready?token=private-token`,
      headers: { "x-shinyhub-demo-smoke": id, cookie: "private-cookie" } } },
  });
  assert.throws(() => inspector.check(), error => {
    assert.match(error.message, /path=\/__demo\/ready, outcome=exception, exceptions=1/);
    assert.doesNotMatch(error.message, /private|token|cookie|demo\.test/);
    return true;
  });
});

test("verification requires distinct two-sided closes for every app", () => {
  const inspector = smokeInspector(id);
  assert.equal(inspector.check(), false);
  for (const path of websocketPaths) inspector.ingest(trace(path));
  assert.equal(inspector.check(), true);
});

test("client success cannot hide server exceptions, error logs, failed invocations or HTTP 5xx", () => {
  for (const fault of [
    { exceptions: [{ message: "Network connection lost" }] },
    { outcome: "exception" }, { outcome: "canceled" },
    { logs: [{ level: "error", message: ["failed"] }] },
    { event: { request: { url: `https://demo.test/?__demo_smoke=${id}` }, response: { status: 500 } } },
  ]) {
    const inspector = smokeInspector(id);
    for (const path of websocketPaths) inspector.ingest(trace(path));
    inspector.ingest(trace(websocketPaths[0], fault));
    assert.throws(() => inspector.check(), /server-side|unsuccessful|5xx/);
  }
});

test("unrelated visitor failures are ignored and missing/abnormal backend evidence never passes", () => {
  const inspector = smokeInspector(id);
  inspector.ingest({ outcome: "exception", event: { request: { url: "https://demo.test/", headers: { cookie: "private" } } } });
  for (const path of websocketPaths.slice(0, 2)) inspector.ingest(trace(path));
  inspector.ingest(trace(websocketPaths[2], { logs: [] }));
  assert.equal(inspector.check(), false);
  inspector.ingest(trace(websocketPaths[2]));
  assert.equal(inspector.check(), true);
});

test("HTTP correlation supports header casing and truncated tail streams fail closed", () => {
  const inspector = smokeInspector(id);
  inspector.ingest({ outcome: "ok", event: { request: { url: "https://demo.test/__demo/status", headers: { "X-ShinyHub-Demo-Smoke": id } } } });
  assert.equal(inspector.observed, true);
  inspector.ingest({ type: "tail-info", message: "sampling" });
  assert.throws(() => inspector.check(), /incomplete telemetry/);
});

test("successful streamed HTTP cancellation does not hide WebSocket failures", () => {
  const inspector = smokeInspector(id);
  inspector.ingest(trace("/app/dash-demo/", { outcome: "canceled", logs: [],
    event: { request: { url: `https://demo.test/app/dash-demo/?__demo_smoke=${id}` }, response: { status: 200 } } }));
  assert.equal(inspector.check(), false);
  for (const path of websocketPaths) inspector.ingest(trace(path));
  assert.equal(inspector.check(), true);
  inspector.ingest(trace(websocketPaths[0], { outcome: "canceled" }));
  assert.throws(() => inspector.check(), /unsuccessful/);
});

test("structured close markers correlate when Cloudflare redacts query values", () => {
  const inspector = smokeInspector(id);
  for (const path of websocketPaths) inspector.ingest({ outcome: "ok", logs: [{ level: "info", message: [{
    event: "demo_websocket_closed", smoke_id: id, smoke_path: path,
    connection_id: path, client_code: 1000, backend_code: 1000,
  }] }], event: { request: { url: `https://demo.test${path}?__demo_smoke=REDACTED` } } });
  assert.equal(inspector.check(), true);
});


for (const outcome of ["responseStreamDisconnected", "canceled"]) test(`${outcome} upgrades need independent clean DO evidence and cannot hide server failures`, () => {
  const record = { outcome, exceptions: [], logs: [],
    event: { request: { method: "GET", url: `https://demo.test${websocketPaths[0]}`,
      headers: { upgrade: "websocket", "x-shinyhub-demo-smoke": id } } } };
  const inspector = smokeInspector(id);
  inspector.ingest(record);
  assert.equal(inspector.check(), false);
  for (const path of websocketPaths) inspector.ingest(trace(path));
  assert.equal(inspector.check(), true);
  for (const fault of [
    { exceptions: [{ message: "Network connection lost" }] },
    { logs: [{ level: "error", message: ["backend failed"] }] },
    { outcome: "exceededCpu" },
  ]) {
    const failed = smokeInspector(id);
    for (const path of websocketPaths) failed.ingest(trace(path));
    failed.ingest({ ...record, ...fault });
    assert.throws(() => failed.check(), /server-side|unsuccessful/);
  }
  const ordinary = smokeInspector(id);
  ordinary.ingest({ ...record, event: { request: { url: "https://demo.test/healthz", headers: { "x-shinyhub-demo-smoke": id } } } });
  assert.throws(() => ordinary.check(), /unsuccessful/);
});
