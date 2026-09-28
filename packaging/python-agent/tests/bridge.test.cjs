const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const handlers = new Map();
const requests = [];
const window = {
  Shiny: {
    addCustomMessageHandler(name, handler) { handlers.set(name, handler); },
    setInputValue(name, value) { requests.push({ name, value }); }
  },
  confirm() { return true; },
  dispatchEvent() {}
};
const context = vm.createContext({
  window,
  document: {},
  crypto: { randomUUID: () => "request_12345678" },
  CustomEvent: class { constructor() {} },
  setTimeout,
  clearTimeout,
  setInterval,
  clearInterval,
  Map,
  Set,
  Promise
});
const source = fs.readFileSync(path.join(__dirname, "../src/shinyhub_agent/www/bridge.js"), "utf8");
vm.runInContext(source, context);

assert.ok(handlers.has("shinyhub-agent-capabilities"));
assert.ok(handlers.has("shinyhub-agent-result"));

handlers.get("shinyhub-agent-capabilities")({
  version: 1,
  session: "this-is-a-session-nonce",
  tools: [{
    name: "get_view",
    description: "Read view",
    inputSchema: { type: "object", properties: {} },
    readOnly: true
  }]
});

(async () => {
  const pending = window.shinyhubAgentTools.invoke("get_view", {});
  const request = requests.at(-1);
  assert.equal(request.name, ".shinyhub_agent_request");
  assert.equal(request.value.session, "this-is-a-session-nonce");
  handlers.get("shinyhub-agent-result")({
    version: 1,
    session: "this-is-a-session-nonce",
    requestId: "request_12345678",
    ok: true,
    result: { period: "year" }
  });
  assert.deepEqual(await pending, { period: "year" });

  await assert.rejects(window.shinyhubAgentTools.invoke("missing", {}), /unavailable/);
  console.log("bridge protocol passed");
})().catch(error => { console.error(error); process.exitCode = 1; });
