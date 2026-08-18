const endpoints = process.argv.slice(2);

if (endpoints.length === 0) {
  console.error("usage: node scripts/demo-websocket-smoke.mjs <wss-url> [...]");
  process.exit(2);
}

async function check(endpoint) {
  await new Promise((resolve, reject) => {
    const socket = new WebSocket(endpoint);
    const timer = setTimeout(() => {
      socket.close();
      reject(new Error(`${endpoint} -> timed out`));
    }, 15_000);

    socket.addEventListener("open", () => {
      clearTimeout(timer);
      socket.close();
      console.log(`${endpoint} -> websocket open`);
      resolve();
    }, { once: true });

    socket.addEventListener("error", () => {
      clearTimeout(timer);
      reject(new Error(`${endpoint} -> websocket error`));
    }, { once: true });
  });
}

for (const endpoint of endpoints) {
  await check(endpoint);
}
