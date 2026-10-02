import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { handleCanaryOperations } from "./canary-operations.ts";

test("the canary deploy cannot claim production domains or Durable Object classes", () => {
  const config = (name: string) => JSON.parse(readFileSync(new URL("../" + name, import.meta.url), "utf8"));
  const canary = config("wrangler.canary.jsonc"), production = config("wrangler.jsonc");
  assert.notEqual(canary.name, production.name);
  assert.notEqual(canary.main, production.main);
  const productionHosts = new Set(production.routes.map((r: { pattern: string }) => r.pattern));
  assert.equal(canary.routes.length, 2);
  for (const route of canary.routes) {
    assert.equal(productionHosts.has(route.pattern), false);
    assert.ok(route.pattern.endsWith("staging.demo.shinyhub.dev"));
    assert.equal(route.custom_domain, true);
  }
  const productionClasses = new Set(production.durable_objects.bindings.map((b: { class_name: string }) => b.class_name));
  for (const binding of canary.durable_objects.bindings) assert.equal(productionClasses.has(binding.class_name), false);
  assert.equal(canary.workers_dev, false);
  assert.ok(canary.compatibility_flags.includes("enable_request_signal"));
  assert.equal(canary.containers[0].scheduling_policy, "durable_object");
});

function fixture() {
  let resolved = 0;
  const calls: string[] = [];
  const controller = {
    async state() { calls.push("state"); return { running: false, activeConnections: 0 }; },
    async start() { calls.push("start"); return { mode: "image" }; },
    async stop() { calls.push("stop"); },
    async snapshot() { calls.push("snapshot"); return { id: "private-restore-handle", size: 123 }; },
    async forgetSnapshot() { calls.push("forget"); },
    async terminate() { calls.push("terminate"); },
  };
  return { calls, resolved: () => resolved,
    fetch(path: string, method = "GET", token?: string) {
      return handleCanaryOperations(new Request("https://staging.demo.shinyhub.dev/__canary/" + path, {
        method, headers: token ? { authorization: "Bearer " + token } : {},
      }), "operator-secret", () => { resolved++; return controller; });
    },
  };
}

test("operator mutations authenticate before obtaining any DO handle", async () => {
  const f = fixture();
  for (const path of ["start", "stop", "snapshot", "terminate"]) {
    assert.equal((await f.fetch(path, "POST")).status, 401);
    assert.equal((await f.fetch(path, "POST", "viewer-cookie")).status, 401);
  }
  assert.equal(f.resolved(), 0); assert.deepEqual(f.calls, []);
});

test("invalid paths and methods cannot call an operator operation", async () => {
  const f = fixture();
  assert.equal((await f.fetch("exec", "POST", "operator-secret")).status, 404);
  const invalid = await f.fetch("terminate", "GET", "operator-secret");
  assert.equal(invalid.status, 405); assert.equal(invalid.headers.get("allow"), "POST");
  assert.equal(f.resolved(), 0);
});

test("snapshot responses omit the private restore handle", async () => {
  const f = fixture();
  const response = await f.fetch("snapshot", "POST", "operator-secret");
  assert.deepEqual(await response.json(), { size: 123 });
  assert.equal(response.headers.get("cache-control"), "no-store");
  assert.equal((await f.fetch("snapshot", "DELETE", "operator-secret")).status, 204);
  assert.deepEqual(f.calls, ["snapshot", "forget"]);
});

test("fault injection is available only as an authenticated explicit mutation", async () => {
  const f = fixture();
  assert.equal((await f.fetch("terminate", "POST", "operator-secret")).status, 204);
  assert.deepEqual(f.calls, ["terminate"]);
});
