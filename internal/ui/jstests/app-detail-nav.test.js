import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  TAB_ROUTES,
  MANAGER_ONLY_TABS,
  resolveDetailAccess,
  SETTINGS_PAGES,
  settingsRouteParams,
  isSameAppSettingsDestination,
} from '../static/views/app-detail-nav.js';

// Contextual navigation keeps the existing renderer permissions while mapping
// individual settings URLs onto the shared settings form.

test('TAB_ROUTES and MANAGER_ONLY_TABS match the app-detail contract', () => {
  assert.deepEqual(TAB_ROUTES, [
    'overview', 'usage', 'logs', 'traces', 'deployments', 'schedules', 'configuration', 'data', 'access',
  ]);
  assert.equal(MANAGER_ONLY_TABS.has('usage'), true);
  assert.equal(MANAGER_ONLY_TABS.has('configuration'), true);
  assert.equal(MANAGER_ONLY_TABS.has('data'), true);
  assert.equal(MANAGER_ONLY_TABS.has('access'), true);
  assert.equal(MANAGER_ONLY_TABS.has('overview'), false);
  assert.equal(MANAGER_ONLY_TABS.has('logs'), true);
  assert.equal(MANAGER_ONLY_TABS.has('schedules'), false);
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

test('resolveDetailAccess returns a pure viewer to canonical Apps', () => {
  const r = resolveDetailAccess({
    user: { role: 'viewer' }, canManage: false, requestedTab: 'logs', slug: 'demo',
  });
  assert.deepEqual(r.redirect, { path: '/apps', replace: true });
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
    user: { role: 'developer' }, canManage: false, requestedTab: 'traces', slug: 'demo',
  });
  assert.equal(r.tab, 'traces');
  assert.equal(r.redirect, null);
});

test('log routes follow the backend manager boundary, including after permissions change', () => {
  for (const role of ['developer', 'operator', 'admin', 'viewer']) {
    assert.equal(resolveDetailAccess({user:{role}, canManage:true, requestedTab:'logs', slug:'demo'}).redirect, null);
    assert.deepEqual(resolveDetailAccess({user:{role}, canManage:false, requestedTab:'logs', slug:'demo'}).redirect,
      {path:role === 'viewer' ? '/apps' : '/apps/demo', replace:true});
  }
});

test('resolveDetailAccess: the viewer / redirect wins over the manager-only tab redirect', () => {
  // A pure viewer requesting a manager-only tab is returned to /apps (the page-level
  // gate), not to /apps/<slug>. The page gate runs first in the original code.
  const r = resolveDetailAccess({
    user: { role: 'viewer' }, canManage: false, requestedTab: 'access', slug: 'demo',
  });
  assert.deepEqual(r.redirect, { path: '/apps', replace: true });
});

test('resolveDetailAccess tolerates a null user (unauthenticated render)', () => {
  const r = resolveDetailAccess({
    user: null, canManage: false, requestedTab: 'overview', slug: 'demo',
  });
  assert.equal(r.tab, 'overview');
  assert.equal(r.redirect, null);
});

test('settings pages map to the shared configuration renderer except Access', () => {
  assert.deepEqual(SETTINGS_PAGES, ['general', 'environment', 'resources', 'scaling', 'access', 'privacy']);
  for (const section of SETTINGS_PAGES) {
    const params = {slug:'forecast', section, extra:'kept'};
    assert.deepEqual(settingsRouteParams(params), {slug:'forecast', section, extra:'kept', tab:section === 'access' ? 'access' : 'configuration'});
    assert.equal(Object.hasOwn(params, 'tab'), false, 'route input is not mutated');
  }
});

test('unknown or omitted settings pages resolve to General and replace stale renderer keys', () => {
  for (const section of [undefined, '', 'unknown', 'files', 'Access', 'overview', '../access']) {
    assert.deepEqual(settingsRouteParams({slug:'forecast', section, tab:'logs'}), {slug:'forecast', section:'general', tab:'configuration'});
  }
  assert.deepEqual(settingsRouteParams(), {tab:'configuration', section:'general'});
});

test('settings destinations preserve the current app mount for every page and legacy settings links', () => {
  for (const section of [...SETTINGS_PAGES, 'future-page']) {
    assert.equal(isSameAppSettingsDestination(`/apps/forecast/settings/${section}`, 'forecast'), true);
    assert.equal(isSameAppSettingsDestination(`/apps/forecast/settings/${section}?mode=edit#field`, 'forecast'), true);
  }
  for (const legacy of ['configuration', 'access']) {
    assert.equal(isSameAppSettingsDestination(`/apps/forecast/${legacy}`, 'forecast'), true);
    assert.equal(isSameAppSettingsDestination(`/apps/forecast/${legacy}?mode=edit#field`, 'forecast'), true);
  }
});

test('settings destinations compare decoded app identifiers exactly once', () => {
  for (const slug of ['sales forecast', 'café', 'a/b', 'sales%2Fforecast', 'forecast?#']) {
    const encoded = encodeURIComponent(slug);
    assert.equal(isSameAppSettingsDestination(`/apps/${encoded}/settings/resources`, slug), true);
    assert.equal(isSameAppSettingsDestination(`/apps/${encoded}/access`, slug), true);
    assert.equal(isSameAppSettingsDestination(`/apps/${encoded}/settings/resources`, `${slug}-other`), false);
  }
  assert.equal(isSameAppSettingsDestination('/apps/%66orecast/settings/%61ccess', 'forecast'), true);
  assert.equal(isSameAppSettingsDestination('/apps/sales%252Fforecast/settings/general', 'sales/forecast'), false, 'double decoding would confuse distinct app identities');
});

test('unrelated, external, nested, or malformed destinations leave the settings mount', () => {
  const paths = [
    '/apps', '/apps/forecast', '/apps/other/settings/general',
    '/apps/forecast/overview', '/apps/forecast/usage', '/apps/forecast/logs', '/apps/forecast/data',
    '/apps/forecast/settings/resources/', '/apps/forecast/configuration/', '/apps/forecast/access/',
    '/apps/forecast/settings', '/apps/forecast/settings/', '/apps/forecast/settings/general/extra',
    '/apps/forecast/configuration/extra', '/apps/forecast/access/extra',
    '/apps/forecast/settings/general/../../logs', '/apps/forecast/settings/a%2Fb',
    '/apps/%E0%A4%A/settings/general', '/apps/forecast/settings/%ZZ',
    'https://example.com/apps/forecast/settings/general', '//example.com/apps/forecast/settings/general',
    '/\t/example.com/apps/forecast/settings/general',
    'apps/forecast/settings/general', '/apps/forecast\\settings/general', '', null, undefined,
  ];
  for (const path of paths) assert.equal(isSameAppSettingsDestination(path, 'forecast'), false, `unexpected settings destination: ${path}`);
  assert.equal(isSameAppSettingsDestination('/apps/forecast/settings/general', ''), false);
  assert.equal(isSameAppSettingsDestination('/apps/forecast/settings/general', null), false);
});

test('the settings URL mapping preserves manager-only permission gates', () => {
  for (const section of SETTINGS_PAGES) {
    const params = settingsRouteParams({slug:'forecast', section});
    assert.deepEqual(resolveDetailAccess({user:{role:'developer'}, canManage:false, requestedTab:params.tab, slug:params.slug}).redirect, {path:'/apps/forecast', replace:true});
    assert.equal(resolveDetailAccess({user:{role:'viewer'}, canManage:true, requestedTab:params.tab, slug:params.slug}).redirect, null);
  }
});
