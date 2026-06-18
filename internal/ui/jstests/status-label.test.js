import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatStatus } from '../static/views/status-label.js';

test('hibernated reads "Sleeping" — pairs with Waking and the standby color', () => {
  assert.equal(formatStatus('hibernated'), 'Sleeping');
});

test('suspended reads "Paused" — the operator word for a resumable replica', () => {
  assert.equal(formatStatus('suspended'), 'Paused');
});

test('standard ops statuses keep their plain, recognized names', () => {
  assert.equal(formatStatus('running'), 'Running');
  assert.equal(formatStatus('healthy'), 'Healthy');
  assert.equal(formatStatus('deploying'), 'Deploying');
  assert.equal(formatStatus('waking'), 'Waking');
  assert.equal(formatStatus('degraded'), 'Degraded');
  assert.equal(formatStatus('crashed'), 'Crashed');
  assert.equal(formatStatus('stopped'), 'Stopped');
  assert.equal(formatStatus('unknown'), 'Unknown');
});

test('an unmapped wire status falls back to sentence case', () => {
  assert.equal(formatStatus('provisioning'), 'Provisioning');
});

test('an empty or missing status renders nothing', () => {
  assert.equal(formatStatus(''), '');
  assert.equal(formatStatus(undefined), '');
  assert.equal(formatStatus(null), '');
});
