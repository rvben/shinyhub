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

// countDocListeners wraps addEventListener/removeEventListener on doc so a
// test can assert on how many are actually registered, which is exactly what
// a grid rebuild leak would grow without bound. It counts per (type, capture)
// pair rather than a single total: jsdom's own selector engine registers a
// permanent, never-removed mouseover/mouseout pair on the document the first
// time anything calls querySelector, which has nothing to do with kebab-menu
// and would otherwise show up as a permanent "leak" of its own.
function countDocListeners(doc) {
  const counts = new Map();
  const key = (type, capture) => `${type}:${capture}`;
  const realAdd = doc.addEventListener.bind(doc);
  const realRemove = doc.removeEventListener.bind(doc);
  doc.addEventListener = (type, fn, opts) => {
    const k = key(type, opts === true);
    counts.set(k, (counts.get(k) || 0) + 1);
    return realAdd(type, fn, opts);
  };
  doc.removeEventListener = (type, fn, opts) => {
    const k = key(type, opts === true);
    counts.set(k, (counts.get(k) || 0) - 1);
    return realRemove(type, fn, opts);
  };
  return { count: (type, capture) => counts.get(key(type, capture)) || 0 };
}

function pressArrowDown(node) {
  const { KeyboardEvent } = node.ownerDocument.defaultView;
  node.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
}

test('a menu left open across a rebuild does not leak its document listeners', () => {
  const dom = new JSDOM(`<!DOCTYPE html>
    <div id="grid">
      <div class="app-card">
        <div class="kebab-menu">
          <button id="toggle" type="button" aria-haspopup="menu" aria-expanded="false">…</button>
          <ul id="list" class="kebab-list" role="menu" hidden>
            <li role="none"><button type="button" role="menuitem">Restart</button></li>
          </ul>
        </div>
      </div>
    </div>`);
  const doc = dom.window.document;
  const grid = doc.getElementById('grid');
  const counted = countDocListeners(doc);

  // Open a menu, then rebuild the grid (fresh markup, fresh wireKebab call)
  // WITHOUT closing the old one first, the way a poll-driven refresh would if
  // it landed while the menu was open. Do this across several rebuilds: a
  // leak grows with every cycle, a fix stays flat.
  //
  // The opening keystroke matters here: opening with a click would also
  // reach every stale instance's onDocClick as a bystander (document-level
  // capture-phase listeners fire for any click anywhere in the document),
  // and that handler already self-closes on an unrecognised target even
  // without this fix, since a detached list never contains the click's
  // target. That makes a click-driven rebuild loop pass whether or not the
  // fix is present, proving nothing. An arrow key does not have that
  // shortcut: a stale instance's onKey only acts when the document's active
  // element is inside its own list, which is never true once that list is
  // detached, so on unfixed code the keydown listener is never removed by
  // anything short of an Escape, a Tab, or an actual outside click.
  for (let i = 0; i < 5; i++) {
    grid.innerHTML = `
      <div class="app-card">
        <div class="kebab-menu">
          <button id="toggle" type="button" aria-haspopup="menu" aria-expanded="false">…</button>
          <ul id="list" class="kebab-list" role="menu" hidden>
            <li role="none"><button type="button" role="menuitem">Restart</button></li>
          </ul>
        </div>
      </div>`;
    const toggle = doc.getElementById('toggle');
    const list = doc.getElementById('list');
    wireKebab(toggle, list, doc.querySelector('.app-card'));
    toggle.focus();
    pressArrowDown(toggle); // opens it, attaching this instance's document listeners
    // A real rebuild's next tick replaces grid.innerHTML again on the next
    // loop iteration without ever calling close() on this instance, and
    // nobody clicks or presses Escape/Tab in between.
  }

  // Only the current, still-open instance should have a registered pair; the
  // previous four rebuilds are stale DOM nobody ever explicitly closed. A fix
  // means each rebuild's own ArrowDown reaches every prior stale onKey too
  // (the same capture-phase bystander effect) and detaches it on the spot; a
  // leak means all five pairs are still sitting there.
  assert.equal(counted.count('click', true), 1, 'exactly one click listener should remain: the live menu\'s');
  assert.equal(counted.count('keydown', true), 1, 'exactly one keydown listener should remain: the live menu\'s');
});

test('a stale kebab instance stops reacting to document clicks after its button is removed', () => {
  const { doc, el } = fixture();
  open(el);
  el('list').remove();
  el('toggle').remove(); // simulate the card being replaced wholesale

  const other = doc.createElement('button');
  doc.body.appendChild(other);
  other.focus();
  click(doc.body);

  // The stale instance must not have touched focus (or thrown) on behalf of
  // DOM that no longer exists.
  assert.equal(doc.activeElement, other);
});
