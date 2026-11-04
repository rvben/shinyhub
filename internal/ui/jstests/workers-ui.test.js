import { beforeEach, afterEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { mountWorkers, visibleWorkers, workerHeartbeat } from '../static/views/workers.js';

const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8').match(/<section id="workers-view"[\s\S]*?<\/section>/)[0];
const fixture = [
  { node_id: 'online', name: 'Reporting pool · Europe and North America', status: 'up', tier: 'analytics', advertise_addr: '127.0.0.1:9443', version: '0.22.0', last_heartbeat: new Date().toISOString() },
  { node_id: 'offline', name: 'Nightly jobs', status: 'down', tier: 'batch', last_heartbeat: '2026-11-01T12:00:00Z' },
  { node_id: 'revoked', name: 'Retired worker', status: 'up', revoked: true, revoked_at: '2026-11-02T12:00:00Z' },
  { node_id: 'joining', name: 'Analytics · 東京', status: 'joining', last_heartbeat: '2026-11-03 12:00:00' },
];
let dom, mounted, poll, savedInterval, savedClear;
const get = (id) => document.getElementById(`workers-${id}`);
const response = (data, status = 200) => ({ status, ok: status === 200, json: async () => data });
function context(api) { return { api, state: { user: { id: 1, role: 'admin' }, sessionIdentity: {} }, onUnauthorized() {}, updateActiveNav() {}, navigate() {} }; }
async function settle(predicate) { for (let i = 0; i < 30; i++) { if (predicate()) return; await new Promise((r) => setImmediate(r)); } assert.fail('View did not settle'); }
function deferred() { let resolve; return { promise: new Promise((r) => { resolve = r; }), resolve: (v) => resolve(v) }; }
beforeEach(() => {
  dom = new JSDOM(html, { url: 'http://localhost/workers', pretendToBeVisual: true, runScripts: 'outside-only' });
  globalThis.document = dom.window.document; globalThis.location = dom.window.location;
  savedInterval = globalThis.setInterval; savedClear = globalThis.clearInterval;
  globalThis.setInterval = (callback) => { poll = callback; return 1; }; globalThis.clearInterval = () => {};
});
afterEach(() => {
  mounted?.unmount(); mounted = null; dom.window.close(); poll = null;
  globalThis.setInterval = savedInterval; globalThis.clearInterval = savedClear;
  delete globalThis.document; delete globalThis.location;
});

test('real registry statuses determine order; search covers identities, tiers and addresses', () => {
  assert.deepEqual(visibleWorkers(fixture).map((w) => w.id), ['offline', 'joining', 'online', 'revoked']);
  assert.equal(visibleWorkers(fixture, '東京')[0].id, 'joining');
  assert.equal(visibleWorkers(fixture, '127.0.0.1')[0].id, 'online');
  assert.equal(visibleWorkers(fixture, 'BATCH')[0].id, 'offline');
  assert.equal(visibleWorkers(fixture, '', 'up').length, 1, 'revoked workers never count as online');
});

test('heartbeat dates distinguish never received, invalid, recent, old, and clock skew', () => {
  const now = Date.parse('2026-11-03T12:00:00Z');
  assert.equal(workerHeartbeat('', now).label, 'Not received');
  assert.equal(workerHeartbeat('not a date', now).label, 'Not reported');
  assert.equal(workerHeartbeat('2026-11-03 11:59:55', now).iso, '2026-11-03T11:59:55.000Z');
  assert.equal(workerHeartbeat('2026-11-03T11:59:55', now).iso, '2026-11-03T11:59:55.000Z');
  assert.equal(workerHeartbeat('2026-11-03T12:59:55+01:00', now).iso, '2026-11-03T11:59:55.000Z');
  assert.equal(workerHeartbeat('2026-11-03T11:59:55Z', now).label, 'Just now');
  assert.equal(workerHeartbeat('2026-11-03T11:58:00Z', now).label, '2m ago');
  assert.equal(workerHeartbeat('2026-11-01T12:00:00Z', now).label, '2d ago');
  assert.equal(workerHeartbeat('2026-11-03T12:05:00Z', now).label, 'Clock ahead');
});

test('initial loading resolves to a truthful, searchable worker inventory and connection details', async () => {
  const pending = deferred(); mounted = mountWorkers(context(async () => pending.promise));
  assert.equal(get('loading').hidden, false); assert.equal(get('table').hidden, true);
  assert.equal(get('refresh').getAttribute('aria-disabled'), 'true');
  pending.resolve(response(fixture)); await settle(() => !get('table').hidden);
  assert.match(get('summary').textContent, /1 online1 offline1 joining1 revoked/);
  assert.equal(get('body').rows[0].querySelector('.worker-name').textContent, 'Nightly jobs');
  assert.match(get('body').textContent, /127.0.0.1:9443/);
  assert.match(get('body').textContent, /cannot connect/);
  assert.equal(get('body').querySelectorAll('.worker-heartbeat')[1].textContent, 'Not received');
  assert.equal(get('refresh').hasAttribute('aria-disabled'), false);
  get('search').value = '東京'; get('search').dispatchEvent(new dom.window.Event('input'));
  assert.equal(get('body').rows.length, 1); assert.equal(get('results').textContent, '1 of 4 workers');
});

test('no matches can be cleared without losing keyboard focus', async () => {
  mounted = mountWorkers(context(async () => response(fixture))); await settle(() => !get('table').hidden);
  get('status-filter').value = 'unknown'; get('status-filter').dispatchEvent(new dom.window.Event('change'));
  assert.equal(get('table').hidden, true); assert.equal(get('no-matches').hidden, false);
  get('clear').click(); assert.equal(get('body').rows.length, 4); assert.equal(document.activeElement, get('search'));
});

test('poll preserves open connection details and focus, then falls back safely if worker disappears', async () => {
  let rows = fixture;
  mounted = mountWorkers(context(async () => response(rows))); await settle(() => !get('table').hidden);
  const detail = get('body').querySelector('details'); detail.open = true; detail.querySelector('summary').focus();
  poll(); await settle(() => get('refresh').textContent === 'Refresh');
  assert.equal(get('body').querySelector('details').open, true);
  assert.equal(document.activeElement.dataset.focusKey, 'worker:offline:details');
  rows = fixture.filter((w) => w.node_id !== 'offline'); poll(); await settle(() => get('body').rows.length === 3);
  assert.equal(document.activeElement, get('search'));
});

test('failed refresh retains filters and inventory, announces staleness, and recovers', async () => {
  let failed = false;
  mounted = mountWorkers(context(async () => failed ? response(null, 503) : response(fixture)));
  await settle(() => !get('table').hidden);
  get('search').value = 'Nightly'; get('search').dispatchEvent(new dom.window.Event('input'));
  failed = true; get('refresh').focus(); get('refresh').click(); await settle(() => !get('error').hidden);
  assert.match(get('error').textContent, /last loaded list/); assert.equal(get('body').rows.length, 1);
  assert.equal(document.activeElement, get('refresh')); assert.equal(get('search').value, 'Nightly');
  failed = false; get('refresh').click(); await settle(() => get('error').hidden);
  assert.match(get('live').textContent, /Workers refreshed/);
});

test('empty inventory teaches setup while initial failure exposes a retry', async () => {
  let failed = true;
  mounted = mountWorkers(context(async () => failed ? response(null, 500) : response([])));
  await settle(() => !get('error').hidden); assert.equal(get('empty').hidden, true);
  failed = false; get('refresh').click(); await settle(() => !get('empty').hidden);
  assert.equal(get('filters').hidden, true); assert.equal(get('table').hidden, true);
  assert.match(get('empty').textContent, /Apps can run on this hub/);
});

test('forbidden responses remove previously visible worker data', async () => {
  let forbidden = false;
  mounted = mountWorkers(context(async () => forbidden ? response(null, 403) : response(fixture)));
  await settle(() => !get('table').hidden); forbidden = true; poll();
  await settle(() => !get('error').hidden);
  assert.equal(get('body').children.length, 0); assert.equal(get('filters').hidden, true);
  assert.equal(get('summary').hidden, true); assert.match(get('error').textContent, /Administrator access/);
});

test('unauthorized responses invoke the session handler and stop polling', async () => {
  let calls = 0, handled = 0;
  const ctx = context(async () => { calls++; return response(null, 401); }); ctx.onUnauthorized = () => handled++;
  mounted = mountWorkers(ctx); await settle(() => handled === 1);
  poll(); await new Promise((r) => setImmediate(r)); assert.equal(calls, 1);
});

test('overlapping requests are suppressed and unmount aborts requests and clears all private rows', async () => {
  let signal, calls = 0; const pending = deferred();
  mounted = mountWorkers(context(async (_, options) => { calls++; signal = options.signal; return pending.promise; }));
  get('refresh').click(); poll(); assert.equal(calls, 1);
  mounted.unmount(); mounted = null; assert.equal(signal.aborted, true);
  pending.resolve(response(fixture)); await new Promise((r) => setImmediate(r));
  assert.equal(get('body').children.length, 0); assert.equal(get('view').hidden, true);
});

test('late responses from an earlier session do not paint the new session', async () => {
  const pending = deferred(), ctx = context(async () => pending.promise);
  mounted = mountWorkers(ctx); ctx.state.sessionIdentity = {}; ctx.state.user = { id: 2, role: 'admin' };
  pending.resolve(response(fixture)); await new Promise((r) => setImmediate(r));
  assert.equal(get('body').children.length, 0);
});

for (const malformed of [{ items: fixture }, [null], [{ status: 'up' }]]) test(`malformed registry response does not become an empty healthy list: ${JSON.stringify(malformed)}`, async () => {
  mounted = mountWorkers(context(async () => response(malformed))); await settle(() => !get('error').hidden);
  assert.equal(get('empty').hidden, true); assert.equal(get('table').hidden, true);
});


test('heartbeat ages advance during a pending request and after failure without rebuilding focused details', async () => {
  const originalNow = Date.now;
  let now = Date.parse('2026-11-03T12:00:00Z');
  Date.now = () => now;
  try {
    const held = deferred(); let calls = 0;
    mounted = mountWorkers(context(async () => ++calls === 1
      ? response([{ ...fixture[0], last_heartbeat: '2026-11-03 12:00:00' }]) : held.promise));
    await settle(() => !get('table').hidden);
    const detail = get('body').querySelector('details'); detail.open = true;
    const toggle = detail.querySelector('summary'); toggle.focus();
    const heartbeat = get('body').querySelector('.worker-heartbeat');
    assert.equal(heartbeat.textContent, 'Just now');
    now += 60000; poll();
    assert.equal(heartbeat.textContent, '1m ago');
    now += 60000; poll();
    assert.equal(calls, 2); assert.equal(heartbeat.textContent, '2m ago');
    assert.equal(document.activeElement, toggle); assert.equal(detail.open, true);
    held.resolve(response(null, 503)); await settle(() => !get('error').hidden);
    now += 60000; poll();
    assert.equal(heartbeat.textContent, '3m ago');
    assert.equal(get('updated').textContent, 'Last updated 3m ago');
    get('search').value = 'Reporting'; get('search').dispatchEvent(new dom.window.Event('input'));
    assert.equal(get('updated').textContent, 'Last updated 3m ago');
    assert.equal(get('error').hidden, false);
  } finally { Date.now = originalNow; }
});

test('capability downgrade immediately clears inventory, aborts pending refresh and redirects once', async () => {
  const held = deferred(); let calls = 0, signal;
  const ctx = context(async (_, options) => {
    signal = options.signal; return ++calls === 1 ? response(fixture) : held.promise;
  });
  const navigations = []; ctx.navigate = (...args) => navigations.push(args);
  mounted = mountWorkers(ctx); await settle(() => !get('table').hidden);
  poll(); ctx.state.user = { id: 1, role: 'viewer' };
  document.dispatchEvent(new dom.window.Event('session-capabilities-change'));
  assert.equal(signal.aborted, true); assert.equal(get('view').hidden, true);
  assert.equal(get('body').children.length, 0); assert.equal(get('summary').textContent, '');
  assert.deepEqual(navigations, [['/apps', { replace: true }]]);
  held.resolve(response(fixture)); await new Promise((r) => setImmediate(r)); poll();
  assert.equal(get('body').children.length, 0); assert.equal(calls, 2); assert.equal(navigations.length, 1);
});

test('poll also clears inventory when access changes before a session notification', async () => {
  let calls = 0, destination;
  const ctx = context(async () => { calls++; return response(fixture); });
  ctx.navigate = (path) => { destination = path; };
  mounted = mountWorkers(ctx); await settle(() => !get('table').hidden);
  ctx.state.user.role = 'viewer'; poll();
  assert.equal(get('view').hidden, true); assert.equal(get('body').children.length, 0);
  assert.equal(calls, 1); assert.equal(destination, '/apps');
});

test('filtered inventory has a clear action and troubled connections link to guidance', async () => {
  mounted = mountWorkers(context(async () => response(fixture))); await settle(() => !get('table').hidden);
  assert.equal(get('reset').hidden, true);
  get('search').value = 'Nightly'; get('search').dispatchEvent(new dom.window.Event('input'));
  assert.equal(get('reset').hidden, false);
  const help = get('body').querySelector('.worker-help');
  assert.equal(help.href, 'https://shinyhub.dev/recipes/remote-worker/#troubleshooting');
  get('reset').click(); assert.equal(get('body').rows.length, 4);
  assert.equal(get('reset').hidden, true); assert.equal(document.activeElement, get('search'));
});


test('populated workers and expanded troubleshooting have no structural WCAG A/AA violations', async () => {
  mounted = mountWorkers(context(async () => response(fixture))); await settle(() => !get('table').hidden);
  get('body').querySelectorAll('details').forEach((detail) => { detail.open = true; });
  const { default: axe } = await import('axe-core');
  dom.window.HTMLCanvasElement.prototype.getContext = () => null;
  dom.window.eval(axe.source);
  const result = await dom.window.axe.run(get('view'), {
    runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'] },
    rules: { 'color-contrast': { enabled: false } },
  });
  assert.equal(result.violations.length, 0, JSON.stringify(result.violations));
});
