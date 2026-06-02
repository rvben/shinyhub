import { test } from 'node:test';
import assert from 'node:assert/strict';
import { backendLabel, metricsText, reasonLabel } from '../static/views/replica-display.js';

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

// reasonLabel

test('reasonLabel returns the reason string when present', () => {
  assert.equal(reasonLabel({ status: 'lost', reason: 'worker unavailable' }), 'worker unavailable');
});

test('reasonLabel returns empty string when reason is absent', () => {
  assert.equal(reasonLabel({ status: 'running' }), '');
});

test('reasonLabel returns empty string for null/non-object input', () => {
  assert.equal(reasonLabel(null), '');
  assert.equal(reasonLabel(undefined), '');
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

test('metricsText returns neutral placeholder when metrics_available is undefined (seed/not-yet-polled)', () => {
  // The GET replicas_status payload has no metrics_available field (db.Replica
  // does not carry it), so seedReplicasFromStatus passes undefined. This is
  // distinct from metrics_available===false (confirmed PID-less): availability
  // is simply not yet known. Return neutral dashes rather than "n/a" so the
  // seed state does not falsely advertise that metrics are unavailable for what
  // may turn out to be a native (PID-backed) replica.
  const got = metricsText({ cpu_percent: 0, rss_bytes: 0 });
  assert.equal(got.cpuText, '—', 'undefined metrics_available must return neutral dash, not n/a');
  assert.equal(got.ramText, '—', 'undefined metrics_available must return neutral dash, not n/a');
  assert.equal(got.note, '', 'undefined metrics_available must return empty note, not the CloudWatch message');
});

test('metricsText three-state contract: false=n/a, true=real, undefined=pending dash', () => {
  // State 1: confirmed PID-less (Fargate/remote_docker) - show n/a with note
  const pidless = metricsText({ metrics_available: false });
  assert.equal(pidless.cpuText, 'n/a');
  assert.equal(pidless.ramText, 'n/a');
  assert.ok(pidless.note, 'PID-less must carry the CloudWatch note');

  // State 2: PID-backed (native) - show real numbers
  const native = metricsText({ metrics_available: true, cpu_percent: 5.0, rss_bytes: 1 << 20 });
  assert.equal(native.cpuText, '5.0%');
  assert.equal(native.ramText, '1 MB');
  assert.equal(native.note, null);

  // State 3: not yet polled (seed from GET replicas_status which lacks the field)
  // - show neutral dashes, not n/a, since availability is not yet known.
  const pending = metricsText({ cpu_percent: 0, rss_bytes: 0 }); // metrics_available omitted
  assert.equal(pending.cpuText, '—');
  assert.equal(pending.ramText, '—');
  assert.equal(pending.note, '');
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
