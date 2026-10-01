const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { JSDOM } = require("jsdom");

const source = fs.readFileSync(path.join(__dirname, "../src/shinyhub_agent/www/chat.js"), "utf8");

// Loads the widget into a fresh page, opens a session and asks one question, so
// each test starts with an answer in flight and drives the events that follow.
function ask(t) {
  const dom = new JSDOM("<!doctype html><body></body>", { runScripts: "outside-only" });
  const window = dom.window;
  t.after(() => window.close());
  const handlers = new Map();
  const inputs = [];
  let ids = 0;
  window.Shiny = {
    addCustomMessageHandler(name, handler) { handlers.set(name, handler); },
    setInputValue(name, value) { inputs.push({ name, value }); }
  };
  Object.defineProperty(window, "crypto", { value: { randomUUID: () => "request_" + (++ids) } });
  window.matchMedia = () => ({ matches: false });
  Object.defineProperty(window.navigator, "clipboard", { value: { writeText: () => Promise.resolve() } });
  window.eval(source);

  const doc = window.document;
  handlers.get("shinyhub-agent-chat-capabilities")({ version: 1, enabled: true, session: "s1" });
  doc.querySelector(".sh-agent-launcher").click();
  let requestId;
  function submit(question) {
    doc.querySelector(".sh-agent-input").value = question;
    doc.querySelector(".sh-agent-composer").requestSubmit();
    requestId = inputs.filter((entry) => entry.name === ".shinyhub_agent_chat_request" && entry.value.message).at(-1).value.requestId;
  }
  submit("Show only 2024");
  const send = (event) => handlers.get("shinyhub-agent-chat-event")(
    Object.assign({ version: 1, session: "s1", requestId }, event));
  return { window, doc, send, inputs, submit };
}

function card(doc) {
  const approval = doc.querySelector(".sh-agent-approval");
  return {
    result: approval.dataset.result,
    label: approval.querySelector(".sh-agent-approval-label").textContent,
    buttons: approval.querySelectorAll("button").length
  };
}

function requestApproval(send) {
  send({ type: "tool_started", name: "set_year", readOnly: false });
  send({ type: "approval_required", approvalId: "a1", name: "set_year",
    arguments: { year: 2024 }, message: "Show only 2024?" });
}

test("an approved change whose tool fails is reported as failed, not left applying", (t) => {
  const { doc, send } = ask(t);
  requestApproval(send);
  doc.querySelector(".sh-agent-apply").click();
  assert.equal(card(doc).result, "applying");

  send({ type: "tool_finished", name: "set_year", ok: false });
  send({ type: "delta", text: "The year could not be changed." });
  send({ type: "done" });

  assert.deepEqual(card(doc), { result: "failed", label: "Change failed", buttons: 0 });
});

test("an approval that times out loses its buttons instead of offering a dead choice", (t) => {
  const { doc, send, inputs } = ask(t);
  requestApproval(send);

  send({ type: "tool_finished", name: "set_year", ok: false });
  assert.deepEqual(card(doc), { result: "expired", label: "Not approved in time", buttons: 0 });

  send({ type: "delta", text: "No change was made." });
  send({ type: "done" });
  assert.equal(doc.querySelectorAll(".sh-agent-approval").length, 1, "the expired card stays in the log");
  assert.equal(inputs.filter((entry) => entry.name === ".shinyhub_agent_chat_decision").length, 0);
});

test("a declined change stays declined when the tool reports failure", (t) => {
  const { doc, send } = ask(t);
  requestApproval(send);
  doc.querySelector(".sh-agent-approval .sh-agent-secondary").click();
  send({ type: "tool_finished", name: "set_year", ok: false });
  send({ type: "delta", text: "The current view was kept." });
  send({ type: "done" });
  assert.deepEqual(card(doc), { result: "declined", label: "Change declined", buttons: 0 });
});

test("a failure of another tool leaves an applying change alone", (t) => {
  const { doc, send } = ask(t);
  requestApproval(send);
  doc.querySelector(".sh-agent-apply").click();
  send({ type: "tool_started", name: "read_rows", readOnly: true });
  send({ type: "tool_finished", name: "read_rows", ok: false });
  assert.equal(card(doc).result, "applying");

  send({ type: "tool_finished", name: "set_year", ok: true });
  send({ type: "action_applied" });
  assert.equal(doc.querySelector(".sh-agent-approval"), null);
  const receipt = doc.querySelector(".sh-agent-receipt");
  assert.equal(receipt.querySelector(".sh-agent-receipt-title").textContent, "Change applied to this view");
  assert.equal(receipt.querySelectorAll("button").length, 0);
});

test("a finished answer is announced once through a polite live region", (t) => {
  const { doc, send } = ask(t);
  const log = doc.querySelector(".sh-agent-log");
  const announcer = doc.querySelector(".sh-agent-sr-only");
  assert.equal(log.getAttribute("aria-live"), "off", "streaming fragments stay silent");
  assert.equal(announcer.getAttribute("aria-live"), "polite");

  send({ type: "delta", text: "Sales peaked " });
  send({ type: "delta", text: "in March." });
  assert.equal(announcer.textContent, "", "nothing is announced while the answer streams");

  send({ type: "done" });
  assert.equal(announcer.textContent, "Answer: Sales peaked in March.");
  assert.equal(announcer.closest("[hidden]"), null, "a hidden region is not announced");
});

test("an interrupted answer is announced by the status, not as a finished answer", (t) => {
  const { doc, send } = ask(t);
  send({ type: "delta", text: "Partial" });
  send({ type: "error", message: "The assistant could not finish." });
  assert.equal(doc.querySelector(".sh-agent-sr-only").textContent, "");
  assert.equal(doc.querySelector(".sh-agent-status-text").textContent, "Answer interrupted");
});

test("a new question clears the live region so identical answers can be announced again", (t) => {
  const { doc, send, submit } = ask(t);
  const announcer = doc.querySelector(".sh-agent-sr-only");
  send({ type: "delta", text: "No change was made." });
  send({ type: "done" });
  assert.equal(announcer.textContent, "Answer: No change was made.");

  submit("Did anything change?");
  assert.equal(announcer.textContent, "", "the previous answer is removed before streaming");
  send({ type: "delta", text: "No change was made." });
  send({ type: "done" });
  assert.equal(announcer.textContent, "Answer: No change was made.");
});

test("starting a new chat removes the previous answer from the live region", (t) => {
  const { doc, send } = ask(t);
  send({ type: "delta", text: "The previous conversation." });
  send({ type: "done" });
  send({ type: "reset" });
  assert.equal(doc.querySelector(".sh-agent-sr-only").textContent, "");
});
