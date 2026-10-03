import assert from "node:assert/strict";
import test from "node:test";
import { passiveDemoStatus } from "./demo-status.ts";

test("monitoring only reads lifecycle state in every phase", async () => {
  for (const [status, expected] of [
    ["stopped", "asleep"], ["stopped_with_code", "asleep"], ["stopping", "asleep"],
    ["running", "starting"], ["healthy", "ready"], ["future-status", "starting"],
  ]) {
    const calls: string[] = [];
    const container = {
      async getState() { calls.push("state"); return { status }; },
      async start() { calls.push("start"); throw new Error("Monitoring woke the container"); },
      async fetch() { calls.push("fetch"); throw new Error("Monitoring renewed container activity"); },
    };
    const response = await passiveDemoStatus("GET", container);
    assert.equal(response.status, 200);
    assert.equal(response.headers.get("cache-control"), "no-store");
    assert.deepEqual(await response.json(), { state: expected });
    assert.deepEqual(calls, ["state"], status);
  }
});

test("non-GET monitoring requests touch nothing", async () => {
  const response = await passiveDemoStatus("POST", {
    async getState() { throw new Error("Unexpected state read"); },
  });
  assert.equal(response.status, 405);
  assert.equal(response.headers.get("allow"), "GET");
});

test("a failed state read cannot report readiness", async () => {
  await assert.rejects(passiveDemoStatus("GET", {
    async getState() { throw new Error("Unavailable"); },
  }), /Unavailable/);
});
