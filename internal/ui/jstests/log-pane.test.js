import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { createLogPane } from '../static/views/log-pane.js';
import { MAX_RENDERED_LOG_ENTRIES } from '../static/views/logs-ui.js';

class FakeEventSource {
  constructor(url, opts) {
    this.url = url;
    this.opts = opts;
    this.closed = false;
    this.onopen = null;
    this.onmessage = null;
    this.onerror = null;
    FakeEventSource.instances.push(this);
  }
  emit(data) {
    if (this.onmessage) this.onmessage({ data });
  }
  error() {
    if (this.onerror) this.onerror();
  }
  open() {
    if (this.onopen) this.onopen();
  }
  close() {
    this.closed = true;
  }
}
FakeEventSource.instances = [];

function setup() {
  FakeEventSource.instances = [];
  const dom = new JSDOM(`<!DOCTYPE html><body>
    <div id="log-pane" hidden>
      <div class="log-pane-header">
        <span id="log-pane-title"></span>
        <button id="log-pane-close" type="button"></button>
      </div>
      <p id="log-pane-status" hidden></p>
      <pre id="log-pane-body"></pre>
    </div>
  </body>`);
  const doc = dom.window.document;
  const traps = [];
  const createFocusTrap = () => {
    const t = {
      activated: 0,
      released: 0,
      active: false,
      activate() { this.activated += 1; this.active = true; },
      release() { this.released += 1; this.active = false; },
    };
    traps.push(t);
    return t;
  };
  const pane = createLogPane({
    pane: doc.getElementById('log-pane'),
    title: doc.getElementById('log-pane-title'),
    body: doc.getElementById('log-pane-body'),
    status: doc.getElementById('log-pane-status'),
    closeButton: doc.getElementById('log-pane-close'),
    EventSourceClass: FakeEventSource,
    createFocusTrap,
    doc,
    setHidden: (el, hidden) => { el.hidden = hidden; },
  });
  return { doc, pane, traps };
}

test('open: sets title, shows the pane, opens a stream, and activates a focus trap', () => {
  const { doc, pane, traps } = setup();
  pane.open({ titleText: 'Logs: demo', url: '/api/apps/demo/logs', withCredentials: true });
  assert.equal(doc.getElementById('log-pane-title').textContent, 'Logs: demo');
  assert.equal(doc.getElementById('log-pane').hidden, false);
  assert.equal(FakeEventSource.instances.length, 1);
  assert.equal(FakeEventSource.instances[0].url, '/api/apps/demo/logs');
  assert.deepEqual(FakeEventSource.instances[0].opts, { withCredentials: true });
  assert.equal(traps[0].activated, 1);
});

test('onmessage appends lines to the body in order', () => {
  const { doc, pane } = setup();
  pane.open({ titleText: 'Logs: demo', url: '/api/apps/demo/logs' });
  const es = FakeEventSource.instances[0];
  es.emit('line one');
  es.emit('line two');
  assert.equal(doc.getElementById('log-pane-body').textContent, 'line one\nline two\n');
});

test('the rendered body is capped at MAX_RENDERED_LOG_ENTRIES, dropping the oldest lines', () => {
  const { doc, pane } = setup();
  pane.open({ titleText: 'Logs: demo', url: '/api/apps/demo/logs' });
  const es = FakeEventSource.instances[0];
  const total = MAX_RENDERED_LOG_ENTRIES + 10;
  for (let i = 1; i <= total; i += 1) es.emit(`line ${i}`);
  const rendered = doc.getElementById('log-pane-body').textContent.trim().split('\n');
  assert.equal(rendered.length, MAX_RENDERED_LOG_ENTRIES);
  assert.equal(rendered[0], 'line 11');
  assert.equal(rendered[rendered.length - 1], `line ${total}`);
});

test('onerror reports a status without closing the stream (native EventSource retries on its own)', () => {
  const { doc, pane } = setup();
  pane.open({ titleText: 'Logs: demo', url: '/api/apps/demo/logs' });
  const es = FakeEventSource.instances[0];
  es.error();
  assert.equal(es.closed, false, 'a transient error must not close the EventSource, or the browser can never auto-retry');
  const status = doc.getElementById('log-pane-status');
  assert.notEqual(status.textContent, '');
  assert.equal(status.hidden, false);
});

test('onopen clears the disconnected status after a reconnect', () => {
  const { doc, pane } = setup();
  pane.open({ titleText: 'Logs: demo', url: '/api/apps/demo/logs' });
  const es = FakeEventSource.instances[0];
  es.error();
  es.open();
  const status = doc.getElementById('log-pane-status');
  assert.equal(status.textContent, '');
  assert.equal(status.hidden, true);
});

test('close: closes the stream, hides the pane, and releases the focus trap', () => {
  const { doc, pane, traps } = setup();
  pane.open({ titleText: 'Logs: demo', url: '/api/apps/demo/logs' });
  const es = FakeEventSource.instances[0];
  pane.close();
  assert.equal(es.closed, true);
  assert.equal(doc.getElementById('log-pane').hidden, true);
  assert.equal(traps[0].released, 1);
});

test('onNavigated closes an open pane (mirrors the sidebar drawer post-mount hook)', () => {
  const { pane } = setup();
  pane.open({ titleText: 'Logs: demo', url: '/api/apps/demo/logs' });
  assert.equal(pane.isOpen(), true);
  pane.onNavigated();
  assert.equal(pane.isOpen(), false);
  assert.equal(FakeEventSource.instances[0].closed, true);
});

test('onNavigated is a no-op when the pane is already closed', () => {
  const { pane, traps } = setup();
  pane.onNavigated();
  assert.equal(pane.isOpen(), false);
  assert.equal(traps.length, 0);
});

test('opening a second stream while already open replaces the first without leaking it', () => {
  const { doc, pane, traps } = setup();
  pane.open({ titleText: 'Logs: app-one', url: '/api/apps/app-one/logs' });
  const first = FakeEventSource.instances[0];
  pane.open({ titleText: 'Logs: app-two', url: '/api/apps/app-two/logs' });
  assert.equal(first.closed, true, 'the previous EventSource must be closed before a new one opens');
  assert.equal(FakeEventSource.instances.length, 2);
  assert.equal(doc.getElementById('log-pane-title').textContent, 'Logs: app-two');
  // The trap is reused (activate is idempotent on the real modalTrap) rather
  // than released and re-created.
  assert.equal(traps.length, 1);
  assert.equal(traps[0].activated, 2);
});

test('close is a no-op when nothing is open', () => {
  const { doc, pane, traps } = setup();
  pane.close();
  assert.equal(doc.getElementById('log-pane').hidden, true);
  assert.equal(traps.length, 0);
});
