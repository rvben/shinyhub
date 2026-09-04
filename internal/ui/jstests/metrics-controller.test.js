import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createMetricsController } from '../static/metrics-controller.js';

// tick() chains two awaits (fetch, then resp.json()); a single macrotask
// flush lets both microtask hops resolve before we assert.
function flush() {
  return new Promise((resolve) => setImmediate(resolve));
}

test('onError fires per target slug when the batch endpoint returns non-2xx', async () => {
  global.fetch = async () => ({ ok: false, status: 401, json: async () => ({}) });
  const errors = [];
  const metrics = createMetricsController({
    intervalMs: 100000,
    onMetrics: () => {},
    onError: (slug, err) => errors.push({ slug, message: err.message }),
  });
  metrics.setTargets(['demo', 'other']);
  await flush();
  metrics.stop();
  assert.equal(errors.length, 2, 'onError must fire once per polled slug');
  assert.deepEqual(errors.map((e) => e.slug).sort(), ['demo', 'other']);
  assert.match(errors[0].message, /401/);
});

test('onError fires on a fetch throw (network failure)', async () => {
  global.fetch = async () => {
    throw new Error('network down');
  };
  const errors = [];
  const metrics = createMetricsController({
    intervalMs: 100000,
    onMetrics: () => {},
    onError: (slug, err) => errors.push({ slug, message: err.message }),
  });
  metrics.setTargets(['demo']);
  await flush();
  metrics.stop();
  assert.equal(errors.length, 1);
  assert.equal(errors[0].slug, 'demo');
  assert.equal(errors[0].message, 'network down');
});

test('a successful poll calls onMetrics and never onError', async () => {
  global.fetch = async () => ({
    ok: true,
    status: 200,
    json: async () => ({ metrics: { demo: { cpu_percent: 1 } } }),
  });
  const seen = [];
  let errorCalls = 0;
  const metrics = createMetricsController({
    intervalMs: 100000,
    onMetrics: (slug, m) => seen.push([slug, m]),
    onError: () => errorCalls++,
  });
  metrics.setTargets(['demo']);
  await flush();
  metrics.stop();
  assert.equal(errorCalls, 0, 'a successful poll must not call onError');
  assert.equal(seen.length, 1);
  assert.equal(seen[0][0], 'demo');
});

// The app-detail page re-declares its metrics target on every render, including
// the in-place render of a tab switch. That must leave the running poller and
// the values already on screen alone; restarting it is what reset the header
// tiles to "—" on every tab click. Paired with the negative control below, so a
// "no extra poll" result cannot be a blind harness.
test('re-targeting the poller at the set it is already polling does not re-poll', async () => {
  let calls = 0;
  global.fetch = async () => {
    calls++;
    return { ok: true, status: 200, json: async () => ({ metrics: { demo: { cpu_percent: 1 } } }) };
  };
  const metrics = createMetricsController({ intervalMs: 100000, onMetrics: () => {} });
  metrics.setTargets(['demo']);
  await flush();
  assert.equal(calls, 1, 'the first target set polls immediately');

  metrics.setTargets(['demo']);
  await flush();
  metrics.stop();
  assert.equal(calls, 1, 'an unchanged target set must not re-poll');
});

test('a genuinely changed target set still polls immediately', async () => {
  const polled = [];
  global.fetch = async (url) => {
    polled.push(String(url));
    return { ok: true, status: 200, json: async () => ({ metrics: {} }) };
  };
  const metrics = createMetricsController({ intervalMs: 100000, onMetrics: () => {} });
  metrics.setTargets(['demo']);
  await flush();
  metrics.setTargets([]);
  metrics.setTargets(['other']);
  await flush();
  metrics.stop();
  assert.equal(polled.length, 2);
  assert.match(polled[1], /slugs=other/);
});

test('a failing poll does not throw when onError is omitted', async () => {
  global.fetch = async () => ({ ok: false, status: 500, json: async () => ({}) });
  const metrics = createMetricsController({ intervalMs: 100000, onMetrics: () => {} });
  metrics.setTargets(['demo']);
  await flush();
  metrics.setTargets([]);
  metrics.stop();
});
