import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatBytes, formatCoreCount, formatCoreCapacity, headerStats, statusPillClass } from '../static/views/stat-format.js';

test('formatBytes: MB above 1 MiB, KB below, zero/null safe', () => {
  assert.equal(formatBytes(0), '0 KB');
  assert.equal(formatBytes(null), '0 KB');
  assert.equal(formatBytes(512 * 1024), '512 KB');
  assert.equal(formatBytes(128 * (1 << 20)), '128 MB');
});

test('headerStats: a single running app uses its own values', () => {
  const s = headerStats({ status: 'running', cpu_percent: 12.34, rss_bytes: 128 * (1 << 20), sessions: 3 }, 1);
  assert.equal(s.cpu, '0.123 cores');
  assert.equal(s.ram, '128 MB');
  assert.equal(s.sessions, '3');
  assert.equal(s.replicas, '1 / 1');
  assert.equal(s.runningCount, 1);
  assert.equal(s.desiredCount, 1);
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
  assert.equal(s.cpu, '0.36 cores');
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
  assert.equal(s.cpu, '0.05 cores');
  assert.equal(s.ram, '50 MB');
  assert.equal(s.replicas, '1 / 1');
});

test('headerStats: a running replica with no rate yet makes the CPU total —', () => {
  // Summing only the replicas that reported would show 10% for an app doing 10%
  // plus an unknown amount, and it would happen on every deploy, when the number
  // is being watched most closely. Memory needs no baseline and stays real.
  const m = { status: 'running', replicas: [
    { status: 'running', cpu_percent: 10, rss_bytes: 100 * (1 << 20), sessions: 2 },
    { status: 'running', cpu_percent: null, rss_bytes: 100 * (1 << 20), sessions: 1 },
  ] };
  const s = headerStats(m, 2);
  assert.equal(s.cpu, '—');
  assert.equal(s.ram, '200 MB');
  assert.equal(s.sessions, '3');
});

test('headerStats: a stopped replica without a rate does not blank the total', () => {
  // A replica that is not running was never going to contribute CPU, so its
  // missing rate says nothing about whether the running total is complete.
  const m = { status: 'running', replicas: [
    { status: 'running', cpu_percent: 12, rss_bytes: 1 << 20, sessions: 1 },
    { status: 'stopped', cpu_percent: null, rss_bytes: 0, sessions: 0 },
  ] };
  assert.equal(headerStats(m, 2).cpu, '0.12 cores');
});

test('headerStats: a stopped replica with an old rate does not inflate the total', () => {
  const m = { status: 'running', replicas: [
    { status: 'running', cpu_percent: 12, rss_bytes: 1 << 20, sessions: 1 },
    { status: 'stopped', cpu_percent: 200, rss_bytes: 1 << 20, sessions: 3 },
  ] };
  const s = headerStats(m, 2);
  assert.equal(s.cpu, '0.12 cores');
  assert.equal(s.ram, '1 MB');
  assert.equal(s.sessions, '1');
});

test('headerStats: a draining worker keeps its sessions and memory in the totals', () => {
  const m = { status: 'running', replicas: [
    { status: 'running', cpu_percent: 10, rss_bytes: 1 << 20, sessions: 2 },
    { status: 'draining', cpu_percent: 40, rss_bytes: 3 << 20, sessions: 5 },
    { status: 'stopped', cpu_percent: 90, rss_bytes: 7 << 20, sessions: 9 },
  ] };
  const s = headerStats(m, 1);
  assert.equal(s.sessions, '7');
  assert.equal(s.ram, '4 MB');
  assert.equal(s.cpu, '0.1 cores');
  assert.equal(s.replicas, '1 / 1');
});

test('headerStats: a draining worker without a rate does not blank CPU', () => {
  const m = { status: 'running', replicas: [
    { status: 'running', cpu_percent: 10, rss_bytes: 1 << 20, sessions: 1 },
    { status: 'draining', cpu_percent: null, rss_bytes: 1 << 20, sessions: 1 },
  ] };
  assert.equal(headerStats(m, 1).cpu, '0.1 cores');
});

test('headerStats: — and n/a are different claims', () => {
  // n/a: this tier never reports live CPU. —: the number is not in yet.
  const pending = { status: 'running', replicas: [
    { status: 'running', cpu_percent: null, rss_bytes: 1 << 20, sessions: 0 },
  ] };
  assert.equal(headerStats(pending, 1).cpu, '—');

  const unsupported = { status: 'running', metrics_available: false, replicas: [
    { status: 'running', cpu_percent: null, rss_bytes: 0, sessions: 0 },
  ] };
  assert.equal(headerStats(unsupported, 1).cpu, 'n/a');
});

test('headerStats: a null legacy cpu_percent reads as — , not 0.0%', () => {
  const s = headerStats({ status: 'running', cpu_percent: null, rss_bytes: 50 * (1 << 20), sessions: 1 }, 1);
  assert.equal(s.cpu, '—');
  assert.equal(s.ram, '50 MB');
});

test('headerStats: a real zero is still reported as 0.0%', () => {
  // The point of the dash is to stop unknown masquerading as idle. A genuinely
  // idle app must still be able to say so.
  const m = { status: 'running', replicas: [
    { status: 'running', cpu_percent: 0, rss_bytes: 1 << 20, sessions: 0 },
  ] };
  assert.equal(headerStats(m, 1).cpu, '0.0 cores');
});

test('headerStats: capacity and host share use independent denominators', () => {
  const s = headerStats({ status: 'running', cpu_cores: 5.1, cpu_capacity_cores: 8,
    cpu_capacity_source: 'quota', cpu_host_cores: 16,
    replicas: [{ status: 'running', cpu_percent: 510 }] }, 1);
  assert.equal(s.cpu, '5.1 / 8 cores');
  assert.equal(s.cpuContext, '32% of host · limit: 8 cores (quota)');
  assert.equal(s.cpuFraction, 5.1 / 8);
  assert.equal(s.cpuSeverity, 'normal');
});

test('headerStats: capacity severity follows the ceiling', () => {
  const m = { status: 'running', cpu_capacity_cores: 4, cpu_host_cores: 16,
    replicas: [{ status: 'running', cpu_percent: 370 }] };
  assert.equal(headerStats(m, 1).cpuSeverity, 'critical');
});

test('headerStats: live desired replicas remain distinct from running replicas', () => {
  const s = headerStats({ status: 'running', replicas_desired: 17, replicas: [
    { status: 'running', cpu_percent: 65 }, { status: 'starting', cpu_percent: null },
  ] }, 16, { autoscale_enabled: true, autoscale_max_replicas: 24 });
  assert.equal(s.replicas, '1 / 17');
  assert.equal(s.runningCount, 1);
  assert.equal(s.desiredCount, 17);
});

test('headerStats: fractional core quota keeps its precision', () => {
  const s = headerStats({ status: 'running', cpu_cores: 0.04, cpu_capacity_cores: 0.05,
    cpu_capacity_source: 'quota', cpu_host_cores: 4,
    replicas: [{ status: 'running', cpu_percent: 4 }] }, 1);
  assert.equal(s.cpu, '0.04 / 0.05 cores');
  assert.equal(s.cpuContext, '1% of host · limit: 0.05 cores (quota)');
});

test('statusPillClass: running gets the is-live pulse, other states do not', () => {
  assert.equal(statusPillClass('running'), 'status-pill status-running is-live');
  assert.equal(statusPillClass('hibernated'), 'status-pill status-hibernated');
  assert.equal(statusPillClass('failed'), 'status-pill status-failed');
});

test('core formatting preserves tiny nonzero usage and exact fractional capacities', () => {
  assert.equal(formatCoreCount(0.001), '0.001 cores');
  assert.equal(formatCoreCount(0.0000001), '0.0000001 cores');
  assert.equal(formatCoreCount(null), '—');
  assert.equal(formatCoreCount(Infinity), '—');
  assert.equal(formatCoreCapacity(0.00004, 0.125), '0.00004 / 0.125 cores');
  assert.equal(formatCoreCapacity(null, 8), '—');
  assert.equal(formatCoreCapacity(1, Infinity), '1.0 cores');
  assert.equal(headerStats({ status: 'running', cpu_percent: 0.1, cpu_host_cores: 4 }, 1).cpuContext,
    '<1% of host · Capacity unknown');
});

test('an explicit unavailable core total never falls back to a partial replica sum', () => {
  const m = { status: 'running', cpu_cores: null, cpu_capacity_cores: 8,
    replicas: [{ status: 'running', cpu_percent: 100 }] };
  assert.equal(headerStats(m, 1).cpu, '—');
  assert.equal(headerStats(m, 1).cpuFraction, null);
  assert.equal(headerStats({ ...m, cpu_cores: 0 }, 1).cpu, '0.0 / 8 cores');
  assert.equal(headerStats({ ...m, cpu_cores: NaN }, 1).cpu, '—');
});

test('invalid or unsupported running replica CPU samples do not masquerade as idle', () => {
  for (const cpu_percent of [undefined, NaN, Infinity, -1]) {
    const m = { status: 'running', replicas: [{ status: 'running', cpu_percent }] };
    assert.equal(headerStats(m, 1).cpu, '—');
  }
  assert.equal(headerStats({ status: 'running', replicas: [] }, 1).cpu, '—');
  assert.equal(headerStats({ status: 'running', replicas: [
    { status: 'running', cpu_percent: 0, metrics_available: false },
  ] }, 1).cpu, '—');
});

test('elastic capacity reports live workers and worker maximum without a replica desired count', () => {
  for (const worker_isolation of ['grouped', 'per_session']) {
    const s = headerStats({ status: 'running', worker_isolation, workers_running: 3,
      max_workers: 12, replicas_desired: 1, replicas: [] }, 1);
    assert.equal(s.isElastic, true);
    assert.equal(s.capacityLabel, 'Workers');
    assert.equal(s.workerIsolation, worker_isolation);
    assert.equal(s.runningCount, 3);
    assert.equal(s.desiredCount, null);
    assert.equal(s.workerMax, 12);
    assert.equal(s.replicas, '3 / 12');
  }
});

test('unknown session counts never become a negative or partial fleet total', () => {
  for (const sessions of [-1, null, undefined, NaN]) {
    assert.equal(headerStats({ status: 'running', sessions }, 1).sessions, '—');
    assert.equal(headerStats({ status: 'running', replicas: [
      { status: 'running', sessions: 4 }, { status: 'running', sessions },
    ] }, 2).sessions, '—');
  }
  assert.equal(headerStats({ status: 'running', sessions: 0 }, 1).sessions, '0');
  assert.equal(headerStats({ status: 'running', replicas: [
    { status: 'running', sessions: 0 }, { status: 'stopped', sessions: -1 },
  ] }, 1).sessions, '0');
});

test('memory totals remain unknown until every live process has a usable sample', () => {
  const measured = { status: 'running', pid: 10, metrics_available: true,
    cpu_percent: 10, rss_bytes: 10 * (1 << 20), sessions: 1 };
  const failed = { status: 'running', pid: 11, metrics_available: false,
    cpu_percent: null, rss_bytes: 0, sessions: 1 };
  const partial = headerStats({ status: 'running', metrics_available: true,
    replicas: [measured, failed] }, 2);
  assert.equal(partial.ram, '—');
  assert.equal(partial.memoryAvailable, false);
  assert.equal(partial.metricsTransient, true);
  assert.match(partial.cpuContext, /sampling failed.*Retry metrics/);
  const allFailed = headerStats({ status: 'running', metrics_available: false, replicas: [failed] }, 1);
  assert.equal(allFailed.cpu, '—');
  assert.equal(allFailed.ram, '—');
  assert.equal(headerStats({ status: 'running', metrics_available: false, cpu_cores: 5,
    replicas: [failed] }, 1).cpu, '—');
  const unsupported = headerStats({ status: 'running', metrics_available: false,
    replicas: [{ ...failed, pid: 0 }] }, 1);
  assert.equal(unsupported.cpu, 'n/a');
  assert.equal(unsupported.ram, 'n/a');
  const recovered = headerStats({ status: 'running', metrics_available: true,
    replicas: [measured, { ...failed, metrics_available: true, cpu_percent: 0, rss_bytes: 1 << 20 }] }, 2);
  assert.equal(recovered.ram, '11 MB');
  assert.equal(recovered.metricsTransient, false);
  assert.equal(headerStats({ status: 'running', metrics_available: false, pid: 12 }, 1).ram, '—');
});
