import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM, VirtualConsole } from 'jsdom';

// Exercise all three shipped scripts together, including the adapter's real
// pending/saved/disconnected ordering. Only Shiny's server and the clock are fake.
const source = path => readFileSync(new URL(path, import.meta.url), 'utf8');
const overlay = source('../../proxy/assets/overlay.js');
const nav = source('../../appnav/assets/nav.js');
const bridge = source('../../../packaging/python-bookmarks/src/shinyhub_bookmarks/www/bridge.js');
const flush = () => new Promise(resolve => setImmediate(resolve));

async function mount(t, { policy = 'when_hidden', status = 200, body = { ready: true },
  reject = '', deferred = false, jquery = false, cooldown = false, storage = true } = {}) {
  const errors = [];
  const virtualConsole = new VirtualConsole();
  virtualConsole.on('jsdomError', error => errors.push(error.message));
  const dom = new JSDOM('<!doctype html><body><label>Region<input id="region" value="Europe"></label></body>', {
    url: 'https://hub.test/app/demo/?campaign=weekly#plot', runScripts: 'dangerously', virtualConsole,
  });
  t.after(() => dom.window.close());
  const w = dom.window, d = w.document;
  let now = Date.now();
  w.Date.now = () => now;
  Object.defineProperty(d, 'hidden', { value: false });
  const timers = new Map();
  let timerID = 0;
  w.setTimeout = (fn, ms) => { timers.set(++timerID, { fn, ms }); return timerID; };
  w.clearTimeout = id => timers.delete(id);
  const runTimer = async ms => {
    const entry = [...timers].findLast(([, timer]) => timer.ms === ms);
    assert.ok(entry, `missing ${ms}ms timer`);
    now += ms;
    timers.delete(entry[0]); entry[1].fn(); await flush(); await flush();
  };
  if (jquery) w.jQuery = () => ({ on(name, handler) { d.addEventListener(name.split('.')[0], handler); } });
  const handlers = {}, inputs = [], fetches = [];
  w.Shiny = {
    addCustomMessageHandler(name, handler) { handlers[name] = handler; },
    setInputValue(name, value) { inputs.push({ name, value }); },
  };
  let settle;
  w.fetch = (url, options) => {
    fetches.push({ url, options });
    const response = { status, headers: { get: () => reject }, json: async () => body };
    if (deferred) return new Promise(resolve => { settle = () => resolve(response); });
    return Promise.resolve(response);
  };
  const inject = (code, attrs) => {
    const tag = d.createElement('script');
    for (const [key, value] of Object.entries(attrs || {})) tag.setAttribute(key, value);
    tag.textContent = code; d.body.appendChild(tag);
  };
  inject(overlay, { 'data-ready-url': '/app/demo/.shinyhub/ready' });
  inject(nav, { id: 'shinyhub-app-nav', 'data-nav-url': '/app/demo/.shinyhub/nav.json',
    'data-current-slug': 'demo', 'data-version-switch': policy });
  inject(bridge);
  d.dispatchEvent(new w.Event('shiny:connected'));
  const capabilities = { version: 1, store: 'url', session: 'session-1', autoSync: true,
    pendingSync: true, syncReady: true, registeredInputs: ['region'], observedBrowserRevision: 0,
    syncRevision: 1, syncFields: ['region'], fields: [{ id: 'region', label: 'Region', value: 'Europe' }] };
  handlers['shinyhub-bookmark-capabilities'](capabilities);
  await runTimer(300);
  const request = inputs.find(input => input.name === '.shinyhub_bookmark_request').value;
  handlers['shinyhub-bookmark-result']({ version: 1, purpose: 'sync', requestId: request.requestId,
    url: '/app/demo/?_inputs_&region=Europe' });
  const url = w.location.href;
  if (cooldown) w.sessionStorage.setItem('shinyhub-recovery:/app/demo/', String(Date.now()));
  if (!storage) Object.defineProperty(w, 'sessionStorage', { get() { throw new Error('disabled'); } });
  return {
    w, d, timers, runTimer, fetches, handlers, capabilities, url,
    emit(name, detail) { w.dispatchEvent(new w.CustomEvent(name, { detail })); },
    edits() { d.getElementById('region').dispatchEvent(new w.Event('input', { bubbles: true })); },
    async disconnect() {
      d.dispatchEvent(new w.Event('shiny:disconnected'));
      const marker = d.createElement('div'); marker.id = 'shiny-disconnected-overlay'; d.body.appendChild(marker);
      await flush(); await flush();
    },
    async settle() { settle(); await flush(); await flush(); },
    reloads: () => errors.filter(error => /Not implemented: navigation/.test(error)).length,
    ui: () => d.getElementById('shinyhub-status-overlay'),
  };
}

for (const jquery of [false, true]) {
  test(`saved dashboard restores without a prompt (${jquery ? 'jQuery' : 'native'} events)`, async t => {
    const m = await mount(t, { jquery });
    m.d.getElementById('region').focus();
    await m.disconnect();
    assert.equal(m.reloads(), 0, 'leave time for native reconnection');
    await m.runTimer(750);
    assert.equal(m.reloads(), 1);
    assert.equal(m.ui(), null);
    assert.equal(m.d.activeElement.id, 'region');
    assert.equal(m.w.location.href, m.url);
    assert.match(m.url, /campaign=weekly.*region=Europe#plot/);
    assert.ok(m.w.sessionStorage.getItem('shinyhub-recovery:/app/demo/'));
    assert.equal(m.fetches[0].options.redirect, 'error');
  });
}

test('a cold dashboard can wake from a verified not-ready response', async t => {
  const m = await mount(t, { status: 503, body: { ready: false }, reject: 'app-not-ready' });
  await m.disconnect();
  await m.runTimer(750);
  assert.equal(m.reloads(), 1);
});

for (const block of ['edit', 'manual', 'cooldown', 'storage', 'chat', 'busy-chat', 'snapshot', 'url', 'legacy', 'session', 'unsettled']) {
  test(`${block} keeps the manual recovery fallback`, async t => {
    const m = await mount(t, { policy: block === 'manual' ? 'manual' : 'when_hidden',
      cooldown: block === 'cooldown', storage: block !== 'storage' });
    if (block === 'edit') m.edits();
    if (block === 'chat') m.emit('shinyhub:chat:state', { version: 1, open: true });
    if (block === 'busy-chat') m.emit('shinyhub:chat:busy', { version: 1, busy: true });
    if (block === 'snapshot') m.emit('shinyhub:session-status', { state: 'snapshot' });
    if (block === 'url') m.w.history.replaceState(null, '', '?different');
    if (block === 'legacy') m.emit('shinyhub:bookmark:sync-status', { version: 1, state: 'pending' });
    if (block === 'session') m.handlers['shinyhub-bookmark-capabilities']({ ...m.capabilities, session: 'session-2' });
    if (block === 'unsettled') m.handlers['shinyhub-bookmark-capabilities']({ ...m.capabilities, syncReady: false });
    await m.disconnect();
    assert.equal(m.reloads(), 0);
    assert.equal(m.ui().getAttribute('role'), 'dialog');
  });
}

test('slow recovery shows a quiet notice without taking focus', async t => {
  const m = await mount(t, { deferred: true });
  m.d.getElementById('region').focus();
  await m.disconnect();
  assert.equal(m.ui(), null);
  await m.runTimer(1200);
  assert.equal(m.ui().getAttribute('role'), 'status');
  assert.equal(m.ui().hasAttribute('aria-modal'), false);
  assert.match(m.ui().textContent, /Reconnecting to your dashboard/);
  assert.equal(m.d.activeElement.id, 'region');
  await m.settle();
  await m.runTimer(0);
  assert.equal(m.reloads(), 1);
});

test('edits during the readiness request cancel automatic recovery', async t => {
  const m = await mount(t, { deferred: true });
  await m.disconnect(); m.edits(); await m.settle();
  assert.equal(m.reloads(), 0);
  assert.equal(m.ui().getAttribute('role'), 'dialog');
});

test('a hanging probe falls back within the recovery budget and ignores its late answer', async t => {
  const m = await mount(t, { deferred: true });
  await m.disconnect(); await m.runTimer(60000); await m.settle();
  assert.equal(m.reloads(), 0);
  assert.equal(m.ui().getAttribute('role'), 'dialog');
});

test('native reconnection cancels recovery and its outstanding probe', async t => {
  const m = await mount(t, { deferred: true });
  await m.disconnect(); await m.runTimer(1200);
  m.d.getElementById('shiny-disconnected-overlay').remove();
  await flush(); await m.settle();
  assert.equal(m.reloads(), 0);
  assert.equal(m.ui(), null);
  assert.equal([...m.timers.values()].some(timer => timer.ms === 60000), false);
});

for (const response of [{ status: 401 }, { status: 403 }, { status: 404 },
  { status: 503 }, { body: { login: true } }, { body: null }]) {
  test(`unverified readiness never reloads: ${JSON.stringify(response)}`, async t => {
    const m = await mount(t, response);
    await m.disconnect();
    assert.equal(m.reloads(), 0);
  });
}

test('native reconnect during the grace period avoids a reload and restores the marker style', async t => {
  const m = await mount(t);
  await m.disconnect();
  const marker = m.d.getElementById('shiny-disconnected-overlay');
  assert.equal(marker.style.opacity, '0');
  assert.equal(marker.getAttribute('aria-hidden'), 'true');
  assert.notEqual(marker.style.display, 'none', 'pointer blocker must remain');
  marker.remove();
  await flush();
  assert.equal(marker.style.opacity, '');
  assert.equal(marker.hasAttribute('aria-hidden'), false);
  assert.equal([...m.timers.values()].some(timer => timer.ms === 750), false);
  assert.equal(m.reloads(), 0);
  assert.equal(m.ui(), null);
});

test('a last-moment edit vetoes the queued reload and restores the disconnect veil', async t => {
  const m = await mount(t);
  await m.disconnect(); m.edits(); await m.runTimer(750);
  assert.equal(m.reloads(), 0);
  assert.equal(m.ui().getAttribute('role'), 'dialog');
  assert.equal(m.d.getElementById('shiny-disconnected-overlay').style.opacity, '');
});

test('keep results cancels recovery, its pending request, and every recovery timer', async t => {
  const m = await mount(t, { deferred: true });
  await m.disconnect(); await m.runTimer(1200);
  m.ui().querySelector('#shinyhub-status-keep').click();
  await m.settle();
  assert.equal(m.reloads(), 0);
  assert.equal(m.d.getElementById('shiny-disconnected-overlay').style.display, 'none');
  assert.equal(m.fetches[0].options.signal.aborted, true);
  assert.equal([...m.timers.values()].some(timer => [750, 1200, 10000, 60000].includes(timer.ms)), false);
});

test('offline recovery preserves results and checks immediately when back online', async t => {
  const m = await mount(t);
  Object.defineProperty(m.w.navigator, 'onLine', { value: false, configurable: true });
  await m.disconnect();
  assert.match(m.ui().textContent, /You’re offline/);
  assert.equal(m.fetches.length, 0);
  Object.defineProperty(m.w.navigator, 'onLine', { value: true, configurable: true });
  m.w.dispatchEvent(new m.w.Event('online'));
  await flush(); await flush();
  assert.equal(m.fetches.length, 1);
  await m.runTimer(750);
  assert.equal(m.reloads(), 1);
});

test('going offline cancels a queued navigation', async t => {
  const m = await mount(t);
  await m.disconnect();
  Object.defineProperty(m.w.navigator, 'onLine', { value: false, configurable: true });
  m.w.dispatchEvent(new m.w.Event('offline'));
  assert.equal([...m.timers.values()].some(timer => timer.ms === 750), false);
  assert.equal(m.reloads(), 0);
  assert.match(m.ui().textContent, /You’re offline/);
});

test('a timed-out probe is aborted and its late response cannot navigate', async t => {
  const m = await mount(t, { deferred: true });
  await m.disconnect(); await m.runTimer(10000); await m.settle();
  assert.equal(m.fetches[0].options.signal.aborted, true);
  assert.equal(m.reloads(), 0);
  assert.equal([...m.timers.values()].some(timer => timer.ms === 750), false);
  await m.runTimer(3000);
  assert.equal(m.fetches.length, 2);
});

for (const status of [401, 403]) {
  test(`${status} explains the access problem and preserves a manual new-tab path`, async t => {
    const m = await mount(t, { status });
    await m.disconnect();
    assert.match(m.ui().textContent, status === 401 ? /Sign in to reconnect/ : /Access to this app has changed/);
    const link = m.ui().querySelector('#shinyhub-status-open');
    assert.equal(link.style.display, 'inline-flex');
    assert.equal(link.target, '_blank');
    assert.equal(m.reloads(), 0);
    assert.equal([...m.timers.values()].some(timer => [10000, 60000].includes(timer.ms)), false);
  });
}

test('a quiet reconnect does not move focus back after the viewer has moved it', async t => {
  const m = await mount(t, { deferred: true });
  m.d.getElementById('region').focus();
  await m.disconnect(); await m.runTimer(1200);
  const other = m.d.createElement('button'); other.textContent = 'Other control'; m.d.body.appendChild(other); other.focus();
  m.d.getElementById('shiny-disconnected-overlay').remove();
  await flush();
  assert.equal(m.d.activeElement, other);
});
