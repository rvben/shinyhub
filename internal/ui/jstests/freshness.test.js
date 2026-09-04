import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createFreshness } from '../static/views/freshness.js';

// A controllable clock, so age is asserted rather than waited for.
function clockAt(start = 1000) {
  const c = { t: start };
  c.now = () => c.t;
  return c;
}

function make(maxAgeMs = 10000, clock = clockAt()) {
  return { f: createFreshness({ maxAgeMs, now: clock.now }), clock };
}

test('nothing is held before the first answer is adopted', () => {
  const { f } = make();
  assert.equal(f.isFresh(), false, 'no copy is not a fresh copy');
  assert.equal(f.isStale(), false);
  assert.equal(f.isOld(), false);
});

test('an adopted answer with no mutations and no elapsed time is fresh', () => {
  const { f } = make();
  f.adopt(f.begin());
  assert.equal(f.isFresh(), true);
  assert.equal(f.isStale(), false);
  assert.equal(f.isOld(), false);
});

test('a mutation after the answer was adopted makes it stale, not merely old', () => {
  const { f } = make();
  f.adopt(f.begin());
  f.mutated();
  assert.equal(f.isStale(), true, 'the held copy predates a change');
  assert.equal(f.isOld(), false, 'no time has passed');
  assert.equal(f.isFresh(), false);
});

// The reason this module counts instead of raising a flag. A change that lands
// while the request is in flight cannot be in the answer that request returns,
// so adopting that answer must NOT clear the staleness. Flag-based bookkeeping
// (set on mutate, clear on adopt) gets exactly this case wrong, and the way it
// is wrong is invisible: the page shows the state from before the user's action.
test('a mutation that lands mid-request leaves the answer it arrives with stale', () => {
  const { f } = make();
  const stamp = f.begin(); // request sent
  f.mutated(); // something changed while it was in flight
  f.adopt(stamp); // its answer comes back and is adopted
  assert.equal(f.isStale(), true, 'the answer was requested before the change, so it cannot include it');
});

test('a mutation before the request is covered by the answer', () => {
  const { f } = make();
  f.adopt(f.begin());
  f.mutated();
  assert.equal(f.isStale(), true);

  const stamp = f.begin(); // refetch sent after the change
  f.adopt(stamp);
  assert.equal(f.isStale(), false, 'the refetch was sent after the change, so its answer includes it');
  assert.equal(f.isFresh(), true);
});

test('age is measured from when the request was sent, not when it was adopted', () => {
  const { f, clock } = make(10000);
  const stamp = f.begin();
  clock.t += 9000; // a slow request
  f.adopt(stamp);
  assert.equal(f.isOld(), false, '9s < 10s');
  clock.t += 1000;
  assert.equal(f.isOld(), true, 'the answer describes the world as of 10s ago');
});

test('an old copy is not stale: it can still be rendered while a refresh follows', () => {
  const { f, clock } = make(10000);
  f.adopt(f.begin());
  clock.t += 60000;
  assert.equal(f.isOld(), true);
  assert.equal(f.isStale(), false, 'time alone never makes a copy wrong');
  assert.equal(f.isFresh(), false);
});

test('forget drops the held copy so a torn-down view reports nothing to reason about', () => {
  const { f, clock } = make(10000);
  f.adopt(f.begin());
  f.mutated();
  assert.equal(f.isStale(), true);
  f.forget();
  clock.t += 60000;
  assert.equal(f.isStale(), false);
  assert.equal(f.isOld(), false);
  assert.equal(f.isFresh(), false);
});

test('mutations counted before anything is adopted do not make the first answer stale', () => {
  const { f } = make();
  f.mutated();
  f.mutated();
  f.adopt(f.begin());
  assert.equal(f.isStale(), false, 'the first answer was requested after both changes');
});
