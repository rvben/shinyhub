import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { focusedKey, restoreFocus, siblingKey } from '../static/views/focus-restore.js';

// A region that rebuilds itself: two keyed controls inside, one outside.
function fixture() {
  const dom = new JSDOM(`<!DOCTYPE html>
    <button id="outside">Outside</button>
    <div id="grid">
      <button id="a" data-focus-key="app:demo:menu">Menu</button>
      <button id="b" data-focus-key="app:other:menu">Menu</button>
      <button id="untagged">Untagged</button>
    </div>`);
  const doc = dom.window.document;
  return { doc, grid: doc.getElementById('grid') };
}

// Rebuild the grid the way renderGridVerbatim does: wipe it, then build fresh
// elements carrying the same keys in a different order.
function rebuild(doc, grid, keys) {
  grid.textContent = '';
  for (const key of keys) {
    const el = doc.createElement('button');
    el.dataset.focusKey = key;
    el.textContent = key;
    grid.appendChild(el);
  }
}

test('focusedKey reports the key of the focused control inside the region', () => {
  const { doc, grid } = fixture();
  doc.getElementById('b').focus();
  assert.equal(focusedKey(grid), 'app:other:menu');
});

test('focusedKey ignores focus outside the region', () => {
  const { doc, grid } = fixture();
  doc.getElementById('outside').focus();
  assert.equal(focusedKey(grid), null);
});

test('focusedKey returns null for an untagged control inside the region', () => {
  const { doc, grid } = fixture();
  doc.getElementById('untagged').focus();
  assert.equal(focusedKey(grid), null);
});

test('focusedKey returns null when nothing is focused', () => {
  const { grid } = fixture();
  assert.equal(focusedKey(grid), null);
});

test('focus survives a rebuild that reorders the region', () => {
  const { doc, grid } = fixture();
  doc.getElementById('a').focus();
  const key = focusedKey(grid);

  rebuild(doc, grid, ['app:other:menu', 'app:third:menu', 'app:demo:menu']);
  assert.equal(doc.activeElement, doc.body, 'the rebuild must have destroyed the focused control');

  assert.equal(restoreFocus(grid, key), true);
  assert.equal(doc.activeElement.dataset.focusKey, 'app:demo:menu');
});

test('restoreFocus reports false when the control is gone and moves nothing', () => {
  const { doc, grid } = fixture();
  doc.getElementById('a').focus();
  const key = focusedKey(grid);

  // The app was deleted, or a filter now excludes it.
  rebuild(doc, grid, ['app:other:menu']);

  assert.equal(restoreFocus(grid, key), false);
  assert.equal(doc.activeElement, doc.body);
});

test('restoreFocus is a no-op without a key, so a rebuild never steals focus', () => {
  const { doc, grid } = fixture();
  const outside = doc.getElementById('outside');
  outside.focus();

  // focusedKey said null because focus was elsewhere; the rebuild must leave it there.
  assert.equal(restoreFocus(grid, focusedKey(grid)), false);
  assert.equal(doc.activeElement, outside);
});

test('siblingKey names another control on the same subject', () => {
  assert.equal(siblingKey('app:demo:deploy', 'title'), 'app:demo:title');
  assert.equal(siblingKey('group:analytics:edit', 'toggle'), 'group:analytics:toggle');
});

test('siblingKey returns null for a key that names no control', () => {
  assert.equal(siblingKey('', 'title'), null);
  assert.equal(siblingKey(null, 'title'), null);
  assert.equal(siblingKey('bare', 'title'), null);
});

test('a card keeps the keyboard when its own control is the thing that vanished', () => {
  // "Deploy first release" disappears the moment that deploy succeeds. The
  // card is still on screen, so focus belongs on it, not on <body>.
  const { doc, grid } = fixture();
  rebuild(doc, grid, ['app:demo:title', 'app:demo:deploy']);
  grid.querySelector('[data-focus-key="app:demo:deploy"]').focus();
  const key = focusedKey(grid);

  rebuild(doc, grid, ['app:demo:title', 'app:demo:open', 'app:demo:menu']);

  assert.equal(restoreFocus(grid, key), false, 'the deploy button is genuinely gone');
  assert.equal(restoreFocus(grid, siblingKey(key, 'title')), true);
  assert.equal(doc.activeElement.dataset.focusKey, 'app:demo:title');
});

test('focusedKey and restoreFocus tolerate a missing region', () => {
  assert.equal(focusedKey(null), null);
  assert.equal(restoreFocus(null, 'app:demo:menu'), false);
});
