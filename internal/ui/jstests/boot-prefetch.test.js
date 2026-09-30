import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createBootPrefetch } from '../static/views/boot-prefetch.js';

// Real Fetch API Responses, so the read-once body contract is the genuine one.
const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

function recorder(body = { fresh: true }) {
  const calls = [];
  const fetchFn = (path, init) => { calls.push({ path, init }); return Promise.resolve(json(body)); };
  return { calls, fetchFn };
}

test('a live prefetched path is served without a new request', async () => {
  const boot = { t: 1000, r: { '/api/apps': Promise.resolve(json({ items: [1] })) } };
  const bp = createBootPrefetch(boot, { now: () => 1500 });
  const { calls, fetchFn } = recorder();
  const resp = await bp.fetch('/api/apps', {}, fetchFn);
  assert.deepEqual(await resp.json(), { items: [1] });
  assert.equal(calls.length, 0);
});

test('every caller of a live path can read its own body', async () => {
  const boot = { t: 0, r: { '/api/apps': Promise.resolve(json({ items: [1] })) } };
  const bp = createBootPrefetch(boot, { now: () => 0 });
  const { calls, fetchFn } = recorder();
  const a = await bp.fetch('/api/apps', {}, fetchFn);
  const b = await bp.fetch('/api/apps', {}, fetchFn);
  assert.deepEqual(await a.json(), { items: [1] });
  assert.deepEqual(await b.json(), { items: [1] });
  assert.equal(calls.length, 0);
});

test('a path that was not prefetched goes to the network with its init', async () => {
  const boot = { t: 0, r: { '/api/apps': Promise.resolve(json({})) } };
  const bp = createBootPrefetch(boot, { now: () => 0 });
  const { calls, fetchFn } = recorder();
  const init = { credentials: 'same-origin' };
  const resp = await bp.fetch('/api/users', init, fetchFn);
  assert.deepEqual(await resp.json(), { fresh: true });
  assert.deepEqual(calls, [{ path: '/api/users', init }]);
});

test('the lookup is by exact path, so a query string is a different request', async () => {
  const boot = { t: 0, r: { '/api/apps': Promise.resolve(json({ prefetched: true })) } };
  const bp = createBootPrefetch(boot, { now: () => 0 });
  const { calls, fetchFn } = recorder();
  await bp.fetch('/api/apps?limit=5', {}, fetchFn);
  assert.equal(calls.length, 1);
});

test('after discard every path goes to the network', async () => {
  const boot = { t: 0, r: { '/api/apps': Promise.resolve(json({ stale: true })) } };
  const bp = createBootPrefetch(boot, { now: () => 0 });
  const { calls, fetchFn } = recorder();
  bp.discard();
  const resp = await bp.fetch('/api/apps', {}, fetchFn);
  assert.deepEqual(await resp.json(), { fresh: true });
  assert.equal(calls.length, 1);
});

test('an entry older than maxAgeMs is never served, and stays expired', async () => {
  let clock = 0;
  const boot = { t: 0, r: { '/api/apps': Promise.resolve(json({ stale: true })) } };
  const bp = createBootPrefetch(boot, { now: () => clock, maxAgeMs: 100 });
  const { calls, fetchFn } = recorder();
  clock = 100;
  assert.deepEqual(await (await bp.fetch('/api/apps', {}, fetchFn)).json(), { stale: true });
  clock = 101;
  assert.deepEqual(await (await bp.fetch('/api/apps', {}, fetchFn)).json(), { fresh: true });
  clock = 50; // a clock that steps back does not resurrect the discarded set
  assert.deepEqual(await (await bp.fetch('/api/apps', {}, fetchFn)).json(), { fresh: true });
  assert.equal(calls.length, 2);
});

test('a prefetch that failed at the network level falls back to a real request', async () => {
  const failed = Promise.reject(new TypeError('Failed to fetch'));
  failed.catch(() => {});
  const bp = createBootPrefetch({ t: 0, r: { '/api/auth/me': failed } }, { now: () => 0 });
  const { calls, fetchFn } = recorder({ user: { role: 'admin' } });
  const resp = await bp.fetch('/api/auth/me', { credentials: 'same-origin' }, fetchFn);
  assert.deepEqual(await resp.json(), { user: { role: 'admin' } });
  assert.equal(calls.length, 1);
});

test('an HTTP error status is the answer, not a reason to refetch', async () => {
  const bp = createBootPrefetch({ t: 0, r: { '/api/auth/me': Promise.resolve(json({ error: 'unauthorized' }, 401)) } }, { now: () => 0 });
  const { calls, fetchFn } = recorder();
  const resp = await bp.fetch('/api/auth/me', {}, fetchFn);
  assert.equal(resp.status, 401);
  assert.equal(calls.length, 0);
});

test('no boot object, or a malformed one, means nothing is ever prefetched', async () => {
  for (const boot of [undefined, null, {}, { t: 0 }, { r: {} }, { t: '0', r: {} }, { t: 0, r: null }]) {
    const bp = createBootPrefetch(boot, { now: () => 0 });
    const { calls, fetchFn } = recorder();
    await bp.fetch('/api/apps', {}, fetchFn);
    assert.equal(calls.length, 1, JSON.stringify(boot));
  }
});

test('an inherited property name is not mistaken for a prefetched path', async () => {
  const bp = createBootPrefetch({ t: 0, r: {} }, { now: () => 0 });
  const { calls, fetchFn } = recorder();
  await bp.fetch('toString', {}, fetchFn);
  assert.equal(calls.length, 1);
});

test('discard releases the parked responses and stops later parking', async () => {
  const boot = { t: 0, r: { '/api/apps': Promise.resolve(json({})) } };
  const bp = createBootPrefetch(boot, { now: () => 0 });
  bp.discard();
  assert.equal(boot.r, null);
});

test('expiry releases the parked responses', async () => {
  const boot = { t: 0, r: { '/api/apps': Promise.resolve(json({})) } };
  const bp = createBootPrefetch(boot, { now: () => 101, maxAgeMs: 100 });
  const { fetchFn } = recorder();
  await bp.fetch('/api/apps', {}, fetchFn);
  assert.equal(boot.r, null);
});

test('a path parked after discard is never served', async () => {
  const boot = { t: 0, r: {} };
  const bp = createBootPrefetch(boot, { now: () => 0 });
  bp.discard();
  // The shell's park() writes through boot.r only while it is set.
  if (boot.r) boot.r['/api/apps'] = Promise.resolve(json({ stale: true }));
  const { calls, fetchFn } = recorder();
  assert.deepEqual(await (await bp.fetch('/api/apps', {}, fetchFn)).json(), { fresh: true });
  assert.equal(calls.length, 1);
});

test('a malformed boot object is left untouched', () => {
  const boot = { t: '0', r: { '/api/apps': 1 } };
  createBootPrefetch(boot).discard();
  assert.deepEqual(boot.r, { '/api/apps': 1 });
});
