import { test } from 'node:test';
import assert from 'node:assert/strict';
import { workerCapacityLine, keepWarmInertNote } from '../static/views/worker-isolation.js';

// workerCapacityLine returns the helper string shown beneath the isolation
// controls on Configuration -> Scaling. It is DOM-free so these tests run
// without jsdom.
//
// RAM formula: maxWorkers * (memMB + 150) / 1024, rounded to nearest GB.
// 20 * (512 + 150) = 13240 MB / 1024 = 12.93 GB -> 13 GB.

test('multiplex mode returns empty string', () => {
  assert.equal(workerCapacityLine('multiplex', 0, 20, 512), '');
});

test('falsy mode returns empty string', () => {
  assert.equal(workerCapacityLine('', 0, 20, 512), '');
  assert.equal(workerCapacityLine(null, 0, 20, 512), '');
  assert.equal(workerCapacityLine(undefined, 0, 20, 512), '');
});

test('per_session + 20 workers + 512 MB shows count and worst-case RAM', () => {
  assert.equal(
    workerCapacityLine('per_session', 0, 20, 512),
    'Up to 20 isolated workers; worst-case ~13 GB',
  );
});

test('per_session + 0 memory omits the RAM clause', () => {
  assert.equal(
    workerCapacityLine('per_session', 0, 20, 0),
    'Up to 20 isolated workers',
  );
});

test('per_session + missing memory omits the RAM clause', () => {
  assert.equal(
    workerCapacityLine('per_session', 0, 5, undefined),
    'Up to 5 isolated workers',
  );
});

test('grouped mode shows workers x clients = sessions', () => {
  assert.equal(
    workerCapacityLine('grouped', 8, 20, 0),
    'Up to 20 workers x 8 clients = 160 sessions',
  );
});

test('grouped mode: session total is workers * groupedSize', () => {
  assert.equal(
    workerCapacityLine('grouped', 4, 10, 512),
    'Up to 10 workers x 4 clients = 40 sessions',
  );
});

test('per_session + maxWorkers 0 returns empty string', () => {
  assert.equal(workerCapacityLine('per_session', 0, 0, 512), '');
});

test('unknown mode returns empty string', () => {
  assert.equal(workerCapacityLine('future_mode', 0, 20, 512), '');
});

// keepWarmInertNote is the Keep warm field's counterpart to the CLI's
// min_warm_replicas advisory: a positive floor on an elastic mode is stored
// but inert, and the note must say so and point at Warm workers.

test('keepWarmInertNote is empty for multiplex and unknown modes', () => {
  assert.equal(keepWarmInertNote(1, 'multiplex'), '');
  assert.equal(keepWarmInertNote(1, ''), '');
  assert.equal(keepWarmInertNote(1, undefined), '');
});

test('keepWarmInertNote is empty when the floor is zero, missing, or malformed', () => {
  assert.equal(keepWarmInertNote(0, 'grouped'), '');
  assert.equal(keepWarmInertNote(undefined, 'grouped'), '');
  assert.equal(keepWarmInertNote('abc', 'per_session'), '');
  assert.equal(keepWarmInertNote(-1, 'grouped'), '');
});

test('keepWarmInertNote names the mode and Warm workers for grouped and per_session', () => {
  for (const mode of ['grouped', 'per_session']) {
    const note = keepWarmInertNote(2, mode);
    assert.match(note, new RegExp(`under ${mode} isolation`));
    assert.match(note, /Warm workers/);
    assert.match(note, /idle/);
  }
  // Numeric strings from an <input> count as a floor too.
  assert.notEqual(keepWarmInertNote('1', 'grouped'), '');
});
