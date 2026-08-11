import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildLaunchpadModel, launchReadiness, appAvatar } from '../static/views/launchpad-model.js';
import { GROUP_ORDER_FIXTURE, GROUP_ORDER_EXPECTED } from './group-order-fixture.js';

test('launchReadiness: openable states carry no status label (no sleeping/running detail)', () => {
  // A viewer never sees internal state - running, hibernated, waking, deploying,
  // and degraded all collapse to "openable, no label".
  for (const status of ['running', 'healthy', 'hibernated', 'suspended', 'deploying', 'waking', 'degraded']) {
    assert.deepEqual(launchReadiness({ status }), { openable: true, label: '' },
      `${status} should be openable with no label`);
  }
});

test('launchReadiness: only an app the viewer cannot open is flagged Unavailable', () => {
  for (const status of ['crashed', 'stopped', 'unknown', '']) {
    assert.deepEqual(launchReadiness({ status }), { openable: false, label: 'Unavailable' },
      `${status} should be not-openable and labelled`);
  }
});

test('launchReadiness: status is authoritative, not soft counters or a digest legacy apps may lack', () => {
  // A running app with a stale deploy_count and no content_digest (a pre-digest
  // legacy deployment) is still openable - status alone proves a live bundle.
  assert.deepEqual(launchReadiness({ status: 'running', deploy_count: 0 }), { openable: true, label: '' });
});

test('launchReadiness: a hibernated app whose LATEST deploy failed stays openable (prior bundle is live)', () => {
  assert.deepEqual(launchReadiness({ status: 'hibernated', last_deployment_status: 'failed' }),
    { openable: true, label: '' });
});

test('appAvatar: deterministic initials and hue from name/slug', () => {
  const a = appAvatar({ name: 'Sales Dashboard', slug: 'sales-dash' });
  assert.equal(a.initials, 'SD');
  assert.ok(a.hue >= 0 && a.hue < 360);
  // Stable across calls for the same slug.
  assert.equal(appAvatar({ name: 'Sales Dashboard', slug: 'sales-dash' }).hue, a.hue);
  // Single-word name -> single initial; slug drives the hue.
  assert.equal(appAvatar({ name: 'demo', slug: 'demo' }).initials, 'D');
});

test('tile model carries emoji', () => {
  const apps = [
    { slug: 'a', name: 'Alpha', status: 'running', deploy_count: 1, icon_emoji: '🚀' },
    { slug: 'b', name: 'Beta', status: 'running', deploy_count: 1 },
  ];
  const m = buildLaunchpadModel(apps, []);
  const tiles = m.groups[0].apps;
  assert.equal(tiles.find((t) => t.slug === 'a').emoji, '🚀', 'an app with icon_emoji carries it on the tile');
  assert.equal(tiles.find((t) => t.slug === 'b').emoji, '', 'an app without icon_emoji carries an empty string');
});

test('buildLaunchpadModel: groups by project; suppresses recently-opened for a small catalog', () => {
  const apps = [
    { slug: 'b', name: 'Beta', project_slug: 'team', status: 'running', deploy_count: 1, description: 'Beta app' },
    { slug: 'a', name: 'Alpha', project_slug: 'team', status: 'hibernated', deploy_count: 1 },
    { slug: 'z', name: 'Zeta', status: 'running', deploy_count: 1 },
  ];
  const m = buildLaunchpadModel(apps, ['z', 'a']);
  assert.equal(m.total, 3);
  // A 3-app catalog fits on screen, so "recently opened" would only echo the grid
  // right below it. It stays empty until the catalog is large enough to scan.
  assert.deepEqual(m.recent, []);
  // Groups: ungrouped first, then named projects by display name; apps alphabetical within.
  assert.deepEqual(m.groups.map((g) => g.project), ['', 'team']);
  assert.deepEqual(m.groups[1].apps.map((t) => t.name), ['Alpha', 'Beta']);
  assert.equal(m.groups[1].apps.find((t) => t.slug === 'b').description, 'Beta app');
});

test('buildLaunchpadModel: a large catalog surfaces recently-opened in order, capped at 6', () => {
  // 9 apps (> the suppression threshold), so recents are a genuine shortcut.
  const apps = Array.from({ length: 9 }, (_, i) => ({
    slug: `app-${i}`, name: `App ${i}`, status: 'running', deploy_count: 1,
  }));
  const recent = ['app-8', 'app-3', 'app-1', 'app-7', 'app-2', 'app-5', 'app-0']; // 7 opened
  const m = buildLaunchpadModel(apps, recent);
  assert.equal(m.total, 9);
  // Most-recent-first, capped at 6, only slugs that still exist.
  assert.deepEqual(m.recent.map((t) => t.slug), ['app-8', 'app-3', 'app-1', 'app-7', 'app-2', 'app-5']);
});

test('buildLaunchpadModel: ungrouped first under no name, never a synthetic default', () => {
  const m = buildLaunchpadModel([
    { slug: 'z', name: 'Zeta', status: 'running', deploy_count: 1 },
    { slug: 'b', name: 'Beta', project_slug: 'team', project_name: 'Team Tools', project_icon_emoji: '🧰', status: 'running', deploy_count: 1 },
  ], []);
  assert.deepEqual(m.groups.map((g) => g.project), ['', 'team']);
  assert.equal(m.groups[0].name, '');
  assert.equal(m.groups[1].name, 'Team Tools');
  assert.equal(m.groups[1].iconEmoji, '🧰');
  // The old synthetic 'default' bucket is gone in every case.
  assert.ok(!m.groups.some((g) => g.project === 'default'));
});

test('buildLaunchpadModel: heading suppressed when ungrouped is the only group', () => {
  const m = buildLaunchpadModel([{ slug: 'a', name: 'A', status: 'running', deploy_count: 1 }], []);
  assert.equal(m.groups.length, 1);
  assert.equal(m.groups[0].showHeading, false);
});

test('buildLaunchpadModel: ungrouped gets a heading once a named project exists', () => {
  const m = buildLaunchpadModel([
    { slug: 'a', name: 'A', status: 'running', deploy_count: 1 },
    { slug: 'b', name: 'B', project_slug: 'team', status: 'running', deploy_count: 1 },
  ], []);
  assert.equal(m.groups[0].showHeading, true);
  assert.equal(m.groups[1].showHeading, true);
});

test('buildLaunchpadModel: an unnamed project falls back to its slug', () => {
  const m = buildLaunchpadModel([
    { slug: 'b', name: 'B', project_slug: 'team', status: 'running', deploy_count: 1 },
  ], []);
  assert.equal(m.groups[0].name, 'team');
});

test('buildLaunchpadModel: a lone named project still gets its heading', () => {
  // Only the UNGROUPED lone group is anonymous enough to suppress; a named
  // project's heading is the organisation the operator asked for.
  const m = buildLaunchpadModel([
    { slug: 'b', name: 'B', project_slug: 'team', project_name: 'Team', status: 'running', deploy_count: 1 },
  ], []);
  assert.equal(m.groups[0].showHeading, true);
});

test('buildLaunchpadModel: same group order as the grid for the same fixture', () => {
  // The Launchpad half of the spec's cross-view ordering requirement. It uses
  // the SAME imported fixture and the SAME expected order as
  // app-grid-groups.test.js (Task 15), so the two views are compared against one
  // input rather than two that merely look alike.
  const m = buildLaunchpadModel(GROUP_ORDER_FIXTURE, []);
  assert.deepEqual(m.groups.map((g) => g.project), GROUP_ORDER_EXPECTED);
});
