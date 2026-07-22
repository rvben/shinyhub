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

test('a session whose driver call threw is errored', () => {
  assert.equal(
    classifyOutcome({
      disconnected: false,
      error: 'TargetClosedError: Target closed',
      actionsAttempted: 3,
      actionsSucceeded: 3,
    }),
    'errored',
  );
});

test('a disconnected and errored session is disconnected, since a torn-down socket outranks the error', () => {
  assert.equal(
    classifyOutcome({
      disconnected: true,
      error: 'TargetClosedError: Target closed',
      actionsAttempted: 3,
      actionsSucceeded: 1,
    }),
    'disconnected',
  );
});

test('an empty-string error is not treated as an error', () => {
  assert.equal(
    classifyOutcome({ disconnected: false, error: '', actionsAttempted: 3, actionsSucceeded: 3 }),
    'healthy',
  );
});

test('a session with no error field behaves exactly as before', () => {
  assert.equal(
    classifyOutcome({ disconnected: false, actionsAttempted: 3, actionsSucceeded: 3 }),
    'healthy',
  );
});

test('summarize counts errored sessions separately, not as healthy or degraded', () => {
  const s = summarize([
    { disconnected: false, error: 'boom', actionsAttempted: 2, actionsSucceeded: 2 },
    { disconnected: false, error: null, actionsAttempted: 2, actionsSucceeded: 2 },
    { disconnected: false, error: null, actionsAttempted: 2, actionsSucceeded: 1 },
  ]);
  assert.equal(s.total, 3);
  assert.equal(s.errored, 1);
  assert.equal(s.healthy, 1);
  assert.equal(s.degraded, 1);
});
