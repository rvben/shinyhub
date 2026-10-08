import { pathToFileURL } from "node:url";

export async function checkWebSocket(endpoint, Socket = WebSocket, timeoutMS = 15_000) {
  await new Promise((resolve, reject) => {
    const socket = new Socket(endpoint);
    let opened = false;
    const timer = setTimeout(() => {
      socket.close();
      reject(new Error(`${endpoint} -> timed out`));
    }, timeoutMS);

    socket.addEventListener("open", () => {
      opened = true;
      socket.close(1000, "Smoke test complete");
    }, { once: true });

    socket.addEventListener("close", (event) => {
      clearTimeout(timer);
      if (opened && event.code === 1000 && event.wasClean) resolve();
      else reject(new Error(`${endpoint} -> unexpected websocket close (${event.code})`));
    }, { once: true });

    socket.addEventListener("error", () => {
      clearTimeout(timer);
      reject(new Error(`${endpoint} -> websocket error`));
    }, { once: true });
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const endpoints = process.argv.slice(2);
  if (endpoints.length === 0) {
    console.error("usage: node scripts/demo-websocket-smoke.mjs <wss-url> [...]");
    process.exit(2);
  }
  for (const endpoint of endpoints) {
    await checkWebSocket(endpoint);
    console.log(`${endpoint} -> websocket open and clean close`);
  }
}
