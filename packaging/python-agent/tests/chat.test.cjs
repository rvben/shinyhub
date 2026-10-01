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
function ask(t, { savedSize = null, blockStorage = false } = {}) {
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
  function reconnect(next) {
    session = next;
    handlers.get("shinyhub-agent-chat-capabilities")({ version: 1, enabled: true, session });
  }
  reconnect("s1");
  doc.querySelector(".sh-agent-launcher").click();
  let requestId;
  function submit(question) {
    doc.querySelector(".sh-agent-input").value = question;
    doc.querySelector(".sh-agent-composer").requestSubmit();
    requestId = inputs.filter((entry) => entry.name === ".shinyhub_agent_chat_request" && entry.value.message).at(-1).value.requestId;
  }
  submit("Show only 2024");
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
