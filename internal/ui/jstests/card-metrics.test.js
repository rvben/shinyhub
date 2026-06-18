import { test } from 'node:test';
import assert from 'node:assert/strict';
import { cardMetricsLabel, instanceCountLabel } from '../static/views/card-metrics.js';

const MB = 1 << 20;

test('cardMetricsLabel sums CPU and RAM across all replicas', () => {
  const m = {
    status: 'running',
    metrics_available: true,
    replicas: [
      { status: 'running', cpu_percent: 2.0, rss_bytes: 100 * MB },
      { status: 'running', cpu_percent: 3.0, rss_bytes: 50 * MB },
    ],
  };
  // 2.0 + 3.0 = 5.0% CPU; 100 + 50 = 150 MB RAM — the true total, not replica 0's slice.
  assert.equal(cardMetricsLabel(m, 2), 'CPU 5.0% · 150 MB RAM');
});

test('cardMetricsLabel is empty when the app is not running', () => {
  for (const status of ['hibernated', 'stopped', 'crashed', 'waking']) {
    assert.equal(cardMetricsLabel({ status }, 1), '');
  }
  assert.equal(cardMetricsLabel(null, 1), '');
});

test('cardMetricsLabel shows n/a for a PID-less backend', () => {
  const m = {
    status: 'running',
    metrics_available: false,
    replicas: [{ status: 'running', cpu_percent: 0, rss_bytes: 0 }],
  };
  assert.equal(cardMetricsLabel(m, 1), 'CPU n/a · n/a RAM');
});

test('instanceCountLabel shows a chip only for scaled apps', () => {
  assert.equal(instanceCountLabel({ replicas: 1 }), '');
  assert.equal(instanceCountLabel({ replicas: 3 }), '3 instances');
  assert.equal(instanceCountLabel({}), ''); // defaults to 1
  assert.equal(instanceCountLabel(null), '');
});
