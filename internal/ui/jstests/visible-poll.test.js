import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { startVisiblePoll } from '../static/views/visible-poll.js';

// A minimal document double that lets a test flip `hidden` and fire the same
// event the real Page Visibility API would, without pulling in a full jsdom
// window for tests that do not otherwise need one.
function fakeDoc() {
  const listeners = new Set();
  return {
    hidden: false,
    addEventListener(type, fn) {
      if (type === 'visibilitychange') listeners.add(fn);
    },
    removeEventListener(type, fn) {
      if (type === 'visibilitychange') listeners.delete(fn);
    },
    dispatch() {
      for (const fn of [...listeners]) fn();
    },
    listenerCount() {
      return listeners.size;
    },
  };
}

test('polls on the given interval while the document stays visible', (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] });
  const doc = fakeDoc();
  let calls = 0;
  startVisiblePoll(doc, 1000, () => { calls += 1; });

  assert.equal(calls, 0, 'construction does not tick on its own');
  t.mock.timers.tick(1000);
  assert.equal(calls, 1);
  t.mock.timers.tick(2000);
  assert.equal(calls, 3);
});

test('going hidden clears the pending interval so no further tick fires', (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] });
  const doc = fakeDoc();
  let calls = 0;
  startVisiblePoll(doc, 1000, () => { calls += 1; });

  t.mock.timers.tick(1000);
  assert.equal(calls, 1);

  doc.hidden = true;
  doc.dispatch();
  t.mock.timers.tick(10000);
  assert.equal(calls, 1, 'no tick fires while hidden, no matter how long it stays hidden');
});

test('becoming visible again ticks immediately and resumes the interval', (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] });
  const doc = fakeDoc();
  let calls = 0;
  startVisiblePoll(doc, 1000, () => { calls += 1; });

  t.mock.timers.tick(1000);
  assert.equal(calls, 1);

  doc.hidden = true;
  doc.dispatch();
  assert.equal(calls, 1, 'going hidden itself is not a tick');

  doc.hidden = false;
  doc.dispatch();
  assert.equal(calls, 2, 'becoming visible ticks immediately, without waiting for the interval');

  t.mock.timers.tick(1000);
  assert.equal(calls, 3, 'the interval resumes after the immediate tick');
});

test('a redundant visibilitychange (still visible, or still hidden) does not double-tick or double-arm', (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] });
  const doc = fakeDoc();
  let calls = 0;
  startVisiblePoll(doc, 1000, () => { calls += 1; });

  doc.dispatch(); // fires while already visible
  assert.equal(calls, 1, 'a visibilitychange while already visible still ticks once (harmless), not twice');

  doc.hidden = true;
  doc.dispatch();
  doc.dispatch(); // fires again while already hidden
  t.mock.timers.tick(5000);
  assert.equal(calls, 1, 'still no ticks after a second hidden notification');
});

test('stop() clears the interval and detaches the visibility listener', (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] });
  const doc = fakeDoc();
  let calls = 0;
  const stop = startVisiblePoll(doc, 1000, () => { calls += 1; });

  assert.equal(doc.listenerCount(), 1);
  stop();
  assert.equal(doc.listenerCount(), 0);

  t.mock.timers.tick(10000);
  assert.equal(calls, 0, 'no more ticks after stop');

  doc.hidden = false;
  doc.dispatch(); // a stray event after stop must be inert
  assert.equal(calls, 0);
});

test('stop() is safe to call more than once', (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] });
  const doc = fakeDoc();
  const stop = startVisiblePoll(doc, 1000, () => {});
  stop();
  assert.doesNotThrow(() => stop());
});

test('with no doc (a plain Node test environment), the poll never pauses', (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] });
  let calls = 0;
  startVisiblePoll(null, 1000, () => { calls += 1; });
  t.mock.timers.tick(3000);
  assert.equal(calls, 3, 'falls back to an ordinary always-armed interval');
});

test('starting while already hidden does not arm until visibility returns', (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] });
  const doc = fakeDoc();
  doc.hidden = true;
  let calls = 0;
  startVisiblePoll(doc, 1000, () => { calls += 1; });

  t.mock.timers.tick(5000);
  assert.equal(calls, 0, 'never armed in the first place');

  doc.hidden = false;
  doc.dispatch();
  assert.equal(calls, 1, 'becoming visible arms it and ticks immediately');
});

// A real jsdom document exercises the actual addEventListener/removeEventListener
// path (the fakeDoc above is a hand-rolled double), so this is the integration
// check that the real Page Visibility surface is wired correctly, not just the
// shape this module assumes it has.
test('wires through a real jsdom document, not just the test double\'s shape', (t) => {
  // jsdom reports document.hidden = true by default unless the window claims
  // to be a visual browsing context; without this the whole test would look
  // like a permanently-hidden tab rather than exercising the visible path.
  const dom = new JSDOM('<!doctype html><body></body>', { pretendToBeVisual: true });
  const doc = dom.window.document;
  t.mock.timers.enable({ apis: ['setInterval'] });
  let calls = 0;
  const stop = startVisiblePoll(doc, 1000, () => { calls += 1; });

  t.mock.timers.tick(1000);
  assert.equal(calls, 1);

  Object.defineProperty(doc, 'hidden', { value: true, configurable: true });
  doc.dispatchEvent(new dom.window.Event('visibilitychange'));
  t.mock.timers.tick(5000);
  assert.equal(calls, 1, 'no ticks while the real document reports hidden');

  Object.defineProperty(doc, 'hidden', { value: false, configurable: true });
  doc.dispatchEvent(new dom.window.Event('visibilitychange'));
  assert.equal(calls, 2);

  stop();
  dom.window.close();
});
