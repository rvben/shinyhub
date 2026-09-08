import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM, VirtualConsole } from 'jsdom';
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

function envelopeFor(app) {
  return {
    app,
    replicas_status: [],
    can_manage: true,
    runtime_mode: 'native',
  };
}

// mountDetail builds the real dashboard DOM, installs the browser globals the
// view modules read off the global scope, and mounts the detail view on the
// requested tab.
async function mountDetail({ app, tab }) {
  const virtualConsole = new VirtualConsole();
  const dom = new JSDOM(INDEX_HTML, {
    url: `https://hub.example.com/apps/${app.slug}${tab === 'overview' ? '' : '/' + tab}`,
    pretendToBeVisual: true,
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
    fetch: async () => { throw new Error('unexpected bare fetch'); },
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

  const calls = [];
  const jsonResponse = (body) => ({
    ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body),
  });
  const ctx = {
    state: { user: { username: 'admin', role: 'admin' } },
    api: async (path) => {
      calls.push(path);
      if (path === `/api/apps/${app.slug}`) return jsonResponse(envelopeFor(app));
      // Every other endpoint the panels reach for answers with an empty list
      // envelope, which each renderer tolerates.
      return jsonResponse({ items: [], total: 0, sources: [], entries: [] });
    },
    canManageApp: () => true,
    navigate: (path) => { calls.push(`navigate:${path}`); },
    onUnauthorized: () => { calls.push('unauthorized'); },
    updateActiveNav: () => {},
    metrics: { setTargets: () => {} },
    setDetailApp: () => {},
    setDetailEnvelope: () => {},
    openDeployModal: () => { calls.push('deploy-modal'); },
    restart: () => { calls.push('restart'); },
    flashToast: () => {},
    setSettingsSlug: () => {},
    populateGeneralTab: () => {},
    populateAutoscaleTab: () => {},
    populateAccessPanel: () => {},
    refreshEnvList: () => {},
    refreshDataTab: () => {},
    refreshMemberList: () => {},
    refreshGroupAccessList: () => {},
    loadSchedules: async () => {},
    loadSharedData: async () => {},
  };

  const route = mountAppDetail(ctx);
  const mount = typeof route === 'function' ? route : route.mount;
  await mount({ slug: app.slug, tab });
  await flush();
  return { dom, window, doc: window.document, calls, restore };
}

test('a failed first deploy does not get the "awaiting first deploy" onboarding on Overview', async () => {
  const h = await mountDetail({ app: CRASHED_APP, tab: 'overview' });
  try {
    const panel = h.doc.getElementById('detail-overview-panel');
    const text = panel.textContent;

    // The regression: the onboarding card and its first-deploy CLI snippet.
    assert.equal(h.doc.getElementById('overview-cli-snippet'), null,
      'a crashed app must not be shown the first-deploy CLI snippet');
    assert.equal(/Deploy your first bundle/.test(text), false,
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
    assert.equal(/Deploy your first bundle/.test(panel.textContent), true);
    assert.equal(
      h.doc.getElementById('overview-cli-snippet').textContent.includes('shinyhub deploy --slug fresh-app'),
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
