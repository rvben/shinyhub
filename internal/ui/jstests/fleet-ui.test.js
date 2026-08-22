import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  isFleetManaged,
  fleetID,
  fleetBadgeText,
  fleetBadgeTooltip,
  FLEET_BADGE_COMPACT_LABEL,
  FLEET_BADGE_TOOLTIP,
  shortContentDigest,
  segmentApps,
  makeFleetBadge,
  makeFleetStateBadge,
  renderFleetBadges,
  fleetFieldLabel,
  formatFleetValue,
  makeFleetStateCard,
} from '../static/views/fleet-ui.js';

test('fleet ownership helpers preserve the id without workflow coaching', () => {
  const app = { managed_by: 'fleet:production-eu' };
  assert.equal(isFleetManaged(app), true);
  assert.equal(fleetID(app), 'production-eu');
  assert.equal(fleetBadgeText(app), 'Fleet · production-eu');
  assert.equal(fleetBadgeTooltip(app), `Fleet production-eu. ${FLEET_BADGE_TOOLTIP}`);
  assert.doesNotMatch(fleetBadgeTooltip(app), /shinyhub|fleet apply|fleet plan/i);
  for (const unmanaged of [{ managed_by: '' }, {}, null, undefined]) {
    assert.equal(isFleetManaged(unmanaged), false);
    assert.equal(fleetID(unmanaged), null);
  }
});

test('compact badge width never depends on an operator-chosen fleet id', () => {
  const { document } = new JSDOM('<body></body>').window;
  const short = makeFleetBadge(document, { managed_by: 'fleet:eu' }, { compact: true });
  const long = makeFleetBadge(document, { managed_by: 'fleet:' + 'a'.repeat(64) }, { compact: true });
  assert.equal(short.textContent, FLEET_BADGE_COMPACT_LABEL);
  assert.equal(short.textContent, long.textContent);
  assert.equal(makeFleetBadge(document, { managed_by: '' }), null);
});

test('shortContentDigest strips sha256 prefix and shortens', () => {
  assert.equal(shortContentDigest('sha256:0123456789abcdef0000'), '0123456789ab');
  assert.equal(shortContentDigest('0123456789abcdef'), '0123456789ab');
  assert.equal(shortContentDigest(''), null);
  assert.equal(shortContentDigest(null), null);
});

test('segmentApps returns all, fleet, and unmanaged slices defensively', () => {
  const a = { slug: 'a', managed_by: 'fleet:eu' };
  const b = { slug: 'b', managed_by: '' };
  const apps = [a, b];
  assert.deepEqual(segmentApps(apps, 'fleet'), [a]);
  assert.deepEqual(segmentApps(apps, 'unmanaged'), [b]);
  assert.deepEqual(segmentApps(apps, 'bogus'), apps);
  assert.notEqual(segmentApps(apps, 'bogus'), apps);
  assert.deepEqual(segmentApps(null, 'fleet'), []);
});

test('header renders neutral ownership plus only meaningful convergence state', () => {
  const dom = new JSDOM('<span id="badges"></span>');
  const host = dom.window.document.getElementById('badges');
  const app = { managed_by: 'fleet:production' };
  renderFleetBadges(host, app, { status: 'temporary_changes' });
  assert.equal(host.hidden, false);
  assert.equal(host.children.length, 2);
  assert.equal(host.children[0].textContent, 'Fleet · production');
  assert.equal(host.children[1].textContent, 'Temporary changes');
  assert.equal(host.children[1].getAttribute('role'), 'status');

  renderFleetBadges(host, app, { status: 'in_sync' });
  assert.equal(host.children.length, 1);
  assert.equal(makeFleetStateBadge(dom.window.document, { status: 'in_sync' }), null);
});

test('fleet values are readable while retaining exact comparison semantics', () => {
  assert.equal(fleetFieldLabel('max_sessions_per_replica'), 'Session cap');
  assert.equal(formatFleetValue('memory_limit_mb', '1024'), '1024 MB');
  assert.equal(formatFleetValue('cpu_quota_percent', '150'), '150%');
  assert.equal(formatFleetValue('visibility', 'private'), 'Private');
  assert.equal(formatFleetValue('name', '"Revenue Pulse"'), 'Revenue Pulse');
  assert.equal(formatFleetValue('project', '""'), 'None');
  assert.equal(formatFleetValue('source', 'sha256:0123456789abcdef'), '0123456789ab');
  assert.equal(formatFleetValue('identity_headers', '(default)'), 'default');
});

test('temporary changes card is always reviewable and has no hide control', () => {
  const { document } = new JSDOM('<body></body>').window;
  const card = makeFleetStateCard(document, {
    status: 'temporary_changes',
    fleet_id: 'production',
    application: {
      applied_at: '2026-08-22T08:00:00Z',
      provenance: { source: { label: 'GitLab pipeline #42' } },
    },
    changes: [
      { key: 'replicas', current: '4', fleet: '2' },
      { key: 'memory_limit_mb', current: '2048', fleet: '1024' },
    ],
  }, {
    configurationHref: '/apps/demo/configuration',
    relativeTime: () => '18 min ago',
  });
  assert.equal(card.className, 'overview-card overview-fleet-state fleet-change-review');
  assert.match(card.textContent, /Temporary changes/);
  assert.match(card.textContent, /next fleet convergence will restore them/i);
  assert.match(card.textContent, /2 changes/);
  assert.match(card.textContent, /Changed outside fleet convergence/);
  assert.match(card.textContent, /GitLab pipeline #42/);
  assert.equal(card.querySelectorAll('tbody tr').length, 2);
  assert.equal(card.querySelector('a').getAttribute('href'), '/apps/demo/configuration');
  assert.doesNotMatch(card.textContent, /hide changes|fleet apply|shinyhub/i);
});

test('incomplete convergence is distinct and details are progressively disclosed', () => {
  const { document } = new JSDOM('<body></body>').window;
  const card = makeFleetStateCard(document, {
    status: 'incomplete',
    fleet_id: 'production',
    error: 'worker pool update failed',
    attempt: {
      run_id: '0123456789abcdef',
      created_at: '2026-08-22T09:00:00Z',
      provenance: { provider: 'github' },
    },
    application: { run_id: 'previous' },
  }, { relativeTime: () => '8 min ago' });
  assert.ok(card.classList.contains('fleet-convergence-card'));
  assert.match(card.textContent, /Fleet convergence incomplete/);
  assert.match(card.textContent, /worker pool update failed/);
  assert.equal(card.querySelector('details').open, false);
  assert.equal(card.querySelector('summary').textContent, 'Review status');
  assert.equal(makeFleetStateCard(document, { status: 'in_sync' }), null);
});
