import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { runInFlight, isInFlight } from '../static/views/in-flight.js';

function fixture() {
  const dom = new JSDOM('<!DOCTYPE html><button id="submit">Save</button>');
  const doc = dom.window.document;
  return { doc, button: doc.getElementById('submit') };
}

// deferred lets a test control exactly when the guarded action resolves, so
// it can assert on the state WHILE the call is still pending.
function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

test('disables the submitter and marks it aria-busy while the action is pending', async () => {
  const { button } = fixture();
  const gate = deferred();
  const call = runInFlight(button, () => gate.promise);
  assert.equal(button.disabled, true);
  assert.equal(button.getAttribute('aria-busy'), 'true');
  gate.resolve('done');
  assert.equal(await call, 'done');
  assert.equal(button.disabled, false);
  assert.equal(button.hasAttribute('aria-busy'), false);
});

test('a second call while the first is pending is a no-op', async () => {
  const { button } = fixture();
  let calls = 0;
  const gate = deferred();
  const first = runInFlight(button, () => { calls += 1; return gate.promise; });
  assert.equal(isInFlight(button), true);
  const second = runInFlight(button, () => { calls += 1; return Promise.resolve('second'); });
  assert.equal(await second, undefined, 'a call arriving while one is pending must not run the action');
  assert.equal(calls, 1);
  gate.resolve('first');
  await first;
  assert.equal(isInFlight(button), false);
});

test('restores the submitter after the action throws', async () => {
  const { button } = fixture();
  await assert.rejects(
    () => runInFlight(button, () => { throw new Error('network error'); }),
    /network error/,
  );
  assert.equal(button.disabled, false);
  assert.equal(button.hasAttribute('aria-busy'), false);
  assert.equal(isInFlight(button), false);
});

test('restores the submitter after the action rejects', async () => {
  const { button } = fixture();
  await assert.rejects(() => runInFlight(button, () => Promise.reject(new Error('boom'))), /boom/);
  assert.equal(button.disabled, false);
  assert.equal(isInFlight(button), false);
});

test('a new call is accepted again once the previous one has settled', async () => {
  const { button } = fixture();
  await runInFlight(button, () => Promise.resolve(1));
  let ran = false;
  await runInFlight(button, () => { ran = true; return Promise.resolve(2); });
  assert.equal(ran, true);
});

test('restores focus to the submitter if it held focus when the call began', async () => {
  const { doc, button } = fixture();
  button.focus();
  assert.equal(doc.activeElement, button);
  const gate = deferred();
  const call = runInFlight(button, () => gate.promise);
  // jsdom does not blur a control on disable the way a browser does, but the
  // guard must not rely on that: it captured "had focus" before disabling.
  gate.resolve();
  await call;
  assert.equal(doc.activeElement, button);
});

test('does not steal focus back if the user moved on while the call was pending', async () => {
  const { doc, button } = fixture();
  const other = doc.createElement('button');
  doc.body.appendChild(other);
  button.focus();
  const gate = deferred();
  const call = runInFlight(button, () => gate.promise);
  other.focus();
  gate.resolve();
  await call;
  assert.equal(doc.activeElement, other);
});

test('never disables and never re-enables a submitter that was already disabled', async () => {
  const { button } = fixture();
  button.disabled = true;
  await runInFlight(button, () => Promise.resolve());
  assert.equal(button.disabled, true, 'a submitter disabled for another reason must stay disabled');
});

test('runs the action normally when called with no submitter', async () => {
  let ran = false;
  const result = await runInFlight(null, () => { ran = true; return Promise.resolve('ok'); });
  assert.equal(ran, true);
  assert.equal(result, 'ok');
});
