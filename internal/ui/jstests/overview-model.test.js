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

// A fleet with no limits set anywhere is fully measurable as usage, so it is
// reported as usage rather than as a fleet whose pressure cannot be determined.
// The coverage counts still record why there is no allocation to measure.
test('buildOverviewModel: a fleet with no enforced limits reports usage, not partial coverage', () => {
  const apps = [
    { slug: 'unlimited', name: 'Unlimited', status: 'running' },
    { slug: 'unenforced', name: 'Unenforced', status: 'running' },
  ];
  const m = buildOverviewModel(apps, ready({
    unlimited: { status: 'running', replicas: [replica(0, { rssMB: 300, memMB: 0, cpuQuota: 0 })] },
    unenforced: { status: 'running', replicas: [replica(0, { rssMB: 100, memoryEnforced: false, cpuEnforced: false })] },
  }));
  assert.equal(m.resources.scale, 'none');
  assert.equal(m.resources.memory.scale, 'none');
  assert.equal(m.resources.state, 'ready');
  assert.equal(m.resources.memory.coverage.unlimitedReplicas, 1);
  assert.equal(m.resources.memory.coverage.unenforcedReplicas, 1);
  assert.equal(m.resources.memory.capacity, 0);
  // Usage is real and shown; a fraction of nothing is not invented.
  assert.equal(m.resources.memory.used, 400 * MIB);
  assert.equal(m.resources.memory.fraction, null);
  assert.equal(m.resources.memory.severity, 'normal');
  assert.equal(m.verdict.headline, 'All systems nominal');
  assert.equal(m.resources.topConsumers.kind, 'memory');
  assert.deepEqual(m.resources.topConsumers.items.map((i) => i.slug), ['unlimited', 'unenforced']);
  assert.equal(m.resources.topConsumers.items[0].fraction, 0.75);
});

test('buildOverviewModel: host capacity supplies the denominator when no app carries a limit', () => {
  const apps = [{ slug: 'a', name: 'A', status: 'running' }];
  const m = buildOverviewModel(apps, {
    ...ready({ a: { status: 'running', replicas: [replica(0, { cpu: 150, rssMB: 2048, memMB: 0, cpuQuota: 0 })] } }),
    host: { cores: 4, cores_source: 'affinity', memory_mb: 8192, memory_source: 'host-total' },
  });
  assert.equal(m.resources.scale, 'host');
  assert.equal(m.resources.cpu.scale, 'host');
  assert.equal(m.resources.cpu.capacity, 400);
  assert.equal(m.resources.cpu.capacitySource, 'affinity');
  assert.equal(m.resources.cpu.fraction, 0.375);
  assert.equal(m.resources.memory.capacity, 8192 * MIB);
  assert.equal(m.resources.memory.fraction, 0.25);
  assert.equal(m.resources.memory.severity, 'normal');
  // There is no per-replica denominator on this scale, so no replica can be
  // named as the peak.
  assert.equal(m.resources.memory.peakFraction, null);
  assert.equal(m.resources.state, 'ready');
  assert.equal(m.verdict.headline, 'All systems nominal');
});

test('buildOverviewModel: host scale still raises a verdict when the box is nearly full', () => {
  const apps = [{ slug: 'a', name: 'A', status: 'running' }];
  const m = buildOverviewModel(apps, {
    ...ready({ a: { status: 'running', replicas: [replica(0, { rssMB: 7900, memMB: 0, cpuQuota: 0 })] } }),
    host: { cores: 4, cores_source: 'affinity', memory_mb: 8192, memory_source: 'host-total' },
  });
  assert.equal(m.resources.memory.severity, 'critical');
  assert.equal(m.verdict.tone, 'critical');
  assert.equal(m.verdict.headline, 'Host capacity is nearly exhausted');
  assert.match(m.verdict.detail, /Memory is at 96% of host capacity across 1 running replica\./);
});

test('buildOverviewModel: a host that reports cores but not memory scales only CPU', () => {
  const apps = [{ slug: 'a', name: 'A', status: 'running' }];
  const m = buildOverviewModel(apps, {
    ...ready({ a: { status: 'running', replicas: [replica(0, { cpu: 100, rssMB: 512, memMB: 0, cpuQuota: 0 })] } }),
    host: { cores: 2, cores_source: 'cgroup-quota' },
  });
  assert.equal(m.resources.cpu.scale, 'host');
  assert.equal(m.resources.cpu.fraction, 0.5);
  assert.equal(m.resources.memory.scale, 'none');
  assert.equal(m.resources.memory.capacity, 0);
  assert.equal(m.resources.memory.fraction, null);
  // One row on the host scale is enough to make this a host-scale panel.
  assert.equal(m.resources.scale, 'host');
});

// A limit on one app is still an allocation to measure, so the panel keeps
// answering the pressure question and keeps saying that coverage is partial.
test('buildOverviewModel: one limited app keeps the whole panel on the limits scale', () => {
  const apps = [{ slug: 'limited', status: 'running' }, { slug: 'free', status: 'running' }];
  const m = buildOverviewModel(apps, {
    ...ready({
      limited: { status: 'running', replicas: [replica(0, { rssMB: 100, memMB: 512 })] },
      free: { status: 'running', replicas: [replica(0, { rssMB: 100, memMB: 0, cpuQuota: 0 })] },
    }),
    host: { cores: 4, cores_source: 'affinity', memory_mb: 8192, memory_source: 'host-total' },
  });
  assert.equal(m.resources.scale, 'limits');
  assert.equal(m.resources.memory.scale, 'limits');
  assert.equal(m.resources.memory.capacity, 512 * MIB);
  assert.equal(m.resources.state, 'partial');
  assert.equal(m.verdict.headline, 'Resource coverage is partial');
  assert.equal(m.verdict.detail, 'Some running replicas have unlimited or unenforced capacity.');
  assert.equal(m.resources.topConsumers, null);
});

test('buildOverviewModel: top consumers rank by memory and stop at three', () => {
  const sizes = [{ slug: 'e', mb: 50 }, { slug: 'a', mb: 400 }, { slug: 'd', mb: 100 },
    { slug: 'b', mb: 300 }, { slug: 'c', mb: 200 }];
  const apps = sizes.map((s) => ({ slug: s.slug, name: s.slug.toUpperCase(), status: 'running' }));
  const metrics = Object.fromEntries(sizes.map((s) => [s.slug, {
    status: 'running', replicas: [replica(0, { rssMB: s.mb, memMB: 0, cpuQuota: 0 })],
  }]));
  const m = buildOverviewModel(apps, ready(metrics));
  assert.deepEqual(m.resources.topConsumers.items.map((i) => i.slug), ['a', 'b', 'c']);
  assert.equal(m.resources.topConsumers.items[0].used, 400 * MIB);
  assert.equal(m.resources.topConsumers.items[0].fraction, 400 / 1050);
});

test('buildOverviewModel: a replica that reports nothing leaves the usage scale partial', () => {
  const apps = [{ slug: 'a', status: 'running' }];
  const m = buildOverviewModel(apps, ready({
    a: { status: 'running', replicas: [
      replica(0, { rssMB: 100, memMB: 0, cpuQuota: 0 }),
      replica(1, { metricsAvailable: false, memMB: 0, cpuQuota: 0 }),
    ] },
  }));
  assert.equal(m.resources.memory.scale, 'none');
  assert.equal(m.resources.state, 'partial');
  assert.equal(m.verdict.detail, 'Some running replicas are not reporting CPU or memory.');
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

const TS = [0, 180, 360, 540, 720, 900];

// Without limits the trend follows the quantity consumed, and against a known
// host it converts back to percentage points so "rising" means the same thing
// it does on the limits scale.
test('buildOverviewModel: usage trend on the host scale moves with percentage points of the host', () => {
  const apps = [{ slug: 'a', status: 'running' }];
  const snapshot = {
    ...ready({ a: { status: 'running', replicas: [replica(0, { cpu: 60, memMB: 0, cpuQuota: 0 })] } }),
    host: { cores: 4, cores_source: 'affinity', memory_mb: 8192, memory_source: 'host-total' },
  };
  const m = buildOverviewModel(apps, snapshot, { historyAvailable: true, historyBySlug: {
    a: { ts: TS, cpu: [10, 10, 10, 60, 60, 60], rss: TS.map(() => 0), instances: [1, 1, 1, 1, 1, 1] },
  } });
  assert.equal(m.resources.cpu.trend.state, 'rising');
  assert.equal(m.resources.cpu.trend.deltaValue, 50);
  assert.equal(m.resources.cpu.trend.deltaFraction, 50 / 400);
});

// The history series is already summed across an app's replicas. Dividing it by
// the instance count is what turns it into a per-replica fraction, and doing so
// here would report a two-replica app as using half what it uses.
test('buildOverviewModel: usage trend reads history as a total, not per replica', () => {
  const apps = [{ slug: 'a', status: 'running' }];
  const snapshot = ready({ a: { status: 'running', replicas: [replica(0, { cpu: 60, memMB: 0, cpuQuota: 0 })] } });
  const series = (instances) => ({ historyAvailable: true, historyBySlug: {
    a: { ts: TS, cpu: [10, 10, 10, 60, 60, 60], rss: TS.map(() => 0), instances },
  } });
  const single = buildOverviewModel(apps, snapshot, series([1, 1, 1, 1, 1, 1]));
  const paired = buildOverviewModel(apps, snapshot, series([2, 2, 2, 2, 2, 2]));
  assert.equal(single.resources.cpu.trend.deltaValue, 50);
  assert.equal(paired.resources.cpu.trend.deltaValue, 50);
});

test('buildOverviewModel: with no denominator a small absolute move stays steady', () => {
  const apps = [{ slug: 'a', status: 'running' }];
  const snapshot = ready({ a: { status: 'running', replicas: [replica(0, { cpu: 12, rssMB: 20, memMB: 0, cpuQuota: 0 })] } });
  const quiet = buildOverviewModel(apps, snapshot, { historyAvailable: true, historyBySlug: {
    a: {
      ts: TS,
      cpu: [10, 10, 10, 12, 12, 12],
      rss: [10, 10, 10, 20, 20, 20].map((v) => v * MIB),
      instances: [1, 1, 1, 1, 1, 1],
    },
  } });
  // 2 points of one core, and 10 MiB, are both inside the noise floor.
  assert.equal(quiet.resources.cpu.trend.state, 'steady');
  assert.equal(quiet.resources.cpu.trend.deltaValue, 2);
  assert.equal(quiet.resources.cpu.trend.deltaFraction, null);
  assert.equal(quiet.resources.memory.trend.state, 'steady');

  const busy = buildOverviewModel(apps, snapshot, { historyAvailable: true, historyBySlug: {
    a: {
      ts: TS,
      cpu: [10, 10, 10, 90, 90, 90],
      rss: [1000, 1000, 1000, 1100, 1100, 1100].map((v) => v * MIB),
      instances: [1, 1, 1, 1, 1, 1],
    },
  } });
  // Above the floor the threshold scales with the fleet's own earlier usage:
  // 100 MiB on top of 1 GiB clears it, and 80 points of CPU clears it outright.
  assert.equal(busy.resources.cpu.trend.state, 'rising');
  assert.equal(busy.resources.memory.trend.state, 'rising');
  assert.equal(busy.resources.memory.trend.deltaValue, 100 * MIB);
});
