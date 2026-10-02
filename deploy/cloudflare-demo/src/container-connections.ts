// Standard accepted WebSockets and open response streams keep the DO resident.
// Deliberately do not use hibernating sockets: the upstream container owns them.
export function bridgeWebSockets(upstream: WebSocket, downstream: WebSocket, finish: () => void): void {
  let settled = false;
  const close = (socket: WebSocket, code: number, reason: string) => {
    try { socket.close(code, reason); } catch { /* Already disconnected. */ }
  };
  const settle = (code: number, reason: string) => {
    if (settled) return;
    settled = true;
    finish();
    // Reserved codes cannot be sent in a close frame.
    if ([1005, 1006, 1015].includes(code)) code = 1000;
    close(upstream, code, reason);
    close(downstream, code, reason);
  };
  for (const [from, to] of [[upstream, downstream], [downstream, upstream]]) {
    from.addEventListener("message", (event) => {
      if (settled) return;
      try { to.send(event.data); }
      catch { settle(1011, "WebSocket forwarding failed"); }
    });
    from.addEventListener("close", (event) => settle(event.code, event.reason));
    from.addEventListener("error", () => settle(1011, "WebSocket disconnected"));
  }
  try {
    upstream.accept();
    downstream.accept();
  } catch (error) {
    settle(1011, "WebSocket upgrade failed");
    throw error;
  }
}

export function trackResponse(response: Response, finish: () => void): Response {
  if (response.webSocket) {
    const { 0: client, 1: server } = new WebSocketPair();
    bridgeWebSockets(response.webSocket, server, finish);
    return new Response(null, { status: response.status, headers: response.headers, webSocket: client });
  }
  if (!response.body) {
    finish();
    return response;
  }
  // Use the runtime's native pipe, matching the Containers SDK. JS-backed
  // response streams can lose their cancel callback at the HTTP/RPC boundary.
  const { readable, writable } = typeof IdentityTransformStream === "undefined"
    ? new TransformStream<Uint8Array, Uint8Array>() : new IdentityTransformStream();
  const upstream = response.body;
  void upstream.pipeTo(writable, { preventCancel: true }).then(
    () => finish(),
    (reason) => {
      // A disconnected client must not wait for native upstream cleanup.
      finish();
      void upstream.cancel(reason).catch(() => {});
    },
  ).catch((error) => console.error("Could not settle container response", error));
  return new Response(readable, { status: response.status, statusText: response.statusText, headers: response.headers });
}
