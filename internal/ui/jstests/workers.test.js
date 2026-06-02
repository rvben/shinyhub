import { test } from 'node:test';
import assert from 'node:assert/strict';
import { workerDisplay } from '../static/views/workers.js';

test('workerDisplay maps an up worker to a healthy badge', () => {
  const d = workerDisplay({ node_id: 'node-1', name: 'w1', tier: 'remote', status: 'up', version: 'v1.2.3' });
  assert.equal(d.node, 'w1 (node-1)');
  assert.equal(d.tier, 'remote');
  assert.equal(d.statusText, 'up');
  assert.equal(d.statusClass, 'running');
  assert.equal(d.version, 'v1.2.3');
});

test('workerDisplay maps a down worker to a lost badge', () => {
  const d = workerDisplay({ node_id: 'node-2', tier: 'remote', status: 'down' });
  assert.equal(d.statusText, 'down');
  assert.equal(d.statusClass, 'lost');
});

test('workerDisplay shows "revoked" regardless of raw status', () => {
  const d = workerDisplay({ node_id: 'node-3', status: 'up', revoked: true });
  assert.equal(d.statusText, 'revoked');
  assert.equal(d.statusClass, 'lost');
});

test('workerDisplay falls back to node_id when name is absent', () => {
  const d = workerDisplay({ node_id: 'node-4', status: 'up' });
  assert.equal(d.node, 'node-4');
});

test('workerDisplay tolerates missing/empty fields', () => {
  const d = workerDisplay({});
  assert.equal(d.node, 'unknown');
  assert.equal(d.tier, '-');
  assert.equal(d.version, '-');
  assert.equal(d.statusClass, 'stopped');
});

test('workerDisplay tolerates null input', () => {
  const d = workerDisplay(null);
  assert.equal(d.node, 'unknown');
  assert.equal(d.statusClass, 'stopped');
});
