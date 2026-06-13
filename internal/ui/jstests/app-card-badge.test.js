import { test } from 'node:test';
import assert from 'node:assert/strict';
import { appCardBadge } from '../static/views/app-card-badge.js';

// Stub for app.js's formatStatus so the helper stays pure and testable.
const fmt = (s) => `S:${s}`;

test('a failed-only deploy badges as Failed, not Awaiting deploy', () => {
  const b = appCardBadge(
    { deploy_count: 0, last_deployment_status: 'failed', status: 'stopped' },
    fmt,
  );
  assert.equal(b.text, 'Failed');
  assert.equal(b.cls, 'badge badge-failed');
});

test('a never-deployed app badges as Awaiting deploy', () => {
  const b = appCardBadge(
    { deploy_count: 0, last_deployment_status: '', status: 'stopped' },
    fmt,
  );
  assert.equal(b.text, 'Awaiting deploy');
  assert.equal(b.cls, 'badge badge-new');
});

test('a successfully deployed app uses its live status', () => {
  const b = appCardBadge({ deploy_count: 2, status: 'running' }, fmt);
  assert.equal(b.text, 'S:running');
  assert.equal(b.cls, 'badge badge-running');
});

test('a later failed deploy on a live app keeps the live status', () => {
  const b = appCardBadge(
    { deploy_count: 1, last_deployment_status: 'failed', status: 'running' },
    fmt,
  );
  assert.equal(b.text, 'S:running');
});
