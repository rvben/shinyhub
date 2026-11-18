import assert from "node:assert/strict";
import test from "node:test";
import { manageDemoSilence } from "../../../scripts/demo-alert-silence.mjs";

const account = "a".repeat(32);
const policy = "b".repeat(32);
const silenceID = "c".repeat(32);
const otherID = "d".repeat(32);
const time = Date.parse("2026-11-18T15:40:00Z");
const env = { CLOUDFLARE_ACCOUNT_ID: account, CLOUDFLARE_DEMO_WORKER_ERRORS_POLICY_ID: policy,
  CLOUDFLARE_NOTIFICATIONS_API_TOKEN: "test-token", SHINYHUB_DEMO_SILENCE_ID: silenceID,
  SHINYHUB_DEMO_VERIFIED_AT: String(time / 1_000) };

type Request = { method: string; path: string; body?: unknown };

function fixture(handle: (request: Request) => unknown, clock = time) {
  const calls: Request[] = [];
  const waits: number[] = [];
  const assertionErrors: unknown[] = [];
  const options = {
    now: () => clock,
    wait: async (ms: number) => { waits.push(ms); },
    fetchAPI: async (url: string, init: RequestInit) => {
      const base = `https://api.cloudflare.com/client/v4/accounts/${account}/alerting/v3`;
      assert.ok(url.startsWith(base));
      assert.equal(init.redirect, "error");
      const request = { method: init.method!, path: url.slice(base.length),
        body: typeof init.body === "string" ? JSON.parse(init.body) : undefined };
      calls.push(request);
      try {
        const result = handle(request);
        return result instanceof Response ? result : Response.json({ success: true, result });
      } catch (error) {
        if (error instanceof assert.AssertionError) assertionErrors.push(error);
        throw error;
      }
    },
  };
  async function run(action: string, overrides = {}) {
    const result = await manageDemoSilence(action, { ...env, ...overrides }, options).catch(error => {
      throw assertionErrors[0] ?? error;
    });
    if (assertionErrors.length) throw assertionErrors[0];
    return result;
  }
  return { calls, waits, run };
}

const monitor = { id: policy, enabled: true, name: "ShinyHub demo — Worker errors", alert_type: "custom_analytics" };
const window = { policy_id: policy, start_time: "2026-11-18T15:40:05.000Z", end_time: "2026-11-18T16:26:00.000Z" };

test("rollout waits for its own confirmed, bounded silence to be active", async () => {
  const prior = { ...window, id: otherID };
  let created = false;
  let listCalls = 0;
  const f = fixture(({ method, path, body }) => {
    if (path === `/policies/${policy}`) return monitor;
    assert.equal(path, "/silences");
    if (method === "POST") {
      assert.deepEqual(body, [window]);
      created = true;
      return undefined; // The Cloudflare create response does not return an ID.
    }
    assert.equal(method, "GET");
    const visible = ++listCalls > 2;
    return created && visible ? [prior, { ...window, id: silenceID }] : [prior];
  });
  assert.deepEqual(await f.run("start"), { id: silenceID, endTime: window.end_time });
  assert.deepEqual(f.waits, [1_000, 5_000]);
  assert.equal(f.calls.filter(call => call.method === "POST").length, 1);
});

test("an ambiguous create response is reconciled without issuing another POST", async () => {
  let created = false;
  let listUnavailable = true;
  const f = fixture(({ method, path }) => {
    if (path.startsWith("/policies/")) return monitor;
    if (method === "POST") {
      created = true;
      throw new Error("socket interrupted");
    }
    if (created && listUnavailable) {
      listUnavailable = false;
      return new Response(null, { status: 503 });
    }
    return created ? [{ ...window, id: silenceID }] : [];
  });
  assert.equal((await f.run("start")).id, silenceID);
  assert.equal(f.calls.filter(call => call.method === "POST").length, 1);
});

test("the configured policy ID is sent verbatim and Cloudflare can return either UUID format", async () => {
  const dashedPolicy = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb";
  let created = false;
  const f = fixture(({ method, path, body }) => {
    if (path.startsWith("/policies/")) {
      assert.equal(path, `/policies/${dashedPolicy}`);
      return monitor;
    }
    if (method === "POST") {
      assert.deepEqual(body, [{ ...window, policy_id: dashedPolicy }]);
      created = true;
      return undefined;
    }
    return created ? [{ ...window, id: silenceID, policy_id: dashedPolicy }] : [];
  });
  assert.equal((await f.run("start", { CLOUDFLARE_DEMO_WORKER_ERRORS_POLICY_ID: dashedPolicy })).id, silenceID);
});

test("a wrong policy, missing token, or unconfirmed silence blocks the rollout", async () => {
  const wrong = fixture(() => ({ ...monitor, name: "ShinyHub demo — Container lifecycle errors" }));
  await assert.rejects(wrong.run("start", { CLOUDFLARE_NOTIFICATIONS_API_TOKEN: "" }), /Notifications Write/);
  assert.equal(wrong.calls.length, 0);
  await assert.rejects(wrong.run("start"), /Expected the enabled Cloudflare monitor/);
  assert.deepEqual(wrong.calls.map(call => call.method), ["GET"]);

  const absent = fixture(({ method, path }) => {
    if (path.startsWith("/policies/")) return monitor;
    return method === "GET" ? [] : Response.json({ success: false, errors: [{}] }, { status: 403 });
  });
  await assert.rejects(absent.run("start"), /POST silence failed \(HTTP 403\)/);
  assert.equal(absent.calls.filter(call => call.method === "POST").length, 1);
});

test("success waits until eleven minutes after verification and confirms removal", async () => {
  let removed = false;
  let deletedReads = 0;
  const silence = { ...window, id: silenceID };
  const f = fixture(({ method, path }) => {
    assert.equal(path, `/silences/${silenceID}`);
    if (method === "DELETE") {
      assert.deepEqual(f.waits, [9 * 60_000]);
      removed = true;
      return undefined;
    }
    assert.equal(method, "GET");
    return removed && ++deletedReads > 1 ? new Response(null, { status: 404 }) : silence;
  }, time + 2 * 60_000);
  assert.deepEqual(await f.run("success"), {});
  assert.deepEqual(f.waits, [9 * 60_000, 1_000]);
  assert.deepEqual(f.calls.map(call => call.method), ["GET", "DELETE", "GET", "GET"]);
});

test("failure removes only the owned policy silence and tolerates a concurrent expiry", async () => {
  let expired = false;
  const f = fixture(({ method, path }) => {
    assert.equal(path, `/silences/${silenceID}`);
    if (method === "DELETE") expired = true;
    return expired ? new Response(null, { status: 404 }) : { ...window, id: silenceID };
  });
  await f.run("failure");
  assert.deepEqual(f.calls.map(call => call.method), ["GET", "DELETE", "GET"]);
  assert.deepEqual(f.waits, []);

  const wrong = fixture(() => ({ ...window, id: silenceID, policy_id: otherID }));
  await assert.rejects(wrong.run("failure"), /does not belong/);
  assert.deepEqual(wrong.calls.map(call => call.method), ["GET"]);
});

test("success fails visibly if the silence vanished or expires too soon", async () => {
  const absent = fixture(() => new Response(null, { status: 404 }));
  await assert.rejects(absent.run("success"), /disappeared/);
  const short = fixture(() => ({ ...window, id: silenceID, end_time: "2026-11-18T15:41:00Z" }));
  await assert.rejects(short.run("success"), /expires before/);
  assert.deepEqual(short.calls.map(call => call.method), ["GET"]);
});

test("a finalizer arriving after cooldown removes the owned silence", async () => {
  let removed = false;
  const f = fixture(({ method }) => {
    if (method === "DELETE") removed = true;
    return removed ? new Response(null, { status: 404 }) : { ...window, id: silenceID };
  }, time + 12 * 60_000);
  assert.deepEqual(await f.run("success"), {});
  assert.deepEqual(f.calls.map(call => call.method), ["GET", "DELETE", "GET"]);
  assert.deepEqual(f.waits, []);
});

test("a failed cleanup can confirm that Cloudflare applied a DELETE", async () => {
  let removed = false;
  const f = fixture(({ method }) => {
    if (method === "DELETE") {
      removed = true;
      throw new Error("socket interrupted");
    }
    return removed ? new Response(null, { status: 404 }) : { ...window, id: silenceID };
  });
  await f.run("failure");
  assert.deepEqual(f.calls.map(call => call.method), ["GET", "DELETE", "GET"]);
});

test("cleanup accepts a retained silence record whose coverage has ended", async () => {
  let removed = false;
  const f = fixture(({ method }) => {
    if (method === "DELETE") {
      removed = true;
      return undefined;
    }
    return { ...window, id: silenceID, end_time: removed ? new Date(time).toISOString() : window.end_time };
  });
  await f.run("failure");
  assert.deepEqual(f.calls.map(call => call.method), ["GET", "DELETE", "GET"]);
});
