import assert from "node:assert/strict";
import test from "node:test";
import { registerHooks } from "node:module";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Run the real SDK with adapters for Workers base classes and bundled assets.
const hook = registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "cloudflare:workers") return { url: "data:text/javascript,export class DurableObject {}; export class WorkerEntrypoint {}", shortCircuit: true };
    if (context.parentURL?.includes("/@cloudflare/containers/") && specifier.startsWith(".") && !specifier.endsWith(".js")) return nextResolve(specifier + ".js", context);
    return nextResolve(specifier, context);
  },
  load(url, context, nextLoad) {
    if (/\.(sh|svg)$/.test(url)) return { format: "module", shortCircuit: true,
      source: "export default " + JSON.stringify(readFileSync(fileURLToPath(url), "utf8")) };
    return nextLoad(url, context);
  },
});
const { ShinyHubDemo } = await import("./index.ts");
hook.deregister();

function fixture(readyAfterMS = 30_000) {
  const demo = Object.create(ShinyHubDemo.prototype);
  const waits: { signal?: AbortSignal; retries: number; waitInterval: number; portToCheck: number }[] = [];
  const starts: { options: unknown; waitOptions: unknown }[] = [];
  demo.defaultPort = 8080;
  demo.syncPendingStoppedEvents = async () => {};
  demo.startContainerIfNotRunning = async (waitOptions: unknown, options: unknown) => {
    starts.push({ options, waitOptions });
    return 0;
  };
  demo.setupMonitorCallbacks = () => {};
  demo.onStart = async () => {};
  demo.ctx = { blockConcurrencyWhile: (fn: () => unknown) => fn() };
  demo.state = { getState: async () => ({ status: "running" }), setHealthy: async () => {} };
  demo.waitForPort = async (options: typeof waits[number]) => {
    waits.push(options);
    if (options.signal?.aborted) throw new Error("cancelled");
    if (options.retries * options.waitInterval < readyAfterMS) throw new Error("readiness timeout");
    return options.retries - Math.ceil(readyAfterMS / options.waitInterval);
  };
  demo.container = { running: true, getTcpPort: () => ({ fetch: async () => Object.assign(new Response(null, { status: 204 }), { webSocket: null }) }) };
  demo.inflightRequests = 0;
  demo.renewActivityTimeout = () => {};
  demo.decrementInflight = () => { demo.inflightRequests--; };
  return { demo, waits, starts };
}

test("SDK automatic fetch allows a fleet taking longer than 20 seconds", async () => {
  const { demo, waits } = fixture();
  const request = new Request("https://demo.shinyhub.dev/healthz");
  assert.equal((await demo.containerFetch(request)).status, 204);
  assert.equal(waits[0].retries * waits[0].waitInterval, 90_000);
  assert.equal(waits[0].signal, request.signal);
  assert.equal(waits[0].portToCheck, 8080);
});

test("object startup preserves ports and explicit shorter timeout failures", async () => {
  const { demo, waits } = fixture();
  await assert.rejects(demo.startAndWaitForPorts({ ports: 8080, cancellationOptions: { portReadyTimeoutMS: 10_000, waitInterval: 500 } }), /readiness timeout/);
  assert.equal(waits[0].retries * waits[0].waitInterval, 10_000);
  await demo.startAndWaitForPorts({ ports: [8080, 8081] });
  assert.deepEqual(waits.slice(1).map(wait => wait.portToCheck), [8080, 8081]);
});

test("startup cancellation still propagates", async () => {
  const { demo } = fixture();
  await assert.rejects(demo.startAndWaitForPorts(8080, { abort: AbortSignal.abort() }), /cancelled/);
});

test("both startup call forms preserve configuration and allocation settings", async () => {
  const options = { envVars: { DEMO_MODE: "test" }, entrypoint: ["/bin/sh", "-c", "test"], enableInternet: false };
  const controller = new AbortController();
  const cancellationOptions = { abort: controller.signal, instanceGetTimeoutMS: 12_000, waitInterval: 500 };
  for (const objectForm of [false, true]) {
    const { demo, starts } = fixture();
    if (objectForm) await demo.startAndWaitForPorts({ ports: 8080, cancellationOptions, startOptions: options });
    else await demo.startAndWaitForPorts(8080, cancellationOptions, options);
    assert.equal(starts[0].options, options);
    assert.deepEqual(starts[0].waitOptions, { signal: controller.signal, retries: 24, waitInterval: 500, portToCheck: 8080 });
    assert.deepEqual(cancellationOptions, { abort: controller.signal, instanceGetTimeoutMS: 12_000, waitInterval: 500 });
  }
});

test("startup still fails if the fleet exceeds the extended deadline", async () => {
  const { demo } = fixture(100_000);
  await assert.rejects(demo.startAndWaitForPorts(), /readiness timeout/);
});

test("real SDK port polling survives slow boots and reports genuinely stuck boots", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 0 });
  for (const readyAfterMS of [24_900, 60_000, Infinity]) {
    const { demo } = fixture();
    // Restore the real polling loop; simulate only the runtime's TCP endpoint.
    delete demo.waitForPort;
    const startedAt = Date.now();
    const errors: unknown[] = [];
    demo.onError = (error: unknown) => { errors.push(error); };
    demo.container.getTcpPort = () => ({
      async fetch() {
        if (Date.now() - startedAt < readyAfterMS) throw new Error("container is not listening");
        return new Response(null, { status: 204 });
      },
    });
    let settled = false;
    const startup = demo.startAndWaitForPorts();
    const result = startup.then(() => ({ ok: true }), (error: unknown) => ({ ok: false, error }))
      .finally(() => { settled = true; });
    // Advance a virtual clock while allowing the SDK's promise chain to drain.
    for (let i = 0; i < 310 && !settled; i++) {
      await new Promise<void>(resolve => setImmediate(resolve));
      t.mock.timers.tick(300);
    }
    assert.ok(settled, "startup must have a bounded deadline");
    assert.equal((await result).ok, Number.isFinite(readyAfterMS));
    assert.equal(errors.length, Number.isFinite(readyAfterMS) ? 0 : 1);
    if (!Number.isFinite(readyAfterMS)) assert.match(String(errors[0]), /after 90000ms/);
  }
});
