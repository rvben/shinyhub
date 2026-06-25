import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildOverviewModel, pulseOrder } from '../static/views/overview-model.js';

const MIB = 1024 * 1024;

test('buildOverviewModel: empty fleet reads as nominal with a deploy prompt', () => {
  const m = buildOverviewModel([], {});
  assert.equal(m.total, 0);
  assert.equal(m.verdict.tone, 'nominal');
  assert.match(m.verdict.headline, /No apps deployed/);
});

test('buildOverviewModel: healthy fleet is nominal with a one-line summary', () => {
  const apps = [
    { slug: 'a', status: 'running' },
    { slug: 'b', status: 'running' },
    { slug: 'c', status: 'hibernated' },
  ];
  const m = buildOverviewModel(apps, {});
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

test('buildOverviewModel: sums live CPU/RAM and flags apps near their memory limit', () => {
  const apps = [
    { slug: 'a', status: 'running', memory_limit_mb: 256 },
    { slug: 'b', status: 'running', memory_limit_mb: 256 },
  ];
  const metrics = {
    a: { cpu_percent: 12, rss_bytes: 240 * MIB }, // 240/256 = 0.94 -> near limit
    b: { cpu_percent: 5, rss_bytes: 100 * MIB }, // 100/256 = 0.39 -> fine
  };
  const m = buildOverviewModel(apps, metrics);
  assert.equal(m.resources.cpuPercent, 17);
  assert.equal(m.resources.rssBytes, 340 * MIB);
  assert.equal(m.resources.running, 2);
  assert.equal(m.resources.nearLimit.length, 1);
  assert.equal(m.resources.nearLimit[0].slug, 'a');
  assert.ok(m.resources.nearLimit[0].fraction > 0.9);
});

test('buildOverviewModel: a scaled app is judged against per-replica capacity, not its single-replica limit', () => {
  // Two replicas each at 50% of a 256 MB limit: summed RSS = the limit, but the
  // fleet capacity is 512 MB, so the app is NOT near its limit.
  const apps = [{ slug: 'scaled', status: 'running', memory_limit_mb: 256, replicas: 2 }];
  const metrics = {
    scaled: {
      status: 'running',
      replicas: [
        { status: 'running', rss_bytes: 128 * MIB },
        { status: 'running', rss_bytes: 128 * MIB },
      ],
    },
  };
  const m = buildOverviewModel(apps, metrics);
  assert.equal(m.resources.rssBytes, 256 * MIB);
  assert.equal(m.resources.nearLimit.length, 0);
});
