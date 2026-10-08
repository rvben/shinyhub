import assert from "node:assert/strict";
import test from "node:test";
import { checkWebSocket } from "../../../scripts/demo-websocket-smoke.mjs";

function socketFixture(closeCode = 1000, wasClean = true, errorAfterOpen = false, neverCloses = false) {
  return class Socket extends EventTarget {
    static closes: unknown[][] = [];
    constructor() {
      super();
      queueMicrotask(() => this.dispatchEvent(new Event("open")));
    }
    close(...args: unknown[]) {
      Socket.closes.push(args);
      if (neverCloses) return;
      queueMicrotask(() => this.dispatchEvent(errorAfterOpen ? new Event("error") :
        Object.assign(new Event("close"), { code: closeCode, wasClean })));
    }
  };
}

test("WebSocket smoke succeeds only after a normal close handshake", async () => {
  const Socket = socketFixture();
  await checkWebSocket("wss://demo.example/test", Socket);
  assert.deepEqual(Socket.closes, [[1000, "Smoke test complete"]]);
});

test("an open socket followed by an error or abnormal close must fail smoke", async () => {
  for (const Socket of [socketFixture(1006, false), socketFixture(1000, false), socketFixture(1000, true, true)]) {
    await assert.rejects(checkWebSocket("wss://demo.example/test", Socket), /websocket (error|close)/);
  }
});

test("a close handshake that never finishes remains bounded", async () => {
  await assert.rejects(checkWebSocket("wss://demo.example/test", socketFixture(1000, true, false, true), 10), /timed out/);
});
