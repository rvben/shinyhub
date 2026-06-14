import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  relativeTime,
  deploymentRowModel,
  deploymentListModels,
} from '../static/views/deployment-row.js';

// The Deployments tab marks the LIVE deployment and suppresses its Roll back
// button. "Live" is the newest *succeeded* deployment — not the newest row and
// not current_version (which is newest-regardless-of-status) — because ShinyHub
// auto-reverts a failed deploy, so a failed/pending latest attempt does not
// change the running bundle.

const NOW = Date.UTC(2026, 5, 14, 12, 0, 0); // fixed clock for relativeTime

test('relativeTime renders compact buckets and tolerates bad input', () => {
  assert.equal(relativeTime(new Date(NOW - 30 * 1000), NOW), '30s ago');
  assert.equal(relativeTime(new Date(NOW - 5 * 60 * 1000), NOW), '5m ago');
  assert.equal(relativeTime(new Date(NOW - 3 * 3600 * 1000), NOW), '3h ago');
  assert.equal(relativeTime(new Date(NOW - 2 * 86400 * 1000), NOW), '2d ago');
  assert.equal(relativeTime(null, NOW), '');
  assert.equal(relativeTime('not-a-date', NOW), '');
});

test('a current succeeded deployment shows its release label and blocks its own rollback', () => {
  const m = deploymentRowModel(
    { id: 9, version: '300', release_number: 3, created_at: NOW - 60000, status: 'succeeded' },
    { isCurrent: true, now: NOW },
  );
  assert.equal(m.isCurrent, true);
  assert.equal(m.canRollback, false);
  assert.equal(m.releaseNumber, 3);
  assert.equal(m.releaseLabel, 'v3');
  assert.equal(m.version, '300'); // epoch kept for the hover/title
});

test('a non-current succeeded deployment offers rollback', () => {
  const m = deploymentRowModel(
    { id: 8, version: '200', release_number: 2, created_at: NOW - 120000, status: 'succeeded' },
    { isCurrent: false, now: NOW },
  );
  assert.equal(m.canRollback, true);
  assert.equal(m.releaseLabel, 'v2');
});

test('failed and pending deployments have no release label and never offer rollback', () => {
  const failed = deploymentRowModel(
    { id: 7, version: '150', release_number: null, created_at: NOW, status: 'failed', failure_reason: 'boom' },
    { isCurrent: false, now: NOW },
  );
  assert.equal(failed.status, 'failed');
  assert.equal(failed.failureReason, 'boom');
  assert.equal(failed.canRollback, false);
  assert.equal(failed.releaseLabel, ''); // no number for a failed attempt

  const pending = deploymentRowModel(
    { id: 6, version: '140', created_at: NOW, status: 'pending' },
    { isCurrent: false, now: NOW },
  );
  assert.equal(pending.canRollback, false);
  assert.equal(pending.releaseLabel, '');
});

test('deploymentListModels marks the newest SUCCEEDED row live, not a failed newest', () => {
  // Newest-first (id DESC); a failed latest attempt sits above the succeeded live
  // bundle. release_number comes from the server (succeeded only; null for failed).
  const rows = [
    { id: 3, version: '300', release_number: null, created_at: NOW - 1000, status: 'failed', failure_reason: 'bad deps' },
    { id: 2, version: '200', release_number: 2, created_at: NOW - 2000, status: 'succeeded' },
    { id: 1, version: '100', release_number: 1, created_at: NOW - 3000, status: 'succeeded' },
  ];
  const models = deploymentListModels(rows, NOW);
  assert.deepEqual(models.map(m => m.releaseLabel), ['', 'v2', 'v1']);
  // The failed newest row is NOT current; the newest succeeded one (v2) is.
  assert.deepEqual(models.map(m => m.isCurrent), [false, true, false]);
  // Rollback: not on the failed row, not on the live row, yes on the older succeeded.
  assert.deepEqual(models.map(m => m.canRollback), [false, false, true]);
});

test('deploymentListModels marks newest row live when all succeeded', () => {
  const rows = [
    { id: 2, version: '200', release_number: 2, created_at: NOW - 1000, status: 'succeeded' },
    { id: 1, version: '100', release_number: 1, created_at: NOW - 2000, status: 'succeeded' },
  ];
  const models = deploymentListModels(rows, NOW);
  assert.deepEqual(models.map(m => m.releaseLabel), ['v2', 'v1']);
  assert.deepEqual(models.map(m => m.isCurrent), [true, false]);
  assert.deepEqual(models.map(m => m.canRollback), [false, true]);
});
