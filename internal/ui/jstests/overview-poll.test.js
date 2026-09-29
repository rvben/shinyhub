import { afterEach, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { apiWithTimeout, mountOverview } from '../static/views/overview.js';

let dom;
let poll;
let mounted;
let originalSetInterval;
let originalClearInterval;

beforeEach(() => {
  // pretendToBeVisual: jsdom otherwise defaults document.hidden to true, which
  // would make mountOverview's visibility-aware poll (startVisiblePoll) never
  // arm the stubbed setInterval below, leaving `poll` null.
  dom = new JSDOM('<!doctype html><body><main id="overview-view" hidden><div id="overview-body"></div></main></body>', {
    url: 'http://localhost/',
    pretendToBeVisual: true,
  });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  globalThis.location = dom.window.location;
  originalSetInterval = globalThis.setInterval;
  originalClearInterval = globalThis.clearInterval;
  globalThis.setInterval = (callback) => { poll = callback; return 1; };
  globalThis.clearInterval = () => {};
});

afterEach(() => {
  if (mounted) mounted.unmount();
  mounted = null;
  poll = null;
  globalThis.setInterval = originalSetInterval;
  globalThis.clearInterval = originalClearInterval;
  dom.window.close();
  delete globalThis.window;
  delete globalThis.document;
  delete globalThis.location;
});

function context(api) {
  return {
    api,
    state: { apps: [], canReadAudit: false, user: {} },
    updateActiveNav() {},
    syncSidebar() {},
    onUnauthorized() {},
    canManageApp() { return false; },
  };
}

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

async function eventually(predicate) {
  for (let i = 0; i < 50; i += 1) {
    if (predicate()) return;
    await new Promise((resolve) => globalThis.setTimeout(resolve, 0));
  }
  assert.fail('condition was not reached');
}

test('a slow Overview load cannot overlap with the next poll', async () => {
  const first = deferred();
  const calls = [];
  mounted = mountOverview(context((path) => {
    calls.push(path);
    return first.promise;
  }));
  assert.deepEqual(calls, ['/api/apps']);

  poll();
  await Promise.resolve();
  assert.deepEqual(calls, ['/api/apps']);

  first.resolve({ ok: true, status: 200, json: async () => ({ items: [] }) });
  await eventually(() => document.getElementById('overview-body').textContent.includes('Deploy your first'));
});

test('a request that never settles is released by the Overview deadline', async () => {
  await assert.rejects(
    apiWithTimeout(() => new Promise(() => {}), '/api/apps', 5),
    (error) => error && error.name === 'AbortError',
  );
});

test('the Overview deadline remains active while a response body is parsed', async () => {
  const controllers = new Set();
  await assert.rejects(
    apiWithTimeout(
      async () => ({ ok: true, json: () => new Promise(() => {}) }),
      '/api/apps',
      5,
      controllers,
      async (response) => ({ response, body: await response.json() }),
    ),
    (error) => error && error.name === 'AbortError',
  );
  assert.equal(controllers.size, 0);
});

test('fleet metrics requests do not encode every app slug into the URL', async () => {
  const calls = [];
  const app = { slug: 'forecast', name: 'Forecast', status: 'running', replicas: 1 };
  const replica = {
    index: 0,
    status: 'running',
    metrics_available: true,
    cpu_percent: 12,
    rss_bytes: 64 * 1024 * 1024,
    effective_memory_limit_mb: 512,
    effective_cpu_quota_percent: 100,
    memory_limit_enforced: true,
    cpu_quota_enforced: true,
    resource_enforcement_known: true,
  };
  mounted = mountOverview(context(async (path) => {
    calls.push(path);
    if (path === '/api/apps') return { ok: true, status: 200, json: async () => ({ items: [app] }) };
    if (path === '/api/apps/metrics') {
      return { ok: true, status: 200, json: async () => ({ generated_at: new Date().toISOString(), metrics: {
        forecast: { status: 'running', replicas: [replica] },
      } }) };
    }
    if (path === '/api/apps/metrics/history') {
      return { ok: true, status: 200, json: async () => ({ history: { forecast: {
        ts: [], cpu: [], rss: [], sessions: [], instances: [],
      } } }) };
    }
    throw new Error(`unexpected request: ${path}`);
  }));

  await eventually(() => document.getElementById('overview-body').textContent.includes('App allocation pressure'));
  assert.deepEqual(calls, ['/api/apps', '/api/apps/metrics', '/api/apps/metrics/history']);
  assert.equal(calls.some((path) => path.includes('?slugs=')), false);
});

test('activity loads independently and never holds the health-first Overview skeleton', async () => {
  const audit = deferred();
  const app = { slug: 'forecast', name: 'Forecast', status: 'running', replicas: 1 };
  const replica = {
    index: 0,
    status: 'running',
    metrics_available: true,
    cpu_percent: 12,
    rss_bytes: 64 * 1024 * 1024,
    effective_memory_limit_mb: 512,
    effective_cpu_quota_percent: 100,
    memory_limit_enforced: true,
    cpu_quota_enforced: true,
    resource_enforcement_known: true,
  };
  const ctx = context(async (path) => {
    if (path === '/api/apps') return { ok: true, status: 200, json: async () => ({ items: [app] }) };
    if (path === '/api/apps/metrics') {
      return { ok: true, status: 200, json: async () => ({ metrics: { forecast: { status: 'running', replicas: [replica] } } }) };
    }
    if (path === '/api/apps/metrics/history') return { ok: true, status: 200, json: async () => ({ history: {} }) };
    if (path === '/api/audit?limit=12') return audit.promise;
    throw new Error(`unexpected request: ${path}`);
  });
  ctx.state.canReadAudit = true;
  mounted = mountOverview(ctx);

  await eventually(() => document.getElementById('overview-body').textContent.includes('App allocation pressure'));
  assert.equal(document.querySelector('.ov-activity').getAttribute('aria-busy'), 'true');
  assert.match(document.getElementById('overview-body').textContent, /Recent changes/);

  audit.resolve({
    ok: true,
    status: 200,
    json: async () => ({ events: [{
      id: 1,
      action: 'deploy',
      resource_type: 'app',
      resource_id: 'forecast',
      username: '__deploy__',
      created_at: new Date().toISOString(),
    }] }),
  });
  await eventually(() => document.getElementById('overview-body').textContent.includes('Deployment automation'));
  assert.equal(document.querySelector('.ov-activity').getAttribute('aria-busy'), null);
});

test('activity request failure renders an unavailable state instead of false emptiness', async () => {
  const app = { slug: 'forecast', name: 'Forecast', status: 'running', replicas: 1 };
  const ctx = context(async (path) => {
    if (path === '/api/apps') return { ok: true, status: 200, json: async () => ({ items: [app] }) };
    if (path === '/api/apps/metrics') return { ok: true, status: 200, json: async () => ({ metrics: {} }) };
    if (path === '/api/apps/metrics/history') return { ok: true, status: 200, json: async () => ({ history: {} }) };
    if (path === '/api/audit?limit=12') return { ok: false, status: 500, json: async () => ({}) };
    throw new Error(`unexpected request: ${path}`);
  });
  ctx.state.canReadAudit = true;
  mounted = mountOverview(ctx);

  await eventually(() => document.getElementById('overview-body').textContent.includes('Activity unavailable'));
  assert.doesNotMatch(document.getElementById('overview-body').textContent, /No changes recorded/);
  assert.match(document.getElementById('overview-body').textContent, /App health is shown above/);
});

test('activity retry stays busy in place and restores focus when it still fails', async () => {
  const retryResponse = deferred();
  const app = { slug: 'forecast', name: 'Forecast', status: 'running', replicas: 1 };
  let auditCalls = 0;
  const ctx = context(async (path) => {
    if (path === '/api/apps') return { ok: true, status: 200, json: async () => ({ items: [app] }) };
    if (path === '/api/apps/metrics') return { ok: true, status: 200, json: async () => ({ metrics: {} }) };
    if (path === '/api/apps/metrics/history') return { ok: true, status: 200, json: async () => ({ history: {} }) };
    if (path === '/api/audit?limit=12') {
      auditCalls += 1;
      if (auditCalls === 1) return { ok: false, status: 500, json: async () => ({}) };
      return retryResponse.promise;
    }
    throw new Error(`unexpected request: ${path}`);
  });
  ctx.state.canReadAudit = true;
  mounted = mountOverview(ctx);

  await eventually(() => document.querySelector('.ov-activity-retry'));
  const retry = document.querySelector('.ov-activity-retry');
  retry.focus();
  retry.click();
  assert.equal(retry.disabled, true);
  assert.equal(retry.textContent, 'Trying again…');
  assert.equal(document.querySelector('.ov-activity').getAttribute('aria-busy'), 'true');

  retryResponse.resolve({ ok: false, status: 500, json: async () => ({}) });
  await eventually(() => document.querySelector('.ov-activity-retry') !== retry);
  assert.equal(document.activeElement, document.querySelector('.ov-activity-retry'));
  assert.match(document.getElementById('overview-live').textContent, /still unavailable/);
});

test('successful activity retry moves focus to the stable heading and announces recovery', async () => {
  const retryResponse = deferred();
  const app = { slug: 'forecast', name: 'Forecast', status: 'running', replicas: 1 };
  let auditCalls = 0;
  const ctx = context(async (path) => {
    if (path === '/api/apps') return { ok: true, status: 200, json: async () => ({ items: [app] }) };
    if (path === '/api/apps/metrics') return { ok: true, status: 200, json: async () => ({ metrics: {} }) };
    if (path === '/api/apps/metrics/history') return { ok: true, status: 200, json: async () => ({ history: {} }) };
    if (path === '/api/audit?limit=12') {
      auditCalls += 1;
      if (auditCalls === 1) return { ok: false, status: 500, json: async () => ({}) };
      return retryResponse.promise;
    }
    throw new Error(`unexpected request: ${path}`);
  });
  ctx.state.canReadAudit = true;
  mounted = mountOverview(ctx);

  await eventually(() => document.querySelector('.ov-activity-retry'));
  document.querySelector('.ov-activity-retry').focus();
  document.querySelector('.ov-activity-retry').click();
  retryResponse.resolve({
    ok: true,
    status: 200,
    json: async () => ({ events: [{
      id: 1,
      action: 'deploy',
      resource_type: 'app',
      resource_id: 'forecast',
      username: '__deploy__',
      created_at: new Date().toISOString(),
    }] }),
  });

  await eventually(() => document.querySelector('.ov-activity-action-link'));
  assert.equal(document.activeElement, document.getElementById('ov-activity-title'));
  assert.equal(document.getElementById('overview-live').textContent, 'Recent changes updated.');
});

test('failed app polls retain a visibly stale scoped snapshot and retry restores freshness', async () => {
  let fail = false;
  let retryResponse = null;
  const app = { slug: 'a', status: 'running', managed_by: 'fleet:prod' };
  mounted = mountOverview(context(async (path) => {
    if (path === '/api/apps' && retryResponse) return retryResponse.promise;
    if (path === '/api/apps' && fail) return { ok: false, status: 500 };
    const data = path === '/api/apps' ? { items: [app] } : path.endsWith('/history') ? { history: {} } : { host: { cores: 4, memory_mb: 8192 }, metrics: { a: { replicas: [{ status: 'running', metrics_available: true, cpu_percent: 10, rss_bytes: 1024, effective_cpu_quota_percent: 0, effective_memory_limit_mb: 0 }] } } };
    return { ok: true, status: 200, json: async () => data };
  }));
  await eventually(() => document.querySelector('#ov-scope-select'));
  const select = document.querySelector('#ov-scope-select');
  select.value = 'fleet:prod'; select.dispatchEvent(new window.Event('change'));
  fail = true; poll();
  await eventually(() => document.querySelector('.ov-refresh-warning'));
  assert.equal(document.querySelector('#ov-scope-select').value, 'fleet:prod');
  assert.match(document.querySelector('.ov-pulse').textContent, /Overview data is stale/);
  assert.ok(document.querySelector('.ov-refresh-warning time').dateTime);
  assert.match(document.getElementById('overview-live').textContent, /could not refresh/);
  retryResponse = deferred();
  document.querySelector('.ov-refresh-warning button').focus();
  document.querySelector('.ov-refresh-warning button').click();
  assert.equal(document.querySelector('.ov-refresh-warning button').disabled, true);
  assert.equal(document.querySelector('.ov-refresh-warning button').textContent, 'Trying again…');
  assert.equal(document.querySelector('.ov-refresh-warning').getAttribute('aria-busy'), 'true');
  retryResponse.resolve({ ok: false, status: 500 });
  await eventually(() => !document.querySelector('.ov-refresh-warning button').disabled);
  assert.match(document.getElementById('overview-live').textContent, /could not refresh/);
  assert.equal(document.activeElement.dataset.focusKey, 'overview:retry');
  retryResponse = null;
  fail = false;
  document.querySelector('.ov-refresh-warning button').focus();
  document.querySelector('.ov-refresh-warning button').click();
  await eventually(() => !document.querySelector('.ov-refresh-warning'));
  assert.equal(document.activeElement.id, 'ov-scope-select');
  assert.match(document.getElementById('overview-live').textContent, /current again/);
});

test('attention focus and pending restarts survive polling, with a stable fallback after recovery', async () => {
  const restart = deferred();
  let healthy = false;
  let appCalls = 0;
  let restartCalls = 0;
  const ctx = context(async (path) => {
    if (path === '/api/apps') appCalls += 1;
    const data = path === '/api/apps' ? { items: [{ slug: 'a', status: healthy ? 'hibernated' : 'crashed' }] } : path.endsWith('/history') ? { history: {} } : { metrics: {} };
    return { ok: true, status: 200, json: async () => data };
  });
  ctx.canManageApp = () => true;
  ctx.restart = () => { restartCalls += 1; return restart.promise; };
  mounted = mountOverview(ctx);
  await eventually(() => document.querySelector('.ov-attn-name'));
  for (const key of ['name', 'open']) {
    document.querySelector(`[data-focus-key="attention:a:${key}"]`).focus();
    const before = appCalls; poll();
    await eventually(() => appCalls > before && document.activeElement.dataset.focusKey === `attention:a:${key}`);
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(document.activeElement.dataset.focusKey, `attention:a:${key}`);
  }
  document.querySelector('[data-focus-key="attention:a:restart"]').click();
  await eventually(() => restartCalls === 1);
  const oldButton = document.querySelector('[data-focus-key="attention:a:restart"]');
  poll();
  await eventually(() => document.querySelector('[data-focus-key="attention:a:restart"]') !== oldButton);
  const nextButton = document.querySelector('[data-focus-key="attention:a:restart"]');
  assert.equal(nextButton.disabled, true);
  assert.equal(nextButton.textContent, 'Restarting…');
  nextButton.click(); assert.equal(restartCalls, 1);
  document.querySelector('.ov-attn-name').focus();
  healthy = true; restart.resolve();
  await eventually(() => !document.querySelector('.ov-attention'));
  assert.equal(document.activeElement.id, 'ov-scope-select');
});
