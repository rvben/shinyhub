import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  groupAppsByProject,
  isSidebarAppActive,
  sidebarAppModel,
  renderSidebarApps,
  highlightSidebarApp,
} from '../static/views/sidebar-nav.js';
import { appCardBadge } from '../static/views/app-card-badge.js';

// app.js injects the shared card-badge model as badgeFor; tests reuse the real
// one so the sidebar's status semantics stay identical to the cards'.
const fmt = (s) => s;
const badgeFor = (a) => appCardBadge(a, fmt);

function container() {
  return new JSDOM('<!DOCTYPE html><div id="c"></div>').window.document;
}

test('groupAppsByProject: ungrouped first, named projects sorted, apps sorted by name', () => {
  const apps = [
    { slug: 'z', name: 'Zed' },
    { slug: 'a', name: 'Alpha', project_slug: 'beta' },
    { slug: 'b', name: 'Bravo' },
    { slug: 'c', name: 'Charlie', project_slug: 'alpha' },
  ];
  const groups = groupAppsByProject(apps);
  assert.equal(groups[0].project, null);
  assert.deepEqual(groups[0].apps.map((a) => a.slug), ['b', 'z']);
  assert.equal(groups[1].project, 'alpha');
  assert.equal(groups[2].project, 'beta');
});

test('groupAppsByProject: empty/whitespace project_slug counts as ungrouped', () => {
  const groups = groupAppsByProject([
    { slug: 'a', name: 'A', project_slug: '' },
    { slug: 'b', name: 'B', project_slug: '  ' },
  ]);
  assert.equal(groups.length, 1);
  assert.equal(groups[0].project, null);
  assert.equal(groups[0].apps.length, 2);
});

test('isSidebarAppActive: own page and nested tabs, segment-boundary safe', () => {
  assert.equal(isSidebarAppActive('/apps/foo', '/apps/foo'), true);
  assert.equal(isSidebarAppActive('/apps/foo', '/apps/foo/logs'), true);
  assert.equal(isSidebarAppActive('/apps/foo', '/apps/foobar'), false);
  assert.equal(isSidebarAppActive('/apps/foo', '/users'), false);
});

test('sidebarAppModel: never-deployed app gets the new (cyan) dot + Awaiting label', () => {
  const m = sidebarAppModel({ slug: 'blank', name: 'Blank', deploy_count: 0, status: 'stopped' }, '/', badgeFor);
  assert.equal(m.dotClass, 'sb-dot sb-dot-new');
  assert.equal(m.statusLabel, 'Awaiting deploy');
  assert.equal(m.href, '/apps/blank');
});

test('sidebarAppModel: failed-only app gets the failed dot (not mislabelled awaiting)', () => {
  const m = sidebarAppModel(
    { slug: 'x', name: 'X', deploy_count: 0, last_deployment_status: 'failed', status: 'stopped' },
    '/', badgeFor,
  );
  assert.equal(m.dotClass, 'sb-dot sb-dot-failed');
  assert.equal(m.statusLabel, 'Failed');
});

test('sidebarAppModel: deployed app uses its live status dot and is active on a nested tab', () => {
  const m = sidebarAppModel({ slug: 'demo', name: 'demo', deploy_count: 3, status: 'running' }, '/apps/demo/logs', badgeFor);
  assert.equal(m.dotClass, 'sb-dot sb-dot-running');
  assert.equal(m.active, true);
});

test('renderSidebarApps: builds grouped rows and marks exactly one active from currentPath', () => {
  const doc = container();
  const c = doc.getElementById('c');
  renderSidebarApps(c, [
    { slug: 'demo', name: 'demo', deploy_count: 1, status: 'running', project_slug: 'default' },
    { slug: 'reports', name: 'reports', deploy_count: 1, status: 'stopped', project_slug: 'default' },
  ], '/apps/demo/logs', badgeFor, doc);
  assert.equal(c.querySelectorAll('a.sidebar-app').length, 2);
  const active = c.querySelectorAll('a.sidebar-app.active');
  assert.equal(active.length, 1);
  assert.equal(active[0].getAttribute('data-app-slug'), 'demo');
  assert.equal(active[0].getAttribute('aria-current'), 'page');
  const projectToggle = c.querySelector('.sidebar-project-group-toggle');
  assert.equal(projectToggle.querySelector('.sidebar-project-group-name').textContent, 'default');
  assert.equal(projectToggle.getAttribute('aria-expanded'), 'true');
  assert.equal(projectToggle.getAttribute('aria-label'), 'default, 2 apps');
});

test('renderSidebarApps: deep-link race — the renderer marks active even when run after the mount', () => {
  // loadAppsIndex is fire-and-forget; it can resolve AFTER the route mounted.
  // The renderer (not a prior highlight call) owns the active row.
  const doc = container();
  const c = doc.getElementById('c');
  renderSidebarApps(c, [{ slug: 'foo', name: 'foo', deploy_count: 1, status: 'running' }], '/apps/foo/logs', badgeFor, doc);
  assert.equal(c.querySelector('a.sidebar-app.active').getAttribute('data-app-slug'), 'foo');
});

test('renderSidebarApps: empty list renders an empty state and no rows', () => {
  const doc = container();
  const c = doc.getElementById('c');
  renderSidebarApps(c, [], '/', badgeFor, doc);
  assert.equal(c.querySelectorAll('a.sidebar-app').length, 0);
  assert.ok(c.querySelector('.sidebar-apps-empty'));
});

test('highlightSidebarApp: moves active in place on navigation, never more than one', () => {
  const doc = container();
  const c = doc.getElementById('c');
  renderSidebarApps(c, [
    { slug: 'demo', name: 'demo', deploy_count: 1, status: 'running' },
    { slug: 'reports', name: 'reports', deploy_count: 1, status: 'running' },
  ], '/apps/demo', badgeFor, doc);
  assert.equal(c.querySelector('.sidebar-app.active').getAttribute('data-app-slug'), 'demo');
  highlightSidebarApp(c, '/apps/reports/logs');
  assert.equal(c.querySelectorAll('.sidebar-app.active').length, 1);
  assert.equal(c.querySelector('.sidebar-app.active').getAttribute('data-app-slug'), 'reports');
});

test('groupAppsByProject: heading uses the project display name and icon', () => {
  const groups = groupAppsByProject([
    { slug: 'a', name: 'A', project_slug: 'team', project_name: 'Team Tools', project_icon_emoji: '🧰' },
  ]);
  assert.equal(groups[0].project, 'team');
  assert.equal(groups[0].name, 'Team Tools');
  assert.equal(groups[0].iconEmoji, '🧰');
});

test('groupAppsByProject: named projects order by display name, not slug', () => {
  const groups = groupAppsByProject([
    { slug: 'a', name: 'A', project_slug: 'zzz', project_name: 'Aaa' },
    { slug: 'b', name: 'B', project_slug: 'aaa', project_name: 'Zzz' },
  ]);
  assert.deepEqual(groups.map((g) => g.project), ['zzz', 'aaa']);
});

test('renderSidebarApps: the project disclosure shows the display name, not the slug', () => {
  const d = container();
  renderSidebarApps(d.getElementById('c'), [
    { slug: 'a', name: 'A', status: 'running', deploy_count: 1, project_slug: 'team', project_name: 'Team Tools' },
  ], '/', badgeFor, d);
  const headings = [...d.querySelectorAll('.sidebar-project-group-name')].map((e) => e.textContent);
  assert.deepEqual(headings, ['Team Tools']);
});

test('renderSidebarApps: projects collapse independently and remember their state', () => {
  const d = container();
  const c = d.getElementById('c');
  renderSidebarApps(c, [
    { slug: 'a', name: 'A', status: 'running', project_slug: 'alpha' },
    { slug: 'b', name: 'B', status: 'running', project_slug: 'beta' },
  ], '/', badgeFor, d);
  const toggles = c.querySelectorAll('.sidebar-project-group-toggle');
  toggles[0].click();
  assert.equal(toggles[0].getAttribute('aria-expanded'), 'false');
  assert.equal(toggles[1].getAttribute('aria-expanded'), 'true');
  assert.equal(c.querySelectorAll('.sidebar-project-group-body')[0].hidden, true);
});

test('highlightSidebarApp reopens the active app project without changing other groups', () => {
  const d = container();
  const c = d.getElementById('c');
  renderSidebarApps(c, [
    { slug: 'a', name: 'A', status: 'running', project_slug: 'alpha' },
    { slug: 'b', name: 'B', status: 'running', project_slug: 'beta' },
  ], '/', badgeFor, d);
  const toggles = c.querySelectorAll('.sidebar-project-group-toggle');
  toggles[0].click();
  toggles[1].click();
  highlightSidebarApp(c, '/apps/a/logs');
  assert.equal(toggles[0].getAttribute('aria-expanded'), 'true');
  assert.equal(toggles[1].getAttribute('aria-expanded'), 'false');
  assert.equal(c.querySelector('.sidebar-app.active').dataset.appSlug, 'a');
});
