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
  assert.match(polled[1], /autoscale_load=1/);
});

test('a failing poll does not throw when onError is omitted', async () => {
  global.fetch = async () => ({ ok: false, status: 500, json: async () => ({}) });
  const metrics = createMetricsController({ intervalMs: 100000, onMetrics: () => {} });
  metrics.setTargets(['demo']);
  await flush();
  metrics.setTargets([]);
  metrics.stop();
});


test('manual refresh retries immediately and reports a missing app instead of silently preserving stale data', async () => {
  let calls = 0;
  global.fetch = async () => ({ ok: true, json: async () => (++calls === 1 ? { metrics: {} } : { metrics: { demo: { cpu_cores: 1 } } }) });
  const errors = [];
  const seen = [];
  const metrics = createMetricsController({ intervalMs: 100000, onMetrics: (slug, m) => seen.push(m), onError: (slug, e) => errors.push(e.message) });
  metrics.setTargets(['demo']);
  await flush();
  assert.deepEqual(errors, ['Metrics missing from response']);
  await metrics.refresh();
  metrics.stop();
  assert.equal(calls, 2);
  assert.equal(seen[0].cpu_cores, 1);
});

function deferred() {
  let resolve, reject;
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail; });
  return { promise, resolve, reject };
}

test('retry cancels an older request and prevents its late body from replacing fresh measurements', async (t) => {
  const requests = [];
  global.fetch = (_url, options) => {
    const pending = deferred();
    requests.push({ ...pending, signal: options.signal });
    return pending.promise;
  };
  const seen = [];
  const errors = [];
  const metrics = createMetricsController({ intervalMs: 100000,
    onMetrics: (_slug, m) => seen.push(m.cpu_cores), onError: (_slug, e) => errors.push(e) });
  t.after(() => metrics.stop());
  metrics.setTargets(['demo']);
  const oldBody = deferred();
  requests[0].resolve({ ok: true, json: () => oldBody.promise });
  await flush();
  const retry = metrics.refresh();
  assert.equal(requests[0].signal.aborted, true);
  requests[1].resolve({ ok: true, json: async () => ({ metrics: { demo: { cpu_cores: 2 } } }) });
  await retry;
  oldBody.resolve({ metrics: { demo: { cpu_cores: 1 } } });
  await flush();
  assert.deepEqual(seen, [2]);
  assert.deepEqual(errors, []);
});

test('retargeting an active poll fetches immediately and discards the previous app response', async (t) => {
  const requests = [];
  global.fetch = (url, options) => {
    const pending = deferred();
    requests.push({ ...pending, url, signal: options.signal });
    return pending.promise;
  };
  const seen = [];
  const metrics = createMetricsController({ intervalMs: 100000, onMetrics: (slug) => seen.push(slug) });
  t.after(() => metrics.stop());
  metrics.setTargets(['demo']);
  metrics.setTargets(['other']);
  assert.equal(requests.length, 2);
  assert.match(requests[1].url, /slugs=other/);
  assert.equal(requests[0].signal.aborted, true);
  requests[1].resolve({ ok: true, json: async () => ({ metrics: { other: { cpu_cores: 2 } } }) });
  await flush();
  requests[0].resolve({ ok: true, json: async () => ({ metrics: { demo: { cpu_cores: 1 } } }) });
  await flush();
  assert.deepEqual(seen, ['other']);
});

test('stopping the poll aborts an in-flight fetch and suppresses its late failure', async () => {
  const pending = deferred();
  let signal;
  global.fetch = (_url, options) => { signal = options.signal; return pending.promise; };
  const seen = [], errors = [];
  const metrics = createMetricsController({ intervalMs: 100000,
    onMetrics: (...args) => seen.push(args), onError: (...args) => errors.push(args) });
  metrics.setTargets(['demo']);
  metrics.stop();
  assert.equal(signal.aborted, true);
  pending.reject(new Error('late network failure'));
  await flush();
  assert.deepEqual(seen, []);
  assert.deepEqual(errors, []);
});

test('clearing targets suppresses an in-flight successful response', async () => {
  const pending = deferred();
  global.fetch = () => pending.promise;
  const seen = [];
  const metrics = createMetricsController({ intervalMs: 100000, onMetrics: (...args) => seen.push(args) });
  metrics.setTargets(['demo']);
  metrics.setTargets([]);
  pending.resolve({ ok: true, json: async () => ({ metrics: { demo: { cpu_cores: 1 } } }) });
  await flush();
  assert.deepEqual(seen, []);
});

test('the same batch set in a different order preserves its in-flight poll', async (t) => {
  let calls = 0;
  const pending = deferred();
  global.fetch = () => { calls++; return pending.promise; };
  const seen = [];
  const metrics = createMetricsController({ intervalMs: 100000, onMetrics: slug => seen.push(slug) });
  t.after(() => metrics.stop());
  metrics.setTargets(['demo', 'other']);
  metrics.setTargets(['other', 'demo']);
  assert.equal(calls, 1);
  pending.resolve({ ok: true, json: async () => ({ metrics: { demo: {}, other: {} } }) });
  await flush();
  assert.deepEqual(seen, ['demo', 'other']);
});
