const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { JSDOM } = require("jsdom");

const source = fs.readFileSync(path.join(__dirname, "../src/shinyhub_agent/www/chat.js"), "utf8");

test("table measurements stay together while descriptive values can wrap", (t) => {
  const { doc, send } = ask(t);
  send({ type: "delta", text: "| Description | Value |\n| --- | ---: |\n| **North** business line | **$442.91K** |\n| Cost rose 2.1% this week | -2.1% |\n| Euro cost | €1,230.50 |\n| Refund | (42.00) |\n| Status | No changes |" });
  send({ type: "done" });
  const rows = doc.querySelectorAll("tbody tr");
  assert.equal(rows[0].cells[0].classList.contains("sh-agent-cell-value"), false);
  for (const row of Array.from(rows).slice(0, 4)) {
    assert.equal(row.cells[1].classList.contains("sh-agent-cell-value"), true);
  }
  assert.equal(rows[4].cells[1].classList.contains("sh-agent-cell-value"), false);
});

test("fallback restores a valid size and remembers compact width after expansion", (t) => {
  const { window, doc } = ask(t, { savedSize: '{"width":620,"expanded":true}' });
  const panel = doc.querySelector(".sh-agent-panel"), button = doc.querySelector(".sh-agent-expand");
  assert.equal(panel.style.getPropertyValue("--sh-chat-width"), "960px");
  assert.equal(button.getAttribute("aria-label"), "Restore assistant width");
  button.click();
  assert.equal(panel.style.getPropertyValue("--sh-chat-width"), "620px");
  const grip = doc.querySelector(".sh-agent-resize");
  grip.dispatchEvent(new window.KeyboardEvent("keydown", { key: "ArrowLeft", shiftKey: true, bubbles: true }));
  assert.equal(panel.style.getPropertyValue("--sh-chat-width"), "684px");
  assert.deepEqual(JSON.parse(window.sessionStorage.getItem("shinyhub:chat:size")), { width: 684, expanded: false });
});

test("bad or denied storage cannot break sizing, and native hosting hides fallback controls", (t) => {
  for (const options of [{ savedSize: "{" }, { savedSize: '{"width":1e99,"expanded":true}' }, { blockStorage: true }]) {
    const { window, doc } = ask(t, options);
    const panel = doc.querySelector(".sh-agent-panel"), button = doc.querySelector(".sh-agent-expand");
    assert.equal(panel.style.getPropertyValue("--sh-chat-width"), "440px");
    button.click();
    assert.equal(panel.style.getPropertyValue("--sh-chat-width"), "960px");
    window.dispatchEvent(new window.CustomEvent("shinyhub:chat:host", {
      detail: { version: 1, available: true, native: true }
    }));
    window.dispatchEvent(new window.Event("resize"));
    assert.equal(button.hidden, true);
    assert.equal(doc.querySelector(".sh-agent-resize").hidden, true);
  }
});

// Loads the widget into a fresh page, opens a session and asks one question, so
// each test starts with an answer in flight and drives the events that follow.
function ask(t, { savedSize = null, blockStorage = false, notice, starters, followups, initialQuestion = true } = {}) {
  const dom = new JSDOM("<!doctype html><body></body>", {
    runScripts: "outside-only", url: "https://apps.example.test/"
  });
  const window = dom.window;
  t.after(() => window.close());
  const handlers = new Map();
  const inputs = [];
  const clipboard = [];
  const frames = new Map();
  let frameId = 0;
  window.requestAnimationFrame = (callback) => { frames.set(++frameId, callback); return frameId; };
  window.cancelAnimationFrame = (id) => frames.delete(id);
  const flush = () => {
    const pending = Array.from(frames.values()); frames.clear();
    pending.forEach((callback) => callback(0));
  };
  let ids = 0;
  window.Shiny = {
    addCustomMessageHandler(name, handler) { handlers.set(name, handler); },
    setInputValue(name, value) { inputs.push({ name, value }); }
  };
  Object.defineProperty(window, "crypto", { value: { randomUUID: () => "request_" + (++ids) } });
  window.matchMedia = () => ({ matches: false });
  if (savedSize !== null) window.sessionStorage.setItem("shinyhub:chat:size", savedSize);
  if (blockStorage) Object.defineProperty(window, "sessionStorage", {
    get() { throw new Error("Storage denied"); }
  });
  Object.defineProperty(window.navigator, "clipboard", { value: { writeText: (text) => {
    clipboard.push(text); return Promise.resolve();
  } } });
  window.eval(source);

  const doc = window.document;
  let session;
  function reconnect(next, nextNotice = notice) {
    session = next;
    handlers.get("shinyhub-agent-chat-capabilities")({ version: 1, enabled: true, session, notice: nextNotice, starters, followups });
  }
  reconnect("s1");
  doc.querySelector(".sh-agent-launcher").click();
  let requestId;
  function submit(question) {
    doc.querySelector(".sh-agent-input").value = question;
    doc.querySelector(".sh-agent-composer").requestSubmit();
    requestId = inputs.filter((entry) => entry.name === ".shinyhub_agent_chat_request" && entry.value.message).at(-1).value.requestId;
  }
  if (initialQuestion) submit("Show only 2024");
  const send = (event) => handlers.get("shinyhub-agent-chat-event")(
    Object.assign({ version: 1, session, requestId }, event));
  return { window, doc, send, inputs, submit, flush, frames, clipboard, reconnect };
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

test("nested approval values show keys and values as literal text", (t) => {
  const { doc, send } = ask(t);
  send({ type: "approval_required", approvalId: "nested", name: "set_filters", message: "Change filters?",
    arguments: { filters: { model: ["alpha", "beta"], cost_center: ["CC123", "<b>literal</b>"] }, empty: [], missing: null } });
  assert.deepEqual(Array.from(doc.querySelectorAll(".sh-agent-approval dt")).map((x) => x.textContent),
    ["Filters · Model", "Filters · Cost Center", "Empty", "Missing"]);
  assert.deepEqual(Array.from(doc.querySelectorAll(".sh-agent-approval dd")).map((x) => x.textContent),
    ["alpha, beta", "CC123, <b>literal</b>", "(empty list)", "null"]);
  assert.equal(doc.querySelector(".sh-agent-approval b"), null);
});

test("shortened lists and descriptions retain inspectable complete action details", (t) => {
  const { doc, send } = ask(t);
  const args = { filters: { model: Array.from({ length: 30 }, (_, i) => "model-" + i) } };
  send({ type: "approval_required", approvalId: "long", name: "set_filters", message: "Change filters?", arguments: args });
  assert.match(doc.querySelector(".sh-agent-approval dd").textContent, /and 22 more/);
  assert.equal(doc.querySelector(".sh-agent-approval-details pre").textContent, JSON.stringify(args, null, 2));
  send({ type: "approval_required", approvalId: "description", name: "set_filters", message: "Change filters?",
    arguments: args, description: "<img src=x> Filter models for this view" });
  assert.equal(doc.querySelector(".sh-agent-approval-description").textContent, "<img src=x> Filter models for this view");
  assert.equal(doc.querySelector(".sh-agent-approval img"), null);
  assert.equal(doc.querySelector(".sh-agent-approval dl"), null);
  assert.match(doc.querySelector(".sh-agent-approval-details pre").textContent, /model-29/);
});

test("approval expiry matches its ID and removes the dead decision", (t) => {
  const { doc, send, inputs } = ask(t);
  requestApproval(send);
  assert.match(doc.querySelector(".sh-agent-approval-deadline").textContent, /30 seconds/);
  send({ type: "approval_expired", approvalId: "previous" });
  assert.equal(doc.querySelectorAll(".sh-agent-approval button").length, 2);
  send({ type: "approval_expired", approvalId: "a1" });
  assert.equal(card(doc).result, "expired");
  assert.equal(card(doc).buttons, 0);
  assert.match(doc.querySelector(".sh-agent-approval-deadline").textContent, /Ask again/);
  assert.equal(inputs.filter((x) => x.name === ".shinyhub_agent_chat_decision").length, 0);
});

test("a decision after its visible deadline cannot be sent", (t) => {
  const { doc, send, inputs, window } = ask(t);
  const originalNow = window.Date.now;
  window.Date.now = () => 1000;
  send({ type: "approval_required", approvalId: "a1", name: "set_year", message: "Change?", arguments: {}, expiresIn: 1 });
  window.Date.now = () => 2000;
  doc.querySelector(".sh-agent-apply").click();
  window.Date.now = originalNow;
  assert.equal(card(doc).result, "expired");
  assert.equal(inputs.filter((x) => x.name === ".shinyhub_agent_chat_decision").length, 0);
});

test("public chat opening is idempotent with the toolbar present", (t) => {
  const { window, doc } = ask(t);
  window.dispatchEvent(new window.CustomEvent("shinyhub:chat:host", {
    detail: { version: 1, available: true, native: true }
  }));
  window.shinyhubAgentChat.close();
  assert.equal(doc.querySelector(".sh-agent-panel").hidden, true);
  assert.equal(window.shinyhubAgentChat.open(), true);
  assert.equal(window.shinyhubAgentChat.open(), true);
  assert.equal(doc.querySelector(".sh-agent-panel").hidden, false);
  window.shinyhubAgentChat.close();
  window.shinyhubAgentChat.close();
  assert.equal(doc.querySelector(".sh-agent-panel").hidden, true);
});

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

test("a question typed while a new chat is starting stays in the composer", (t) => {
  const { doc, send, inputs, submit } = ask(t);
  send({ type: "delta", text: "The previous conversation." });
  send({ type: "done" });
  const questions = () => inputs.filter((entry) => entry.value.message).length;
  const resets = () => inputs.filter((entry) => entry.value.action === "reset").length;
  const input = doc.querySelector(".sh-agent-input"), before = questions();
  doc.querySelector(".sh-agent-reset").click();
  doc.querySelector(".sh-agent-reset").click();
  assert.equal(resets(), 1, "one reset is requested");
  input.value = "First question"; doc.querySelector(".sh-agent-composer").requestSubmit();
  assert.equal(questions(), before, "nothing is sent to the thread being cleared");
  assert.equal(input.value, "First question");
  send({ type: "reset" });
  assert.equal(input.value, "First question");
  submit("First question");
  assert.equal(questions(), before + 1);
  assert.match(doc.querySelector(".sh-agent-user").textContent, /First question$/);
});

test("the composer holds a draft until the app answers New chat, however long that takes", (t) => {
  const { window, doc, send, inputs, submit } = ask(t);
  send({ type: "done" });
  const questions = () => inputs.filter((entry) => entry.value.message).length;
  const input = doc.querySelector(".sh-agent-input"), before = questions();
  const trySend = () => { input.value = "First question"; doc.querySelector(".sh-agent-composer").requestSubmit(); };
  const timeouts = [];
  window.setTimeout = (callback) => timeouts.push(callback);
  doc.querySelector(".sh-agent-reset").click();
  const reset = inputs.at(-1).value.requestId;
  timeouts.forEach((callback) => callback()); // any amount of time passes
  trySend();
  assert.equal(questions(), before, "a slow reset still holds the draft");
  assert.equal(input.value, "First question");
  send({ type: "error", requestId: "some_other_request", message: "Unrelated" });
  trySend();
  assert.equal(questions(), before, "only the answer to this reset releases the composer");
  send({ type: "error", requestId: reset, message: "Wait for the answer to finish, then start a new chat." });
  assert.equal(doc.querySelector(".sh-agent-status-text").textContent, "Wait for the answer to finish, then start a new chat.");
  assert.match(doc.querySelector(".sh-agent-user").textContent, /Show only 2024$/, "a refused New chat keeps the transcript");
  submit("First question");
  assert.equal(questions(), before + 1);
});

test("New chat during a running Undo asks for nothing and leaves the composer usable", (t) => {
  const { doc, send, inputs, submit } = ask(t);
  send({ type: "action_applied", name: "set_year", actionId: "a1", undoExpiresIn: null });
  send({ type: "done" });
  const count = (pick) => inputs.filter((entry) => pick(entry.value)).length;
  const before = count((value) => value.message);
  doc.querySelector(".sh-agent-undo").click();
  const undo = inputs.at(-1).value.requestId;
  doc.querySelector(".sh-agent-reset").click();
  assert.equal(count((value) => value.action === "reset"), 0);
  submit("Next question");
  assert.equal(count((value) => value.message), before + 1, "the composer is not held for a reset that was never sent");
  send({ type: "done" });
  send({ type: "undo_result", requestId: undo, actionId: "a1", ok: true });
  doc.querySelector(".sh-agent-reset").click();
  assert.equal(count((value) => value.action === "reset"), 1, "New chat works once the Undo has finished");
});

test("starting a new chat removes the previous answer from the live region", (t) => {
  const { doc, send } = ask(t);
  send({ type: "delta", text: "The previous conversation." });
  send({ type: "done" });
  send({ type: "reset" });
  assert.equal(doc.querySelector(".sh-agent-sr-only").textContent, "");
});

function answer(doc) {
  return Array.from(doc.querySelectorAll('.sh-agent-assistant .sh-agent-message-body')).at(-1);
}

test('basic Markdown renders semantic blocks while questions remain literal', (t) => {
  const { doc, send, submit } = ask(t);
  send({ type: 'delta', text: '## Weekly results\n\n**Cost** rose *slightly*. Use `get_view`.\nNext line.\n\n- **North**\n  - Europe\n- South\n\n3. First\n4. Second\n\n| Line | Cost | Share |\n| --- | ---: | :---: |\n| North | $442.91K | 65% |\n| South | $238K | 35% |' });
  send({ type: 'done' });
  const body = answer(doc);
  assert.equal(body.querySelector('.sh-agent-markdown-heading').textContent, 'Weekly results');
  assert.equal(body.querySelector('strong').textContent, 'Cost');
  assert.equal(body.querySelector('em').textContent, 'slightly');
  assert.equal(body.querySelector('code').textContent, 'get_view');
  assert.equal(body.querySelectorAll('br').length, 1);
  assert.equal(body.querySelector('ul > li > ul > li').textContent, 'Europe');
  assert.equal(body.querySelector('ol').getAttribute('start'), '3');
  assert.equal(body.querySelectorAll('tbody tr').length, 2);
  assert.equal(body.querySelectorAll('th[scope="col"]').length, 3);
  assert.equal(body.querySelector('td.sh-agent-align-right').textContent, '$442.91K');
  assert.equal(body.querySelector('td.sh-agent-align-center').textContent, '65%');
  submit('**Do not format my question**');
  const user = Array.from(doc.querySelectorAll('.sh-agent-user .sh-agent-message-body')).at(-1);
  assert.equal(user.textContent, '**Do not format my question**');
  assert.equal(user.children.length, 0);
});

test('code, escapes and unsupported constructs stay literal', (t) => {
  const { doc, send } = ask(t);
  send({ type: 'delta', text: '\\*literal\\* and **bold *nested* words** and ***both*** and ``a ` tick``\n\n```python\n<img src=x onerror=alert(1)>\n**not bold** | not a table\n```\n\n[x](javascript:alert(1)) ![image](https://example.test/x) <https://example.test>\n\n- [x] Task\n\n> Quote\n\n[^note] _underscore_ ~~strike~~ &amp;' });
  send({ type: 'done' });
  const body = answer(doc);
  assert.match(body.textContent, /\*literal\*/);
  assert.equal(body.querySelector('strong em').textContent, 'nested');
  assert.equal(body.querySelectorAll('strong em')[1].textContent, 'both');
  assert.equal(body.querySelector('code').textContent, 'a ` tick');
  assert.equal(body.querySelector('pre code').textContent, '<img src=x onerror=alert(1)>\n**not bold** | not a table\n');
  assert.equal(body.querySelector('pre strong'), null);
  assert.equal(body.querySelector('a,img,script,blockquote,input,del'), null);
  assert.equal(body.querySelector('ul'), null, 'task lists stay visible as source');
  assert.match(body.textContent, /\[x\]\(javascript:alert\(1\)\)/);
  assert.match(body.textContent, /_underscore_ ~~strike~~ &amp;/);
});

test('injected output cannot create active DOM, URLs, styles or attributes', (t) => {
  const { window, doc, send } = ask(t);
  const payload = '<img src=x onerror=alert(1)>\n<script>window.pwned=true</script>\n<svg onload=alert(1)>\n<iframe src="https://example.test">\n<style>body{display:none}</style>\n[x](javascript:alert(1))\n![x](https://example.test/leak)\n<a href="https://example.test">x</a>\n\n| <img src=x> | **Safe** |\n| --- | --- |\n| <script>alert(1)</script> | [x](data:text/html,boom) |';
  send({ type: 'delta', text: payload }); send({ type: 'done' });
  const body = answer(doc);
  assert.equal(window.pwned, undefined);
  assert.equal(body.querySelector('a,img,script,svg,iframe,style,link,object,embed'), null);
  assert.match(body.textContent, /<img src=x onerror=alert\(1\)>/);
  assert.match(body.textContent, /<script>window.pwned=true<\/script>/);
  for (const node of body.querySelectorAll('*')) {
    for (const attr of node.attributes) {
      assert.ok(!/^(on|src|href|style)/i.test(attr.name), attr.name);
    }
  }
});

test('tables support escaped pipes, optional outer pipes and retain malformed values', (t) => {
  const { doc, send } = ask(t);
  send({ type: 'delta', text: 'Name | Value\n--- | ---:\na\\|b | `x\\|y`\nleft | right | DO NOT LOSE\n\n| Bad | Header |\n| --- |\n| one | two |' });
  send({ type: 'done' });
  const body = answer(doc);
  assert.equal(body.querySelectorAll('table').length, 1);
  assert.deepEqual(Array.from(body.querySelectorAll('td'), (node) => node.textContent), ['a|b', 'x|y']);
  assert.match(body.textContent, /left \| right \| DO NOT LOSE/);
  assert.match(body.textContent, /\| Bad \| Header \|/);
  assert.match(body.textContent, /\| one \| two \|/);
});

test('unfinished markup stays visible and tables keep completed rows while streaming', (t) => {
  const { doc, send, flush, frames } = ask(t);
  send({ type: 'delta', text: '**Still ' });
  send({ type: 'delta', text: 'open' });
  assert.equal(frames.size, 1, 'deltas share a frame');
  flush();
  assert.equal(answer(doc).textContent, '**Still open');
  assert.equal(answer(doc).querySelector('strong'), null);
  send({ type: 'delta', text: '**\n\n| A | B |\n| --- | ---' }); flush();
  assert.equal(answer(doc).querySelector('table'), null, 'partial separator is plain text');
  send({ type: 'delta', text: ' |\n| first | 1 |\n| sec' }); flush();
  const wrapper = answer(doc).querySelector('.sh-agent-table-scroll');
  const first = wrapper.querySelector('tbody tr');
  wrapper.scrollLeft = 70;
  assert.equal(wrapper.querySelectorAll('tbody tr').length, 1);
  assert.match(answer(doc).textContent, /\| sec$/);
  send({ type: 'delta', text: 'ond | 2 |' }); flush();
  assert.equal(wrapper.querySelectorAll('tbody tr').length, 1, 'trailing pipe does not finalize a row');
  send({ type: 'done' });
  assert.equal(answer(doc).querySelector('.sh-agent-table-scroll'), wrapper);
  assert.equal(wrapper.querySelector('tbody tr'), first);
  assert.equal(wrapper.scrollLeft, 70);
  assert.equal(wrapper.querySelectorAll('tbody tr').length, 2, 'done completes a row without a newline');
});

test('an opening fence streams literal code without waiting for its closing fence', (t) => {
  const { doc, send, flush } = ask(t);
  send({ type: 'delta', text: '```js' }); flush();
  assert.equal(answer(doc).querySelector('pre'), null);
  send({ type: 'delta', text: '\n**literal**\n<img src=x>' }); flush();
  const pre = answer(doc).querySelector('pre');
  assert.equal(pre.textContent, '**literal**\n<img src=x>');
  assert.equal(pre.querySelector('strong,img'), null);
  send({ type: 'delta', text: '\n```' });
  send({ type: 'done' });
  assert.equal(answer(doc).querySelector('pre'), pre);
  assert.equal(pre.textContent, '**literal**\n<img src=x>\n');
});

test('final rendering is independent of every possible delta boundary', (t) => {
  const { doc, send, submit, flush } = ask(t);
  const source = '**bold *italic***\r\n\r\n- item\n  - child\n\n| A | B |\n| --- | ---: |\n| a\\|b | 12 |\n\n~~~\n`literal`\n~~~\n\n<img src=x onerror=alert(1)> [x](javascript:alert(1))';
  send({ type: 'delta', text: source }); send({ type: 'done' });
  const expected = answer(doc).innerHTML;
  for (let split = 0; split <= source.length; split++) {
    send({ type: 'reset' }); submit('Again');
    send({ type: 'delta', text: source.slice(0, split) }); flush();
    send({ type: 'delta', text: source.slice(split) }); flush();
    send({ type: 'done' });
    assert.equal(answer(doc).innerHTML, expected, `boundary ${split}`);
  }
  send({ type: 'reset' }); submit('One character at a time');
  for (const char of source) { send({ type: 'delta', text: char }); flush(); }
  send({ type: 'done' });
  assert.equal(answer(doc).innerHTML, expected);
});

test('copy uses the exact source of its own answer even after another question', async (t) => {
  const { doc, send, submit, clipboard, frames } = ask(t);
  const source = ' **Cost:** $442K\r\n\r\n| A | B |\r\n| --- | --- |\r\n| x | y | ';
  send({ type: 'delta', text: source }); send({ type: 'done' });
  assert.equal(frames.size, 0, 'completion flushes and cancels the pending frame');
  const copy = doc.querySelector('.sh-agent-copy');
  submit('Next question'); send({ type: 'delta', text: 'Another answer.' }); send({ type: 'done' });
  copy.click(); await Promise.resolve();
  assert.deepEqual(clipboard, [source]);
  assert.equal(copy.textContent, 'Copied');
});

test('copy failures offer retry without an unhandled rejection', async (t) => {
  const { window, doc, send, clipboard } = ask(t);
  const write = window.navigator.clipboard.writeText;
  window.navigator.clipboard.writeText = () => Promise.reject(new Error('denied'));
  send({ type: 'delta', text: '**Answer**' }); send({ type: 'done' });
  const copy = doc.querySelector('.sh-agent-copy');
  copy.click(); await Promise.resolve(); await Promise.resolve();
  assert.equal(copy.textContent, 'Copy failed · Retry');
  window.navigator.clipboard.writeText = write;
  copy.click(); await Promise.resolve();
  assert.deepEqual(clipboard, ['**Answer**']);
});

test('completed table announcements retain boundaries and streaming stays silent', (t) => {
  const { doc, send, flush } = ask(t);
  const announcer = doc.querySelector('.sh-agent-sr-only');
  send({ type: 'delta', text: '| Name | Cost |\n| --- | ---: |\n| North | $100 |' }); flush();
  assert.equal(announcer.textContent, '');
  send({ type: 'done' });
  assert.equal(announcer.textContent, 'Answer: Name; Cost; \nNorth; $100;');
});

test('scroll intent is measured at render time rather than when a delta arrives', (t) => {
  const { doc, send, flush } = ask(t);
  const log = doc.querySelector('.sh-agent-log');
  Object.defineProperty(log, 'scrollHeight', { configurable: true, get: () => 1000 });
  Object.defineProperty(log, 'clientHeight', { configurable: true, get: () => 200 });
  log.scrollTop = 800;
  send({ type: 'delta', text: 'First' });
  log.scrollTop = 150; flush();
  assert.equal(log.scrollTop, 150, 'reading earlier content prevents follow-scroll');
  assert.equal(doc.querySelector('.sh-agent-jump').hidden, false);
  log.scrollTop = 800;
  send({ type: 'delta', text: '\nSecond' }); flush();
  assert.equal(log.scrollTop, 1000);
});

test('selected text survives growth and formatting changes in the active answer', (t) => {
  const { window, doc, send, flush } = ask(t);
  send({ type: 'delta', text: '**Cost rose' }); flush();
  const node = answer(doc).querySelector('p').firstChild;
  window.getSelection().setBaseAndExtent(node, 2, node, 6);
  send({ type: 'delta', text: '** today.' }); flush();
  assert.equal(window.getSelection().toString(), 'Cost');
});

test('interruption and session replacement flush or cancel pending work safely', (t) => {
  const { doc, send, flush, frames, submit } = ask(t);
  send({ type: 'delta', text: '**Partial**' });
  send({ type: 'error', message: 'Stopped.' });
  assert.equal(answer(doc).querySelector('strong').textContent, 'Partial');
  assert.equal(frames.size, 0);
  assert.equal(doc.querySelector('.sh-agent-copy'), null);
  submit('Again'); send({ type: 'delta', text: 'Old content' });
  send({ type: 'reset' }); flush();
  assert.equal(doc.querySelector('.sh-agent-assistant'), null);
});


test('adjacent nested emphasis and repeated list numbers follow basic Markdown conventions', (t) => {
  const { doc, send } = ask(t);
  send({ type: 'delta', text: '**bold *italic*** and *italic **bold***\n\n1. First\n1. Second\n\n- Parent\n  - Child\n    - Deeper stays literal' });
  send({ type: 'done' });
  const body = answer(doc);
  assert.equal(body.querySelector('strong > em').textContent, 'italic');
  assert.equal(body.querySelector('em > strong').textContent, 'bold');
  assert.equal(body.querySelectorAll('ol > li').length, 2);
  assert.equal(body.querySelector('ol li[value]'), null);
  assert.equal(body.querySelectorAll('ul').length, 2);
  assert.match(body.querySelector('ul ul').textContent, /- Deeper stays literal/);
});


test('a reconnect cancels queued rendering and late events cannot contaminate the next answer', (t) => {
  const { doc, send, flush, frames, submit, reconnect, inputs } = ask(t);
  const old = inputs.at(-1).value;
  send({ type: 'delta', text: '**Old answer**' });
  reconnect('s2');
  assert.equal(frames.size, 0);
  assert.equal(doc.querySelector('.sh-agent-assistant'), null);
  submit('A fresh question');
  send({ type: 'delta', session: old.session, requestId: old.requestId, text: 'Late old content' });
  send({ type: 'delta', text: '**Fresh answer**' }); flush();
  send({ type: 'done' });
  assert.equal(answer(doc).textContent, 'Fresh answer');
});

test('opaque HTML text preserves line breaks without interpreting Markdown in attributes', (t) => {
  const { doc, send } = ask(t);
  send({ type: 'delta', text: '<img\n src="**literal**"\n onerror="alert(1)">\nAfter' });
  send({ type: 'done' });
  const body = answer(doc);
  assert.equal(body.querySelectorAll('br').length, 3);
  assert.equal(body.querySelector('img,strong'), null);
  assert.match(body.textContent, /\*\*literal\*\*/);
});


test('comparison operators are ordinary text rather than incomplete HTML tags', (t) => {
  const { doc, send } = ask(t);
  send({ type: 'delta', text: 'Cost < 5% is **small**; 2 <= 3 is *true*.' });
  send({ type: 'done' });
  const body = answer(doc);
  assert.equal(body.querySelector('strong').textContent, 'small');
  assert.equal(body.querySelector('em').textContent, 'true');
  assert.equal(body.textContent, 'Cost < 5% is small; 2 <= 3 is true.');
});


test('blank lines preserve a single numbered list and its nested items', (t) => {
  const { doc, send } = ask(t);
  send({ type: 'delta', text: '1. North\n\n1. South\n\n   - Europe\n\n   - Asia\n\n1. Other\n\nA separate paragraph.' });
  send({ type: 'done' });
  const body = answer(doc);
  assert.equal(body.querySelectorAll('ol').length, 1);
  assert.equal(body.querySelectorAll('ol > li').length, 3);
  assert.equal(body.querySelectorAll('ol > li > ul').length, 1);
  assert.equal(body.querySelectorAll('ul > li').length, 2);
  assert.equal(body.lastElementChild.textContent, 'A separate paragraph.');
});

test('following blocks terminate a table even when they contain matching pipe counts', (t) => {
  const { doc, send, submit } = ask(t);
  for (const [block, selector] of [
    ['- Note | check totals', 'ul'],
    ['1. Note | check totals', 'ol'],
    ['## Note | check totals', '.sh-agent-markdown-heading'],
    ['```text | label\ncode\n```', 'pre'],
    ['> Note | check totals', 'p'],
    ['- [x] Note | check totals', 'p'],
  ]) {
    send({ type: 'delta', text: '| Name | Cost |\n| --- | ---: |\n| North | $100 |\n' + block });
    send({ type: 'done' });
    const body = answer(doc);
    assert.equal(body.querySelectorAll('tbody tr').length, 1, block);
    assert.equal(body.children.length, 2, block);
    assert.ok(body.lastElementChild.matches(selector), block);
    send({ type: 'reset' }); submit('Next');
  }
});

test('gateway budget errors show the safe message and allow another request', (t) => {
  const { doc, send, submit } = ask(t);
  const message = 'The assistant budget is used up. Contact your administrator.';
  send({ type: 'error', code: 'budget_exceeded', message });
  assert.equal(doc.querySelector('.sh-agent-error').textContent, message);
  submit('Try again');
  send({ type: 'delta', text: 'Available again' });
  send({ type: 'done' });
  assert.equal(answer(doc).textContent, 'Available again');
});

test("notice is literal, persistent panel content in fallback and native layouts", (t) => {
  const notice = '<img src=x onerror=alert(1)> **Check important figures**';
  const { window, doc, send, submit, reconnect } = ask(t, { notice, initialQuestion: false });
  const element = doc.querySelector('.sh-agent-notice');
  assert.equal(element.textContent, notice);
  assert.equal(element.children.length, 1);
  assert.equal(element.firstElementChild.tagName, "SPAN");
  assert.equal(element.firstElementChild.children.length, 0);
  assert.equal(element.firstElementChild.textContent, notice);
  assert.equal(element.getAttribute('role'), 'note');
  assert.equal(element.tabIndex, 0);
  assert.equal(doc.querySelector('.sh-agent-panel').classList.contains('sh-agent-with-notice'), true);
  assert.equal(element.getAttribute('aria-label'), 'App notice');
  assert.equal(doc.querySelector('.sh-agent-input').getAttribute('aria-describedby'), element.firstElementChild.id);
  assert.equal(element.closest('.sh-agent-log, .sh-agent-header'), null);
  assert.equal(element.nextElementSibling.className, 'sh-agent-log-wrap');
  submit('What changed?');
  send({ type: 'delta', text: 'Costs increased.' });
  send({ type: 'done' });
  send({ type: 'reset' });
  assert.equal(doc.querySelector('.sh-agent-notice'), element);
  assert.equal(element.textContent, notice);
  window.shinyhubAgentChat.close();
  window.shinyhubAgentChat.open();
  assert.equal(doc.querySelector('.sh-agent-notice'), element);
  window.dispatchEvent(new window.CustomEvent('shinyhub:chat:host', {
    detail: { version: 1, available: true, native: true }
  }));
  assert.equal(element.closest('.sh-agent-panel').hidden, false);
  reconnect('s1');
  assert.equal(doc.querySelectorAll('.sh-agent-notice').length, 1);
  reconnect('s2', 'New session notice');
  assert.equal(element.textContent, 'New session notice');
  reconnect('s3', null);
  assert.equal(doc.querySelector('.sh-agent-notice'), null);
  assert.equal(doc.querySelector('.sh-agent-input').hasAttribute('aria-describedby'), false);
  assert.equal(doc.querySelector('.sh-agent-panel').classList.contains('sh-agent-with-notice'), false);
});

test("absent and blank notices preserve the existing empty state", (t) => {
  for (const notice of [undefined, null, '', '  \n ']) {
    const { doc } = ask(t, { notice, initialQuestion: false });
    assert.equal(doc.querySelector('.sh-agent-notice'), null);
    assert.equal(doc.querySelector('.sh-agent-empty h3').textContent, 'Start with this view');
    assert.equal(doc.querySelectorAll('.sh-agent-suggestion').length, 2);
  }
});


test("custom starters replace defaults, submit on click, and empty lists hide buttons", (t) => {
  const { doc, inputs } = ask(t, { initialQuestion: false, starters: ["Which lines cost most?", "<b>Literal</b>"] });
  const buttons = doc.querySelectorAll(".sh-agent-empty .sh-agent-suggestion");
  assert.deepEqual(Array.from(buttons, b => b.textContent), ["Which lines cost most?", "<b>Literal</b>"]);
  assert.equal(doc.querySelector(".sh-agent-empty b"), null);
  buttons[0].click();
  assert.equal(inputs.at(-1).value.message, "Which lines cost most?");
  const empty = ask(t, { initialQuestion: false, starters: [] });
  assert.equal(empty.doc.querySelectorAll(".sh-agent-suggestion").length, 0);
});

test("follow-ups wait for done, fill and focus without sending, and preserve a draft", (t) => {
  const { doc, send, inputs, window } = ask(t, { followups: 2 });
  send({ type: "delta", text: "Answer." });
  send({ type: "suggestions", questions: [" Cost? ", "COST?", "<b>Trend?</b>", "Other?"] });
  assert.equal(doc.querySelector(".sh-agent-followups"), null);
  send({ type: "done" });
  const group = doc.querySelector('[aria-label="Suggested follow-up questions"]');
  assert.equal(group.getAttribute("role"), "group");
  const buttons = group.querySelectorAll("button");
  assert.deepEqual(Array.from(buttons, b => b.textContent), ["Cost?", "<b>Trend?</b>"]);
  assert.equal(group.querySelector("b"), null);
  assert.match(doc.querySelector(".sh-agent-sr-only").textContent, /Suggested follow-up questions available/);
  const count = inputs.length;
  buttons[0].focus();
  assert.equal(doc.activeElement, buttons[0]);
  buttons[0].click();
  const input = doc.querySelector(".sh-agent-input");
  assert.equal(input.value, "Cost?");
  assert.equal(doc.activeElement, input);
  assert.equal(inputs.length, count);
  assert.equal(buttons[1].disabled, true);
  assert.equal(buttons[1].getAttribute("aria-describedby"), group.querySelector(".sh-agent-followup-hint").id);
  buttons[1].click();
  assert.equal(input.value, "Cost?");
  assert.equal(group.querySelector(".sh-agent-followup-hint").hidden, false);
  input.value = "";
  input.dispatchEvent(new window.Event("input"));
  assert.equal(buttons[1].disabled, false);
  assert.equal(buttons[1].hasAttribute("aria-describedby"), false);
});

test("follow-ups are opt-in and never attached to an empty answer", (t) => {
  for (const options of [{}, { followups: 2 }]) {
    const { doc, send } = ask(t, options);
    if (!options.followups) send({ type: "delta", text: "Answer." });
    send({ type: "suggestions", questions: ["Cost?"] });
    send({ type: "done" });
    assert.equal(doc.querySelector(".sh-agent-followups"), null);
  }
});

for (const interruption of ["error", "stop", "expired", "reconnect", "reset", "next"]) {
  test(`follow-ups cannot survive ${interruption} or be revived by stale events`, (t) => {
    const { doc, send, submit, reconnect } = ask(t, { followups: 2 });
    send({ type: "delta", text: "Answer." });
    send({ type: "suggestions", questions: ["Cost?"] });
    if (interruption === "error") send({ type: "error", message: "Failed" });
    else if (interruption === "stop") Array.from(doc.querySelectorAll("button")).find(b => b.textContent === "Stop").click();
    else if (interruption === "expired") {
      requestApproval(send);
      send({ type: "approval_expired", approvalId: "a1" });
    } else {
      send({ type: "done" });
      assert.ok(doc.querySelector(".sh-agent-followups"));
      if (interruption === "reconnect") reconnect("s2");
      if (interruption === "reset") { doc.querySelector(".sh-agent-reset").click(); send({ type: "reset" }); }
      if (interruption === "next") submit("Another question");
    }
    send({ type: "suggestions", requestId: "stale_request", questions: ["Stale?"] });
    if (["stop", "expired"].includes(interruption)) {
      send({ type: "suggestions", questions: ["Too late?"] });
      send({ type: "done" });
    }
    assert.equal(doc.querySelector(".sh-agent-followups"), null);
  });
}

test("successful Undo removes suggestions for the previous view", (t) => {
  const { doc, send, inputs } = ask(t, { followups: 2 });
  send({ type: "action_applied", name: "set_year", actionId: "action1", receipt: "Showing year" });
  send({ type: "delta", text: "Showing year." });
  send({ type: "suggestions", questions: ["What happened this year?"] });
  send({ type: "done" });
  assert.ok(doc.querySelector(".sh-agent-followups"));
  doc.querySelector(".sh-agent-undo").click();
  const undo = inputs.at(-1).value;
  send({ type: "undo_result", requestId: undo.requestId, actionId: "action1", ok: true, detail: "Previous view restored" });
  assert.equal(doc.querySelector(".sh-agent-followups"), null);
});

function undoClock(window) {
  let now = 1000, id = 0;
  const timers = new Map();
  window.Date.now = () => now;
  window.setInterval = (callback) => { timers.set(++id, callback); return id; };
  window.clearInterval = (id) => timers.delete(id);
  return {
    timers,
    advance(ms, tick = true) {
      now += ms;
      if (tick) Array.from(timers.values()).forEach((callback) => callback());
    },
  };
}

test("reactive withdrawal removes the matching Undo while preserving the receipt", (t) => {
  const { window, doc, send } = ask(t);
  const clock = undoClock(window);
  send({ type: "action_applied", name: "set_dates", actionId: "a1",
    receipt: "Date range set to September", undoExpiresIn: 3600 });
  send({ type: "undo_withdrawn", actionId: "old", message: "Wrong action" });
  send({ type: "undo_withdrawn", actionId: "a1", session: "old-session" });
  assert.ok(doc.querySelector(".sh-agent-undo"));
  send({ type: "undo_withdrawn", actionId: "a1",
    message: "The date range changed again. <b>Literal text</b>" });
  assert.equal(doc.querySelector(".sh-agent-undo"), null);
  assert.equal(doc.querySelector(".sh-agent-receipt-title").textContent, "Date range set to September");
  assert.equal(doc.querySelector(".sh-agent-receipt-detail").textContent,
    "The date range changed again. <b>Literal text</b>");
  assert.equal(doc.querySelector(".sh-agent-receipt-detail b"), null);
  assert.equal(clock.timers.size, 0);
  send({ type: "done" });
  clock.advance(3600000);
  assert.equal(doc.querySelector(".sh-agent-undo"), null);
  assert.match(doc.querySelector(".sh-agent-receipt-detail").textContent, /date range changed/);
});

test("withdrawal after an answer ends works for an unlimited window", (t) => {
  const { doc, send } = ask(t);
  send({ type: "action_applied", name: "set_dates", actionId: "a1", undoExpiresIn: null });
  send({ type: "done" });
  send({ type: "undo_withdrawn", actionId: "a1", requestId: undefined });
  assert.equal(doc.querySelector(".sh-agent-undo"), null);
  assert.equal(doc.querySelector(".sh-agent-receipt-detail").textContent, "Undo no longer available");
});

test("late withdrawal cannot remove a newer Undo or overwrite an undo result", (t) => {
  const { doc, send, inputs } = ask(t);
  send({ type: "action_applied", name: "set_dates", actionId: "a1", undoExpiresIn: null });
  send({ type: "action_applied", name: "set_dates", actionId: "a2", undoExpiresIn: null });
  send({ type: "undo_withdrawn", actionId: "a1" });
  assert.equal(doc.querySelectorAll(".sh-agent-undo").length, 1);
  send({ type: "done" });
  doc.querySelector(".sh-agent-undo").click();
  send({ type: "undo_withdrawn", actionId: "a2" });
  assert.equal(doc.querySelector(".sh-agent-undo").textContent, "Undoing…");
  send({ type: "undo_result", actionId: "a2", ok: true, requestId: inputs.at(-1).value.requestId });
  send({ type: "undo_withdrawn", actionId: "a2" });
  assert.equal(doc.querySelectorAll(".sh-agent-receipt-title")[1].textContent, "Change undone");
});

test("Undo expires while the answer is streaming and cannot be revived by done", (t) => {
  const { window, doc, send } = ask(t);
  const clock = undoClock(window);
  send({ type: "action_applied", name: "set_year", actionId: "a1", receipt: "Showing year", undoExpiresIn: 2 });
  const button = doc.querySelector(".sh-agent-undo");
  assert.equal(button.disabled, true);
  assert.match(button.title, /2 more seconds/);
  clock.advance(1000);
  assert.match(button.title, /1 more second$/);
  clock.advance(1000);
  assert.equal(doc.querySelector(".sh-agent-undo"), null);
  assert.equal(doc.querySelector(".sh-agent-receipt-title").textContent, "Showing year");
  assert.equal(doc.querySelector(".sh-agent-receipt-detail").textContent, "Undo no longer available");
  assert.equal(doc.querySelector(".sh-agent-receipt-detail").hidden, false);
  assert.equal(clock.timers.size, 0);
  send({ type: "done" });
  assert.equal(doc.querySelector(".sh-agent-undo"), null);
});

test("Undo checks the deadline on click even if background timers have not run", (t) => {
  const { window, doc, send, inputs } = ask(t);
  const clock = undoClock(window);
  send({ type: "action_applied", name: "set_year", actionId: "a1", undoExpiresIn: 1 });
  send({ type: "done" });
  clock.advance(1000, false);
  doc.querySelector(".sh-agent-undo").click();
  assert.equal(inputs.some((entry) => entry.value.action === "undo"), false);
  assert.equal(doc.querySelector(".sh-agent-undo"), null);
});

for (const seconds of [0, 3600, null, undefined]) {
  test("Undo deadline supports " + String(seconds) + " seconds", (t) => {
    const { window, doc, send } = ask(t);
    const clock = undoClock(window);
    send({ type: "action_applied", name: "set_year", actionId: "a1", undoExpiresIn: seconds });
    send({ type: "done" });
    if (seconds === 0) {
      assert.equal(doc.querySelector(".sh-agent-undo"), null);
      assert.equal(clock.timers.size, 0);
      return;
    }
    clock.advance(300000);
    assert.equal(!!doc.querySelector(".sh-agent-undo"), seconds !== undefined);
    clock.advance(3300000);
    assert.equal(!!doc.querySelector(".sh-agent-undo"), seconds === null);
    assert.equal(clock.timers.size, 0);
  });
}

for (const outcome of ["success", "failure", "reset", "reconnect", "replacement", "non-undoable"]) {
  test("Undo timer cleanup on " + outcome + " preserves the final receipt", (t) => {
    const { window, doc, send, inputs, reconnect } = ask(t);
    const clock = undoClock(window);
    send({ type: "action_applied", name: "set_year", actionId: "a1", undoExpiresIn: 2 });
    if (outcome === "replacement" || outcome === "non-undoable") {
      send({ type: "action_applied", name: "set_month", receipt: "Showing month",
        ...(outcome === "replacement" ? { actionId: "a2", undoExpiresIn: null } : {}) });
    }
    send({ type: "done" });
    if (outcome === "success" || outcome === "failure") {
      doc.querySelector(".sh-agent-undo").click();
      assert.equal(clock.timers.size, 0);
      clock.advance(3000);
      assert.equal(doc.querySelector(".sh-agent-undo").textContent, "Undoing…");
      send({ type: "undo_result", requestId: inputs.at(-1).value.requestId, actionId: "a1",
        ok: outcome === "success", message: "The date range changed again." });
    }
    if (outcome === "reset") send({ type: "reset" });
    if (outcome === "reconnect") reconnect("s2");
    assert.equal(clock.timers.size, 0);
    clock.advance(300000);
    assert.equal(doc.body.textContent.includes("Undo no longer available"), false);
    if (outcome === "replacement") {
      assert.equal(doc.querySelectorAll(".sh-agent-undo").length, 1);
    } else {
      assert.equal(doc.querySelector(".sh-agent-undo"), null);
    }
    if (outcome === "failure") {
      assert.equal(doc.querySelector(".sh-agent-receipt-detail").textContent, "The date range changed again.");
    }
  });
}

test("question limits count Unicode characters consistently with the server", (t) => {
  const starter = "💸".repeat(119) + "?";
  const { doc, send, submit } = ask(t, { initialQuestion: false, starters: [starter], followups: 1 });
  assert.equal(doc.querySelector(".sh-agent-empty .sh-agent-suggestion")?.textContent, starter);
  submit("Cost?");
  send({ type: "delta", text: "Answer." });
  const question = "💸".repeat(99) + "?";
  send({ type: "suggestions", questions: [question] });
  send({ type: "done" });
  assert.equal(doc.querySelector(".sh-agent-followups button")?.textContent, question);
});

test("chart cards survive streamed Markdown and stale chart events are ignored", (t) => {
  const { window, doc, send } = ask(t);
  let disposed = 0;
  window.shinyhubAgentCharts = {
    create(chart) { const figure = doc.createElement('figure'); figure.className = 'sh-agent-chart'; figure.textContent = chart.title; return figure; },
    dispose() { disposed++; }
  };
  send({ type: 'chart', chart: {title:'Sales by month'} });
  send({ type: 'delta', text: '**Sales** rose.' });
  send({ type: 'done' });
  assert.equal(doc.querySelector('.sh-agent-chart').textContent, 'Sales by month');
  assert.equal(doc.querySelector('.sh-agent-assistant strong:not(.sh-agent-speaker)').textContent,'Sales');
  send({ type: 'chart', chart: {title:'Stale chart'} });
  assert.equal(doc.querySelectorAll('.sh-agent-chart').length,1);
  send({ type: 'reset' });
  assert.equal(doc.querySelectorAll('.sh-agent-chart').length,0);
  assert.equal(disposed,1);
});
