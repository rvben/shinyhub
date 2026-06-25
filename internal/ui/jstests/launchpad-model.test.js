import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildLaunchpadModel, launchReadiness, appAvatar } from '../static/views/launchpad-model.js';

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

test('buildLaunchpadModel: groups by project; suppresses recently-opened for a small catalog', () => {
  const apps = [
    { slug: 'b', name: 'Beta', project_slug: 'team', status: 'running', deploy_count: 1, description: 'Beta app' },
    { slug: 'a', name: 'Alpha', project_slug: 'team', status: 'hibernated', deploy_count: 1 },
    { slug: 'z', name: 'Zeta', project_slug: 'default', status: 'running', deploy_count: 1 },
  ];
  const m = buildLaunchpadModel(apps, ['z', 'a']);
  assert.equal(m.total, 3);
  // A 3-app catalog fits on screen, so "recently opened" would only echo the grid
  // right below it. It stays empty until the catalog is large enough to scan.
  assert.deepEqual(m.recent, []);
  // Groups: 'default' before 'team' (alphabetical); apps alphabetical within.
  assert.deepEqual(m.groups.map((g) => g.project), ['default', 'team']);
  assert.deepEqual(m.groups[1].apps.map((t) => t.name), ['Alpha', 'Beta']);
  assert.equal(m.groups[1].apps.find((t) => t.slug === 'b').description, 'Beta app');
});

test('buildLaunchpadModel: a large catalog surfaces recently-opened in order, capped at 6', () => {
  // 9 apps (> the suppression threshold), so recents are a genuine shortcut.
  const apps = Array.from({ length: 9 }, (_, i) => ({
    slug: `app-${i}`, name: `App ${i}`, project_slug: 'default', status: 'running', deploy_count: 1,
  }));
  const recent = ['app-8', 'app-3', 'app-1', 'app-7', 'app-2', 'app-5', 'app-0']; // 7 opened
  const m = buildLaunchpadModel(apps, recent);
  assert.equal(m.total, 9);
  // Most-recent-first, capped at 6, only slugs that still exist.
  assert.deepEqual(m.recent.map((t) => t.slug), ['app-8', 'app-3', 'app-1', 'app-7', 'app-2', 'app-5']);
});
