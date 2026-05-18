import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  isFleetManaged,
  fleetBadgeText,
  FLEET_BADGE_TOOLTIP,
  shortContentDigest,
  DIGEST_LABEL,
  DIGEST_DISCLAIMER,
  segmentApps,
  makeFleetBadge,
  renderFleetDigest,
} from '../static/views/fleet-ui.js';

test('isFleetManaged: non-empty managed_by string is managed', () => {
  assert.equal(isFleetManaged({ managed_by: 'fleet:eu' }), true);
});

test('isFleetManaged: empty string / missing / null / non-object is unmanaged', () => {
  assert.equal(isFleetManaged({ managed_by: '' }), false);
  assert.equal(isFleetManaged({}), false);
  assert.equal(isFleetManaged({ managed_by: null }), false);
  assert.equal(isFleetManaged(null), false);
  assert.equal(isFleetManaged(undefined), false);
});

test('fleetBadgeText: prefixed managed_by, null when unmanaged', () => {
  assert.equal(fleetBadgeText({ managed_by: 'fleet:eu' }), 'managed by fleet:eu');
  assert.equal(fleetBadgeText({ managed_by: '' }), null);
});

test('FLEET_BADGE_TOOLTIP states the revert contract and the plan command', () => {
  assert.match(FLEET_BADGE_TOOLTIP, /governed by a fleet manifest/);
  assert.match(FLEET_BADGE_TOOLTIP, /reverted on the next `fleet apply`/);
  assert.match(FLEET_BADGE_TOOLTIP, /shinyhub fleet plan -f <your-manifest>/);
});

test('shortContentDigest: strips sha256: prefix and shortens', () => {
  assert.equal(shortContentDigest('sha256:0123456789abcdef0000'), '0123456789ab');
  assert.equal(shortContentDigest('0123456789abcdef'), '0123456789ab');
  assert.equal(shortContentDigest(''), null);
  assert.equal(shortContentDigest(null), null);
  assert.equal(shortContentDigest(undefined), null);
});

test('DIGEST copy says it is not a conformance signal and points at fleet plan', () => {
  assert.equal(DIGEST_LABEL, 'live deployment digest');
  assert.match(DIGEST_DISCLAIMER, /not a manifest-conformance signal/);
  assert.match(DIGEST_DISCLAIMER, /shinyhub fleet plan -f <your-manifest>/);
});

test('segmentApps: all / fleet / unmanaged; defensive on non-array', () => {
  const a = { slug: 'a', managed_by: 'fleet:eu' };
  const b = { slug: 'b', managed_by: '' };
  const apps = [a, b];
  assert.deepEqual(segmentApps(apps, 'all'), [a, b]);
  assert.deepEqual(segmentApps(apps, 'fleet'), [a]);
  assert.deepEqual(segmentApps(apps, 'unmanaged'), [b]);
  assert.deepEqual(segmentApps(apps, 'bogus'), [a, b]);
  assert.deepEqual(segmentApps(null, 'fleet'), []);
  assert.notEqual(segmentApps(apps, 'all'), apps);
});

test('makeFleetBadge: builds a titled badge for managed apps, null otherwise', () => {
  const { document } = new JSDOM('<body></body>').window;
  const el = makeFleetBadge(document, { managed_by: 'fleet:eu' });
  assert.equal(el.tagName, 'SPAN');
  assert.equal(el.className, 'badge badge-fleet');
  assert.equal(el.textContent, 'managed by fleet:eu');
  assert.equal(el.title, FLEET_BADGE_TOOLTIP);
  assert.equal(makeFleetBadge(document, { managed_by: '' }), null);
});

test('renderFleetDigest: populates + reveals for managed app with digest', () => {
  const dom = new JSDOM('<div id="d" hidden></div>');
  const c = dom.window.document.getElementById('d');
  renderFleetDigest(c, {
    managed_by: 'fleet:eu',
    content_digest: 'sha256:0123456789abcdef',
    last_deployed_at: '2026-05-18T10:00:00Z',
  });
  assert.equal(c.hidden, false);
  assert.match(c.textContent, /live deployment digest/);
  assert.match(c.textContent, /0123456789ab/);
  assert.match(c.textContent, /not a manifest-conformance signal/);
});

test('renderFleetDigest: stays hidden + empty when unmanaged or no digest', () => {
  const dom = new JSDOM('<div id="d"></div>');
  const c = dom.window.document.getElementById('d');
  renderFleetDigest(c, { managed_by: '', content_digest: 'sha256:x' });
  assert.equal(c.hidden, true);
  assert.equal(c.textContent, '');
  renderFleetDigest(c, { managed_by: 'fleet:eu', content_digest: '' });
  assert.equal(c.hidden, true);
  assert.equal(c.textContent, '');
});
