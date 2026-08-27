import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';

const BRIDGE_JS = new URL('../../../packaging/python-bookmarks/src/shinyhub_bookmarks/www/bridge.js', import.meta.url);
const source = readFileSync(BRIDGE_JS, 'utf8');

function mountBridge() {
  const dom = new JSDOM('<!doctype html><body></body>', {
    runScripts: 'dangerously',
    url: 'https://hub.test/app/demo/',
  });
  const handlers = {};
  const inputs = [];
  dom.window.Shiny = {
    addCustomMessageHandler(name, handler) {
      handlers[name] = handler;
    },
    setInputValue(id, value, options) {
      inputs.push({ id, value, options });
    },
  };
  const script = dom.window.document.createElement('script');
  script.textContent = source;
  dom.window.document.body.appendChild(script);
  return { dom, window: dom.window, handlers, inputs };
}

const flush = (window, delay = 0) => new Promise((resolve) => window.setTimeout(resolve, delay));

test('the Python bridge translates server capabilities into the versioned browser event', () => {
  const m = mountBridge();
  const received = [];
  m.window.addEventListener('shinyhub:bookmark:capabilities', (event) => received.push(event.detail));

  const message = {
    version: 1,
    store: 'url',
    fields: [{ id: 'region', label: 'Region', value: 'Europe' }],
  };
  m.handlers['shinyhub-bookmark-capabilities'](message);

  assert.deepEqual(received, [message]);
  assert.deepEqual(Object.keys(m.handlers).sort(), [
    'shinyhub-bookmark-capabilities',
    'shinyhub-bookmark-error',
    'shinyhub-bookmark-result',
  ]);
});

test('the Python bridge forwards create requests as event-priority Shiny inputs', () => {
  const m = mountBridge();
  m.inputs.length = 0;
  const detail = { version: 1, requestId: 'request-1', include: ['region'] };

  m.window.dispatchEvent(new m.window.CustomEvent('shinyhub:bookmark:create', { detail }));

  assert.deepEqual(JSON.parse(JSON.stringify(m.inputs)), [{
    id: '.shinyhub_bookmark_request',
    value: detail,
    options: { priority: 'event' },
  }]);
});

test('switcher discovery asks Shiny to republish capabilities', () => {
  const m = mountBridge();
  m.inputs.length = 0;

  m.window.dispatchEvent(new m.window.CustomEvent('shinyhub:bookmark:discover', {
    detail: { version: 1, source: 'shinyhub' },
  }));

  assert.equal(m.inputs.length, 1);
  assert.equal(m.inputs[0].id, '.shinyhub_bookmark_discover');
  assert.equal(m.inputs[0].value.version, 1);
  assert.equal(m.inputs[0].options.priority, 'event');
});

test('the bridge ignores protocol versions it does not understand', () => {
  const m = mountBridge();
  const received = [];
  m.window.addEventListener('shinyhub:bookmark:result', (event) => received.push(event.detail));

  m.handlers['shinyhub-bookmark-result']({ version: 2, requestId: 'future', url: 'https://example.test' });

  assert.deepEqual(received, []);
});

test('the bridge installs when Shiny becomes available after the dependency loads', async () => {
  const dom = new JSDOM('<!doctype html><body></body>', {
    runScripts: 'dangerously',
    url: 'https://hub.test/app/demo/',
  });
  const script = dom.window.document.createElement('script');
  script.textContent = source;
  dom.window.document.body.appendChild(script);
  const handlers = {};
  const inputs = [];
  dom.window.Shiny = {
    addCustomMessageHandler(name, handler) { handlers[name] = handler; },
    setInputValue(id, value, options) { inputs.push({ id, value, options }); },
  };

  await flush(dom.window, 120);

  assert.equal(typeof handlers['shinyhub-bookmark-capabilities'], 'function');
  assert.ok(inputs.some((entry) => entry.id === '.shinyhub_bookmark_discover'));
});
