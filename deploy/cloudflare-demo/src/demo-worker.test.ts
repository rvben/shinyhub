import assert from "node:assert/strict";
import test from "node:test";
import { registerHooks } from "node:module";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const hook = registerHooks({
  load(url, context, nextLoad) {
    if (url.endsWith(".svg")) return { format: "module", shortCircuit: true,
      source: "export default " + JSON.stringify(readFileSync(fileURLToPath(url), "utf8")) };
    return nextLoad(url, context);
  },
});
const { createDemoWorker } = await import("./demo-worker.ts");
hook.deregister();
const control = "staging.demo.shinyhub.dev";
const apps = "apps.staging.demo.shinyhub.dev";

function fixture(status = "stopped", passiveReadiness = false) {
  const counts = { resolve: 0, state: 0, start: 0, fetch: 0 };
  const requests: Request[] = [];
  const work: Promise<unknown>[] = [];
  let response = () => Response.json({ ok: true });
  const backend = {
    async getState() { counts.state++; return { status }; },
    async start() { counts.start++; },
    async fetch(request: Request) { counts.fetch++; requests.push(request); return response(); },
  };
  const worker = createDemoWorker({ controlHost: control, appHost: apps, passiveReadiness,
    container() { counts.resolve++; return backend; } });
  const ctx = { waitUntil(p: Promise<unknown>) { work.push(p); } } as ExecutionContext;
  return { counts, requests, work,
    fetch(request: Request) { return worker.fetch!(request, {}, ctx); },
    setStatus(value: string) { status = value; },
    setResponse(value: () => Response) { response = value; },
  };
}

test("unknown hosts, robots and rejected app APIs never resolve a container", async () => {
  const f = fixture();
  for (const [url, status] of [
    ["https://demo.shinyhub.dev/", 404],
    ["https://" + control + "/robots.txt", 200],
    ["https://" + apps + "/api/auth/session", 404],
  ] as const) assert.equal((await f.fetch(new Request(url))).status, status);
  assert.deepEqual(f.counts, { resolve: 0, state: 0, start: 0, fetch: 0 });
});

test("cold entry and readiness probes cannot start or proxy into the container", async () => {
  const f = fixture();
  const entry = await f.fetch(new Request("https://" + control + "/"));
  const html = await entry.text();
  assert.equal(entry.status, 200);
  assert.equal(entry.headers.get("x-shinyhub-demo-state"), "asleep");
  assert.ok(html.includes('action="https://' + control + '/__demo/start"'));
  const ready = await f.fetch(new Request("https://" + control + "/__demo/ready"));
  assert.equal(ready.status, 503);
  assert.equal(ready.headers.get("x-shinyhub-demo-state"), "asleep");
  assert.equal(f.counts.start, 0); assert.equal(f.counts.fetch, 0);
});

test("disabled OAuth probes never touch the container, even with browser headers or a warm memo", async (t) => {
  const errors = t.mock.method(console, "error", () => {});
  for (const status of ["stopped", "starting", "healthy"]) {
    const f = fixture(status);
    if (status === "healthy") {
      // Populate the fast path, which must not bypass edge rejection.
      await f.fetch(new Request("https://" + control + "/api/auth/providers"));
    }
    const before = { ...f.counts };
    for (const host of [control, apps]) {
      for (const provider of ["github", "google", "oidc", "%67oogle"]) {
        for (const route of ["", "/login", "/callback?state=x&code=y", "/login/"]) {
          for (const method of ["GET", "HEAD", "POST"]) {
            const response = await f.fetch(new Request(`https://${host}/api/auth/${provider}${route}`, {
              method, headers: { "sec-fetch-dest": "document", accept: "text/html" },
            }));
            assert.equal(response.status, 404);
            assert.equal(response.headers.get("location"), null);
            assert.equal(response.headers.get("cache-control"), "no-store");
          }
        }
      }
    }
    assert.deepEqual(f.counts, before);
    assert.equal(f.work.length, 0);
  }
  assert.equal(errors.mock.callCount(), 0);
});

test("unknown paths never resolve or refresh a container in any lifecycle state", async (t) => {
  const errors = t.mock.method(console, "error", () => {});
  for (const status of ["stopped", "starting", "healthy"]) {
    const f = fixture(status);
    if (status === "healthy") await f.fetch(new Request("https://" + control + "/api/server-info"));
    const before = { ...f.counts };
    for (const host of [control, apps]) {
      for (const path of ["/.env", "/.git/config", "/wp-login.php", "/phpmyadmin/", "/cgi-bin/test", "/metrics", "/debug/pprof/", "/__demo/unknown",
        "/api/unknown", "/static/unknown.js", "/apps/unknown", "/app/unknown/", "/projects/unknown", "/api/auth/%zz/login", "/static/%252e%252e/app.js"]) {
        for (const method of ["GET", "HEAD", "POST"]) {
          const response = await f.fetch(new Request(`https://${host}${path}?demo_next=/`, {
            method, headers: { "sec-fetch-dest": "document", accept: "text/html" },
          }));
          assert.equal(response.status, 404, method + " " + host + path);
          assert.equal(response.headers.get("location"), null);
          assert.equal(response.headers.get("cache-control"), "no-store");
        }
      }
    }
    assert.deepEqual(f.counts, before);
    assert.equal(f.work.length, 0);
  }
  assert.equal(errors.mock.callCount(), 0);
});

test("cold browser-shaped background requests have no redirect or allocation", async () => {
  const f = fixture();
  for (const path of ["/api/server-info", "/api/auth/providers", "/api/apps", "/static/app.js", "/healthz", "/readyz", "/favicon.ico"]) {
    const response = await f.fetch(new Request(`https://${control}${path}`, {
      headers: { "sec-fetch-dest": "document", accept: "text/html" },
    }));
    assert.equal(response.status, 503, path);
    assert.equal(response.headers.get("location"), null, path);
  }
  assert.equal(f.counts.start, 0);
  assert.equal(f.counts.fetch, 0);
  assert.equal(f.work.length, 0);
});

test("browser wakes carry deep links through staging URLs", async () => {
  const f = fixture();
  const destination = "/apps/operations-dashboard?tab=overview";
  const response = await f.fetch(new Request("https://" + control + "/__demo/start?demo_next=" + encodeURIComponent(destination), { method: "POST" }));
  assert.equal(response.headers.get("x-shinyhub-demo-state"), "waking");
  const html = await response.text();
  assert.ok(html.includes("https://" + control + "/?demo_next="));
  assert.equal(html.includes("https://demo.shinyhub.dev/"), false);
  await Promise.all(f.work); assert.equal(f.counts.start, 1); assert.equal(f.counts.fetch, 0);
});

test("cold app navigation redirects to the staging control host without allocating", async () => {
  const f = fixture();
  const response = await f.fetch(new Request("https://" + apps + "/app/dash-demo/", { headers: { "sec-fetch-dest": "document" } }));
  assert.equal(response.status, 303);
  assert.equal(new URL(response.headers.get("location")!).hostname, control);
  assert.equal(f.counts.start, 0);
});

test("cross-site start and session submissions cannot allocate or create sessions", async () => {
  const f = fixture();
  await f.fetch(new Request("https://" + control + "/__demo/start", { method: "POST", headers: { "sec-fetch-site": "cross-site" } }));
  assert.equal(f.counts.start, 0);
  f.setStatus("healthy");
  const response = await f.fetch(new Request("https://" + control + "/__demo/session", { method: "POST", headers: { "sec-fetch-site": "cross-site" } }));
  assert.equal(response.status, 403); assert.equal(f.counts.fetch, 0);
});

test("one-click entry requests a viewer session on staging and preserves its destination", async () => {
  const f = fixture("healthy");
  f.setResponse(() => new Response("{}", { headers: { "set-cookie": "session=test; Secure; HttpOnly; Path=/" } }));
  const response = await f.fetch(new Request("https://" + control + "/__demo/session?demo_next=%2Fapps%2Fidentity-demo", { method: "POST" }));
  assert.equal(response.status, 303); assert.equal(response.headers.get("location"), "/apps/identity-demo");
  assert.ok(response.headers.get("set-cookie")!.includes("HttpOnly"));
  assert.equal(f.requests[0].url, "https://" + control + "/api/auth/session");
  assert.equal((await f.requests[0].json()).username, "demo-viewer");
});

test("failed proxy responses invalidate the warm memo so a later navigation can recover", async () => {
  const f = fixture("healthy");
  f.setResponse(() => new Response("failed", { status: 503 }));
  assert.equal((await f.fetch(new Request("https://" + control + "/"))).status, 503);
  f.setStatus("stopped");
  const response = await f.fetch(new Request("https://" + control + "/", { headers: { "sec-fetch-dest": "document" } }));
  assert.equal(response.headers.get("x-shinyhub-demo-state"), "waking");
  await Promise.all(f.work); assert.equal(f.counts.start, 1);
});

test("intentional asleep and readiness 503s do not log upstream failures", async (t) => {
  const errors = t.mock.method(console, "error", () => {});
  const f = fixture();
  assert.equal((await f.fetch(new Request("https://" + control + "/api/server-info"))).status, 503);
  assert.equal((await f.fetch(new Request("https://" + control + "/__demo/ready"))).status, 503);
  f.setStatus("starting");
  f.setResponse(() => new Response(null, { status: 503 }));
  assert.equal((await f.fetch(new Request("https://" + control + "/__demo/ready"))).status, 503);
  assert.equal(errors.mock.callCount(), 0);
});

test("real upstream 5xxs log bounded errors without request data", async (t) => {
  const errors = t.mock.method(console, "error", () => {});
  for (const status of [500, 502, 503]) {
    const f = fixture("healthy");
    f.setResponse(() => new Response("private upstream body", { status }));
    const response = await f.fetch(new Request("https://" + apps + "/app/identity-demo/?token=private", {
      headers: { cookie: "session=private" },
    }));
    assert.equal(response.status, status);
    assert.equal(await response.text(), "private upstream body");
    assert.deepEqual(errors.mock.calls.at(-1)!.arguments, [
      { event: "demo_upstream_failed", operation: "proxy", status },
    ]);
  }
  const login = fixture("healthy");
  login.setResponse(() => new Response(null, { status: 503 }));
  assert.equal((await login.fetch(new Request("https://" + control + "/__demo/session", { method: "POST" }))).status, 303);
  assert.deepEqual(errors.mock.calls.at(-1)!.arguments, [
    { event: "demo_upstream_failed", operation: "viewer_session", status: 503 },
  ]);
  assert.equal(errors.mock.callCount(), 4);
});

test("native readiness polling checks live health without proxying or trusting the warm memo", async () => {
  const f = fixture("healthy", true);
  const probe = () => f.fetch(new Request("https://" + control + "/__demo/ready"));
  assert.equal((await probe()).status, 204);
  f.setStatus("starting");
  const starting = await probe();
  assert.equal(starting.status, 503);
  assert.equal(starting.headers.get("x-shinyhub-demo-state"), "starting");
  f.setStatus("stopped");
  const asleep = await probe();
  assert.equal(asleep.status, 503);
  assert.equal(asleep.headers.get("x-shinyhub-demo-state"), "asleep");
  assert.equal(f.counts.state, 3);
  assert.equal(f.counts.fetch, 0);
  assert.equal(f.counts.start, 0);
  const navigation = await f.fetch(new Request("https://" + control + "/", { headers: { "sec-fetch-dest": "document" } }));
  assert.equal(navigation.headers.get("x-shinyhub-demo-state"), "waking");
  await Promise.all(f.work);
  assert.equal(f.counts.start, 1);
  assert.equal(f.counts.fetch, 0);
});


test("monitoring observes current state without touching activity or filling the warm memo", async () => {
  for (const passive of [false, true]) {
    const f = fixture("healthy", passive);
    const response = await f.fetch(new Request("https://" + control + "/__demo/status"));
    assert.equal(response.status, 200);
    assert.deepEqual(await response.json(), { state: "ready" });
    assert.deepEqual(f.counts, { resolve: 1, state: 1, start: 0, fetch: 0 });
    f.setStatus("stopped");
    await f.fetch(new Request("https://" + control + "/__demo/start", { method: "POST" }));
    await Promise.all(f.work);
    assert.equal(f.counts.state, 2);
    assert.equal(f.counts.start, 1);
    assert.equal(f.counts.fetch, 0);
  }
});

test("monitoring bypasses a previously populated warm memo", async () => {
  const f = fixture("healthy");
  await f.fetch(new Request("https://" + control + "/"));
  f.setStatus("stopped");
  const response = await f.fetch(new Request("https://" + control + "/__demo/status"));
  assert.deepEqual(await response.json(), { state: "asleep" });
  assert.deepEqual(f.counts, { resolve: 2, state: 2, start: 0, fetch: 1 });
});

test("non-GET monitoring requests cannot start, proxy or read state", async () => {
  const f = fixture();
  const response = await f.fetch(new Request("https://" + control + "/__demo/status", { method: "POST" }));
  assert.equal(response.status, 405);
  assert.equal(response.headers.get("allow"), "GET");
  assert.deepEqual(f.counts, { resolve: 1, state: 0, start: 0, fetch: 0 });
});
