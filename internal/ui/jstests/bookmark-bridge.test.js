import { afterEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';

const BRIDGE_JS = new URL('../../../packaging/python-bookmarks/src/shinyhub_bookmarks/www/bridge.js', import.meta.url);
const source = readFileSync(BRIDGE_JS, 'utf8');
const mountedWindows = [];

afterEach(() => {
  while (mountedWindows.length) mountedWindows.pop().close();
});

function mountBridge({ throwOnInput = false, timeoutMap = {} } = {}) {
  const dom = new JSDOM('<!doctype html><body></body>', {
    runScripts: 'dangerously',
    url: 'https://hub.test/app/demo/',
  });
  const handlers = {};
  const inputs = [];
  let shouldThrowOnInput = throwOnInput;
  const nativeSetTimeout = dom.window.setTimeout.bind(dom.window);
  dom.window.setTimeout = (callback, delay, ...args) => nativeSetTimeout(
    callback,
    Object.hasOwn(timeoutMap, delay) ? timeoutMap[delay] : delay,
    ...args,
  );
  dom.window.Shiny = {
    addCustomMessageHandler(name, handler) {
      handlers[name] = handler;
    },
    setInputValue(id, value, options) {
      if (shouldThrowOnInput) throw new Error('connection closed');
      inputs.push({ id, value, options });
    },
  };
  const script = dom.window.document.createElement('script');
  script.textContent = source;
  dom.window.document.body.appendChild(script);
  mountedWindows.push(dom.window);
  return {
    dom,
    window: dom.window,
    handlers,
    inputs,
    setThrowOnInput(value) { shouldThrowOnInput = value; },
  };
}

const flush = (window, delay = 0) => new Promise((resolve) => window.setTimeout(resolve, delay));
const requests = (mounted) => mounted.inputs.filter(
  (entry) => entry.id === '.shinyhub_bookmark_request',
);

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

test('registered filter changes replace the current URL after a short debounce', async () => {
  const m = mountBridge();
  m.inputs.length = 0;
  m.window.location.hash = '#results';
  const pushes = [];
  const nativePushState = m.window.history.pushState.bind(m.window.history);
  m.window.history.pushState = (...args) => {
    pushes.push(args);
    return nativePushState(...args);
  };

  m.handlers['shinyhub-bookmark-capabilities']({
    version: 1,
    store: 'url',
    autoSync: true,
    syncRevision: 1,
    fields: [
      { id: 'region', label: 'Region', value: 'Americas' },
      { id: 'year', label: 'Year', value: '2026' },
    ],
  });

  await flush(m.window, 350);
  assert.equal(requests(m).length, 1);
  assert.deepEqual(Array.from(requests(m)[0].value.include), ['region', 'year']);
  assert.equal(requests(m)[0].value.purpose, 'sync');
  assert.equal(requests(m)[0].value.syncRevision, 1);

  m.handlers['shinyhub-bookmark-result']({
    version: 1,
    requestId: requests(m)[0].value.requestId,
    purpose: 'sync',
    syncRevision: 1,
    url: 'https://hub.test/app/demo/?_inputs_=saved',
  });

  assert.equal(m.window.location.search, '?_inputs_=saved');
  assert.equal(m.window.location.hash, '#results');
  assert.equal(pushes.length, 0, 'filter changes must not add Back-button entries');
  const ack = m.inputs.find((entry) => entry.id === '.shinyhub_bookmark_sync_ack');
  assert.equal(ack.value.syncRevision, 1);
});

test('initial capabilities do not rewrite a clean app URL', async () => {
  const m = mountBridge();
  m.inputs.length = 0;

  m.handlers['shinyhub-bookmark-capabilities']({
    version: 1,
    store: 'url',
    autoSync: false,
    fields: [{ id: 'region', label: 'Region', value: 'Europe' }],
  });

  await flush(m.window, 350);
  assert.equal(m.inputs.length, 0);
  assert.equal(m.window.location.search, '');
});

test('rapid filter changes coalesce into one URL request', async () => {
  const m = mountBridge();
  m.inputs.length = 0;
  const message = (value) => ({
    version: 1,
    store: 'url',
    autoSync: true,
    fields: [{ id: 'region', label: 'Region', value }],
  });

  m.handlers['shinyhub-bookmark-capabilities'](message('Europe'));
  await flush(m.window, 100);
  m.handlers['shinyhub-bookmark-capabilities'](message('Americas'));
  await flush(m.window, 100);
  m.handlers['shinyhub-bookmark-capabilities'](message('Asia'));
  await flush(m.window, 350);

  assert.equal(requests(m).length, 1);
  assert.match(requests(m)[0].value.requestId, /^url-sync-/);
});

test('manual selective links wait for an in-flight URL sync', async () => {
  const m = mountBridge();
  m.inputs.length = 0;
  m.handlers['shinyhub-bookmark-capabilities']({
    version: 1,
    store: 'url',
    autoSync: true,
    fields: [{ id: 'region', label: 'Region', value: 'Americas' }],
  });
  await flush(m.window, 350);
  const syncRequest = requests(m)[0].value;
  const manualRequest = {
    version: 1,
    requestId: 'request-manual',
    include: ['region'],
  };

  m.window.dispatchEvent(new m.window.CustomEvent('shinyhub:bookmark:create', {
    detail: manualRequest,
  }));
  assert.equal(requests(m).length, 1, 'manual creation raced the background request');

  m.handlers['shinyhub-bookmark-result']({
    version: 1,
    requestId: syncRequest.requestId,
    url: 'https://hub.test/app/demo/?_inputs_=synced',
  });

  assert.equal(requests(m).length, 2);
  assert.equal(requests(m)[1].value.requestId, 'request-manual');
});

test('automatic URL sync rejects a different origin or app path', async () => {
  const m = mountBridge();
  m.inputs.length = 0;
  const requestSync = async (url) => {
    m.handlers['shinyhub-bookmark-capabilities']({
      version: 1,
      store: 'url',
      autoSync: true,
      fields: [{ id: 'region', label: 'Region', value: 'Americas' }],
    });
    await flush(m.window, 350);
    const request = m.inputs.at(-1).value;
    m.handlers['shinyhub-bookmark-result']({ version: 1, requestId: request.requestId, url });
  };

  await requestSync('https://elsewhere.test/app/demo/?_inputs_=unsafe');
  assert.equal(m.window.location.search, '');
  await requestSync('https://hub.test/app/other/?_inputs_=unsafe');
  assert.equal(m.window.location.search, '');
});

test('a stale sync result is discarded and the latest revision is saved immediately', async () => {
  const m = mountBridge();
  m.inputs.length = 0;
  const capability = (revision, value) => ({
    version: 1,
    store: 'url',
    autoSync: true,
    syncRevision: revision,
    fields: [{ id: 'region', label: 'Region', value }],
  });

  m.handlers['shinyhub-bookmark-capabilities'](capability(1, 'Europe'));
  await flush(m.window, 350);
  const first = requests(m)[0].value;
  m.handlers['shinyhub-bookmark-capabilities'](capability(2, 'Asia'));
  m.handlers['shinyhub-bookmark-result']({
    version: 1,
    purpose: 'sync',
    requestId: first.requestId,
    syncRevision: 1,
    url: 'https://hub.test/app/demo/?_inputs_=stale',
  });

  await flush(m.window, 10);
  assert.equal(m.window.location.search, '');
  assert.equal(requests(m).length, 2);
  const latest = requests(m)[1].value;
  assert.equal(latest.syncRevision, 2);

  m.handlers['shinyhub-bookmark-result']({
    version: 1,
    purpose: 'sync',
    requestId: latest.requestId,
    syncRevision: 2,
    url: 'https://hub.test/app/demo/?_inputs_=latest',
  });
  assert.equal(m.window.location.search, '?_inputs_=latest');
});

test('republishing the same revision does not discard its in-flight result', async () => {
  const m = mountBridge();
  m.inputs.length = 0;
  const capability = {
    version: 1,
    store: 'url',
    autoSync: true,
    syncRevision: 4,
    fields: [{ id: 'region', label: 'Region', value: 'Asia' }],
  };
  m.handlers['shinyhub-bookmark-capabilities'](capability);
  await flush(m.window, 350);
  const active = requests(m)[0].value;
  m.handlers['shinyhub-bookmark-capabilities'](capability);
  m.handlers['shinyhub-bookmark-result']({
    version: 1,
    purpose: 'sync',
    requestId: active.requestId,
    syncRevision: 4,
    url: 'https://hub.test/app/demo/?_inputs_=same-revision',
  });
  await flush(m.window, 10);

  assert.equal(m.window.location.search, '?_inputs_=same-revision');
  assert.equal(requests(m).length, 1);
});

test('a transient sync error retries once, reports failure, and recovers on a later change', async () => {
  const m = mountBridge();
  m.inputs.length = 0;
  const statuses = [];
  m.window.addEventListener('shinyhub:bookmark:sync-status', (event) => {
    statuses.push(event.detail.state);
  });
  const capability = (revision) => ({
    version: 1,
    store: 'url',
    autoSync: true,
    syncRevision: revision,
    fields: [{ id: 'region', label: 'Region', value: String(revision) }],
  });

  m.handlers['shinyhub-bookmark-capabilities'](capability(1));
  await flush(m.window, 350);
  m.handlers['shinyhub-bookmark-error']({
    version: 1,
    purpose: 'sync',
    requestId: requests(m)[0].value.requestId,
    code: 'serialization_failed',
  });
  await flush(m.window, 800);
  assert.equal(requests(m).length, 2);
  m.handlers['shinyhub-bookmark-error']({
    version: 1,
    purpose: 'sync',
    requestId: requests(m)[1].value.requestId,
    code: 'serialization_failed',
  });
  assert.deepEqual(statuses, ['error']);

  m.handlers['shinyhub-bookmark-capabilities'](capability(2));
  await flush(m.window, 350);
  const recovered = requests(m)[2].value;
  m.handlers['shinyhub-bookmark-result']({
    version: 1,
    purpose: 'sync',
    requestId: recovered.requestId,
    syncRevision: 2,
    url: 'https://hub.test/app/demo/?_inputs_=recovered',
  });
  assert.deepEqual(statuses, ['error', 'saved']);
  assert.equal(m.window.location.search, '?_inputs_=recovered');
});

test('a thrown Shiny input dispatch cannot wedge later manual link requests', () => {
  const m = mountBridge();
  m.inputs.length = 0;
  const errors = [];
  m.window.addEventListener('shinyhub:bookmark:error', (event) => errors.push(event.detail));
  m.setThrowOnInput(true);
  m.window.dispatchEvent(new m.window.CustomEvent('shinyhub:bookmark:create', {
    detail: { version: 1, requestId: 'first', include: ['region'] },
  }));
  assert.equal(errors[0].code, 'dispatch_failed');

  m.setThrowOnInput(false);
  m.window.dispatchEvent(new m.window.CustomEvent('shinyhub:bookmark:create', {
    detail: { version: 1, requestId: 'second', include: ['region'] },
  }));
  assert.equal(requests(m)[0].value.requestId, 'second');
});

test('manual request IDs that resemble sync IDs are still forwarded', () => {
  const m = mountBridge();
  m.inputs.length = 0;
  const results = [];
  m.window.addEventListener('shinyhub:bookmark:result', (event) => results.push(event.detail));
  const detail = { version: 1, requestId: 'url-sync-user-choice', include: ['region'] };
  m.window.dispatchEvent(new m.window.CustomEvent('shinyhub:bookmark:create', { detail }));
  m.handlers['shinyhub-bookmark-result']({
    version: 1,
    requestId: detail.requestId,
    url: 'https://hub.test/app/demo/?_inputs_=manual',
  });
  assert.equal(results.length, 1);
});

test('reserved background fields are stripped from manual create events', () => {
  const m = mountBridge();
  m.inputs.length = 0;
  const results = [];
  m.window.addEventListener('shinyhub:bookmark:result', (event) => results.push(event.detail));
  m.window.dispatchEvent(new m.window.CustomEvent('shinyhub:bookmark:create', {
    detail: {
      version: 1,
      requestId: 'manual-reserved',
      include: ['region'],
      purpose: 'sync',
      syncRevision: 99,
    },
  }));
  const sent = requests(m)[0].value;
  assert.equal(sent.purpose, undefined);
  assert.equal(sent.syncRevision, undefined);
  m.handlers['shinyhub-bookmark-result']({
    version: 1,
    requestId: sent.requestId,
    url: 'https://hub.test/app/demo/?_inputs_=manual',
  });
  assert.equal(results.length, 1);
});

test('a queued manual request receives an error when its own deadline expires', async () => {
  const m = mountBridge({ timeoutMap: { 3000: 10, 7000: 15 } });
  m.inputs.length = 0;
  const errors = [];
  m.window.addEventListener('shinyhub:bookmark:error', (event) => errors.push(event.detail));
  m.handlers['shinyhub-bookmark-capabilities']({
    version: 1,
    store: 'url',
    autoSync: true,
    syncRevision: 1,
    fields: [{ id: 'region', label: 'Region', value: 'Europe' }],
  });
  await flush(m.window, 350);
  m.window.dispatchEvent(new m.window.CustomEvent('shinyhub:bookmark:create', {
    detail: { version: 1, requestId: 'queued-manual', include: ['region'] },
  }));
  await flush(m.window, 40);

  assert.equal(requests(m).length, 2);
  assert.equal(requests(m)[1].value.requestId, 'queued-manual');
  assert.equal(errors.at(-1).requestId, 'queued-manual');
  assert.equal(errors.at(-1).code, 'request_timeout');
});

test('legal field IDs inherited by ordinary objects remain bookmarkable', async () => {
  const m = mountBridge();
  m.inputs.length = 0;
  m.handlers['shinyhub-bookmark-capabilities']({
    version: 1,
    store: 'url',
    autoSync: true,
    syncRevision: 1,
    fields: [{ id: 'constructor', label: 'Constructor', value: 'A' }],
  });
  await flush(m.window, 350);
  assert.deepEqual(Array.from(requests(m)[0].value.include), ['constructor']);
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
  dom.window.close();
});

test('an initial discovery dispatch failure retries without disabling the bridge', async () => {
  const m = mountBridge({ throwOnInput: true });
  m.setThrowOnInput(false);
  await flush(m.window, 120);

  assert.equal(typeof m.handlers['shinyhub-bookmark-capabilities'], 'function');
  assert.ok(m.inputs.some((entry) => entry.id === '.shinyhub_bookmark_discover'));
});
