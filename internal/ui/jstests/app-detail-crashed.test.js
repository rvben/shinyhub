import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM, VirtualConsole } from 'jsdom';
import axe from 'axe-core';
import { settingsRouteParams } from '../static/views/app-detail-nav.js';
import {
  awaitingFirstDeploy,
  firstDeployFailed,
  hasDeployAttempt,
  hasSucceededDeploy,
} from '../static/views/app-deploy-state.js';

// An app whose FIRST deploy crashed on startup used to render the brand-new-app
// onboarding card on Overview and "This app is awaiting its first deploy" on
// Logs, because both gated on `deploy_count === 0`. deploy_count only counts
// SUCCESSFUL deploys, so a crash-on-first-deploy app is indistinguishable from a
// never-deployed one by that counter - and the Deployments tab, which correctly
// says "deploy failed ... Check the app logs for the cause", pointed the
// operator straight at the tab that hid the traceback.
//
// These tests drive the REAL app-detail.js through mountAppDetail against the
// real index.html, so they assert what the operator actually sees rather than a
// re-implementation of the gate.

const VIEWS_DIR = new URL('../static/views/', import.meta.url);
const INDEX_HTML = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');

// app-detail.js is the one view module written with browser-absolute
// ('/static/views/...') specifiers, which Node cannot resolve. Point those at
// the same files on disk and import the result. Only module resolution is
// rewritten; every byte of the logic under test is the shipped byte.
const appDetailSource = readFileSync(new URL('app-detail.js', VIEWS_DIR), 'utf8');
if (!appDetailSource.includes('export function mountAppDetail')) {
  throw new Error('app-detail.js does not look like the app detail view');
}
const importable = appDetailSource.replaceAll("'/static/views/", `'${VIEWS_DIR.href}`);
// A rewrite that silently matched nothing would leave an unimportable module,
// so prove it changed the source before relying on it.
assert.notEqual(importable, appDetailSource, 'import specifier rewrite matched nothing');
const { mountAppDetail } = await import(
  'data:text/javascript;base64,' + Buffer.from(importable, 'utf8').toString('base64')
);

const flush = () => new Promise((r) => setTimeout(r, 0));

// The crashed fixture is the shape GET /api/apps/{slug} really returns for the
// app in the report: a failed first deploy. deploy_count is 0 because nothing
// succeeded; last_deployment_status is the newest deployments row's status and
// last_deployed_at is MAX(created_at) over every row regardless of status
// (internal/db/queries.go deploymentSummarySQL).
const CRASHED_APP = {
  slug: 'crash-app',
  name: 'Crash App',
  description: 'Explains intentional failures for release testing.',
  status: 'starting',
  deploy_count: 0,
  deploying: false,
  last_deployment_status: 'failed',
  last_deployed_at: '2026-09-06T10:00:00Z',
  last_error: 'RuntimeError: intentional crash on import for QA testing',
  replicas: 1,
  access: 'private',
};

// A genuinely brand-new app: created, never deployed. Every deployment-summary
// field is absent, which is the only state that earns the onboarding card.
const NEW_APP = {
  slug: 'fresh-app',
  name: 'Fresh App',
  status: 'stopped',
  deploy_count: 0,
  deploying: false,
  replicas: 1,
  access: 'private',
};

function envelopeFor(app, rejectsByReason) {
  return {
    app,
    replicas_status: [],
    can_manage: true,
    runtime_mode: 'native',
    rejects_by_reason: rejectsByReason,
  };
}

// mountDetail builds the real dashboard DOM, installs the browser globals the
// view modules read off the global scope, and mounts the detail view on the
// requested tab.
async function mountDetail({ app, tab = 'overview', section, rejectsByReason, detailEnvelope, detailResponse, beforeMount, historyFetch, canManage = true, deployments = [] }) {
  const params = section ? settingsRouteParams({slug:app.slug, section}) : {slug:app.slug, tab};
  const virtualConsole = new VirtualConsole();
  const dom = new JSDOM(INDEX_HTML, {
    url: `https://hub.example.com/apps/${encodeURIComponent(app.slug)}${section ? '/settings/' + section : tab === 'overview' ? '' : '/' + tab}`,
    pretendToBeVisual: true,
    runScripts: 'outside-only',
    virtualConsole,
  });
  const { window } = dom;
  const previous = {};
  const globals = {
    window,
    document: window.document,
    location: window.location,
    navigator: window.navigator,
    requestAnimationFrame: (fn) => window.setTimeout(fn, 0),
    cancelAnimationFrame: (id) => window.clearTimeout(id),
    CustomEvent: window.CustomEvent,
    Event: window.Event,
    Node: window.Node,
    HTMLElement: window.HTMLElement,
    getComputedStyle: window.getComputedStyle.bind(window),
    fetch: historyFetch || (async () => { throw new Error('unexpected bare fetch'); }),
  };
  for (const [key, value] of Object.entries(globals)) {
    previous[key] = Object.getOwnPropertyDescriptor(globalThis, key);
    Object.defineProperty(globalThis, key, { value, configurable: true, writable: true });
  }
  const restore = () => {
    for (const [key, descriptor] of Object.entries(previous)) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
    window.close();
  };

  const calls = [], detailApps = [], navigations = [], populates = {general:0, autoscale:0, access:0, environment:0};
  const jsonResponse = (body) => ({
    ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body),
  });
  const ctx = {
    state: { user: { username: 'admin', role: 'admin' }, sessionIdentity:'initial-session' },
    api: async (path) => {
      calls.push(path);
      if (path === `/api/apps/${app.slug}`) return detailResponse ? detailResponse() : jsonResponse(detailEnvelope ? detailEnvelope() : envelopeFor(app, rejectsByReason));
      if (path.split('?')[0] === `/api/apps/${app.slug}/deployments`) return jsonResponse({ items: deployments });
      // Every other endpoint the panels reach for answers with an empty list
      // envelope, which each renderer tolerates.
      return jsonResponse({ items: [], total: 0, sources: [], entries: [] });
    },
    canManageApp: (_user, current) => typeof canManage === 'function' ? canManage(current) : canManage,
    navigate: (path, options) => { calls.push(`navigate:${path}`); navigations.push({path, ...options}); },
    onUnauthorized: () => { calls.push('unauthorized'); },
    updateActiveNav: () => {},
    metrics: { setTargets: () => {} },
    setDetailApp: current => { detailApps.push(current); },
    setDetailEnvelope: () => {},
    openDeployModal: () => { calls.push('deploy-modal'); },
    restart: () => { calls.push('restart'); },
    start: () => { calls.push('start'); },
    flashToast: () => {},
    setSettingsSlug: () => {},
    populateGeneralTab: current => { populates.general++; window.document.getElementById('general-name').value = current.name; window.document.getElementById('resources-cpu').value = '1'; },
    populateAutoscaleTab: () => { populates.autoscale++; window.document.getElementById('autoscale-min').value = '1'; },
    populateAccessPanel: () => { populates.access++; window.document.getElementById('grant-username').value = ''; },
    refreshEnvList: () => { populates.environment++; },
    refreshDataTab: () => {},
    refreshMemberList: () => {},
    refreshGroupAccessList: () => {},
    loadSchedules: () => () => {},
    loadSharedData: async () => {},
  };

  const route = mountAppDetail(ctx);
  const mount = typeof route === 'function' ? route : route.mount;
  beforeMount?.(ctx);
  const mounted = await mount(params);
  await flush();
  return { dom, window, doc: window.document, calls, detailApps, navigations, populates, route, mounted, ctx, restore };
}

for (const stage of ['response', 'body']) {
  test(`an initial app ${stage} arriving after a session change cannot restore prior-session detail`, async () => {
    let release, ctx;
    const pending = new Promise(resolve => { release = resolve; });
    const loading = mountDetail({app:CRASHED_APP, section:'general', beforeMount:value => { ctx = value; },
      detailResponse:() => stage === 'response' ? pending : {ok:true, status:200, json:() => pending}});
    await flush();
    ctx.state.sessionIdentity = 'replacement-session';
    const body = envelopeFor(CRASHED_APP);
    release(stage === 'response' ? {ok:true, status:200, json:async () => body} : body);
    const h = await loading;
    try {
      assert.deepEqual(h.detailApps, [], 'a superseded session must not repopulate cached app identity');
      assert.equal(h.doc.getElementById('app-detail-view').hidden, true);
      assert.notEqual(h.doc.getElementById('app-detail-heading').textContent, CRASHED_APP.name);
      assert.deepEqual(h.populates, {general:0, autoscale:0, access:0, environment:0});
      assert.deepEqual(h.navigations, [], 'the old response must not route the replacement session');
    } finally { h.restore(); }
  });
}

test('settings updates recheck management permission after adopting a required fresh envelope', async () => {
  let current = envelopeFor(CRASHED_APP);
  const h = await mountDetail({app:CRASHED_APP, section:'resources', detailEnvelope:() => current, canManage:app => app.can_manage === true});
  try {
    h.ctx.state.user.role = 'developer';
    current = {...envelopeFor(CRASHED_APP), can_manage:false};
    h.doc.dispatchEvent(new h.window.CustomEvent('shinyhub:mutated'));
    await h.mounted.update(settingsRouteParams({slug:CRASHED_APP.slug, section:'access'}));
    assert.equal(h.calls.filter(path => path === '/api/apps/crash-app').length, 2);
    assert.deepEqual(h.navigations, [{path:'/apps/crash-app', replace:true}]);
    assert.equal(h.populates.access, 0, 'a cached permission must not initialize a newly forbidden form');
    assert.equal(h.doc.getElementById('detail-access-panel').hidden, true);
  } finally { h.restore(); }
});

test('background permission revocation redirects away from the active settings page', async () => {
  let current = envelopeFor(CRASHED_APP);
  const h = await mountDetail({app:CRASHED_APP, section:'access', detailEnvelope:() => current, canManage:app => app.can_manage === true});
  const realNow = Date.now;
  try {
    h.ctx.state.user.role = 'developer';
    current = {...envelopeFor(CRASHED_APP), can_manage:false};
    Date.now = () => realNow() + 31000;
    h.route.onLiveMetrics(CRASHED_APP.slug, {status:CRASHED_APP.status, replicas:[]});
    await flush(); await flush();
    assert.equal(h.calls.filter(path => path === '/api/apps/crash-app').length, 2);
    assert.deepEqual(h.navigations, [{path:'/apps/crash-app', replace:true}]);
    assert.equal(h.populates.access, 1, 'revocation must not repopulate the access form');
  } finally { Date.now = realNow; h.restore(); }
});

test('settings ignore file provenance while preserving ordinary editing and access forms', async () => {
  const envelope = { ...envelopeFor(CRASHED_APP), configuration_sources: {
    bundle_status:'available', fleet_status:'recorded', bundle_env_keys:['REGION'],
    fields:{cpu_quota_percent:{source:'bundle',current:'100',declared:'200',differs:true}},
  }, fleet_state:{status:'temporary_changes',changes:[{key:'visibility',fleet:'private',current:'public'}]} };
  const h = await mountDetail({app:CRASHED_APP, section:'general', detailEnvelope:() => envelope});
  try {
    const draft = h.doc.getElementById('general-description');
    draft.value = 'Keep this draft';
    for (const section of ['resources', 'environment', 'scaling', 'privacy', 'access', 'general']) {
      await h.mounted.update(settingsRouteParams({slug:CRASHED_APP.slug, section}));
      assert.equal(h.doc.querySelector('.configuration-field-source, .configuration-save-source, .configuration-env-source, .fleet-field-state, .fleet-state-context'), null);
      assert.equal(h.doc.getElementById('configuration-files'), null);
      assert.equal(draft.value, 'Keep this draft');
    }
  } finally { h.restore(); }
});

test('settings page updates initialize each shared form once and preserve configuration and access drafts', async () => {
  const h = await mountDetail({app:CRASHED_APP, section:'resources'});
  try {
    const doc = h.doc, panel = doc.getElementById('detail-configuration-panel');
    const title = doc.getElementById('general-name'), cpu = doc.getElementById('resources-cpu'), autoscale = doc.getElementById('autoscale-min');
    assert.equal(h.window.location.pathname, '/apps/crash-app/settings/resources');
    assert.equal(panel.dataset.settingsPage, 'resources');
    assert.deepEqual(h.populates, {general:1, autoscale:1, access:0, environment:1});
    title.value = 'Unsaved title'; cpu.value = '0.29'; autoscale.value = '3';
    for (const section of ['environment', 'scaling', 'privacy', 'general', 'resources']) {
      await h.mounted.update(settingsRouteParams({slug:CRASHED_APP.slug, section}));
      assert.equal(panel.dataset.settingsPage, section);
      assert.equal(doc.getElementById('general-name'), title); assert.equal(title.value, 'Unsaved title');
      assert.equal(doc.getElementById('resources-cpu'), cpu); assert.equal(cpu.value, '0.29');
      assert.equal(doc.getElementById('autoscale-min'), autoscale); assert.equal(autoscale.value, '3');
    }
    assert.deepEqual(h.populates, {general:1, autoscale:1, access:0, environment:1});
    await h.mounted.update(settingsRouteParams({slug:CRASHED_APP.slug, section:'access'}));
    const access = doc.getElementById('grant-username'); access.value = 'Pending member';
    assert.equal(doc.getElementById('detail-access-panel').hidden, false);
    assert.equal(panel.hidden, true);
    await h.mounted.update(settingsRouteParams({slug:CRASHED_APP.slug, section:'resources'}));
    assert.equal(title.value, 'Unsaved title'); assert.equal(cpu.value, '0.29'); assert.equal(autoscale.value, '3');
    await h.mounted.update(settingsRouteParams({slug:CRASHED_APP.slug, section:'access'}));
    assert.equal(doc.getElementById('grant-username'), access); assert.equal(access.value, 'Pending member');
    assert.deepEqual(h.populates, {general:1, autoscale:1, access:1, environment:1});
    assert.equal(h.calls.filter(path => path === '/api/apps/crash-app').length, 1, 'page changes reuse the current app envelope');
    await h.mounted.update({slug:CRASHED_APP.slug, tab:'overview'});
    await h.mounted.update(settingsRouteParams({slug:CRASHED_APP.slug, section:'access'}));
    assert.equal(access.value, '', 'returning after leaving settings reloads the access form baseline');
    assert.deepEqual(h.populates, {general:1, autoscale:1, access:2, environment:1});
    await h.mounted.update(settingsRouteParams({slug:CRASHED_APP.slug, section:'resources'}));
    assert.equal(title.value, CRASHED_APP.name); assert.equal(cpu.value, '1'); assert.equal(autoscale.value, '1');
    assert.deepEqual(h.populates, {general:2, autoscale:2, access:2, environment:2});
  } finally { h.restore(); }
});

test('a direct Access settings arrival initializes access before configuration without losing either draft', async () => {
  const h = await mountDetail({app:CRASHED_APP, section:'access'});
  try {
    const member = h.doc.getElementById('grant-username'); member.value = 'Pending member';
    assert.equal(h.window.location.pathname, '/apps/crash-app/settings/access');
    assert.equal(h.doc.getElementById('detail-access-panel').hidden, false);
    assert.deepEqual(h.populates, {general:0, autoscale:0, access:1, environment:0});
    await h.mounted.update(settingsRouteParams({slug:CRASHED_APP.slug, section:'general'}));
    const title = h.doc.getElementById('general-name'); title.value = 'Unsaved title';
    assert.equal(member.value, 'Pending member');
    await h.mounted.update(settingsRouteParams({slug:CRASHED_APP.slug, section:'access'}));
    assert.equal(member.value, 'Pending member'); assert.equal(title.value, 'Unsaved title');
    assert.deepEqual(h.populates, {general:1, autoscale:1, access:1, environment:1});
  } finally { h.restore(); }
});

test('Overview distinguishes failed history from collecting and can retry', async () => {
  let succeed = false;
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 1, last_deployment_status: 'succeeded' };
  const h = await mountDetail({ app, tab: 'overview', historyFetch: async () => {
    if (!succeed) throw new Error('offline');
    const now = Math.floor(Date.now() / 1000);
    return { ok: true, json: async () => ({ window_seconds: 3600, interval_seconds: 30,
      series: { ts: [now - 30, now], cpu: [5, 7], rss: [10, 12], sessions: [1, 2], instances: [1, 1] } }) };
  } });
  try {
    assert.match(h.doc.querySelector('#overview-trends').textContent, /Metrics history is unavailable/);
    succeed = true;
    const retry = h.doc.querySelector('.trends-load-state button');
    retry.focus();
    retry.click();
    assert.equal(retry.disabled, true);
    assert.equal(retry.textContent, 'Checking…');
    assert.equal(h.doc.querySelector('#overview-trends').getAttribute('aria-busy'), 'true');
    await flush();
    assert.equal(h.doc.querySelector('.trends-load-state'), null);
    assert.equal(h.doc.querySelectorAll('#overview-trends .trend-row').length, 4);
    assert.equal(h.doc.activeElement, h.doc.querySelector('#trends-heading'));
    assert.equal(h.doc.querySelector('.trends-refresh-status').textContent, 'Metrics history updated.');
    assert.equal(h.doc.querySelector('#overview-trends').hasAttribute('aria-busy'), false);
  } finally { h.restore(); }
});

test('history retry prevents duplicate requests and preserves focus after another failure', async () => {
  let rejectRetry;
  let requests = 0;
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 1, last_deployment_status: 'succeeded' };
  const h = await mountDetail({ app, tab: 'overview', historyFetch: () => {
    requests++;
    if (requests === 1) return Promise.reject(new Error('offline'));
    return new Promise((_resolve, reject) => { rejectRetry = reject; });
  } });
  try {
    const retry = h.doc.querySelector('.trends-load-state button');
    retry.focus();
    retry.click();
    retry.click();
    await flush();
    assert.equal(requests, 2);
    assert.equal(h.doc.querySelector('.trends-refresh-status').textContent, 'Checking metrics history…');
    rejectRetry(new Error('still offline'));
    await flush();
    const replacement = h.doc.querySelector('.trends-load-state button');
    assert.equal(h.doc.activeElement, replacement);
    assert.equal(replacement.disabled, false);
    assert.equal(replacement.textContent, 'Retry');
    assert.equal(h.doc.querySelector('#overview-trends').hasAttribute('aria-busy'), false);
  } finally { h.restore(); }
});

test('history retry respects navigation to another control while loading', async () => {
  let resolveRetry;
  let requests = 0;
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 1, last_deployment_status: 'succeeded' };
  const h = await mountDetail({ app, tab: 'overview', historyFetch: () => {
    if (++requests === 1) return Promise.reject(new Error('offline'));
    return new Promise(resolve => { resolveRetry = resolve; });
  } });
  try {
    const retry = h.doc.querySelector('.trends-load-state button');
    retry.focus();
    retry.click();
    await flush();
    const otherControl = h.doc.querySelector('#app-detail-open');
    otherControl.focus();
    const now = Math.floor(Date.now() / 1000);
    resolveRetry({ ok: true, json: async () => ({ window_seconds: 900, interval_seconds: 30,
      series: { ts: [now - 30, now], cpu: [5, 7], rss: [10, 12], sessions: [1, 2], instances: [1, 1] } }) });
    await flush();
    assert.equal(h.doc.activeElement, otherControl);
    assert.equal(h.doc.querySelector('.trends-refresh-status').textContent, 'Metrics history updated.');
  } finally { h.restore(); }
});

test('elastic Overview identifies workers in process capacity and health readings', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 1,
    last_deployment_status: 'succeeded', effective_worker_isolation: 'grouped', max_sessions_per_replica: 20 };
  const h = await mountDetail({ app, tab: 'overview' });
  try {
    assert.equal(h.doc.querySelector('#overview-replicas-heading').textContent, 'Workers');
    assert.match(h.doc.querySelector('#overview-replicas-cap').textContent, /sessions\/worker/);
    h.route.onLiveMetrics(app.slug, { status: 'running', worker_isolation: 'grouped',
      replicas: [{ status: 'running' }, { status: 'running' }] });
    assert.match(h.doc.querySelector('#overview-health-detail').textContent, /2 workers running/);
    h.route.onLiveMetrics(app.slug, { status: 'running', worker_isolation: 'grouped',
      replicas: [{ status: 'running' }, { status: 'starting' }] });
    assert.equal(h.doc.querySelector('#overview-health-action').textContent, 'Inspect workers');
    assert.match(h.doc.querySelector('#overview-health-detail').textContent, /1 of 2 workers running/);
  } finally { h.restore(); }
});

test('an older history response cannot overwrite a newer chart', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 1, last_deployment_status: 'succeeded' };
  const pending = [];
  const h = await mountDetail({ app, tab: 'overview',
    historyFetch: () => new Promise(resolve => pending.push(resolve)) });
  const realNow = Date.now;
  const response = cpu => ({ ok: true, json: async () => ({ window_seconds: 3600, interval_seconds: 30,
    series: { ts: [Math.floor(Date.now() / 1000) - 30, Math.floor(Date.now() / 1000)],
      cpu: [1, cpu], rss: [10, 12], sessions: [1, 2], instances: [1, 1] } }) });
  try {
    assert.equal(pending.length, 1);
    Date.now = () => realNow() + 31000;
    h.route.onLiveMetrics(app.slug, { status: 'running', replicas: [{ status: 'running' }] });
    await flush();
    assert.equal(pending.length, 2);
    pending[1](response(9));
    await flush();
    assert.equal(h.doc.querySelector('[data-metric="cpu"] .trend-value').textContent, '0.09 cores');
    pending[0](response(3));
    await flush();
    assert.equal(h.doc.querySelector('[data-metric="cpu"] .trend-value').textContent, '0.09 cores');
  } finally { Date.now = realNow; h.restore(); }
});

test('Overview marks failed live metrics and refreshes admission issues after the next check', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 1, last_deployment_status: 'succeeded' };
  let current = envelopeFor(app, null);
  let fail = false;
  const h = await mountDetail({ app, tab: 'overview', detailEnvelope: () => {
    if (fail) throw new Error('health check failed');
    return current;
  } });
  const realNow = Date.now;
  try {
    assert.match(h.doc.querySelector('#overview-health-title').textContent, /serving normally/);
    h.route.onMetricsError(app.slug);
    assert.match(h.doc.querySelector('#overview-health-title').textContent, /out of date/);
    current = envelopeFor(app, { window_seconds: 600, counts: { 'pool-saturated': 3 } });
    Date.now = () => realNow() + 31000;
    h.route.onLiveMetrics(app.slug, { status: 'running', replicas: [{ status: 'running' }] });
    await flush();
    assert.match(h.doc.querySelector('#overview-health-title').textContent, /admission issues/);
    assert.equal(h.doc.querySelector('#overview-rejects-by-reason').hidden, false);
    assert.equal(h.doc.querySelector('#overview-health-action').getAttribute('href'), '#overview-rejects-by-reason');
    fail = true;
    Date.now = () => realNow() + 62000;
    h.route.onLiveMetrics(app.slug, { status: 'running', replicas: [{ status: 'running' }] });
    await flush();
    assert.match(h.doc.querySelector('#overview-health-title').textContent, /out of date/);
    fail = false;
    Date.now = () => realNow() + 93000;
    h.route.onLiveMetrics(app.slug, { status: 'running', replicas: [{ status: 'running' }] });
    await flush();
    assert.match(h.doc.querySelector('#overview-health-title').textContent, /admission issues/);
  } finally { Date.now = realNow; h.restore(); }
});

test('returning to Overview restores the last live health reading', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 1, last_deployment_status: 'succeeded' };
  const h = await mountDetail({ app, tab: 'overview' });
  try {
    h.route.onLiveMetrics(app.slug, { status: 'running', replicas: [{ status: 'running' }] });
    await h.mounted.update({ slug: app.slug, tab: 'configuration' });
    await h.mounted.update({ slug: app.slug, tab: 'overview' });
    assert.match(h.doc.querySelector('#overview-health-detail').textContent, /1 replica running/);
    assert.equal(h.doc.querySelector('#overview-health-freshness').textContent, 'Live metrics checked recently');
  } finally { await flush(); h.mounted.unmount(); await flush(); h.restore(); }
});

test('rendered Overview health and admission controls have no structural WCAG A/AA violations', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 1, last_deployment_status: 'succeeded' };
  const h = await mountDetail({ app, tab: 'overview',
    rejectsByReason: { window_seconds: 600, counts: { 'pool-saturated': 2 } } });
  try {
    h.window.eval(axe.source);
    const results = await h.window.axe.run(h.doc.getElementById('detail-overview-panel'), {
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'] },
      rules: { 'color-contrast': { enabled: false } },
    });
    const findings = Array.from(results.violations, v => `${v.id}: ${Array.from(v.nodes, n => n.target.join(' ')).join(', ')}`);
    assert.equal(findings.length, 0, findings.join('\n'));
  } finally { h.restore(); }
});

test('a saturated app points an admin from the admission signal to capacity settings', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 1, last_deployment_status: 'succeeded' };
  const h = await mountDetail({
    app,
    tab: 'overview',
    rejectsByReason: { window_seconds: 600, counts: { 'pool-saturated': 3, 'render-deferred': 80 } },
  });
  try {
    const panel = h.doc.getElementById('detail-overview-panel');
    const action = panel.querySelector('#overview-rejects-action');
    assert.equal(panel.querySelector('#overview-rejects-by-reason').hidden, false);
    assert.match(panel.textContent, /Recent admission signals/);
    assert.match(panel.textContent, /Session cap reached/);
    assert.equal(action.getAttribute('href'), '/apps/crash-app/settings/scaling');
    assert.equal(action.textContent, 'Review capacity settings');
    assert.equal(action.hasAttribute('data-nav'), true);
  } finally {
    h.restore();
  }
});

test('a failed first deploy does not get the "awaiting first deploy" onboarding on Overview', async () => {
  const h = await mountDetail({ app: CRASHED_APP, tab: 'overview' });
  try {
    const panel = h.doc.getElementById('detail-overview-panel');
    const text = panel.textContent;

    // The regression: the onboarding card and its first-deploy CLI snippet.
    assert.equal(h.doc.getElementById('overview-cli-snippet'), null,
      'a crashed app must not be shown the first-deploy CLI snippet');
    assert.equal(/Deploy your first release/.test(text), false,
      'a crashed app must not be told to deploy its first bundle');
    assert.equal(/isn't running yet/.test(text), false);

    // What it must show instead: that a deploy happened and failed.
    assert.equal(/Deploy failed/.test(text), true,
      'Overview must name the failed deploy');
    assert.equal(/never started/.test(text), true);

    // The cause the operator came for, and a route to the full output.
    assert.equal(
      panel.querySelector('#overview-failed-error-text').textContent,
      'RuntimeError: intentional crash on import for QA testing',
    );
    assert.equal(panel.querySelector('#overview-failed-error').hidden, false);
    const hrefs = [...panel.querySelectorAll('a[data-nav]')].map((a) => a.getAttribute('href'));
    assert.deepEqual(hrefs, ['/apps/crash-app/logs', '/apps/crash-app/deployments']);
  } finally {
    h.restore();
  }
});

test('the detail header shows a configured description and hides an empty one', async () => {
  const described = await mountDetail({ app: CRASHED_APP, tab: 'overview' });
  try {
    const description = described.doc.getElementById('app-detail-description');
    assert.equal(description.textContent, 'Explains intentional failures for release testing.');
    assert.equal(description.hidden, false);
  } finally {
    described.restore();
  }

  const empty = await mountDetail({ app: NEW_APP, tab: 'overview' });
  try {
    const description = empty.doc.getElementById('app-detail-description');
    assert.equal(description.textContent, '');
    assert.equal(description.hidden, true);
  } finally {
    empty.restore();
  }
});

test('a failed first deploy gets the log viewer, not the "awaiting first deploy" copy', async () => {
  const h = await mountDetail({ app: CRASHED_APP, tab: 'logs' });
  try {
    const panel = h.doc.getElementById('detail-logs-panel');
    const text = panel.textContent;

    // The regression: the Deployments tab tells the operator to check the logs,
    // and the Logs tab answered that there had never been a deploy.
    assert.equal(/awaiting its first deploy/.test(text), false,
      'a crashed app must not be told it is awaiting its first deploy');
    assert.equal(panel.querySelector('.logs-empty'), null,
      'the first-deploy logs empty state must not render for a crashed app');

    // A viewer was actually mounted: it asks the server for this app's sources.
    assert.equal(
      h.calls.some((c) => typeof c === 'string' && c.includes('/api/apps/crash-app/logs')),
      true,
      `the log viewer must request this app's logs; calls were ${JSON.stringify(h.calls)}`,
    );
  } finally {
    h.restore();
  }
});

test('a genuinely never-deployed app still gets the first-deploy onboarding', async () => {
  // The negative control for both tests above: the empty state is not simply
  // gone, it is now reserved for the app it was written for.
  const h = await mountDetail({ app: NEW_APP, tab: 'overview' });
  try {
    const panel = h.doc.getElementById('detail-overview-panel');
    assert.equal(/Deploy your first release/.test(panel.textContent), true);
    assert.equal(
      h.doc.getElementById('overview-cli-snippet').textContent.includes('shinyhub deploy . --slug fresh-app --wait'),
      true,
    );
    assert.equal(/Deploy failed/.test(panel.textContent), false);
  } finally {
    h.restore();
  }
});

test('a genuinely never-deployed app still gets the first-deploy logs empty state', async () => {
  const h = await mountDetail({ app: NEW_APP, tab: 'logs' });
  try {
    const panel = h.doc.getElementById('detail-logs-panel');
    assert.equal(panel.querySelector('.logs-empty') !== null, true);
    assert.equal(/awaiting its first deploy/.test(panel.textContent), true);
    assert.equal(
      h.calls.some((c) => typeof c === 'string' && c.includes('/api/apps/fresh-app/logs')),
      false,
      'no log viewer should be mounted for an app that was never deployed',
    );
  } finally {
    h.restore();
  }
});

// The predicates themselves. deploy_count is deliberately 0 in every failed /
// pending case below: that is the whole reason it cannot be the gate.

test('hasSucceededDeploy reads the three success signals, never deploy_count alone', () => {
  assert.equal(hasSucceededDeploy({ deploy_count: 1 }), true);
  assert.equal(hasSucceededDeploy({ release_number: 2 }), true);
  assert.equal(hasSucceededDeploy({ released_at: '2026-09-06T10:00:00Z' }), true);
  assert.equal(hasSucceededDeploy({ deploy_count: 0, last_deployment_status: 'failed' }), false);
  assert.equal(hasSucceededDeploy({}), false);
  assert.equal(hasSucceededDeploy(null), false);
});

test('hasDeployAttempt separates a failed-only deploy from a never-deployed app', () => {
  assert.equal(hasDeployAttempt({ deploy_count: 0, last_deployment_status: 'failed' }), true);
  assert.equal(hasDeployAttempt({ deploy_count: 0, last_deployment_status: 'pending' }), true);
  assert.equal(hasDeployAttempt({ deploy_count: 0, last_deployed_at: '2026-09-06T10:00:00Z' }), true);
  assert.equal(hasDeployAttempt({ deploy_count: 0, deploying: true }), true);
  assert.equal(hasDeployAttempt({ deploy_count: 2 }), true);
  // Never deployed: every deployment-summary field is absent (they are
  // omitempty on the wire), so nothing marks an attempt.
  assert.equal(hasDeployAttempt({ deploy_count: 0 }), false);
  assert.equal(hasDeployAttempt({ deploy_count: 0, last_deployment_status: '' }), false);
});

test('awaitingFirstDeploy is true only for an app nobody has ever tried to deploy', () => {
  assert.equal(awaitingFirstDeploy(NEW_APP), true);
  assert.equal(awaitingFirstDeploy(CRASHED_APP), false);
  assert.equal(awaitingFirstDeploy({ deploy_count: 0, last_deployment_status: 'pending' }), false);
  assert.equal(awaitingFirstDeploy({ deploy_count: 3, last_deployment_status: 'failed' }), false);
});

test('firstDeployFailed is the crashed-on-first-deploy case only', () => {
  assert.equal(firstDeployFailed(CRASHED_APP), true);
  assert.equal(firstDeployFailed(NEW_APP), false);
  // A failed REdeploy is not a failed first deploy: a working version is still
  // serving, so the overview must show it rather than "never started".
  assert.equal(firstDeployFailed({ deploy_count: 3, last_deployment_status: 'failed' }), false);
  assert.equal(firstDeployFailed({ release_number: 1, last_deployment_status: 'failed' }), false);
  assert.equal(firstDeployFailed({ deploy_count: 0, last_deployment_status: 'pending' }), false);
  assert.equal(firstDeployFailed({ deploy_count: 0, last_deployment_status: 'succeeded' }), false);
});

test('serving deployment fence is visible on operational health', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 1,
    last_deployment_status: 'failed', deployment_repair_required: true };
  const h = await mountDetail({ app, tab: 'overview' });
  try {
    assert.equal(h.doc.querySelector('#overview-health-title').textContent, 'Deployment repair required');
    assert.match(h.doc.querySelector('#overview-health-detail').textContent, /cannot recover after a restart/);
    assert.equal(h.doc.querySelector('#overview-health-action').hidden, true);
    assert.equal(h.doc.querySelector('.release-history').getAttribute('href'), `/apps/${app.slug}/deployments`);
  } finally { h.restore(); }
});


test('an undeployed app shows setup instead of empty telemetry or a launch action', async () => {
  const h = await mountDetail({ app: { ...NEW_APP, status: 'running' }, tab: 'overview' });
  try {
    assert.equal(h.doc.querySelector('.app-detail-capacity').hidden, true);
    assert.equal(h.doc.getElementById('app-detail-open').hidden, true);
    assert.equal(h.doc.querySelector('.first-deploy .emptystate-eyebrow'), null);
    assert.equal(h.doc.querySelector('.first-deploy-setup a').getAttribute('href'), '/apps/fresh-app/settings/general');
    const copied = [];
    Object.defineProperty(h.window.navigator, 'clipboard', { configurable: true, value: { writeText: async text => copied.push(text) } });
    h.doc.getElementById('overview-cli-copy').click();
    await flush();
    assert.deepEqual(copied, ['shinyhub deploy . --slug fresh-app --wait --host https://hub.example.com']);
    assert.match(h.doc.getElementById('overview-cli-copy-status').textContent, /Deployment command copied/);
    h.doc.getElementById('overview-deploy-btn').click();
    assert.ok(h.calls.includes('deploy-modal'));
  } finally { h.restore(); }
});

test('first-release instructions explain a blocked clipboard and respect view-only access', async () => {
  const h = await mountDetail({ app: NEW_APP, tab: 'overview' });
  try {
    Object.defineProperty(h.window.navigator, 'clipboard', { configurable: true, value: { writeText: async () => { throw new Error('denied'); } } });
    h.doc.getElementById('overview-cli-copy').click();
    await flush();
    assert.match(h.doc.getElementById('overview-cli-copy-status').textContent, /copy them manually/);
  } finally { h.restore(); }
  const viewer = await mountDetail({ app: NEW_APP, tab: 'overview', canManage: false });
  try {
    assert.equal(viewer.doc.getElementById('overview-cli-copy'), null);
    assert.equal(viewer.doc.getElementById('overview-deploy-btn'), null);
    assert.match(viewer.doc.querySelector('.first-deploy').textContent, /An app manager can deploy/);
  } finally { viewer.restore(); }
});

test('a released app retains live telemetry and its launch action', async () => {
  const h = await mountDetail({ app: { ...NEW_APP, status: 'running', last_deployment_status: 'succeeded' }, tab: 'overview' });
  try {
    assert.equal(h.doc.querySelector('.app-detail-capacity').hidden, false);
    assert.equal(h.doc.getElementById('app-detail-open').hidden, false);
    assert.equal(h.doc.querySelector('.first-deploy'), null);
  } finally { h.restore(); }
});


test('health remains in the shared header on every app page; metrics belong to Overview', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 2, last_deployment_status: 'succeeded' };
  const h = await mountDetail({ app, tab: 'logs' });
  try {
    const health = h.doc.querySelector('#overview-health');
    assert.equal(health.hidden, false);
    assert.ok(health.closest('.app-detail-header'));
    assert.equal(h.doc.querySelector('#app-detail-logs'), null);
    assert.equal(h.doc.querySelector('.app-technical-summary').hidden, true);
    h.route.onMetricsError(app.slug);
    assert.match(health.textContent, /out of date/);
    await h.mounted.update({ slug: app.slug, tab: 'overview' });
    assert.equal(h.doc.querySelector('.app-technical-summary').hidden, false);
  } finally { h.restore(); }
});

test('in-flight redeploy takes precedence over a still-running previous release', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 2, deploying: true };
  const h = await mountDetail({ app, tab: 'overview' });
  try {
    assert.equal(h.doc.querySelector('#overview-health-title').textContent, 'Deployment in progress');
    assert.match(h.doc.querySelector('#overview-health-detail').textContent, /previous release is still serving/);
    assert.equal(h.doc.querySelector('#overview-health-recover').hidden, true);
    assert.equal(h.doc.querySelector('#app-detail-open').hidden, false);
  } finally { h.restore(); }
});

test('a failed redeploy explains that the previous release is still available', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 2 };
  const h = await mountDetail({ app, tab: 'overview' });
  try {
    assert.equal(h.doc.querySelector('#overview-health-title').textContent, 'Latest deployment failed');
    assert.match(h.doc.querySelector('#overview-health-detail').textContent, /previous release is still serving/);
    assert.equal(h.doc.querySelector('#app-detail-open').hidden, false);
  } finally { h.restore(); }
});

test('crash recovery shows the actual error once and respects management permission', async () => {
  for (const canManage of [true, false]) {
    const app = { ...CRASHED_APP, status: 'crashed', deploy_count: 2, last_error: '<script>failure</script>' };
    const h = await mountDetail({ app, tab: 'overview', canManage });
    try {
      const health = h.doc.querySelector('#overview-health');
      assert.equal(health.querySelector('pre').textContent, '<script>failure</script>');
      assert.equal(health.querySelector('script'), null);
      assert.equal(h.doc.querySelector('.crash-banner'), null);
      const recover = h.doc.querySelector('#overview-health-recover');
      assert.equal(recover.hidden, !canManage);
      recover.click();
      await flush();
      assert.equal(h.calls.includes('restart'), canManage);
    } finally { h.restore(); }
  }
});

test('stopped apps offer an idempotent start and sleeping apps remain launchable', async () => {
  for (const status of ['stopped', 'hibernated']) {
    const app = { ...CRASHED_APP, status, deploy_count: 2, last_deployment_status: 'succeeded' };
    const h = await mountDetail({ app, tab: 'overview' });
    try {
      assert.equal(h.doc.querySelector('#app-detail-open').hidden, status === 'stopped');
      h.doc.querySelector('#overview-health-recover').click();
      await flush();
      assert.ok(h.calls.includes('start'));
      assert.equal(h.calls.includes('restart'), false);
    } finally { h.restore(); }
  }
});

test('inspect replicas points to processes without hiding them behind a disclosure', async () => {
  const app = { ...CRASHED_APP, status: 'degraded', deploy_count: 2 };
  const h = await mountDetail({ app, tab: 'overview' });
  try {
    assert.equal(h.doc.querySelector('.overview-technical'), null);
    const processes = h.doc.querySelector('#overview-replicas-list');
    assert.ok(processes);
    assert.equal(processes.closest('details'), null);
    assert.equal(h.doc.querySelector('#overview-health-action').getAttribute('href'), '#overview-replicas-list');
  } finally { h.restore(); }
});


test('a deployment repair fence offers review instead of a futile restart', async () => {
  const app = { ...CRASHED_APP, status: 'crashed', deploy_count: 2, deployment_repair_required: true };
  const h = await mountDetail({ app, tab: 'overview' });
  try {
    assert.equal(h.doc.querySelector('#overview-health-recover').hidden, true);
    assert.equal(h.doc.querySelector('#overview-health-action').hidden, true);
    assert.equal(h.doc.querySelector('.release-history').getAttribute('href'), `/apps/${app.slug}/deployments`);
    h.doc.querySelector('#overview-health-recover').click();
    await flush();
    assert.equal(h.calls.includes('restart'), false);
  } finally { h.restore(); }
});


test('header avatar opens this app’s existing icon editor and focuses its controls', async () => {
  const app = { ...CRASHED_APP, status: 'running', deploy_count: 2, last_deployment_status: 'succeeded' };
  const h = await mountDetail({ app, tab: 'overview' });
  try {
    const edit = h.doc.querySelector('#app-detail-icon-edit');
    const upload = h.doc.querySelector('#general-icon-upload');
    let scrolled = false;
    upload.closest('.scaling-row').scrollIntoView = () => { scrolled = true; };
    h.ctx.navigate = async path => {
      assert.equal(path, `/apps/${app.slug}/settings/general`);
      await h.mounted.update(settingsRouteParams({slug:app.slug, section:'general'}));
    };
    assert.equal(edit.hidden, false);
    assert.equal(edit.getAttribute('aria-label'), 'Edit app icon');
    assert.equal(h.doc.querySelector('#app-detail-heading').textContent, app.name);
    edit.click();
    await flush();
    assert.equal(h.doc.querySelector('#detail-configuration-panel').hidden, false);
    assert.equal(h.doc.activeElement, upload);
    assert.equal(scrolled, true);
  } finally { h.restore(); }
});

test('avatar editing respects read-only access and a vetoed navigation', async () => {
  for (const canManage of [false, true]) {
    const app = { ...CRASHED_APP, status: 'running', deploy_count: 2 };
    const h = await mountDetail({ app, tab: 'overview', canManage });
    try {
      const edit = h.doc.querySelector('#app-detail-icon-edit');
      assert.equal(edit.hidden, !canManage);
      h.doc.querySelector('#app-detail-heading').focus();
      const focused = h.doc.activeElement;
      edit.click();
      await flush();
      assert.equal(h.calls.includes(`navigate:/apps/${app.slug}/settings/general`), canManage);
      assert.equal(h.doc.activeElement, focused, 'do not focus a hidden editor when navigation is vetoed');
    } finally { h.restore(); }
  }
});

test('a vetoed avatar edit from another settings page preserves that page and its draft focus', async () => {
  const h = await mountDetail({app:CRASHED_APP, section:'resources'});
  try {
    const panel = h.doc.getElementById('detail-configuration-panel');
    const cpu = h.doc.getElementById('resources-cpu'); cpu.value = '0.29'; cpu.focus();
    let scrolled = false;
    h.doc.getElementById('general-icon-upload').closest('.scaling-row').scrollIntoView = () => { scrolled = true; };
    h.doc.getElementById('app-detail-icon-edit').click();
    await flush();
    assert.ok(h.calls.includes('navigate:/apps/crash-app/settings/general'));
    assert.equal(h.window.location.pathname, '/apps/crash-app/settings/resources');
    assert.equal(panel.dataset.settingsPage, 'resources');
    assert.equal(cpu.value, '0.29');
    assert.equal(h.doc.activeElement, cpu);
    assert.equal(scrolled, false);
  } finally { h.restore(); }
});


test('deployment actions are available only to app managers', async () => {
  for (const canManage of [true, false]) {
    const app = { ...CRASHED_APP, status: 'running', deploy_count: 2 };
    const h = await mountDetail({ app, tab: 'deployments', canManage, deployments: [
      { id: 2, status: 'succeeded', version: 'new', created_at: '2026-10-31T12:00:00Z' },
      { id: 1, status: 'succeeded', version: 'old', created_at: '2026-10-30T12:00:00Z' },
    ] });
    try {
      assert.equal(!!h.doc.querySelector('#detail-deploy-new'), canManage);
      assert.equal(!!h.doc.querySelector('.rollback-btn'), canManage);
      assert.equal(h.doc.querySelectorAll('.deployment-current-badge').length, 1);
      assert.equal(h.doc.querySelector('.deployment-live-note'), null);
    } finally { h.restore(); }
  }
});

const DEPLOYMENT_HISTORY = [
  { id: 3, version: 'failed-bundle', status: 'failed', failure_reason: 'Dependency installation failed', created_at: '2026-11-01T12:00:00Z' },
  { id: 2, version: 'current-bundle', release_number: 2, status: 'succeeded', created_at: '2026-10-31T12:00:00Z' },
  { id: 1, version: 'old-bundle', release_number: 1, status: 'succeeded', created_at: '2026-10-30T12:00:00Z' },
];
const deploymentResponse = body => ({ ok: true, status: 200, json: async () => body });
const HISTORY_APP = { ...CRASHED_APP, status: 'running', deploy_count: 2 };

async function deploymentHarness({ history = DEPLOYMENT_HISTORY, handle, ...options } = {}) {
  return mountDetail({ app: HISTORY_APP, tab: 'deployments', deployments: history, ...options,
    beforeMount: ctx => {
      const api = ctx.api;
      ctx.api = (path, init) => handle?.(path, init, ctx) ?? api(path, init);
      options.beforeMount?.(ctx);
    } });
}

test('deployment history separates failed attempts from the current release and exposes exact timestamps', async () => {
  const h = await deploymentHarness();
  try {
    const rows = h.doc.querySelectorAll('.deployment-row');
    assert.equal(rows.length, 3);
    assert.equal(rows[0].querySelector('.deployment-failure-reason').textContent, 'Dependency installation failed');
    assert.equal(rows[0].querySelector('.deployment-number').textContent, '');
    assert.equal(rows[0].querySelector('a').getAttribute('href'), '/apps/crash-app/logs');
    assert.equal(rows[1].querySelector('.deployment-current-badge').textContent, 'Current');
    assert.equal(rows[1].querySelector('button'), null);
    assert.equal(rows[2].querySelector('button').getAttribute('aria-label'), 'Roll back to v1');
    assert.equal(rows[1].querySelector('time').dateTime, '2026-10-31T12:00:00.000Z');
    assert.match(rows[1].querySelector('time').textContent, /2026/);
    assert.equal(h.doc.querySelector('.deployment-source-detail'), null, 'missing provenance must not repeat itself');
  } finally { h.mounted.unmount(); h.restore(); }
});

test('deployment refresh retains history on malformed and network responses, and Retry recovers', async () => {
  let mode = 'ready', requests = 0;
  const h = await deploymentHarness({ handle: path => {
    if (!path.includes('/deployments')) return;
    requests++;
    if (mode === 'network') return Promise.reject(new TypeError('Failed to fetch'));
    return Promise.resolve(deploymentResponse(mode === 'malformed' ? {} : { items: DEPLOYMENT_HISTORY }));
  } });
  try {
    for (const failure of ['malformed', 'network']) {
      mode = failure;
      h.doc.querySelector('#detail-deployments-refresh').click();
      await flush();
      assert.equal(h.doc.querySelectorAll('.deployment-row').length, 3);
      assert.equal(h.doc.querySelector('#detail-deployments-empty').hidden, true);
      assert.match(h.doc.querySelector('#detail-deployments-error').textContent, /last loaded history/);
      assert.equal(h.doc.querySelector('#detail-deployments-refresh').disabled, false);
    }
    mode = 'ready';
    h.doc.querySelector('#detail-deployments-retry').click();
    await flush();
    assert.equal(requests, 4);
    assert.equal(h.doc.querySelector('#detail-deployments-error').hidden, true);
  } finally { h.mounted.unmount(); h.restore(); }
});

test('initial deployment loading is visible and a late result cannot repaint a departed tab', async () => {
  let resolve;
  const h = await deploymentHarness({ handle: path => path.includes('/deployments') ? new Promise(r => { resolve = r; }) : undefined });
  try {
    assert.equal(h.doc.querySelector('#app-detail-view').hidden, false);
    assert.match(h.doc.querySelector('#detail-deployments-status').textContent, /Loading deployments/);
    await h.mounted.update({ slug: HISTORY_APP.slug, tab: 'overview' });
    resolve(deploymentResponse({ items: DEPLOYMENT_HISTORY }));
    await flush();
    assert.equal(h.doc.querySelectorAll('.deployment-row').length, 0);
  } finally { h.mounted.unmount(); h.restore(); }
});

test('rollback names the release, locks competing actions, and stays on history after success', async () => {
  let resolve, posts = 0, confirmation = '', history = DEPLOYMENT_HISTORY;
  const toasts = [];
  const h = await deploymentHarness({ handle: (path, init) => {
    if (path.includes('/deployments')) return Promise.resolve(deploymentResponse({ items: history }));
    if (!path.endsWith('/rollback')) return;
    posts++;
    assert.equal(init.method, 'POST');
    assert.equal(init.headers['X-Shinyhub-Deploy-Channel'], 'dashboard');
    assert.deepEqual(JSON.parse(init.body), { deployment_id: 1 });
    return new Promise(r => { resolve = r; });
  }, beforeMount: ctx => { ctx.flashToast = (...args) => toasts.push(args); } });
  try {
    h.window.confirm = message => { confirmation = message; return true; };
    const btn = h.doc.querySelector('.rollback-btn');
    btn.click(); btn.click();
    assert.equal(posts, 1);
    assert.match(confirmation, /to v1\?/);
    assert.match(confirmation, /Active sessions may be interrupted/);
    assert.equal(h.doc.querySelector('#detail-deploy-new').disabled, true);
    assert.match(h.doc.querySelector('#detail-deployments-status').textContent, /Rolling back/);
    history = [{ ...DEPLOYMENT_HISTORY[2], id: 4, release_number: 3 }, ...history];
    resolve(deploymentResponse({ warning: 'Rollback committed; schedules are still converging.' }));
    await flush(); await flush();
    assert.equal(h.doc.querySelector('.deployment-row-current .deployment-number').textContent, 'v3');
    assert.deepEqual(h.navigations, []);
    assert.equal(h.doc.querySelector('#detail-deploy-new').disabled, false);
    assert.equal(toasts[0][1], 'warning');
    assert.equal(h.calls.filter(path => path === '/api/apps/crash-app').length, 2, 'header metadata must be refreshed too');
  } finally { h.mounted.unmount(); h.restore(); }
});

test('cancelled and failed rollbacks keep history available without retrying the mutation', async () => {
  let posts = 0;
  const h = await deploymentHarness({ handle: path => {
    if (path.endsWith('/rollback')) { posts++; return Promise.reject(new TypeError('offline')); }
  } });
  try {
    h.window.confirm = () => false;
    h.doc.querySelector('.rollback-btn').click();
    assert.equal(posts, 0);
    h.window.confirm = () => true;
    h.doc.querySelector('.rollback-btn').click();
    await flush();
    assert.equal(posts, 1);
    assert.match(h.doc.querySelector('#detail-deployments-error').textContent, /Could not confirm rollback/);
    assert.equal(h.doc.querySelector('.rollback-btn').disabled, false);
    assert.equal(h.doc.querySelectorAll('.deployment-row').length, 3);
  } finally { h.mounted.unmount(); h.restore(); }
});

test('pending deployments refresh when visible, retain expanded development sessions, and stop after leaving', async () => {
  const session = { development_session: { id: 'dev-1', status: 'active' } };
  let history = [ { id: 4, version: 'next', status: 'pending', provenance: session }, ...DEPLOYMENT_HISTORY ];
  let requests = 0;
  const h = await deploymentHarness({ handle: path => {
    if (path.includes('/deployments')) { requests++; return Promise.resolve(deploymentResponse({ items: history })); }
  } });
  try {
    assert.match(h.doc.querySelector('#detail-deployments-status').textContent, /updates automatically/);
    assert.equal(h.doc.querySelector('.rollback-btn').disabled, true);
    const details = h.doc.querySelector('.development-session-details');
    details.open = false;
    history = [{ ...history[0], status: 'succeeded', release_number: 3 }, ...DEPLOYMENT_HISTORY];
    h.doc.dispatchEvent(new h.window.Event('visibilitychange'));
    await flush();
    assert.equal(h.doc.querySelector('.development-session-details').open, false);
    assert.equal(h.doc.querySelector('.deployment-current-badge').textContent, 'Current');
    assert.equal(h.doc.querySelector('#detail-deployments-status').hidden, true);
    assert.equal(requests, 2);
    await h.mounted.update({ slug: HISTORY_APP.slug, tab: 'overview' });
    await flush();
    assert.equal(requests, 3, 'Overview loads its own recent activity once');
    h.doc.dispatchEvent(new h.window.Event('visibilitychange'));
    await flush();
    assert.equal(requests, 3, 'the previous deployments poll has stopped');
  } finally { h.mounted.unmount(); h.restore(); }
});

test('earlier deployment pages remain reachable and refresh replaces all loaded pages', async () => {
  const all = Array.from({ length: 55 }, (_, i) => ({ id: 55-i, version: String(55-i), release_number: 55-i, status: 'succeeded' }));
  const paths = [];
  const h = await deploymentHarness({ handle: path => {
    if (!path.includes('/deployments')) return;
    paths.push(path);
    const offset = Number(new URL(path, 'https://hub.example.com').searchParams.get('offset') || 0);
    return Promise.resolve(deploymentResponse({ items: all.slice(offset, offset+50), total: all.length }));
  } });
  try {
    assert.equal(h.doc.querySelectorAll('.deployment-row').length, 50);
    h.doc.querySelector('#detail-deployments-earlier').click();
    await flush();
    assert.equal(h.doc.querySelectorAll('.deployment-row').length, 55);
    assert.equal(h.doc.querySelector('#detail-deployments-announcement').textContent, 'Earlier deployments loaded.');
    assert.equal(h.doc.querySelector('#detail-deployments-earlier').hidden, true);
    h.doc.querySelector('#detail-deployments-refresh').click();
    await flush();
    assert.equal(h.doc.querySelectorAll('.deployment-row').length, 55);
    assert.equal(paths.filter(path => path.includes('offset=50')).length, 2);
    assert.equal(h.doc.querySelectorAll('.deployment-current-badge').length, 1);
  } finally { h.mounted.unmount(); h.restore(); }
});

test('deployment responses from a replaced session cannot reveal history or trigger logout', async () => {
  let resolve;
  const h = await deploymentHarness({ handle: path => path.includes('/deployments') ? new Promise(r => { resolve = r; }) : undefined });
  try {
    h.ctx.state.sessionIdentity = 'replacement';
    resolve({ ok: false, status: 401 });
    await flush();
    assert.equal(h.doc.querySelectorAll('.deployment-row').length, 0);
    assert.equal(h.calls.includes('unauthorized'), false);
  } finally { h.mounted.unmount(); h.restore(); }
});

test('deployment refresh restores its keyboard control and does not steal focus from another action', async () => {
  let resolve, delayed = false;
  const h = await deploymentHarness({ handle: path => {
    if (!path.includes('/deployments') || !delayed) return;
    return new Promise(r => { resolve = r; });
  } });
  try {
    delayed = true;
    const refresh = h.doc.querySelector('#detail-deployments-refresh');
    refresh.focus(); refresh.click();
    h.doc.body.focus();
    resolve(deploymentResponse({ items: DEPLOYMENT_HISTORY }));
    await flush();
    assert.equal(h.doc.activeElement, refresh);
    refresh.click();
    const deploy = h.doc.querySelector('#detail-deploy-new');
    deploy.focus();
    resolve(deploymentResponse({ items: DEPLOYMENT_HISTORY }));
    await flush();
    assert.equal(h.doc.activeElement, deploy);
  } finally { h.mounted.unmount(); h.restore(); }
});

test('deployment history distinguishes empty, missing, and expired-session responses', async () => {
  for (const status of [200, 404, 401]) {
    const h = await deploymentHarness({ handle: path => path.includes('/deployments')
      ? Promise.resolve({ ok: status === 200, status, json: async () => status === 200 ? { items: [] } : { error: 'App unavailable.' } }) : undefined });
    try {
      assert.equal(h.doc.querySelector('#detail-deployments-empty').hidden, status !== 200);
      assert.equal(h.doc.querySelector('#detail-deployments-error').hidden, status !== 404);
      assert.equal(h.calls.includes('unauthorized'), status === 401);
    } finally { h.mounted.unmount(); h.restore(); }
  }
});

test('rendered deployment history has no structural WCAG A/AA violations', async () => {
  const h = await deploymentHarness();
  try {
    h.window.eval(axe.source);
    const result = await h.window.axe.run(h.doc.querySelector('#detail-deployments-panel'), {
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'] },
      rules: { 'color-contrast': { enabled: false } },
    });
    assert.deepEqual(Array.from(result.violations, v => v.id), []);
  } finally { h.mounted.unmount(); h.restore(); }
});

test('shared app header keeps its structure across destinations and suppresses redundant links', async () => {
  const h = await deploymentHarness();
  try {
    const header = h.doc.querySelector('.app-detail-header');
    const summary = h.doc.querySelector('.app-detail-summary');
    const health = h.doc.querySelector('#overview-health');
    const release = h.doc.querySelector('#app-detail-release');
    const actions = header.querySelector('.app-detail-actions');
    const actionNodes = [...actions.querySelectorAll('a, button')];
    assert.equal(header.querySelector('.breadcrumb, [aria-label="Breadcrumb"]'), null);
    assert.equal(header.querySelector('#app-detail-logs'), null);
    assert.deepEqual(actionNodes.map(node => node.id), [
      'app-detail-open', 'app-detail-kebab', 'app-detail-deploy', 'app-detail-restart',
      'app-detail-sleep', 'app-detail-stop', 'app-detail-start',
    ]);
    const targets = [
      { slug: HISTORY_APP.slug, tab: 'deployments' },
      { slug: HISTORY_APP.slug, tab: 'overview' },
      { slug: HISTORY_APP.slug, tab: 'logs' },
      { slug: HISTORY_APP.slug, tab: 'traces' },
      { slug: HISTORY_APP.slug, tab: 'usage' },
      { slug: HISTORY_APP.slug, tab: 'schedules' },
      { slug: HISTORY_APP.slug, tab: 'data' },
      ...['general', 'environment', 'resources', 'scaling', 'privacy', 'access'].map(section => settingsRouteParams({ slug: HISTORY_APP.slug, section })),
    ];
    for (const params of targets) {
      await h.mounted.update(params);
      assert.equal(h.doc.querySelector('.app-detail-header'), header);
      assert.equal(h.doc.querySelector('.app-detail-summary'), summary);
      assert.equal(header.querySelector('.app-detail-actions'), actions);
      assert.deepEqual([...actions.querySelectorAll('a, button')], actionNodes, 'page changes preserve the same header action nodes');
      assert.equal(header.querySelector('.breadcrumb, [aria-label="Breadcrumb"]'), null);
      assert.equal(h.doc.querySelector('#app-detail-logs'), null);
      assert.equal(summary.contains(health), true);
      assert.equal(summary.contains(release), true);
      assert.equal(h.doc.querySelector('.release-history').hidden, params.tab === 'deployments');
      assert.equal(h.doc.querySelector('#overview-health-action').hidden, true, 'release history already supplies the deployment destination');
      assert.match(health.textContent, /previous release is still serving/);
    }
  } finally { h.mounted.unmount(); await flush(); h.restore(); }
});

test('deployment field relationships are explicit in regular and development-session items', async () => {
  const session = { development_session: { id: 'dev-labelled', status: 'ended' } };
  const h = await deploymentHarness({ history: [
    { ...DEPLOYMENT_HISTORY[0], provenance: session },
    ...DEPLOYMENT_HISTORY.slice(1),
  ] });
  try {
    const rows = [...h.doc.querySelectorAll('.deployment-row')];
    assert.equal(rows.length, 3);
    assert.ok(h.doc.querySelector('.development-session-attempts .deployment-fields'));
    for (const row of rows) {
      const dl = row.querySelector('dl');
      assert.ok(dl, 'each deployment exposes native definition-list semantics');
      const groups = [...dl.children];
      assert.deepEqual(groups.map(group => group.querySelector('dt').textContent), row.querySelector('.deployment-current-badge')
        ? ['Deployment', 'Source & outcome', 'Started'] : ['Deployment', 'Source & outcome', 'Started', 'Actions']);
      for (const group of groups) {
        assert.equal(group.children.length, 2);
        assert.equal(group.children[0].tagName, 'DT');
        assert.equal(group.children[1].tagName, 'DD');
        assert.ok(group.children[1].textContent.trim());
      }
    }
  } finally { h.mounted.unmount(); h.restore(); }
});

test('manual refresh announces completion even when history is unchanged; failed refresh does not', async () => {
  let resolve, fail = false, delayed = false;
  const h = await deploymentHarness({ handle: path => {
    if (!path.includes('/deployments') || !delayed) return;
    return new Promise(r => { resolve = () => r(fail
      ? { ok: false, status: 503, json: async () => ({ error: 'Unavailable. Try again.' }) }
      : deploymentResponse({ items: DEPLOYMENT_HISTORY })); });
  } });
  try {
    const announcement = h.doc.querySelector('#detail-deployments-announcement');
    assert.equal(announcement.textContent, '');
    assert.equal(announcement.getAttribute('role'), 'status');
    assert.equal(announcement.getAttribute('aria-live'), 'polite');
    assert.equal(announcement.hidden, false);
    delayed = true;
    const original = h.doc.querySelector('.deployment-row');
    for (let i = 0; i < 2; i++) {
      h.doc.querySelector('#detail-deployments-refresh').click();
      assert.equal(announcement.textContent, '', 'clear the prior announcement before another request');
      resolve(); await flush();
      assert.equal(announcement.textContent, 'Deployment history updated.');
      assert.equal(h.doc.querySelector('.deployment-row'), original, 'unchanged history preserves existing nodes');
    }
    fail = true;
    h.doc.querySelector('#detail-deployments-refresh').click();
    resolve(); await flush();
    assert.equal(announcement.textContent, '');
    assert.equal(h.doc.querySelector('#detail-deployments-error').hidden, false);
    fail = false;
    h.doc.querySelector('#detail-deployments-retry').click();
    resolve(); await flush();
    assert.equal(announcement.textContent, 'Deployment history updated.');
  } finally { h.mounted.unmount(); h.restore(); }
});

test('pending background polls do not announce manual refresh completion', async () => {
  let history = [{ id: 4, version: 'next', status: 'pending' }, ...DEPLOYMENT_HISTORY];
  const h = await deploymentHarness({ handle: path => path.includes('/deployments')
    ? Promise.resolve(deploymentResponse({ items: history })) : undefined });
  try {
    const announcement = h.doc.querySelector('#detail-deployments-announcement');
    h.doc.dispatchEvent(new h.window.Event('visibilitychange'));
    await flush();
    assert.equal(announcement.textContent, '');
    history = [{ ...history[0], status: 'succeeded', release_number: 3 }, ...DEPLOYMENT_HISTORY];
    h.doc.dispatchEvent(new h.window.Event('visibilitychange'));
    await flush();
    assert.equal(announcement.textContent, '');
    assert.equal(h.doc.querySelector('#detail-deployments-status').hidden, true);
  } finally { h.mounted.unmount(); h.restore(); }
});

test('history without management permission has no empty Actions definition', async () => {
  const h = await deploymentHarness({ canManage: false });
  try {
    for (const row of h.doc.querySelectorAll('.deployment-row')) {
      for (const group of row.querySelector('dl').children) {
        assert.ok(group.querySelector('dd').textContent.trim(), 'every announced field must have a value');
      }
    }
    assert.equal(h.doc.querySelectorAll('.rollback-btn').length, 0);
    assert.equal(h.doc.querySelectorAll('.deployment-action').length, 2, 'only the visual heading and failed-attempt log action remain');
  } finally { h.mounted.unmount(); h.restore(); }
});

test('Overview refresh checks live metrics once, retains the shared header, and announces completion', async () => {
  let metricsRefreshes = 0;
  const h = await mountDetail({app:{...CRASHED_APP,status:'running',deploy_count:2,last_deployment_status:'succeeded'},
    beforeMount:ctx => {ctx.metrics.refresh=async () => {metricsRefreshes++;};},
    historyFetch:async () => ({ok:true,json:async () => ({enabled:true,series:{ts:[]},window_seconds:900})})});
  try {
    const header=h.doc.querySelector('#app-detail-heading');
    const refresh=h.doc.querySelector('#overview-refresh');
    refresh.click();refresh.click();await flush();await flush();
    assert.equal(metricsRefreshes,1);
    assert.equal(h.doc.querySelector('#app-detail-heading'),header);
    assert.equal(refresh.disabled,false);
    assert.equal(h.doc.querySelector('#overview-refresh-status').textContent,'Overview updated.');
    assert.equal(h.doc.querySelector('.app-technical-summary').tagName,'DIV','live measurements remain visible on mobile');
    await h.mounted.update({slug:CRASHED_APP.slug,tab:'logs'});
    assert.equal(h.doc.querySelector('#app-overview-heading').hidden,true);
    assert.equal(h.doc.querySelector('#overview-refresh-status').hidden,true);
  } finally {h.mounted.unmount();h.restore();}
});

test('read-only users cannot redeploy from a failed first attempt', async () => {
  const h=await mountDetail({app:CRASHED_APP,canManage:false});
  try {assert.equal(h.doc.querySelector('#overview-deploy-btn'),null);assert.equal(h.calls.includes('deploy-modal'),false);}
  finally {h.mounted.unmount();h.restore();}
});

test('an old Overview refresh cannot announce completion after leaving and returning', async () => {
  let finish;
  const h=await mountDetail({app:{...CRASHED_APP,status:'running',deploy_count:2},
    beforeMount:ctx=>{ctx.metrics.refresh=()=>new Promise(resolve=>{finish=resolve;});},
    historyFetch:async()=>({ok:true,json:async()=>({enabled:true,series:{ts:[]},window_seconds:900})})});
  try {
    h.doc.querySelector('#overview-refresh').click();
    await h.mounted.update({slug:CRASHED_APP.slug,tab:'logs'});
    await h.mounted.update({slug:CRASHED_APP.slug,tab:'overview'});
    finish();await flush();await flush();
    assert.equal(h.doc.querySelector('#overview-refresh-status').textContent,'');
    assert.equal(h.doc.querySelector('#overview-refresh').disabled,false);
  } finally {h.mounted.unmount();h.restore();}
});

test('a background history refresh preserves keyboard inspection and the selected instant', async () => {
  const now=Math.floor(Date.now()/1000), realNow=Date.now;
  const history={window_seconds:900,interval_seconds:30,series:{ts:[now-60,now-30,now],cpu:[10,20,30],rss:[100,200,300],sessions:[1,2,3],instances:[1,1,2]}};
  const app={...CRASHED_APP,status:'running',deploy_count:2,last_deployment_status:'succeeded'};
  const h=await mountDetail({app,historyFetch:async()=>({ok:true,json:async()=>history})});
  try {
    const old=h.doc.querySelector('[data-history-chart="memory"]');old.focus();old.dispatchEvent(new h.window.KeyboardEvent('keydown',{key:'Home'}));
    Date.now=()=>realNow()+31000;
    h.route.onLiveMetrics(app.slug,{status:'running',replicas:[{status:'running'}]});
    await flush();await flush();
    const current=h.doc.querySelector('[data-history-chart="memory"]');
    assert.notEqual(current,old);assert.equal(h.doc.activeElement,current);
    assert.equal(current.getAttribute('aria-valuenow'),'0');assert.equal(h.doc.querySelector('.trend-value').textContent,'0.1 cores');
    assert.equal(h.doc.querySelector('.trends-card').dataset.inspectTimestamp,String(now-60));
    h.doc.querySelector('.trends-latest').focus();
    Date.now=()=>realNow()+62000;
    h.route.onLiveMetrics(app.slug,{status:'running',replicas:[{status:'running'}]});
    await flush();await flush();
    const latest=h.doc.querySelector('.trends-latest');
    assert.equal(h.doc.activeElement,latest,'refresh preserves the contextual button focus');
    latest.click();
    assert.equal(h.doc.activeElement,h.doc.querySelector('[data-history-chart="memory"]'),'Latest returns to the same chart after refresh');
    assert.equal(h.doc.querySelector('.trends-card').dataset.inspectActive,'false');
    Date.now=()=>realNow()+93000;
    h.route.onLiveMetrics(app.slug,{status:'running',replicas:[{status:'running'}]});
    await flush();await flush();
    assert.equal(h.doc.querySelector('.trends-card').dataset.inspectActive,'false','refresh does not restart inspection after returning to latest');
    assert.equal(h.doc.activeElement,h.doc.querySelector('[data-history-chart="memory"]'));
  } finally {Date.now=realNow;h.mounted.unmount();h.restore();}
});
