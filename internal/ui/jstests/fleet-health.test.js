import { test } from 'node:test';
import assert from 'node:assert/strict';
import { summariseFleetHealth } from '../static/views/fleet-health.js';

test('summariseFleetHealth: all-healthy → running/green', () => {
  const s = summariseFleetHealth({
    apps: { total: 5, running: 5, stopped: 0, degraded: 0 },
    replicas: { running: 12, lost: 0, stopped: 0 },
    workers: { total: 2, up: 2, down: 0, revoked: 0 },
    tiers: [{ tier: 'local', runtime: 'native', replicas_running: 12, replicas_lost: 0 }],
    degraded_apps: [],
  });
  assert.equal(s.statusClass, 'running');
  assert.equal(s.statusLabel, 'healthy');
  assert.match(s.headline, /5 apps/);
  assert.match(s.headline, /2\/2 workers up/);
  assert.equal(s.tierChips.length, 0);
});

test('summariseFleetHealth: lost replicas → lost/red + tier chip + degraded list', () => {
  const s = summariseFleetHealth({
    apps: { total: 7, running: 6, stopped: 1, degraded: 1 },
    replicas: { running: 20, lost: 2, stopped: 3 },
    workers: { total: 3, up: 2, down: 1, revoked: 0 },
    tiers: [
      { tier: 'local', runtime: 'native', replicas_running: 12, replicas_lost: 0 },
      { tier: 'remote', runtime: 'remote_docker', replicas_running: 8, replicas_lost: 2, workers_down: 1 },
    ],
    degraded_apps: [{ slug: 'dash', tier: 'remote', lost: 2, reason: 'worker unavailable' }],
  });
  assert.equal(s.statusClass, 'lost');
  assert.equal(s.statusLabel, 'degraded');
  assert.match(s.headline, /1 degraded/);
  assert.match(s.headline, /2 replicas lost/);
  assert.equal(s.tierChips.length, 1);
  assert.equal(s.tierChips[0].tier, 'remote');
  assert.equal(s.tierChips[0].lost, 2);
  assert.equal(s.degraded[0].slug, 'dash');
});

test('summariseFleetHealth: worker down but no lost replicas → stopped/amber warning', () => {
  const s = summariseFleetHealth({
    apps: { total: 3, running: 3, stopped: 0, degraded: 0 },
    replicas: { running: 6, lost: 0, stopped: 0 },
    workers: { total: 2, up: 1, down: 1, revoked: 0 },
    tiers: [{ tier: 'remote', runtime: 'remote_docker', replicas_running: 6, replicas_lost: 0, workers_down: 1 }],
    degraded_apps: [],
  });
  assert.equal(s.statusClass, 'stopped');
  assert.equal(s.statusLabel, 'warning');
  assert.equal(s.tierChips.length, 1); // surfaced because a worker is down
});

test('summariseFleetHealth: no workers block (worker hosting off) is fine', () => {
  const s = summariseFleetHealth({
    apps: { total: 2, running: 2, degraded: 0 },
    replicas: { running: 4, lost: 0 },
    tiers: [],
    degraded_apps: [],
  });
  assert.equal(s.statusClass, 'running');
  assert.doesNotMatch(s.headline, /workers/);
});

test('summariseFleetHealth: tolerates null/empty input', () => {
  const s = summariseFleetHealth(null);
  assert.equal(s.statusClass, 'running');
  assert.equal(s.tierChips.length, 0);
  assert.equal(s.degraded.length, 0);
});
