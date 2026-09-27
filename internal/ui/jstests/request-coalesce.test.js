import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createGETCoalescer } from '../static/views/request-coalesce.js';

// A minimal stand-in for a Fetch API Response: real callers get a genuine
// Response (which supports .clone()), so the fake here mirrors just enough
// of that contract for the coalescer's own logic to be exercised in
// isolation, independent of any DOM or network stack.
function fakeResponse(status, jsonBody) {
  const make = () => ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => jsonBody,
    clone: () => make(),
  });
  return make();
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

function isAbortError(err) {
  return err instanceof Error && err.name === 'AbortError';
}

test('two concurrent GETs to the same path share one underlying fetch', async () => {
  const calls = [];
  const gate = deferred();
  const coalescedGET = createGETCoalescer((path) => { calls.push(path); return gate.promise; });

  const first = coalescedGET('/api/apps');
  const second = coalescedGET('/api/apps');
  assert.equal(calls.length, 1, 'only one underlying fetch for two overlapping callers');

  gate.resolve(fakeResponse(200, { items: [{ slug: 'a' }] }));
  const [firstResp, secondResp] = await Promise.all([first, second]);
  assert.deepEqual(await firstResp.json(), { items: [{ slug: 'a' }] });
  assert.deepEqual(await secondResp.json(), { items: [{ slug: 'a' }] });
  assert.notEqual(firstResp, secondResp, 'each caller gets its own cloned response');
});

test('different paths are never coalesced', async () => {
  const calls = [];
  const coalescedGET = createGETCoalescer((path) => {
    calls.push(path);
    return Promise.resolve(fakeResponse(200, {}));
  });
  await Promise.all([coalescedGET('/api/apps'), coalescedGET('/api/workers')]);
  assert.deepEqual(calls, ['/api/apps', '/api/workers']);
});

test('a settled request does not stay coalesced for the next navigation', async () => {
  let calls = 0;
  const coalescedGET = createGETCoalescer(() => {
    calls += 1;
    return Promise.resolve(fakeResponse(200, {}));
  });
  await coalescedGET('/api/apps');
  await coalescedGET('/api/apps');
  assert.equal(calls, 2, 'a second, non-overlapping call issues its own fetch');
});

test('a caller aborting stops only that caller, not a sibling sharing the same fetch', async () => {
  const gate = deferred();
  const coalescedGET = createGETCoalescer(() => gate.promise);
  const controller = new AbortController();

  const aborted = coalescedGET('/api/apps', { signal: controller.signal });
  const plain = coalescedGET('/api/apps');

  controller.abort();
  await assert.rejects(aborted, isAbortError);

  gate.resolve(fakeResponse(200, { items: [] }));
  const resp = await plain;
  assert.equal(resp.ok, true, 'the non-aborted caller still gets the shared result');
});

test('an already-aborted signal rejects without waiting for the shared fetch', async () => {
  const gate = deferred(); // never resolved in this test
  const coalescedGET = createGETCoalescer(() => gate.promise);
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(coalescedGET('/api/apps', { signal: controller.signal }), isAbortError);
});

test('a rejected underlying fetch propagates to every coalesced waiter', async () => {
  const gate = deferred();
  const coalescedGET = createGETCoalescer(() => gate.promise);
  const first = coalescedGET('/api/apps');
  const second = coalescedGET('/api/apps');
  gate.reject(new Error('network error'));
  await assert.rejects(first, /network error/);
  await assert.rejects(second, /network error/);
});
