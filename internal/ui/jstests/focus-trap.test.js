import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { focusableElements, createFocusTrap } from '../static/views/focus-trap.js';

function fixture() {
  const dom = new JSDOM(`<!DOCTYPE html><body>
    <button id="opener">Open</button>
    <div id="modal">
      <button id="first">First</button>
      <input id="mid" type="text">
      <button id="disabled" disabled>Disabled</button>
      <section id="hiddenwrap" hidden><button id="buried">Buried</button></section>
      <button id="last">Last</button>
    </div>
  </body>`);
  return dom.window.document;
}

function tab(doc, { shift = false } = {}) {
  const evt = new doc.defaultView.KeyboardEvent('keydown', {
    key: 'Tab', shiftKey: shift, bubbles: true, cancelable: true,
  });
  // dispatchEvent returns false when a listener called preventDefault().
  const notPrevented = doc.dispatchEvent(evt);
  return { prevented: !notPrevented };
}

test('focusableElements skips disabled controls and hidden subtrees', () => {
  const doc = fixture();
  const ids = focusableElements(doc.getElementById('modal')).map(e => e.id);
  assert.deepEqual(ids, ['first', 'mid', 'last']);
});

test('Tab on the last focusable wraps to the first', () => {
  const doc = fixture();
  const modal = doc.getElementById('modal');
  const trap = createFocusTrap(modal, doc);
  trap.activate();
  doc.getElementById('last').focus();
  const { prevented } = tab(doc);
  assert.equal(prevented, true);
  assert.equal(doc.activeElement.id, 'first');
});

test('Shift+Tab on the first focusable wraps to the last', () => {
  const doc = fixture();
  const modal = doc.getElementById('modal');
  const trap = createFocusTrap(modal, doc);
  trap.activate();
  doc.getElementById('first').focus();
  const { prevented } = tab(doc, { shift: true });
  assert.equal(prevented, true);
  assert.equal(doc.activeElement.id, 'last');
});

test('Tab in the middle is left to the browser (not trapped)', () => {
  const doc = fixture();
  const modal = doc.getElementById('modal');
  createFocusTrap(modal, doc).activate();
  doc.getElementById('first').focus();
  const { prevented } = tab(doc); // forward from first → browser handles
  assert.equal(prevented, false);
});

test('release() restores focus to the element active at activate()', () => {
  const doc = fixture();
  const opener = doc.getElementById('opener');
  opener.focus();
  const trap = createFocusTrap(doc.getElementById('modal'), doc);
  trap.activate();
  doc.getElementById('mid').focus();
  trap.release();
  assert.equal(doc.activeElement.id, 'opener');
});

test('release() detaches the keydown handler', () => {
  const doc = fixture();
  const modal = doc.getElementById('modal');
  const trap = createFocusTrap(modal, doc);
  trap.activate();
  trap.release();
  doc.getElementById('last').focus();
  const { prevented } = tab(doc);
  assert.equal(prevented, false); // handler gone → not trapped
});
