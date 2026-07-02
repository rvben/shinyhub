import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  TAB_ROUTES,
  MANAGER_ONLY_TABS,
  resolveDetailAccess,
  tabViewModels,
} from '../static/views/app-detail-nav.js';

// The app-detail page resolves which tab to show and whether the visitor is
// allowed on the page / the requested tab, then renders a roving-tabindex
// tablist. These decisions used to live inline in mountAppDetail (untestable
// IIFE-adjacent code); extracting them keeps the routing/visibility contract
// under test. See internal/ui/static/views/app-detail.js for the wiring.

test('TAB_ROUTES and MANAGER_ONLY_TABS match the app-detail contract', () => {
  assert.deepEqual(TAB_ROUTES, [
    'overview', 'logs', 'traces', 'deployments', 'configuration', 'data', 'access',
  ]);
  assert.equal(MANAGER_ONLY_TABS.has('configuration'), true);
  assert.equal(MANAGER_ONLY_TABS.has('data'), true);
  assert.equal(MANAGER_ONLY_TABS.has('access'), true);
  assert.equal(MANAGER_ONLY_TABS.has('overview'), false);
  assert.equal(MANAGER_ONLY_TABS.has('logs'), false);
});

test('resolveDetailAccess falls back to overview for an unknown tab', () => {
  const r = resolveDetailAccess({
    user: { role: 'admin' }, canManage: true, requestedTab: 'bogus', slug: 'demo',
  });
  assert.equal(r.tab, 'overview');
  assert.equal(r.redirect, null);
});

test('resolveDetailAccess passes a valid tab through with no redirect for a manager', () => {
  const r = resolveDetailAccess({
    user: { role: 'developer' }, canManage: true, requestedTab: 'configuration', slug: 'demo',
  });
  assert.equal(r.tab, 'configuration');
  assert.equal(r.redirect, null);
});

test('resolveDetailAccess bounces a pure viewer off the detail page to /', () => {
  const r = resolveDetailAccess({
    user: { role: 'viewer' }, canManage: false, requestedTab: 'logs', slug: 'demo',
  });
  assert.deepEqual(r.redirect, { path: '/', replace: true });
});

test('resolveDetailAccess keeps a viewer who manages THIS app on the page', () => {
  // A per-app manager (viewer global role + can_manage on this app) is not bounced.
  const r = resolveDetailAccess({
    user: { role: 'viewer' }, canManage: true, requestedTab: 'configuration', slug: 'demo',
  });
  assert.equal(r.tab, 'configuration');
  assert.equal(r.redirect, null);
});

test('resolveDetailAccess redirects a non-manager off a manager-only tab to the app root', () => {
  // A developer/operator without manage rights on this private app can view the
  // page but not the configuration/data/access tabs.
  const r = resolveDetailAccess({
    user: { role: 'developer' }, canManage: false, requestedTab: 'data', slug: 'demo',
  });
  assert.deepEqual(r.redirect, { path: '/apps/demo', replace: true });
});

test('resolveDetailAccess lets a non-manager view a non-manager tab', () => {
  const r = resolveDetailAccess({
    user: { role: 'developer' }, canManage: false, requestedTab: 'logs', slug: 'demo',
  });
  assert.equal(r.tab, 'logs');
  assert.equal(r.redirect, null);
});

test('resolveDetailAccess: the viewer / redirect wins over the manager-only tab redirect', () => {
  // A pure viewer requesting a manager-only tab is bounced to / (the page-level
  // gate), not to /apps/<slug>. The page gate runs first in the original code.
  const r = resolveDetailAccess({
    user: { role: 'viewer' }, canManage: false, requestedTab: 'access', slug: 'demo',
  });
  assert.deepEqual(r.redirect, { path: '/', replace: true });
});

test('resolveDetailAccess tolerates a null user (unauthenticated render)', () => {
  const r = resolveDetailAccess({
    user: null, canManage: false, requestedTab: 'overview', slug: 'demo',
  });
  assert.equal(r.tab, 'overview');
  assert.equal(r.redirect, null);
});

test('tabViewModels marks the active tab and builds correct hrefs', () => {
  const models = tabViewModels('demo', 'logs', true);
  assert.deepEqual(models.map(m => m.route), TAB_ROUTES);

  const overview = models.find(m => m.route === 'overview');
  const logs = models.find(m => m.route === 'logs');
  const cfg = models.find(m => m.route === 'configuration');

  // Overview keeps the bare /apps/<slug> URL; every other tab has its segment.
  assert.equal(overview.href, '/apps/demo');
  assert.equal(logs.href, '/apps/demo/logs');
  assert.equal(cfg.href, '/apps/demo/configuration');

  // Active state + ARIA only on the active tab.
  assert.equal(logs.active, true);
  assert.equal(logs.ariaSelected, true);
  assert.equal(logs.ariaCurrent, 'page');
  assert.equal(overview.active, false);
  assert.equal(overview.ariaSelected, false);
  assert.equal(overview.ariaCurrent, null);

  // Roving tabindex: only the active tab is in the page Tab order.
  assert.equal(logs.tabindex, '0');
  assert.equal(overview.tabindex, '-1');
});

test('tabViewModels hides manager-only tabs from a non-manager', () => {
  const models = tabViewModels('demo', 'overview', false);
  const hiddenByRoute = Object.fromEntries(models.map(m => [m.route, m.hidden]));
  assert.equal(hiddenByRoute.configuration, true);
  assert.equal(hiddenByRoute.data, true);
  assert.equal(hiddenByRoute.access, true);
  assert.equal(hiddenByRoute.overview, false);
  assert.equal(hiddenByRoute.logs, false);
  assert.equal(hiddenByRoute.traces, false);
  assert.equal(hiddenByRoute.deployments, false);
});

test('tabViewModels shows every tab to a manager', () => {
  const models = tabViewModels('demo', 'overview', true);
  assert.equal(models.every(m => m.hidden === false), true);
});
