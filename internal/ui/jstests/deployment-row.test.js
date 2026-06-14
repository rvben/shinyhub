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

test('a current succeeded deployment blocks its own rollback', () => {
  const m = deploymentRowModel(
    { id: 9, version: '300', created_at: NOW - 60000, status: 'succeeded' },
    { deployNumber: 3, isCurrent: true, now: NOW },
  );
  assert.equal(m.isCurrent, true);
  assert.equal(m.canRollback, false);
  assert.equal(m.deployNumber, '#3');
});

test('a non-current succeeded deployment offers rollback', () => {
  const m = deploymentRowModel(
    { id: 8, version: '200', created_at: NOW - 120000, status: 'succeeded' },
    { deployNumber: 2, isCurrent: false, now: NOW },
  );
  assert.equal(m.canRollback, true);
});

test('failed and pending deployments never offer rollback and carry their status', () => {
  const failed = deploymentRowModel(
    { id: 7, version: '150', created_at: NOW, status: 'failed', failure_reason: 'boom' },
    { deployNumber: 1, isCurrent: false, now: NOW },
  );
  assert.equal(failed.status, 'failed');
  assert.equal(failed.failureReason, 'boom');
  assert.equal(failed.canRollback, false);

  const pending = deploymentRowModel(
    { id: 6, version: '140', created_at: NOW, status: 'pending' },
    { deployNumber: 1, isCurrent: false, now: NOW },
  );
  assert.equal(pending.canRollback, false);
});

test('deploymentListModels marks the newest SUCCEEDED row live, not a failed newest', () => {
  // Newest-first: a failed latest attempt sits above the succeeded live bundle.
  const rows = [
    { id: 3, version: '300', created_at: NOW - 1000, status: 'failed', failure_reason: 'bad deps' },
    { id: 2, version: '200', created_at: NOW - 2000, status: 'succeeded' },
    { id: 1, version: '100', created_at: NOW - 3000, status: 'succeeded' },
  ];
  const models = deploymentListModels(rows, NOW);
  assert.deepEqual(models.map(m => m.deployNumber), ['#3', '#2', '#1']);
  // The failed newest row is NOT current; the newest succeeded one (#2) is.
  assert.deepEqual(models.map(m => m.isCurrent), [false, true, false]);
  // Rollback: not on the failed row, not on the live row, yes on the older succeeded.
  assert.deepEqual(models.map(m => m.canRollback), [false, false, true]);
});

test('deploymentListModels marks newest row live when all succeeded', () => {
  const rows = [
    { id: 2, version: '200', created_at: NOW - 1000, status: 'succeeded' },
    { id: 1, version: '100', created_at: NOW - 2000, status: 'succeeded' },
  ];
  const models = deploymentListModels(rows, NOW);
  assert.deepEqual(models.map(m => m.isCurrent), [true, false]);
  assert.deepEqual(models.map(m => m.canRollback), [false, true]);
});
