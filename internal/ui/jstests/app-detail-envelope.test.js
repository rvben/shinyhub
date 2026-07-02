import { test } from 'node:test';
import assert from 'node:assert/strict';
import { normalizeAppEnvelope } from '../static/views/app-detail-envelope.js';

// GET /api/apps/:slug returns a wrapped envelope
// ({ app, replicas_status, can_manage, runtime_mode, resource_enforcement,
//   release_number, released_at, released_version }; see internal/api/apps.go
// handleGetApp). normalizeAppEnvelope folds the envelope-level fields back onto
// the app object so the view reads app.* consistently. Getting this unwrap wrong
// makes every field undefined and silently breaks the detail page (the
// wrapped-response regression class the contract tests guard).

test('normalizeAppEnvelope unwraps body.app and copies envelope fields onto it', () => {
  const body = {
    app: { slug: 'demo', name: 'Demo', deploy_count: 3 },
    replicas_status: [{ index: 0, status: 'running' }],
    can_manage: true,
    runtime_mode: 'docker',
    resource_enforcement: { memory: true, cpu: false },
    release_number: 4,
    released_at: '2026-07-01T00:00:00Z',
    released_version: '1717200000',
  };
  const { app, replicasStatus } = normalizeAppEnvelope(body);
  assert.equal(app.slug, 'demo');
  assert.equal(app.name, 'Demo');
  assert.equal(app.can_manage, true);
  assert.equal(app.runtime_mode, 'docker');
  assert.deepEqual(app.resource_enforcement, { memory: true, cpu: false });
  assert.equal(app.release_number, 4);
  assert.equal(app.released_at, '2026-07-01T00:00:00Z');
  assert.equal(app.released_version, '1717200000');
  assert.deepEqual(replicasStatus, [{ index: 0, status: 'running' }]);
});

test('normalizeAppEnvelope tolerates an unwrapped body (no app key)', () => {
  // Defensive: if the server ever returns the app directly, body.app || body
  // still yields a usable app object.
  const body = { slug: 'demo', name: 'Demo', deploy_count: 0 };
  const { app, replicasStatus } = normalizeAppEnvelope(body);
  assert.equal(app.slug, 'demo');
  assert.deepEqual(replicasStatus, []);
});

test('normalizeAppEnvelope nulls absent release fields (hides the version chip)', () => {
  // A freshly-created app has no succeeded deploy: release_number/released_at/
  // released_version are absent and must normalize to null so the header hides
  // the "vN" chip and the deployed-ago meta rather than showing stale values.
  const body = { app: { slug: 'demo', name: 'Demo', deploy_count: 0 } };
  const { app } = normalizeAppEnvelope(body);
  assert.equal(app.release_number, null);
  assert.equal(app.released_at, null);
  assert.equal(app.released_version, null);
});

test('normalizeAppEnvelope ignores non-conforming envelope field types', () => {
  // Only well-typed envelope fields are copied: a non-boolean can_manage, a
  // non-string runtime_mode, a non-object resource_enforcement, and a
  // non-number release_number are all dropped rather than corrupting app.*.
  const body = {
    app: { slug: 'demo' },
    can_manage: 'yes',
    runtime_mode: 42,
    resource_enforcement: 'nope',
    release_number: '4',
    replicas_status: 'not-an-array',
  };
  const { app, replicasStatus } = normalizeAppEnvelope(body);
  assert.equal('can_manage' in app, false);
  assert.equal('runtime_mode' in app, false);
  assert.equal('resource_enforcement' in app, false);
  assert.equal(app.release_number, null);
  assert.deepEqual(replicasStatus, []);
});

test('normalizeAppEnvelope: falsy released_at/released_version normalize to null', () => {
  const body = { app: { slug: 'demo' }, released_at: '', released_version: '' };
  const { app } = normalizeAppEnvelope(body);
  assert.equal(app.released_at, null);
  assert.equal(app.released_version, null);
});
