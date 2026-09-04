import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { wireKebab, shouldReturnFocus } from '../static/views/kebab-menu.js';

// A card kebab: a toggle, a list of lifecycle items, and a control outside the
// menu that a handler could legitimately move focus to.
function fixture() {
  const dom = new JSDOM(`<!DOCTYPE html>
    <div class="app-card">
      <div class="kebab-menu">
        <button id="toggle" type="button" aria-haspopup="menu" aria-expanded="false">…</button>
        <ul id="list" class="kebab-list" role="menu" hidden>
          <li role="none"><button id="restart" type="button" role="menuitem">Restart</button></li>
          <li role="none"><button id="sleep" type="button" role="menuitem">Sleep</button></li>
          <li role="none" hidden><button id="stop" type="button" role="menuitem">Stop</button></li>
        </ul>
      </div>
      <button id="confirm" type="button">Restart app</button>
    </div>`);
  const doc = dom.window.document;
  const el = (id) => doc.getElementById(id);
  const handle = wireKebab(el('toggle'), el('list'), doc.querySelector('.app-card'));
  return { dom, doc, el, handle };
}

function click(node) {
  const { MouseEvent } = node.ownerDocument.defaultView;
  node.dispatchEvent(new MouseEvent('click', { bubbles: true }));
}

function open(el) {
  click(el('toggle'));
}

test('opening the menu focuses its first available item', () => {
  const { doc, el } = fixture();
  open(el);
  assert.equal(doc.activeElement, el('restart'));
  assert.equal(el('list').hidden, false);
  assert.equal(el('toggle').getAttribute('aria-expanded'), 'true');
});

test('activating an item hands the keyboard back to the toggle', () => {
  // Without this, closing hides the list while the clicked item holds focus and
  // the browser drops focus on <body>: the visitor has to tab in from the top of
  // the page to reach the card they were working in.
  const { doc, el } = fixture();
  open(el);
  click(el('restart'));

  assert.equal(el('list').hidden, true, 'the menu still closes');
  assert.equal(doc.activeElement, el('toggle'));
});

test('an item whose handler has already lost focus still hands the keyboard back', () => {
  // The Sleep/Start path sets btn.disabled = true before its first await, and a
  // browser blurs an element the moment it is disabled. Focus is therefore
  // already on <body> when the menu closes, so "is focus still in the list" is
  // not enough to recognize this case.
  //
  // jsdom does not implement blur-on-disable, so the test does it explicitly
  // rather than relying on the disable to have that effect here.
  const { doc, el } = fixture();
  open(el);
  el('sleep').focus();
  el('sleep').addEventListener('click', (e) => {
    e.currentTarget.blur();
    e.currentTarget.disabled = true;
  });
  click(el('sleep'));

  assert.equal(doc.activeElement, el('toggle'));
});

test('an item that opens a confirmation keeps focus there', () => {
  // Restart focuses the card's "Restart app" button. Reclaiming focus for the
  // toggle here would be the same bug pointed the other way.
  const { doc, el } = fixture();
  open(el);
  el('restart').addEventListener('click', () => el('confirm').focus());
  click(el('restart'));

  assert.equal(el('list').hidden, true);
  assert.equal(doc.activeElement, el('confirm'));
});

test('a click on the list padding neither closes the menu nor moves focus', () => {
  const { doc, el } = fixture();
  open(el);
  click(el('list'));

  assert.equal(el('list').hidden, false);
  assert.equal(doc.activeElement, el('restart'));
});

test('Escape closes the menu and returns focus to the toggle', () => {
  const { dom, doc, el } = fixture();
  open(el);
  doc.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));

  assert.equal(el('list').hidden, true);
  assert.equal(doc.activeElement, el('toggle'));
});

test('arrow keys cycle the available items and skip hidden ones', () => {
  const { dom, doc, el } = fixture();
  open(el);
  const down = () => doc.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));

  down();
  assert.equal(doc.activeElement, el('sleep'));
  // Stop sits in a hidden <li>, so the cycle wraps past it back to the top.
  down();
  assert.equal(doc.activeElement, el('restart'));
});

test('closing through the handle leaves focus alone', () => {
  // The metrics poller closes a menu whose app no longer offers any action. The
  // visitor may be anywhere on the page by then; that close is not their doing.
  const { doc, el, handle } = fixture();
  open(el);
  el('confirm').focus();
  handle.close();

  assert.equal(el('list').hidden, true);
  assert.equal(doc.activeElement, el('confirm'));
});

test('shouldReturnFocus says yes for focus inside the list and for no focus', () => {
  const { doc, el } = fixture();
  assert.equal(shouldReturnFocus(el('list'), el('restart')), true);
  assert.equal(shouldReturnFocus(el('list'), doc.body), true);
  assert.equal(shouldReturnFocus(el('list'), null), true);
});

test('shouldReturnFocus says no for a control the handler focused elsewhere', () => {
  const { el } = fixture();
  assert.equal(shouldReturnFocus(el('list'), el('confirm')), false);
  assert.equal(shouldReturnFocus(null, el('confirm')), false);
});

test('wireKebab tolerates missing elements', () => {
  const { el } = fixture();
  assert.equal(wireKebab(null, el('list'), null), null);
  assert.equal(wireKebab(el('toggle'), null, null), null);
});
