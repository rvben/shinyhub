import assert from "node:assert/strict";
import test from "node:test";
import { bridgeWebSockets, trackResponse } from "./container-connections.ts";

class Socket extends EventTarget {
  accepted = false;
  messages: unknown[] = [];
  closes: { code: number; reason: string }[] = [];
  failSend = false;
  failAccept = false;
  accept() { if (this.failAccept) throw new Error("accept failed"); this.accepted = true; }
  send(data: unknown) { if (this.failSend) throw new Error("send failed"); this.messages.push(data); }
  close(code: number, reason: string) { this.closes.push({ code, reason }); }
  emit(type: string, fields: object = {}) { this.dispatchEvent(Object.assign(new Event(type), fields)); }
}

test("WebSockets forward text and binary in both directions, and settle exactly once", () => {
  const upstream = new Socket(), downstream = new Socket();
  let finished = 0;
  bridgeWebSockets(upstream as unknown as WebSocket, downstream as unknown as WebSocket, () => finished++);
  assert.ok(upstream.accepted && downstream.accepted);
  const binary = new Uint8Array([1, 2, 3]).buffer;
  downstream.emit("message", { data: "client" });
  upstream.emit("message", { data: binary });
  assert.deepEqual(upstream.messages, ["client"]);
  assert.deepEqual(downstream.messages, [binary]);
  for (const socket of [upstream, downstream]) {
    socket.emit("close", { code: 1000, reason: "done" });
    socket.emit("error");
    socket.emit("message", { data: "too late" });
  }
  assert.equal(finished, 1);
  assert.deepEqual(upstream.closes, [{ code: 1000, reason: "done" }]);
  assert.deepEqual(downstream.closes, upstream.closes);
  assert.deepEqual(upstream.messages, ["client"]);
});

test("close/error/send/accept failures clean up both WebSockets without reserved close codes", () => {
  for (const side of ["upstream", "downstream"] as const) {
    for (const failure of ["close", "error", "send", "accept"] as const) {
      const sockets = { upstream: new Socket(), downstream: new Socket() };
      const source = sockets[side], other = sockets[side === "upstream" ? "downstream" : "upstream"];
      let finished = 0;
      if (failure === "accept") source.failAccept = true;
      const bridge = () => bridgeWebSockets(sockets.upstream as unknown as WebSocket, sockets.downstream as unknown as WebSocket, () => finished++);
      if (failure === "accept") assert.throws(bridge, /accept failed/);
      else {
        bridge();
        if (failure === "send") { other.failSend = true; source.emit("message", { data: "test" }); }
        else source.emit(failure, { code: 1006, reason: "" });
      }
      source.emit("error");
      assert.equal(finished, 1, `${side}: ${failure}`);
      assert.equal(source.closes.length, 1);
      assert.equal(other.closes.length, 1);
      assert.equal(other.closes[0].code, failure === "close" ? 1000 : 1011);
    }
  }
});

test("ordinary response bodies preserve headers/status and complete activity at EOF", async () => {
  let finished = 0;
  const response = trackResponse(new Response("hello", { status: 201, headers: { "x-test": "kept" } }), () => finished++);
  assert.equal(response.status, 201);
  assert.equal(response.headers.get("x-test"), "kept");
  assert.equal(await response.text(), "hello");
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(finished, 1);
});

test("stream cancellation reaches upstream and releases activity once even during a pending read", async () => {
  let finished = 0, cancelled: unknown;
  const upstream = new ReadableStream({ cancel(reason) { cancelled = reason; } });
  const response = trackResponse(new Response(upstream), () => finished++);
  const reader = response.body!.getReader();
  const pending = reader.read();
  await reader.cancel("client disconnected");
  assert.equal((await pending).done, true);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(cancelled, "client disconnected");
  assert.equal(finished, 1);
});

test("upstream stream errors and bodyless responses release activity", async () => {
  let finished = 0;
  const upstream = new ReadableStream({ pull() { throw new Error("stream failed"); } });
  const response = trackResponse(new Response(upstream), () => finished++);
  await assert.rejects(response.text(), /stream failed/);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(finished, 1);
  const empty = new Response(null, { status: 204 });
  assert.equal(trackResponse(empty, () => finished++), empty);
  assert.equal(finished, 2);
});

test("client cancellation releases activity before slow upstream cleanup completes", async () => {
  let finished = 0;
  let release!: () => void;
  const cleanup = new Promise<void>((resolve) => { release = resolve; });
  const response = trackResponse(new Response(new ReadableStream({ cancel() { return cleanup; } })), () => finished++);
  const cancelled = response.body!.cancel();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(finished, 1);
  release();
  await cancelled;
  assert.equal(finished, 1);
});
