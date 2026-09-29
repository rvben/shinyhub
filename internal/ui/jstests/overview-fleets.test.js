import { afterEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { buildOverviewWorkspace } from '../static/views/overview-model.js';
import { mountOverview, renderResourcePressure } from '../static/views/overview.js';

const MIB = 1024 * 1024;
const apps = [
  { slug: 'a', name: 'Alpha', status: 'running', managed_by: 'fleet:prod' },
  { slug: 'b', name: 'Beta', status: 'running', managed_by: 'fleet:prod' },
  { slug: 'c', name: 'Gamma', status: 'crashed', managed_by: 'fleet:staging' },
  { slug: 'd', name: 'Delta', status: 'running' },
];
const replica = (rss) => ({ index: 0, status: 'running', metrics_available: true, cpu_percent: 10, rss_bytes: rss * MIB, effective_memory_limit_mb: 0, effective_cpu_quota_percent: 0 });
const metrics = { state: 'ready', host: { cores: 4, memory_mb: 8192 }, metrics: {
  a: { replicas: [replica(300)] }, b: { replicas: [replica(100)] },
  d: { replicas: [replica(600)] },
} };
let dom, mounted, originalSetInterval, originalClearInterval;
afterEach(() => {
  mounted?.unmount(); mounted = null;
  if (originalSetInterval) globalThis.setInterval = originalSetInterval;
  if (originalClearInterval) globalThis.clearInterval = originalClearInterval;
  originalSetInterval = null; originalClearInterval = null;
  dom?.window.close(); dom = null;
  delete globalThis.document; delete globalThis.window; delete globalThis.location;
});
function setup() {
  dom = new JSDOM('<!doctype html><body><main id="overview-view"><div id="overview-body"></div></main></body>', { url: 'http://localhost/', pretendToBeVisual: true });
  globalThis.document = dom.window.document;
  globalThis.window = dom.window;
  globalThis.location = dom.window.location;
}
async function eventually(predicate) {
  for (let i = 0; i < 50; i += 1) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  assert.fail('overview did not settle');
}

test('named fleet selection recomputes health, usage, app shares and limit capacity', () => {
  const hub = buildOverviewWorkspace(apps, metrics);
  assert.equal(hub.fleetCount, 2);
  assert.deepEqual(hub.scopes.map((s) => [s.key, s.total]), [['all', 4], ['fleet:prod', 2], ['fleet:staging', 1], ['unmanaged', 1]]);
  assert.equal(hub.model.resources.memory.observedUsed, 1000 * MIB);
  assert.equal(hub.model.resources.topConsumers.items.find((i) => i.slug === 'a').fraction, 0.3);
  const prod = buildOverviewWorkspace(apps, metrics, {}, 'fleet:prod');
  assert.equal(prod.model.attention.length, 0);
  assert.equal(prod.model.total, 2);
  assert.equal(prod.model.resources.memory.used, 400 * MIB);
  assert.equal(prod.model.resources.memory.capacity, 8192 * MIB, 'shared host capacity is not apportioned between named fleets');
  assert.equal(prod.model.resources.topConsumers.items[0].fraction, 0.75);
  assert.equal(prod.model.resources.topConsumers.items[0].fleetID, 'prod');
  assert.equal(prod.groups.length, 0);
  const limited = structuredClone(metrics);
  for (const slug of ['a', 'b', 'd']) Object.assign(limited.metrics[slug].replicas[0], { effective_memory_limit_mb: 1024, memory_limit_enforced: true, resource_enforcement_known: true });
  assert.equal(buildOverviewWorkspace(apps, limited, {}, 'fleet:prod').model.resources.memory.capacity, 2048 * MIB);
});

test('unmanaged apps and fleet names matching reserved keys remain distinct; deleted selections fall back to hub', () => {
  const list = [{ slug: 'a', managed_by: 'fleet:all' }, { slug: 'b', managed_by: 'fleet:unmanaged' }, { slug: 'c' }];
  assert.equal(buildOverviewWorkspace(list, {}, {}, 'fleet:all').model.total, 1);
  assert.equal(buildOverviewWorkspace(list, {}, {}, 'unmanaged').model.total, 1);
  assert.equal(buildOverviewWorkspace(list, {}, {}, 'fleet:gone').selectedKey, 'all');
  assert.equal(buildOverviewWorkspace([], {}).fleetCount, 0);
});

test('fleet rendering identifies the share denominator and partial coverage without exposing other fleets', () => {
  setup();
  const prod = buildOverviewWorkspace(apps, metrics, {}, 'fleet:prod');
  const panel = renderResourcePressure(prod.model.resources);
  assert.match(panel.textContent, /Fleet · prod/);
  assert.match(panel.textContent, /75% of measured usage/);
  assert.match(panel.textContent, /shared host capacity/);
  assert.doesNotMatch(panel.textContent, /Delta|staging|of fleet/);
  assert.match(panel.querySelector('.ov-consumer-share').getAttribute('aria-label'), /measured memory usage in Fleet · prod/);
  const partial = structuredClone(metrics);
  partial.metrics.b.replicas.push({ ...replica(100), index: 1, metrics_available: false });
  const incomplete = renderResourcePressure(buildOverviewWorkspace(apps, partial, {}, 'fleet:prod').model.resources);
  assert.match(incomplete.textContent, /Partial reporting/);
  assert.match(incomplete.textContent, /2 of 3 running replicas reporting/);
});

test('changing scope updates the mounted view immediately, preserves focus and keeps sidebar apps global', async () => {
  setup();
  const calls = [];
  let servedApps = apps;
  let servedMetrics = metrics;
  let poll;
  originalSetInterval = globalThis.setInterval; originalClearInterval = globalThis.clearInterval;
  globalThis.setInterval = (callback) => { poll = callback; return 1; };
  globalThis.clearInterval = () => {};
  const ctx = {
    state: { apps: [], user: {}, canReadAudit: true },
    updateActiveNav() {}, syncSidebar() {}, onUnauthorized() {}, canManageApp() { return false; },
    async api(path) {
      calls.push(path);
      let body;
      if (path === '/api/apps') body = { items: servedApps };
      else if (path === '/api/apps/metrics') body = servedMetrics;
      else if (path === '/api/apps/metrics/history') body = { history: {} };
      else if (path === '/api/audit?limit=12') body = { events: [] };
      else throw new Error(`unexpected request ${path}`);
      return { ok: true, status: 200, json: async () => body };
    },
  };
  mounted = mountOverview(ctx);
  await eventually(() => document.querySelector('#ov-scope-select'));
  assert.equal(document.querySelectorAll('.ov-fleet-row').length, 3);
  let scrolled = false;
  dom.window.Element.prototype.scrollIntoView = function () { scrolled = this.classList.contains('ov-scope'); };
  document.querySelector('[data-focus-key="overview:fleet:prod"]').click();
  assert.equal(scrolled, true);
  const back = document.querySelector('#ov-scope-select');
  back.value = 'all'; back.dispatchEvent(new dom.window.Event('change'));
  const before = calls.length;
  const select = document.querySelector('#ov-scope-select');
  select.focus(); select.value = 'fleet:prod';
  select.dispatchEvent(new dom.window.Event('change'));
  assert.equal(document.querySelector('#ov-scope-select').value, 'fleet:prod');
  assert.equal(document.activeElement.id, 'ov-scope-select');
  assert.equal(document.querySelector('.ov-attention'), null);
  assert.match(document.querySelector('.ov-resources').textContent, /75% of measured usage/);
  assert.match(document.querySelector('.ov-activity').textContent, /Across the hub/);
  assert.equal(ctx.state.apps.length, 4);
  assert.equal(calls.length, before, 'selection uses the existing batch snapshot');
  servedMetrics = structuredClone(metrics);
  servedMetrics.metrics.a.replicas[0].rss_bytes = 100 * MIB;
  poll();
  await eventually(() => document.querySelector('.ov-resources').textContent.includes('50% of measured usage'));
  assert.equal(document.querySelector('#ov-scope-select').value, 'fleet:prod');
  assert.equal(document.activeElement.id, 'ov-scope-select');
  servedApps = apps.filter((app) => app.managed_by !== 'fleet:prod');
  poll();
  await eventually(() => document.querySelector('#ov-scope-select').value === 'all');
  assert.equal(document.querySelectorAll('#ov-scope-select option').length, 3);
  assert.equal(ctx.state.apps.length, 2);
  assert.match(document.getElementById('overview-live').textContent, /selected fleet is no longer available/);
  assert.match(document.querySelector('.ov-grid').textContent, /Showing all apps/);
});


test('fleet trends use only the selected apps history', () => {
  const series = (early, late) => ({ ts: [0, 60, 120, 180, 240, 300], cpu: [early, early, early, late, late, late], rss: [0, 0, 0, 0, 0, 0] });
  const history = { historyAvailable: true, historyBySlug: { a: series(10, 10), b: series(10, 10), d: series(10, 100) } };
  assert.equal(buildOverviewWorkspace(apps, metrics, history).model.resources.cpu.trend.state, 'rising');
  assert.equal(buildOverviewWorkspace(apps, metrics, history, 'fleet:prod').model.resources.cpu.trend.state, 'steady');
});


test('a missing CPU sample is unavailable while the fleets memory remains measurable', () => {
  setup();
  const missingCPU = structuredClone(metrics);
  missingCPU.metrics.a.replicas[0].cpu_percent = null;
  missingCPU.metrics.b.replicas[0].cpu_percent = null;
  const res = buildOverviewWorkspace(apps, missingCPU, {}, 'fleet:prod').model.resources;
  assert.equal(res.cpu.fraction, null);
  const panel = renderResourcePressure(res);
  assert.match(panel.querySelector('.ov-capacity-row').textContent, /Unavailable/);
  assert.doesNotMatch(panel.querySelector('.ov-capacity-row').textContent, /0%|0 \/ 4/);
  assert.equal(panel.querySelector('.ov-capacity-row').querySelector('meter'), null);
  assert.equal(panel.querySelectorAll('meter').length, 1);
});
