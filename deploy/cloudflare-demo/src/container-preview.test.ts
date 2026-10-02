import assert from "node:assert/strict";
import test from "node:test";
import { authorized, ContainerPreview, IDLE_TIMEOUT_MS, usableSnapshot } from "./container-preview.ts";

function fixture(policy: "default" | "durable_object" = "durable_object") {
  let now = 100;
  let healthy = true;
  let restoreFails = false;
  let imageFails = false;
  let startFailures = 0;
  let capacityFailures = 0;
  let monitorFails = false;
  let destroys = 0;
  const starts: (ContainerStartupOptions | undefined)[] = [];
  const healthRequests: Request[] = [];
  const timeouts: number[] = [];
  const store = new Map<string, unknown>();
  let alarm: number | null = null;
  const snapshot = { id: "snapshot-one", size: 1000 };
  const runtime = {
    running: false,
    start(options?: ContainerStartupOptions) {
      starts.push(options);
      if (startFailures-- > 0) throw new Error("allocation unavailable");
      const restored = Boolean(options?.containerSnapshot);
      if (restored && restoreFails) throw new Error("snapshot expired");
      if (!restored && imageFails) throw new Error("image allocation failed");
      runtime.running = true;
    },
    async destroy() { destroys++; runtime.running = false; },
    async setInactivityTimeout(ms: number | bigint) {
      if (capacityFailures-- > 0) {
        runtime.running = false;
        throw new Error("There is no container instance that can be provided to this Durable Object, try again later");
      }
      timeouts.push(Number(ms));
    },
    async snapshotContainer() { return snapshot; },
    monitor() { return monitorFails ? Promise.reject(new Error("container entrypoint exited with code 1")) : new Promise<void>(() => {}); },
    getTcpPort(port: number) {
      assert.equal(port, 8080);
      return { async fetch(request: Request) { healthRequests.push(request); return new Response(null, { status: healthy ? 204 : 503 }); } } as Fetcher;
    },
  };
  const storage = {
    async get<T>(key: string) { return store.get(key) as T | undefined; },
    async put<T>(key: string, value: T) { store.set(key, value); },
    async delete(key: string) { return store.delete(key); },
    async setAlarm(timestamp: number) { alarm = timestamp; },
    async deleteAlarm() { alarm = null; },
  };
  const options = { policy, image: "digest-one", instance: "standard-1" as const };
  const make = (image = "digest-one", entrypoint?: string[], origins: { controlOrigin?: string; appOrigin?: string } = {}) => new ContainerPreview(runtime, storage, { ...options, image, entrypoint, ...origins }, () => now, async (ms) => { now += ms; });
  return { controller: make(), make, runtime, starts, timeouts, store, snapshot, healthRequests,
    scheduledAlarm() { return alarm; },
    destroyCount() { return destroys; },
    failRestore() { restoreFails = true; }, failImage() { imageFails = true; },
    failMonitor() { monitorFails = true; },
    allocationRetries() { startFailures = 2; }, capacityRetries(count: number) { capacityFailures = count; },
    unhealthy() { healthy = false; }, advance(ms: number) { now += ms; } };
}

test("all benchmark requests require an explicitly configured bearer token", () => {
  const plain = new Request("https://preview.example/start");
  assert.equal(authorized(plain, undefined), false);
  assert.equal(authorized(plain, "secret"), false);
  assert.equal(authorized(new Request(plain, { headers: { authorization: "Bearer wrong" } }), "secret"), false);
  assert.equal(authorized(new Request(plain, { headers: { authorization: "Bearer secret" } }), "secret"), true);
});

test("idle alarms stop resident containers without readiness probes extending the deadline", async () => {
  const f = fixture();
  await f.controller.start();
  assert.equal(f.scheduledAlarm(), 100 + IDLE_TIMEOUT_MS);
  f.advance(IDLE_TIMEOUT_MS - 1);
  assert.equal(await f.controller.ready(), true);
  await f.controller.alarm();
  assert.equal(f.runtime.running, true);
  assert.equal(f.scheduledAlarm(), 100 + IDLE_TIMEOUT_MS);
  f.advance(1);
  await f.controller.alarm();
  assert.equal(f.runtime.running, false);
  assert.equal(f.scheduledAlarm(), null);
  assert.equal(f.store.has("idle-deadline"), false);
});

test("warm starts and admitted app requests extend idle time; old alarms use the new deadline", async () => {
  const f = fixture();
  await f.controller.start();
  f.advance(1000);
  await f.controller.start();
  assert.equal(f.scheduledAlarm(), 1100 + IDLE_TIMEOUT_MS);
  f.advance(1000);
  assert.equal((await f.controller.proxy(new Request("http://demo.shinyhub.dev/"))).status, 204);
  const deadline = f.scheduledAlarm();
  assert.equal(deadline, 2100 + IDLE_TIMEOUT_MS);
  await f.controller.alarm();
  assert.equal(f.scheduledAlarm(), deadline);
  assert.equal(f.runtime.running, true);
});

test("browser HTTPS requests use HTTP on the native port with host, body, headers and cancellation intact", async () => {
  const f = fixture();
  await f.controller.start();
  const abort = new AbortController();
  const response = await f.controller.proxy(new Request("https://apps.staging.demo.shinyhub.dev/app/demo/chat?view=week", {
    method: "POST", headers: { "x-forwarded-proto": "https", "content-type": "application/json" },
    body: '{"message":"hello"}', signal: abort.signal,
  }));
  assert.equal(response.status, 204);
  const upstream = f.healthRequests.at(-1)!;
  assert.equal(upstream.url, "http://apps.staging.demo.shinyhub.dev/app/demo/chat?view=week");
  assert.equal(upstream.method, "POST");
  assert.equal(upstream.headers.get("x-forwarded-proto"), "https");
  assert.equal(await upstream.text(), '{"message":"hello"}');
  abort.abort();
  assert.equal(upstream.signal.aborted, true);
});

test("controller restarts preserve the idle deadline and explicit stop removes it", async () => {
  const f = fixture();
  await f.controller.start();
  f.advance(IDLE_TIMEOUT_MS);
  const restarted = f.make();
  await restarted.resume();
  assert.equal(f.scheduledAlarm(), 100 + IDLE_TIMEOUT_MS);
  await restarted.alarm();
  assert.equal(f.runtime.running, false);
  await restarted.start();
  await restarted.stop();
  assert.equal(f.scheduledAlarm(), null);
  assert.equal(f.store.has("idle-deadline"), false);
  await restarted.alarm();
  assert.equal(f.runtime.running, false);
});

test("manual stop remains available while restoring the native inactivity timeout", async () => {
  const f = fixture();
  await f.controller.start();
  let entered!: () => void;
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  const called = new Promise<void>((resolve) => { entered = resolve; });
  f.runtime.setInactivityTimeout = async () => { entered(); await pending; };
  const resuming = f.controller.resume();
  await called;
  await f.controller.stop();
  assert.equal(f.runtime.running, false);
  assert.equal(f.scheduledAlarm(), null);
  release();
  await resuming;
});

test("state/readiness/proxy requests never start an asleep container", async () => {
  const f = fixture();
  assert.equal(await f.controller.ready(), false);
  assert.equal((await f.controller.proxy(new Request("http://demo.shinyhub.dev/"))).status, 503);
  assert.deepEqual(f.starts, []);
  await assert.rejects(f.controller.snapshot(), /ready/);
});

test("concurrent starts allocate once and set the ten-minute inactivity timeout", async () => {
  const f = fixture();
  const results = await Promise.all([f.controller.start(), f.controller.start()]);
  assert.equal(f.starts.length, 1);
  assert.deepEqual(f.starts[0], { image: "digest-one", instance: "standard-1", enableInternet: true,
    env: { SHINYHUB_AUTH_SECRET: f.store.get("demo-auth-secret"), SHINYHUB_DEPLOY_TOKEN: `shk_${f.store.get("demo-deploy-token")}` },
  });
  assert.ok(results.every((result) => result.mode === "image"));
  assert.deepEqual(f.timeouts, [IDLE_TIMEOUT_MS]);
  assert.equal((await f.controller.start()).mode, "warm");
  await f.controller.resume();
  assert.deepEqual(f.timeouts, [IDLE_TIMEOUT_MS, IDLE_TIMEOUT_MS]);
});

test("baseline uses configuration startup and rejects snapshots", async () => {
  const f = fixture("default");
  await f.controller.start();
  assert.deepEqual(f.starts, [undefined]);
  await assert.rejects(f.controller.snapshot(), /durable_object/);
});

test("both policies use the same bootstrap script, including snapshot restores", async () => {
  const entrypoint = ["/bin/sh", "-c", "test startup script"];
  for (const policy of ["default", "durable_object"] as const) {
    const f = fixture(policy);
    const controller = f.make("digest-one", entrypoint);
    await controller.start();
    assert.deepEqual(f.starts[0]?.entrypoint, entrypoint);
    if (policy === "default") {
      assert.equal(f.starts[0]?.image, undefined);
      assert.equal(f.starts[0]?.instance, undefined);
    } else {
      await controller.snapshot();
      await controller.stop();
      await f.make("digest-one", entrypoint).start();
      assert.deepEqual(f.starts[1]?.entrypoint, entrypoint);
      assert.equal(f.starts[1]?.image, undefined);
    }
    await controller.stop();
  }
});

test("pending headers and streaming responses survive alarms; idle starts after the last disconnect", async () => {
  const f = fixture();
  await f.controller.start();
  let respond!: () => void;
  const pending = new Promise<void>((resolve) => { respond = resolve; });
  f.runtime.getTcpPort = () => ({ async fetch(request: Request) {
    if (new URL(request.url).pathname === "/healthz") return new Response(null, { status: 204 });
    await pending;
    return new Response(new ReadableStream({ start(c) { c.enqueue(new Uint8Array([42])); } }));
  } }) as Fetcher;
  const first = f.controller.proxy(new Request("http://apps.demo.shinyhub.dev/one"));
  // The alarm queues after admission while the fetch is still pending.
  await f.controller.alarm();
  assert.equal(f.controller.activeConnections, 1);
  f.advance(IDLE_TIMEOUT_MS + 1);
  await f.controller.alarm();
  assert.equal(f.runtime.running, true);
  const second = f.controller.proxy(new Request("http://apps.demo.shinyhub.dev/two"));
  await f.controller.alarm();
  assert.equal(f.controller.activeConnections, 2);
  respond();
  const a = await first;
  const b = await second;
  await a.body!.cancel();
  await new Promise((resolve) => setImmediate(resolve));
  await f.controller.alarm();
  assert.equal(f.controller.activeConnections, 1);
  f.advance(IDLE_TIMEOUT_MS * 2);
  await f.controller.alarm();
  assert.equal(f.runtime.running, true);
  await b.body!.cancel();
  await new Promise((resolve) => setImmediate(resolve));
  await f.controller.alarm();
  assert.equal(f.controller.activeConnections, 0);
  const deadline = f.scheduledAlarm();
  f.advance(IDLE_TIMEOUT_MS - 1);
  await f.controller.alarm();
  assert.equal(f.runtime.running, true);
  assert.equal(f.scheduledAlarm(), deadline);
  f.advance(1);
  await f.controller.alarm();
  assert.equal(f.runtime.running, false);
});

test("explicit stop invalidates old connections without postponing a new instance's shutdown", async () => {
  const f = fixture();
  await f.controller.start();
  f.runtime.getTcpPort = () => ({ async fetch(request: Request) {
    return new Response(new URL(request.url).pathname === "/healthz" ? null : new ReadableStream(), { status: 200 });
  } }) as Fetcher;
  const response = await f.controller.proxy(new Request("http://apps.demo.shinyhub.dev/stream"));
  await f.controller.stop();
  assert.equal(f.controller.activeConnections, 0);
  await f.controller.start();
  const deadline = f.scheduledAlarm();
  f.advance(1000);
  await response.body!.cancel();
  await f.controller.alarm();
  assert.equal(f.scheduledAlarm(), deadline);
});

test("a reset drops stale connection counts and grants a fresh idle window", async () => {
  const f = fixture();
  await f.controller.start();
  f.store.set("active-connections", true);
  f.advance(IDLE_TIMEOUT_MS * 2);
  const restarted = f.make();
  await restarted.resume();
  assert.equal(restarted.activeConnections, 0);
  assert.equal(f.store.has("active-connections"), false);
  assert.equal(f.scheduledAlarm(), 100 + IDLE_TIMEOUT_MS * 3);
  await restarted.alarm();
  assert.equal(f.runtime.running, true);
});

test("failed upstream requests release their activity lease", async () => {
  const f = fixture();
  await f.controller.start();
  f.runtime.getTcpPort = () => ({ async fetch(request: Request) {
    if (new URL(request.url).pathname === "/healthz") return new Response(null, { status: 204 });
    throw new Error("upstream disconnected");
  } }) as Fetcher;
  await assert.rejects(f.controller.proxy(new Request("http://apps.demo.shinyhub.dev/")), /upstream disconnected/);
  await f.controller.alarm();
  assert.equal(f.controller.activeConnections, 0);
  assert.equal(f.store.has("active-connections"), false);
});

test("client abort releases activity even when the upstream stream never ends", async () => {
  const f = fixture();
  await f.controller.start();
  f.runtime.getTcpPort = () => ({ async fetch(request: Request) {
    return new Response(new URL(request.url).pathname === "/healthz" ? null : new ReadableStream(), { status: 200 });
  } }) as Fetcher;
  const abort = new AbortController();
  const response = await f.controller.proxy(new Request("http://apps.demo.shinyhub.dev/stream", { signal: abort.signal }));
  assert.equal(f.controller.activeConnections, 1);
  f.advance(IDLE_TIMEOUT_MS * 2);
  abort.abort();
  await f.controller.alarm();
  assert.equal(f.controller.activeConnections, 0);
  assert.equal(f.runtime.running, true);
  f.advance(IDLE_TIMEOUT_MS);
  await f.controller.alarm();
  assert.equal(f.runtime.running, false);
  await response.body!.cancel();
});

test("snapshot restore passes the handle without an image and refreshes retention", async () => {
  const f = fixture();
  await f.controller.start();
  assert.equal((await f.controller.snapshot()).id, f.snapshot.id);
  await f.controller.stop();
  f.advance(5000);
  const result = await f.controller.start();
  assert.equal(result.mode, "snapshot");
  assert.deepEqual(f.starts[1], { containerSnapshot: f.snapshot, instance: "standard-1", enableInternet: true,
    env: { SHINYHUB_AUTH_SECRET: f.store.get("demo-auth-secret"), SHINYHUB_DEPLOY_TOKEN: `shk_${f.store.get("demo-deploy-token")}` },
  });
  assert.equal((f.store.get("filesystem-snapshot") as { lastUsedAt: number }).lastUsedAt, 5100);
});

test("snapshot restores reuse encryption and bootstrap credentials after a controller restart", async () => {
  const f = fixture();
  await f.controller.start();
  const secret = f.starts[0]!.env!.SHINYHUB_AUTH_SECRET;
  const deployToken = f.starts[0]!.env!.SHINYHUB_DEPLOY_TOKEN;
  assert.match(secret, /^[a-f0-9]{64}$/);
  assert.match(deployToken, /^shk_[a-f0-9]{64}$/);
  await f.controller.snapshot();
  await f.controller.stop();
  const restored = f.make();
  const result = await restored.start();
  assert.equal(result.mode, "snapshot");
  assert.equal(f.starts[1]!.env!.SHINYHUB_AUTH_SECRET, secret);
  assert.equal(f.starts[1]!.env!.SHINYHUB_DEPLOY_TOKEN, deployToken);
  assert.ok(!JSON.stringify(result).includes(secret));
  assert.ok(!JSON.stringify(result).includes(deployToken));
  await restored.stop();
  await restored.forgetSnapshot();
  await f.make().start();
  assert.equal(f.starts[2]!.env!.SHINYHUB_AUTH_SECRET, secret);
  assert.equal(f.starts[2]!.env!.SHINYHUB_DEPLOY_TOKEN, deployToken);
});

test("an invalid stored encryption key fails before allocating a container", async () => {
  const f = fixture();
  f.store.set("demo-auth-secret", "invalid");
  await assert.rejects(f.controller.start(), /authentication key is invalid/);
  assert.equal(f.starts.length, 0);
});

test("expired and image-incompatible snapshots are discarded", async () => {
  for (const changedImage of [false, true]) {
    const f = fixture();
    await f.controller.start();
    await f.controller.snapshot();
    await f.controller.stop();
    if (!changedImage) f.advance(30 * 24 * 60 * 60 * 1000);
    assert.equal((await f.make(changedImage ? "digest-two" : "digest-one").start()).mode, "image");
    assert.equal(f.store.has("filesystem-snapshot"), false);
    assert.equal(f.starts.at(-1)?.image, changedImage ? "digest-two" : "digest-one");
  }
  assert.equal(usableSnapshot({ handle: { id: "one", size: 1 }, image: "one", lastUsedAt: 200 }, "one", 100), false);
});

test("an image update never relabels a running old container's snapshot", async () => {
  const f = fixture();
  await f.controller.start();
  const updated = f.make("digest-two");
  await assert.rejects(updated.snapshot(), /current image/);
  await assert.rejects(updated.start(), /older image/);
  await updated.stop();
  await updated.start();
  await updated.snapshot();
  assert.equal((f.store.get("filesystem-snapshot") as { image: string }).image, "digest-two");
});

test("failed snapshot restores fall back once to the current image", async () => {
  const f = fixture();
  await f.controller.start();
  await f.controller.snapshot();
  await f.controller.stop();
  f.failRestore();
  assert.equal((await f.controller.start()).mode, "image-after-snapshot-failure");
  assert.equal(f.store.has("filesystem-snapshot"), false);
  assert.equal(f.starts.at(-1)?.image, "digest-one");
});

test("allocation retries are bounded and failed boots stop the container", async () => {
  const transient = fixture();
  transient.allocationRetries();
  await transient.controller.start();
  assert.equal(transient.starts.length, 3);
  const failed = fixture();
  failed.failImage();
  await assert.rejects(failed.controller.start(), /allocation failed/);
  assert.equal(failed.starts.length, 3);
  assert.equal(failed.runtime.running, false);
  const unhealthy = fixture();
  unhealthy.unhealthy();
  await assert.rejects(unhealthy.controller.start(), /90 seconds/);
  assert.equal(unhealthy.runtime.running, false);
});

test("asynchronous capacity errors retry for at most thirty seconds", async () => {
  const transient = fixture("default");
  transient.capacityRetries(2);
  assert.equal((await transient.controller.start()).readyMs, 2000);
  assert.equal(transient.starts.length, 3);
  assert.equal(transient.destroyCount(), 2);
  const exhausted = fixture("default");
  exhausted.capacityRetries(100);
  await assert.rejects(exhausted.controller.start(), /no container instance/);
  assert.equal(exhausted.starts.length, 31);
  assert.equal(exhausted.runtime.running, false);
});

test("asynchronous entrypoint failures are reported and the instance is stopped", async () => {
  const f = fixture();
  f.failMonitor();
  await assert.rejects(f.controller.start(), /entrypoint exited/);
  assert.equal(f.runtime.running, false);
});


test("staging origins and encryption credentials survive snapshot starts together", async () => {
  const f = fixture();
  const controller = f.make("digest-one", undefined, {
    controlOrigin: "https://staging.demo.shinyhub.dev", appOrigin: "https://apps.staging.demo.shinyhub.dev",
  });
  await controller.start(); await controller.snapshot(); await controller.stop();
  assert.equal((await controller.start()).mode, "snapshot");
  for (const start of f.starts) {
    assert.equal(start!.env!.SHINYHUB_BASE_URL, "https://staging.demo.shinyhub.dev");
    assert.equal(start!.env!.SHINYHUB_APP_ORIGIN, "https://apps.staging.demo.shinyhub.dev");
    assert.equal(start!.env!.SHINYHUB_AUTH_SECRET, f.starts[0]!.env!.SHINYHUB_AUTH_SECRET);
  }
  assert.ok(f.healthRequests.every(r => new URL(r.url).hostname === "staging.demo.shinyhub.dev"));
});
