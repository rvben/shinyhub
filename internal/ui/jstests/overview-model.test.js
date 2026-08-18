import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildOverviewModel, pulseOrder } from '../static/views/overview-model.js';

const MIB = 1024 * 1024;

function replica(index, { cpu = 0, rssMB = 0, memMB = 512, cpuQuota = 100,
  metricsAvailable = true, memoryEnforced = true, cpuEnforced = true } = {}) {
  return {
    index,
    status: 'running',
    metrics_available: metricsAvailable,
    cpu_percent: cpu,
    rss_bytes: rssMB * MIB,
    effective_memory_limit_mb: memMB,
    effective_cpu_quota_percent: cpuQuota,
    memory_limit_enforced: memoryEnforced,
    cpu_quota_enforced: cpuEnforced,
    resource_enforcement_known: true,
  };
}

function ready(metrics, generatedAt = '2026-08-18T09:00:00Z') {
  return { state: 'ready', metrics, generatedAt };
}

test('buildOverviewModel: empty fleet is neutral and not presented as healthy', () => {
  const m = buildOverviewModel([], {});
  assert.equal(m.total, 0);
  assert.equal(m.verdict.tone, 'empty');
  assert.equal(m.verdict.headline, 'Fleet not configured');
});

test('buildOverviewModel: healthy fleet is nominal with a one-line summary', () => {
  const apps = [
    { slug: 'a', status: 'running' },
    { slug: 'b', status: 'running' },
    { slug: 'c', status: 'hibernated' },
  ];
  const m = buildOverviewModel(apps, ready({
    a: { status: 'running', replicas: [replica(0)] },
    b: { status: 'running', replicas: [replica(0)] },
  }));
  assert.equal(m.verdict.tone, 'nominal');
  assert.equal(m.verdict.headline, 'All systems nominal');
  assert.equal(m.counts.healthy, 2);
  assert.equal(m.counts.sleeping, 1);
  assert.match(m.verdict.detail, /3 apps/);
  assert.match(m.verdict.detail, /1 sleeping/);
});

test('buildOverviewModel: crashed/degraded/failed-deploy apps drive a critical verdict; an intentionally-stopped app does not', () => {
  const apps = [
    { slug: 'ok', status: 'running' },
    { slug: 'boom', name: 'Boom', status: 'crashed' },
    { slug: 'lost', status: 'degraded' },
    // A failed deployment leaves the app "stopped"; it still needs attention.
    { slug: 'flop', status: 'stopped', last_deployment_status: 'failed', deploy_count: 0 },
    // Intentionally stopped after a good deploy: idle, NOT attention.
    { slug: 'paused', status: 'stopped', last_deployment_status: 'succeeded' },
  ];
  const m = buildOverviewModel(apps, {});
  assert.equal(m.verdict.tone, 'critical');
  assert.equal(m.verdict.headline, '3 apps need attention');
  assert.equal(m.attention.length, 3);
  assert.equal(m.counts.idle, 1); // the intentionally-stopped app
  assert.equal(m.attention.find((a) => a.slug === 'boom').reason, 'Crashed on startup');
  assert.equal(m.attention.find((a) => a.slug === 'flop').reason, 'First deployment failed');
  assert.equal(m.attention.find((a) => a.slug === 'lost').reason, 'Replicas lost');
});

test('buildOverviewModel: segments cover every bucket in a stable order', () => {
  const m = buildOverviewModel([{ slug: 'a', status: 'running' }], {});
  assert.deepEqual(m.segments.map((s) => s.key), pulseOrder);
  assert.equal(m.segments.find((s) => s.key === 'healthy').count, 1);
});

test('buildOverviewModel: uses resolved limits and stable units in fleet denominators', () => {
  const apps = [{ slug: 'a', status: 'running' }, { slug: 'b', status: 'running' }];
  const m = buildOverviewModel(apps, ready({
    a: { status: 'running', replicas: [replica(0, { cpu: 40, rssMB: 200, memMB: 512, cpuQuota: 100 })] },
    b: { status: 'running', replicas: [replica(0, { cpu: 50, rssMB: 300, memMB: 1024, cpuQuota: 200 })] },
  }));
  assert.equal(m.resources.cpu.used, 90);
  assert.equal(m.resources.cpu.capacity, 300);
  assert.equal(m.resources.memory.used, 500 * MIB);
  assert.equal(m.resources.memory.capacity, 1536 * MIB);
  assert.equal(m.resources.cpu.coverage.coveredReplicas, 2);
  assert.equal(m.resources.state, 'ready');
});

test('buildOverviewModel: an idle running replica counts and genuine zero remains observable', () => {
  const apps = [{ slug: 'idle', status: 'running' }];
  const m = buildOverviewModel(apps, ready({ idle: { status: 'running', replicas: [replica(0)] } }));
  assert.equal(m.resources.runningApps, 1);
  assert.equal(m.resources.runningReplicas, 1);
  assert.equal(m.resources.cpu.observedUsed, 0);
  assert.equal(m.resources.memory.observedUsed, 0);
  assert.equal(m.resources.state, 'ready');
});

test('buildOverviewModel: request failure is unavailable, never synthetic zero', () => {
  const apps = [{ slug: 'a', status: 'running', replicas: 1 }];
  const m = buildOverviewModel(apps, { state: 'unavailable', metrics: {}, generatedAt: null });
  assert.equal(m.resources.state, 'unavailable');
  assert.equal(m.resources.cpu.fraction, null);
  assert.equal(m.resources.memory.fraction, null);
  assert.equal(m.verdict.headline, 'Resource metrics unavailable');
});

test('buildOverviewModel: stale state preserves last good values and timestamp', () => {
  const metrics = { a: { status: 'running', replicas: [replica(0, { cpu: 25, rssMB: 128 })] } };
  const m = buildOverviewModel([{ slug: 'a', status: 'running' }], {
    state: 'stale', metrics, generatedAt: '2026-08-18T08:59:00Z',
  });
  assert.equal(m.resources.state, 'stale');
  assert.equal(m.resources.cpu.used, 25);
  assert.equal(m.resources.generatedAt, '2026-08-18T08:59:00Z');
  assert.equal(m.verdict.headline, 'Resource data is stale');
});

test('buildOverviewModel: unlimited and unenforced replicas produce partial coverage', () => {
  const apps = [{ slug: 'unlimited', status: 'running' }, { slug: 'unenforced', status: 'running' }];
  const m = buildOverviewModel(apps, ready({
    unlimited: { status: 'running', replicas: [replica(0, { memMB: 0, cpuQuota: 0 })] },
    unenforced: { status: 'running', replicas: [replica(0, { memoryEnforced: false, cpuEnforced: false })] },
  }));
  assert.equal(m.resources.state, 'partial');
  assert.equal(m.resources.memory.coverage.unlimitedReplicas, 1);
  assert.equal(m.resources.memory.coverage.unenforcedReplicas, 1);
  assert.equal(m.resources.memory.capacity, 0);
  assert.equal(m.verdict.headline, 'Resource coverage is partial');
});

test('buildOverviewModel: one hot replica is not masked by a cool sibling', () => {
  const apps = [{ slug: 'scaled', name: 'Scaled', status: 'running' }];
  const m = buildOverviewModel(apps, ready({
    scaled: { status: 'running', replicas: [
      replica(0, { rssMB: 251, memMB: 256 }),
      replica(1, { rssMB: 10, memMB: 256 }),
    ] },
  }));
  assert.ok(m.resources.memory.fraction < 0.6, 'aggregate stays cool');
  assert.ok(m.resources.memory.peakFraction > 0.95, 'hottest replica is retained');
  assert.equal(m.resources.memory.severity, 'critical');
  assert.equal(m.resources.hotspots[0].replicaIndex, 0);
  assert.equal(m.verdict.headline, 'Resource limit critical');
});

test('buildOverviewModel: warning and critical boundaries are exact for CPU and memory', () => {
  const warning = buildOverviewModel([{ slug: 'warn', status: 'running' }], ready({
    warn: { status: 'running', replicas: [replica(0, { cpu: 85, cpuQuota: 100, rssMB: 85, memMB: 100 })] },
  }));
  assert.equal(warning.resources.cpu.severity, 'warning');
  assert.equal(warning.resources.memory.severity, 'warning');

  const critical = buildOverviewModel([{ slug: 'crit', status: 'running' }], ready({
    crit: { status: 'running', replicas: [replica(0, { cpu: 95, cpuQuota: 100, rssMB: 95, memMB: 100 })] },
  }));
  assert.equal(critical.resources.cpu.severity, 'critical');
  assert.equal(critical.resources.memory.severity, 'critical');
});

test('buildOverviewModel: hotspots sort critical first and retain all alerts', () => {
  const apps = Array.from({ length: 6 }, (_, i) => ({ slug: `app-${i}`, status: 'running' }));
  const metrics = Object.fromEntries(apps.map((app, i) => [app.slug, {
    status: 'running', replicas: [replica(0, { rssMB: i === 5 ? 98 : 90, memMB: 100 })],
  }]));
  const m = buildOverviewModel(apps, ready(metrics));
  assert.equal(m.resources.hotspots.length, 6);
  assert.equal(m.resources.hotspots[0].severity, 'critical');
  assert.equal(m.resources.hiddenHotspotCount, 2);
});

test('buildOverviewModel: computes rising, falling, and collecting 15-minute trends', () => {
  const apps = [{ slug: 'a', status: 'running' }];
  const metrics = ready({ a: { status: 'running', replicas: [replica(0, { cpu: 20, rssMB: 20, memMB: 100, cpuQuota: 100 })] } });
  const ts = [0, 180, 360, 540, 720, 900];
  const rising = buildOverviewModel(apps, metrics, { historyAvailable: true, historyBySlug: {
    a: { ts, cpu: [10, 10, 10, 20, 20, 20], rss: [10, 10, 10, 20, 20, 20].map((v) => v * MIB), instances: [1, 1, 1, 1, 1, 1] },
  } });
  assert.equal(rising.resources.cpu.trend.state, 'rising');
  assert.equal(rising.resources.cpu.trend.deltaFraction, 0.1);

  const falling = buildOverviewModel(apps, metrics, { historyAvailable: true, historyBySlug: {
    a: { ts, cpu: [30, 30, 30, 20, 20, 20], rss: [30, 30, 30, 20, 20, 20].map((v) => v * MIB), instances: [1, 1, 1, 1, 1, 1] },
  } });
  assert.equal(falling.resources.cpu.trend.state, 'falling');

  const collecting = buildOverviewModel(apps, metrics, { historyAvailable: true, historyBySlug: {
    a: { ts: [0, 30], cpu: [10, 20], rss: [10, 20], instances: [1, 1] },
  } });
  assert.equal(collecting.resources.cpu.trend.state, 'collecting');
});
