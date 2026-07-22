import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyOutcome, summarize } from './detect.mjs';

test('a session whose socket closed is disconnected regardless of action count', () => {
  assert.equal(
    classifyOutcome({ disconnected: true, actionsAttempted: 10, actionsSucceeded: 10 }),
    'disconnected',
  );
});

test('a connected session completing every action is healthy', () => {
  assert.equal(
    classifyOutcome({ disconnected: false, actionsAttempted: 10, actionsSucceeded: 10 }),
    'healthy',
  );
});

test('a connected session losing some actions is degraded, not disconnected', () => {
  assert.equal(
    classifyOutcome({ disconnected: false, actionsAttempted: 10, actionsSucceeded: 6 }),
    'degraded',
  );
});

test('a session that attempted nothing is not counted as healthy', () => {
  assert.equal(
    classifyOutcome({ disconnected: false, actionsAttempted: 0, actionsSucceeded: 0 }),
    'degraded',
  );
});

test('summarize reports rates over the whole fleet', () => {
  const s = summarize([
    { disconnected: true, actionsAttempted: 4, actionsSucceeded: 1 },
    { disconnected: false, actionsAttempted: 4, actionsSucceeded: 4 },
    { disconnected: false, actionsAttempted: 4, actionsSucceeded: 4 },
    { disconnected: false, actionsAttempted: 4, actionsSucceeded: 2 },
  ]);
  assert.equal(s.total, 4);
  assert.equal(s.disconnected, 1);
  assert.equal(s.healthy, 2);
  assert.equal(s.degraded, 1);
  assert.equal(s.disconnectRate, 0.25);
  assert.equal(s.actionSuccessRate, 11 / 16);
});

test('summarize on an empty fleet reports null rates, never zero', () => {
  const s = summarize([]);
  assert.equal(s.total, 0);
  assert.equal(s.disconnectRate, null);
  assert.equal(s.actionSuccessRate, null);
});
