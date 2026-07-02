import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { nextTabIndex, createTablistNav } from '../static/views/tablist-keys.js';

// --- nextTabIndex: pure WAI-ARIA tablist index resolution ---

test('nextTabIndex moves right and left with wrap-around', () => {
  const hidden = [false, false, false];
  assert.equal(nextTabIndex(hidden, 0, 'ArrowRight'), 1);
  assert.equal(nextTabIndex(hidden, 2, 'ArrowRight'), 0); // wraps
  assert.equal(nextTabIndex(hidden, 0, 'ArrowLeft'), 2);  // wraps
  assert.equal(nextTabIndex(hidden, 1, 'ArrowLeft'), 0);
});

test('nextTabIndex treats Down/Up like Right/Left', () => {
  const hidden = [false, false, false];
  assert.equal(nextTabIndex(hidden, 0, 'ArrowDown'), 1);
  assert.equal(nextTabIndex(hidden, 1, 'ArrowUp'), 0);
});

test('nextTabIndex Home/End jump to first/last visible', () => {
  const hidden = [false, false, false, false];
  assert.equal(nextTabIndex(hidden, 2, 'Home'), 0);
  assert.equal(nextTabIndex(hidden, 1, 'End'), 3);
});

test('nextTabIndex skips hidden tabs', () => {
  // indices 3,4,5 hidden (manager-only tabs hidden from a viewer)
  const hidden = [false, false, false, true, true, true, false];
  assert.equal(nextTabIndex(hidden, 2, 'ArrowRight'), 6); // skips 3,4,5
  assert.equal(nextTabIndex(hidden, 6, 'ArrowRight'), 0); // wraps over hidden
  assert.equal(nextTabIndex(hidden, 0, 'ArrowLeft'), 6);  // wraps back over hidden
  assert.equal(nextTabIndex(hidden, 2, 'End'), 6);        // last VISIBLE, not last
});

test('nextTabIndex returns -1 for non-navigation keys', () => {
  const hidden = [false, false];
  assert.equal(nextTabIndex(hidden, 0, 'Enter'), -1);
  assert.equal(nextTabIndex(hidden, 0, 'a'), -1);
  assert.equal(nextTabIndex(hidden, 0, 'Tab'), -1);
});

test('nextTabIndex returns -1 when no tabs are visible', () => {
  assert.equal(nextTabIndex([true, true], 0, 'ArrowRight'), -1);
});

// --- createTablistNav: DOM keyboard wiring ---

function fixture({ hiddenTabs = [] } = {}) {
  const tabsHtml = ['overview', 'logs', 'configuration', 'data']
    .map((t) => `<a href="/apps/x/${t}" role="tab" id="tab-${t}" data-tab="${t}"${hiddenTabs.includes(t) ? ' hidden' : ''}>${t}</a>`)
    .join('');
  const dom = new JSDOM(`<!DOCTYPE html><body>
    <nav class="settings-tabs" role="tablist">${tabsHtml}</nav>
  </body>`);
  return dom.window.document;
}

function arrow(doc, el, key) {
  el.focus();
  const evt = new doc.defaultView.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true });
  const notPrevented = el.dispatchEvent(evt);
  return { prevented: !notPrevented };
}

test('ArrowRight moves focus to the next tab and activates it', () => {
  const doc = fixture();
  const activated = [];
  createTablistNav(doc.querySelector('.settings-tabs'), doc, { onActivate: (el) => activated.push(el.id) });
  const { prevented } = arrow(doc, doc.getElementById('tab-overview'), 'ArrowRight');
  assert.equal(prevented, true);
  assert.equal(doc.activeElement.id, 'tab-logs');
  assert.deepEqual(activated, ['tab-logs']);
});

test('ArrowRight skips a hidden tab', () => {
  const doc = fixture({ hiddenTabs: ['logs'] });
  createTablistNav(doc.querySelector('.settings-tabs'), doc, {});
  arrow(doc, doc.getElementById('tab-overview'), 'ArrowRight');
  assert.equal(doc.activeElement.id, 'tab-configuration'); // logs skipped
});

test('landing on a tab sets roving tabindex (only it is 0)', () => {
  const doc = fixture();
  createTablistNav(doc.querySelector('.settings-tabs'), doc, {});
  arrow(doc, doc.getElementById('tab-overview'), 'ArrowRight');
  assert.equal(doc.getElementById('tab-logs').getAttribute('tabindex'), '0');
  assert.equal(doc.getElementById('tab-overview').getAttribute('tabindex'), '-1');
  assert.equal(doc.getElementById('tab-configuration').getAttribute('tabindex'), '-1');
});

test('a non-navigation key is left untouched (no preventDefault, no move)', () => {
  const doc = fixture();
  createTablistNav(doc.querySelector('.settings-tabs'), doc, {});
  const { prevented } = arrow(doc, doc.getElementById('tab-overview'), 'Enter');
  assert.equal(prevented, false);
  assert.equal(doc.activeElement.id, 'tab-overview');
});

test('destroy() detaches the handler', () => {
  const doc = fixture();
  const nav = createTablistNav(doc.querySelector('.settings-tabs'), doc, {});
  nav.destroy();
  const { prevented } = arrow(doc, doc.getElementById('tab-overview'), 'ArrowRight');
  assert.equal(prevented, false);
  assert.equal(doc.activeElement.id, 'tab-overview'); // no move after destroy
});
