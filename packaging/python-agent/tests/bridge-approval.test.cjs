const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { JSDOM } = require("jsdom");

const source = fs.readFileSync(path.join(__dirname, "../src/shinyhub_agent/www/bridge.js"), "utf8");

function mount(t) {
  const dom = new JSDOM("<!doctype html><body></body>", {
    runScripts: "outside-only", url: "https://apps.example.test/"
  });
  t.after(() => dom.window.close());
  const window = dom.window, handlers = new Map(), requests = [];
  window.Shiny = {
    addCustomMessageHandler: (name, fn) => handlers.set(name, fn),
    setInputValue: (name, value) => requests.push(value)
  };
  window.eval(source);
  function connect(session = "this-is-a-session-nonce") {
    handlers.get("shinyhub-agent-capabilities")({ version: 1, session, tools: [
      { name: "set_filters", description: "Change filters", readOnly: false, confirmation: "Change filters?" }
    ] });
  }
  function respond(request, result, ok = true) {
    handlers.get("shinyhub-agent-result")({ version: 1, session: request.session,
      requestId: request.requestId, ok, result, message: ok ? undefined : "October has no data yet." });
  }
  connect();
  return { window, doc: window.document, requests, connect, respond };
}

test("browser writes preflight an immutable snapshot and show literal arguments before execution", async (t) => {
  const { window, doc, requests, respond } = mount(t);
  const args = { filters: { model: ["alpha", "<b>literal</b>"] } };
  const pending = window.shinyhubAgentTools.invoke("set_filters", args);
  const prepared = requests.at(-1);
  assert.equal(prepared.action, "prepare");
  assert.equal(doc.querySelector(".sh-agent-confirm"), null);
  args.filters.model[0] = "changed while waiting";
  respond(prepared, { arguments: prepared.arguments, confirmation: "Change filters?", description: "Filter Models = alpha" });
  await Promise.resolve();
  assert.equal(doc.querySelector(".sh-agent-confirm p").textContent, "Filter Models = alpha");
  assert.match(doc.querySelector(".sh-agent-confirm-values").textContent, /alpha/);
  assert.equal(doc.querySelector(".sh-agent-confirm b"), null);
  doc.querySelector(".sh-agent-confirm-apply").click();
  await Promise.resolve();
  const executed = requests.at(-1);
  assert.equal(executed.action, "execute");
  assert.equal(executed.arguments.filters.model[0], "alpha");
  respond(executed, { applied: true });
  assert.deepEqual(await pending, { applied: true });
});

test("browser preflight rejection and visitor decline never dispatch execution", async (t) => {
  const { window, doc, requests, respond } = mount(t);
  const rejected = window.shinyhubAgentTools.invoke("set_filters", {});
  respond(requests.at(-1), null, false);
  await assert.rejects(rejected, /October has no data/);
  assert.equal(doc.querySelector(".sh-agent-confirm"), null);
  const declined = window.shinyhubAgentTools.invoke("set_filters", {});
  const prepared = requests.at(-1);
  respond(prepared, { arguments: {}, confirmation: "Change filters?" });
  await Promise.resolve();
  doc.querySelector(".sh-agent-confirm button").click();
  await assert.rejects(declined, /cancelled/);
  assert.equal(requests.filter((x) => x.action === "execute").length, 0);
});

test("a reconnect cancels a browser approval rather than applying it to the next session", async (t) => {
  const { window, doc, requests, respond, connect } = mount(t);
  const pending = window.shinyhubAgentTools.invoke("set_filters", {});
  respond(requests.at(-1), { arguments: {}, confirmation: "Change filters?" });
  await Promise.resolve();
  assert.ok(doc.querySelector(".sh-agent-confirm"));
  connect("another-session-nonce");
  await assert.rejects(pending, /cancelled/);
  assert.equal(doc.querySelector(".sh-agent-confirm"), null);
  assert.equal(requests.filter((x) => x.action === "execute").length, 0);
});

test("parallel browser writes cannot open competing approval cards", async (t) => {
  const { window, requests, respond } = mount(t);
  const first = window.shinyhubAgentTools.invoke("set_filters", {});
  await assert.rejects(window.shinyhubAgentTools.invoke("set_filters", {}), /awaiting confirmation/);
  assert.equal(requests.filter((x) => x.action === "prepare").length, 1);
  respond(requests.at(-1), null, false);
  await assert.rejects(first, /October/);
});
