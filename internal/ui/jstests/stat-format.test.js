import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatBytes, headerStats, statusPillClass } from '../static/views/stat-format.js';

test('formatBytes: MB above 1 MiB, KB below, zero/null safe', () => {
  assert.equal(formatBytes(0), '0 KB');
  assert.equal(formatBytes(null), '0 KB');
  assert.equal(formatBytes(512 * 1024), '512 KB');
  assert.equal(formatBytes(128 * (1 << 20)), '128 MB');
});

test('headerStats: a single running app uses its own values', () => {
  const s = headerStats({ status: 'running', cpu_percent: 12.34, rss_bytes: 128 * (1 << 20), sessions: 3 }, 1);
  assert.equal(s.cpu, '12.3%');
  assert.equal(s.ram, '128 MB');
  assert.equal(s.sessions, '3');
  assert.equal(s.replicas, '1 / 1');
  assert.equal(s.multiReplica, false);
});

test('headerStats: not running → CPU/Memory/Sessions are —, Replicas still 0/N', () => {
  const s = headerStats({ status: 'hibernated', cpu_percent: 0, rss_bytes: 0 }, 2);
  assert.equal(s.cpu, '—');
  assert.equal(s.ram, '—');
  assert.equal(s.sessions, '—');
  assert.equal(s.replicas, '0 / 2');
});

test('headerStats: aggregates across replicas (sum cpu/rss/sessions, running count)', () => {
  const m = { status: 'running', replicas: [
    { status: 'running', cpu_percent: 10, rss_bytes: 100 * (1 << 20), sessions: 2 },
    { status: 'running', cpu_percent: 26, rss_bytes: 100 * (1 << 20), sessions: 5 },
    { status: 'stopped', cpu_percent: 0, rss_bytes: 0, sessions: 0 },
  ] };
  const s = headerStats(m, 3);
  assert.equal(s.cpu, '36.0%');
  assert.equal(s.ram, '200 MB');
  assert.equal(s.sessions, '7');
  assert.equal(s.replicas, '2 / 3');
  assert.equal(s.multiReplica, true);
});

test('headerStats: an empty replicas array means zero tracked replicas, not a false 1', () => {
  const s = headerStats({ status: 'running', replicas: [] }, 3);
  assert.equal(s.replicas, '0 / 3');
});

test('headerStats: metrics_available false (Fargate) → CPU/Memory are n/a, Sessions stay real', () => {
  const m = { status: 'running', metrics_available: false, replicas: [
    { status: 'running', cpu_percent: 0, rss_bytes: 0, sessions: 4 },
  ] };
  const s = headerStats(m, 1);
  assert.equal(s.cpu, 'n/a');
  assert.equal(s.ram, 'n/a');
  assert.equal(s.sessions, '4');
  assert.equal(s.replicas, '1 / 1');
});

test('headerStats: falls back to top-level legacy fields when no replicas array', () => {
  const s = headerStats({ status: 'running', cpu_percent: 5, rss_bytes: 50 * (1 << 20), sessions: 1 }, 1);
  assert.equal(s.cpu, '5.0%');
  assert.equal(s.ram, '50 MB');
  assert.equal(s.replicas, '1 / 1');
});

test('statusPillClass: running gets the is-live pulse, other states do not', () => {
  assert.equal(statusPillClass('running'), 'status-pill status-running is-live');
  assert.equal(statusPillClass('hibernated'), 'status-pill status-hibernated');
  assert.equal(statusPillClass('failed'), 'status-pill status-failed');
});
