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

test('metrics retry reports pending, failure and recovery while retaining keyboard focus', async () => {
  let metricsFail = true;
  let appsFail = false;
  let pending = null;
  const app = { slug: 'forecast', name: 'Forecast', status: 'running' };
  const metrics = { host: { cores: 8, memory_mb: 65536 }, metrics: { forecast: { replicas: [{
    index: 0, status: 'running', metrics_available: true, cpu_percent: 10, rss_bytes: 128 * 1024 * 1024,
    effective_cpu_quota_percent: 0, effective_memory_limit_mb: 0, resource_enforcement_known: true,
  }] } } };
  mounted = mountOverview(context(async (path) => {
    if (path === '/api/apps' && appsFail) return { ok: false, status: 503 };
    if (path === '/api/apps/metrics' && pending) await pending.promise;
    if (path === '/api/apps/metrics' && metricsFail) return { ok: false, status: 503 };
    return { ok: true, status: 200, json: async () => path === '/api/apps' ? { items: [app] }
      : path === '/api/apps/metrics' ? metrics : { history: {} } };
  }));
  const retry = () => document.querySelector('[data-focus-key="usage:retry"]');
  await eventually(() => retry());
  retry().focus();
  pending = deferred();
  retry().click();
  assert.equal(retry().getAttribute('aria-disabled'), 'true');
  assert.match(retry().textContent, /Trying again/);
  assert.equal(document.querySelector('.ov-resources').getAttribute('aria-busy'), 'true');
  pending.resolve(); pending = null;
  await eventually(() => !retry().hasAttribute('aria-disabled'));
  assert.equal(document.activeElement, retry());
  assert.match(document.querySelector('#overview-live').textContent, /could not refresh/);
  pending = deferred();
  poll();
  retry().click();
  assert.match(retry().textContent, /Trying again/);
  pending.resolve(); pending = null;
  await eventually(() => !retry().hasAttribute('aria-disabled'));
  assert.match(document.querySelector('#overview-live').textContent, /could not refresh/);
  metricsFail = false;
  retry().click();
  await eventually(() => !retry());
  assert.equal(document.activeElement.id, 'ov-resources-title');
  assert.match(document.querySelector('#overview-live').textContent, /Resource readings refreshed/);
  assert.match(document.querySelector('.ov-resources').textContent, /64.0 GB/);
  metricsFail = true;
  poll();
  await eventually(() => retry());
  assert.match(document.querySelector('.ov-resources').textContent, /Last reported app usage/);
  assert.match(document.querySelector('.ov-resources').textContent, /128 MB/);
  assert.match(document.querySelector('#overview-live').textContent, /stale/);
  metricsFail = false;
  poll();
  await eventually(() => !retry());
  assert.doesNotMatch(document.querySelector('.ov-resources').textContent, /Last reported app usage/);
  // A broader outage during resource retry must not leave its button inert.
  metricsFail = true;
  poll();
  await eventually(() => retry());
  appsFail = true;
  retry().focus(); retry().click();
  await eventually(() => document.querySelector('.ov-refresh-warning'));
  const overallRetry = document.querySelector('[data-focus-key="overview:retry"]');
  assert.equal(retry(), null, 'a broader outage has one retry action');
  assert.equal(document.activeElement, overallRetry);
  appsFail = false; metricsFail = false;
  overallRetry.click();
  await eventually(() => !document.querySelector('.ov-refresh-warning'));
  assert.equal(document.querySelector('.ov-refresh-warning'), null);
  assert.equal(document.activeElement.dataset.focusKey, 'overview:status');
});

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

  await eventually(() => document.getElementById('overview-body').textContent.includes('App resource usage'));
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

  await eventually(() => document.getElementById('overview-body').textContent.includes('App resource usage'));
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
    const data = path === '/api/apps' ? { items: [app, { slug: 'outside', status: 'hibernated' }] } : path.endsWith('/history') ? { history: {} } : { host: { cores: 4, memory_mb: 8192 }, metrics: { a: { replicas: [{ status: 'running', metrics_available: true, cpu_percent: 10, rss_bytes: 1024, effective_cpu_quota_percent: 0, effective_memory_limit_mb: 0 }] } } };
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
  assert.equal(document.querySelector('.ov-refresh-warning button').getAttribute('aria-disabled'), 'true');
  assert.equal(document.querySelector('.ov-refresh-warning button').textContent, 'Trying again…');
  assert.equal(document.querySelector('.ov-refresh-warning').getAttribute('aria-busy'), 'true');
  retryResponse.resolve({ ok: false, status: 500 });
  await eventually(() => !document.querySelector('.ov-refresh-warning button').hasAttribute('aria-disabled'));
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
    const data = path === '/api/apps' ? { items: [{ slug: 'a', deploy_count: 1, status: healthy ? 'hibernated' : 'crashed' }] } : path.endsWith('/history') ? { history: {} } : { metrics: {} };
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
  assert.equal(document.activeElement.dataset.focusKey, 'overview:status');
});

test('critical resource verdict leads to pressure alerts before secondary coverage gaps', async () => {
  const apps = [{ slug: 'hot', status: 'running' }, { slug: 'free', status: 'running' }];
  const sample = { index: 0, status: 'running', metrics_available: true, rss_bytes: 500 * 1024 * 1024, cpu_percent: 10, effective_memory_limit_mb: 512, effective_cpu_quota_percent: 100, memory_limit_enforced: true, cpu_quota_enforced: true, resource_enforcement_known: true };
  mounted = mountOverview(context(async (path) => ({ ok: true, status: 200, json: async () => path === '/api/apps'
    ? { items: apps } : path.endsWith('/history') ? { history: {} }
      : { metrics: { hot: { replicas: [sample] }, free: { replicas: [{ ...sample, effective_memory_limit_mb: 0, metrics_available: false }] } } },
  })));
  await eventually(() => document.querySelector('[data-focus-key="pressure:review"]'));
  const actions = [...document.querySelectorAll('.ov-pulse-actions button')];
  assert.equal(actions[0].textContent, 'Review pressure alerts');
  assert.equal(actions[1].textContent, 'Review resource coverage');
  const target = document.getElementById('ov-pressure-title');
  let scrolled = false;
  target.scrollIntoView = () => { scrolled = true; };
  actions[0].click();
  assert.equal(document.activeElement, target);
  assert.equal(scrolled, true);
  apps.push({ slug: 'reports', name: 'Weekly reports', status: 'crashed' });
  poll();
  await eventually(() => document.querySelector('[data-focus-key="attention:reports:open"]'));
  const mixedActions = [...document.querySelectorAll('.ov-pulse-actions button')];
  assert.deepEqual(mixedActions.map((button) => button.textContent), [
    'Review pressure alerts', 'Review resource coverage',
  ]);
  const incident = document.querySelector('.ov-pulse .ov-attention');
  assert.match(incident.textContent, /Weekly reports/);
  const inspect = incident.querySelector('.ov-attn-actions a');
  assert.equal(inspect.textContent, 'View logs');
  assert.equal(inspect.getAttribute('href'), '/apps/reports/logs');
  assert.equal(document.querySelectorAll('.ov-attention').length, 1);
  assert.equal(mixedActions[0].classList.contains('ov-primary-response'), true);
  document.getElementById('ov-pressure-title').scrollIntoView = () => {};
  mixedActions[0].click();
  assert.equal(document.activeElement.id, 'ov-pressure-title');
});

test('critical total usage and replica pressure each have a correctly ordered jump action', async () => {
  const apps = [{ slug: 'hot', status: 'running' }, { slug: 'free', status: 'running' }];
  const sample = { index: 0, status: 'running', metrics_available: true, rss_bytes: 500 * 1024 * 1024, cpu_percent: 10, effective_memory_limit_mb: 512, effective_cpu_quota_percent: 100, memory_limit_enforced: true, cpu_quota_enforced: true, resource_enforcement_known: true };
  mounted = mountOverview(context(async (path) => ({ ok: true, status: 200, json: async () => path === '/api/apps'
    ? { items: apps } : path.endsWith('/history') ? { history: {} }
      : { host: { cores: 4, memory_mb: 1024 }, metrics: { hot: { replicas: [sample] }, free: { replicas: [{ ...sample, effective_memory_limit_mb: 0 }] } } },
  })));
  await eventually(() => document.querySelector('[data-focus-key="usage:review"]'));
  assert.match(document.querySelector('.ov-pulse-headline').textContent, /App usage is nearing available capacity/);
  const actions = [...document.querySelectorAll('.ov-pulse-actions button')];
  assert.deepEqual(actions.map((button) => button.textContent), ['Review app usage', 'Review pressure alerts']);
  const usage = document.getElementById('ov-resources-title');
  usage.scrollIntoView = () => {};
  actions[0].click();
  assert.equal(document.activeElement, usage);
  assert.equal(document.querySelector('[data-focus-key="coverage:review"]'), null, 'optional gaps do not become a warning action');
  assert.match(document.querySelector('.ov-coverage').textContent, /All replicas are reporting. Setting app limits is optional/);
});


test('first deployment failures lead to deployments without an invalid restart action', async () => {
  const ctx = context(async (path) => ({ ok: true, status: 200, json: async () => path === '/api/apps'
    ? { items: [{ slug: 'first', status: 'stopped', deploy_count: 0, last_deployment_status: 'failed' }] }
    : path.endsWith('/history') ? { history: {} } : { metrics: {} },
  }));
  ctx.canManageApp = () => true;
  mounted = mountOverview(ctx);
  await eventually(() => document.querySelector('.ov-attn-actions a'));
  assert.equal(document.querySelector('.ov-attn-actions a').getAttribute('href'), '/apps/first/deployments');
  assert.equal(document.querySelector('.ov-attn-actions button'), null);
});

for (const kind of ['error', 'network-error']) test(`${kind} restart feedback remains visible in its incident row across polling`, async () => {
  let appCalls = 0;
  const ctx = context(async (path) => {
    if (path === '/api/apps') appCalls++;
    return { ok: true, status: 200, json: async () => path === '/api/apps'
      ? { items: [{ slug: 'crash', status: 'crashed', deploy_count: 1 }] }
      : path.endsWith('/history') ? { history: {} } : { metrics: {} } };
  });
  ctx.canManageApp = () => true;
  ctx.restart = async () => ({ kind, message: 'Could not restart this app.' });
  mounted = mountOverview(ctx);
  await eventually(() => document.querySelector('.ov-attn-actions button'));
  document.querySelector('.ov-attn-actions button').click();
  await eventually(() => document.querySelector('.ov-attn-error'));
  assert.match(document.querySelector('.ov-attn-error').textContent, /Could not restart/);
  const before = appCalls; poll();
  await eventually(() => appCalls > before);
  assert.match(document.querySelector('.ov-attn-error').textContent, /Could not restart/);
  assert.equal(document.querySelector('.ov-attn-actions button').disabled, false);
});

test('malformed app responses retain the last snapshot and expose a retry', async () => {
  let malformed = false;
  mounted = mountOverview(context(async (path) => ({ ok: true, status: 200, json: async () => path === '/api/apps'
    ? malformed ? { message: 'unexpected response' } : { items: [{ slug: 'known', status: 'hibernated' }] }
    : path.endsWith('/history') ? { history: {} } : { metrics: {} },
  })));
  await eventually(() => document.querySelector('.ov-pulse'));
  malformed = true; poll();
  await eventually(() => document.querySelector('.ov-refresh-warning'));
  assert.match(document.querySelector('.ov-pulse').textContent, /Sleeping/);
  assert.equal(document.querySelector('.ov-firstrun'), null);
  assert.ok(document.querySelector('[data-focus-key="overview:retry"]'));
});


test('scope choices appear only when they narrow the app set and disappearing choices preserve focus', async () => {
  let apps = [{ slug: 'a', status: 'hibernated' }, { slug: 'b', status: 'hibernated' }];
  mounted = mountOverview(context(async (path) => ({ ok: true, status: 200, json: async () => path === '/api/apps'
    ? { items: apps } : path.endsWith('/history') ? { history: {} } : { metrics: {} },
  })));
  await eventually(() => document.querySelector('.ov-pulse'));
  assert.equal(document.querySelector('#ov-scope-select'), null, 'unmanaged and all apps are identical');
  apps = apps.map((app) => ({ ...app, managed_by: 'fleet:production' }));
  poll();
  await eventually(() => document.querySelector('.ov-fleets'));
  assert.equal(document.querySelector('#ov-scope-select'), null, 'one fleet covering all apps is also redundant');
  apps = [apps[0], { ...apps[1], managed_by: null }];
  poll();
  await eventually(() => document.querySelector('#ov-scope-select'));
  const select = document.querySelector('#ov-scope-select');
  select.focus(); select.value = 'fleet:production';
  select.dispatchEvent(new window.Event('change'));
  assert.equal(document.querySelector('.ov-pulse').getAttribute('aria-label'), 'Fleet · production status');
  apps = apps.map((app) => ({ ...app, managed_by: null }));
  poll();
  await eventually(() => !document.querySelector('#ov-scope-select'));
  assert.equal(document.activeElement.dataset.focusKey, 'overview:status');
  assert.equal(document.querySelector('.ov-pulse').getAttribute('aria-label'), 'All apps status');
  assert.match(document.getElementById('overview-live').textContent, /selected fleet is no longer available/);
});

test('activity names update from the refreshed app snapshot without losing event focus', async () => {
  let name = 'Sales dashboard';
  const ctx = context(async (path) => ({ ok: true, status: 200, json: async () => path === '/api/apps'
    ? { items: [{ slug: 'sales', name, status: 'hibernated' }] }
    : path === '/api/audit?limit=12' ? { events: [{ id: 1, action: 'restart', resource_type: 'app', resource_id: 'sales', username: 'operator', created_at: '2026-11-03T14:30:00Z' }] }
      : path.endsWith('/history') ? { history: {} } : { metrics: {} },
  }));
  ctx.state.canReadAudit = true;
  mounted = mountOverview(ctx);
  await eventually(() => document.querySelector('.ov-activity-target')?.textContent === name);
  document.querySelector('.ov-activity-target').focus();
  name = 'Sales dashboard · 東京'; poll();
  await eventually(() => document.querySelector('.ov-activity-target')?.textContent === name);
  assert.equal(document.activeElement.textContent, name);
  assert.match(document.querySelector('.ov-activity-action-link').getAttribute('aria-label'), /Sales dashboard · 東京/);
});
