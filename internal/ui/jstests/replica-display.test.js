import { test } from 'node:test';
import assert from 'node:assert/strict';
import { backendLabel, metricsText } from '../static/views/replica-display.js';

// backendLabel

test('backendLabel returns "provider:tier" when both present', () => {
  assert.equal(backendLabel({ provider: 'fargate', tier: 'burst' }), 'fargate:burst');
});

test('backendLabel returns "provider:tier" for native/local', () => {
  assert.equal(backendLabel({ provider: 'native', tier: 'local' }), 'native:local');
});

test('backendLabel returns provider alone when tier is absent', () => {
  assert.equal(backendLabel({ provider: 'fargate' }), 'fargate');
});

test('backendLabel returns tier alone when provider is absent', () => {
  assert.equal(backendLabel({ tier: 'burst' }), 'burst');
});

test('backendLabel returns "unknown" when both are absent', () => {
  assert.equal(backendLabel({}), 'unknown');
});

test('backendLabel returns "unknown" for null input', () => {
  assert.equal(backendLabel(null), 'unknown');
});

// metricsText

test('metricsText returns n/a + note when metrics_available is false', () => {
  const got = metricsText({ metrics_available: false, cpu_percent: 0, rss_bytes: 0 });
  assert.equal(got.cpuText, 'n/a');
  assert.equal(got.ramText, 'n/a');
  assert.ok(got.note && got.note.length > 0, 'note must be non-empty for PID-less backend');
  assert.match(got.note, /CloudWatch|worker host/i);
});

test('metricsText returns formatted numbers when metrics_available is true', () => {
  const got = metricsText({ metrics_available: true, cpu_percent: 1.5, rss_bytes: 104857600 });
  assert.equal(got.cpuText, '1.5%');
  assert.equal(got.ramText, '100 MB');
  assert.equal(got.note, null);
});

test('metricsText returns dash for genuinely-zero RAM when metrics_available is true', () => {
  const got = metricsText({ metrics_available: true, cpu_percent: 0, rss_bytes: 0 });
  assert.equal(got.cpuText, '0.0%');
  // The existing em-dash treatment for zero RAM is preserved.
  assert.match(got.ramText, /^[—-]$/);
  assert.equal(got.note, null);
});

test('metricsText returns KB when rss_bytes < 1MB', () => {
  const got = metricsText({ metrics_available: true, cpu_percent: 2.3, rss_bytes: 512 * 1024 });
  assert.equal(got.cpuText, '2.3%');
  assert.match(got.ramText, /KB/);
  assert.equal(got.note, null);
});

test('metricsText defaults metrics_available to false for undefined', () => {
  // A response that predates plan 01 omits metrics_available; treat as PID-less
  // so stale consumers do not mislead with "0.0% CPU".
  const got = metricsText({ cpu_percent: 0, rss_bytes: 0 });
  assert.equal(got.cpuText, 'n/a');
  assert.equal(got.ramText, 'n/a');
});

// Mixed-tier replica set

test('mixed-tier set: native replica shows real %, fargate shows n/a', () => {
  const native  = { provider: 'native',  tier: 'local', metrics_available: true,  cpu_percent: 3.7, rss_bytes: 209715200 };
  const fargate = { provider: 'fargate', tier: 'burst', metrics_available: false, cpu_percent: 0,   rss_bytes: 0 };

  const nativeLabel  = backendLabel(native);
  const fargateLabel = backendLabel(fargate);
  assert.equal(nativeLabel,  'native:local');
  assert.equal(fargateLabel, 'fargate:burst');

  const nm = metricsText(native);
  assert.equal(nm.cpuText, '3.7%');
  assert.equal(nm.ramText, '200 MB');
  assert.equal(nm.note, null);

  const fm = metricsText(fargate);
  assert.equal(fm.cpuText, 'n/a');
  assert.equal(fm.ramText, 'n/a');
  assert.ok(fm.note, 'fargate replica must carry a note explaining why metrics are absent');
});
