import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { createSidebarDrawer } from '../static/views/sidebar-drawer.js';

function setup() {
  const dom = new JSDOM(`<!DOCTYPE html><body>
    <button id="toggle" aria-expanded="false"></button>
    <div id="backdrop" hidden></div>
    <aside id="sidebar"><a href="/">x</a></aside>
    <div id="content"></div>
  </body>`);
  const doc = dom.window.document;
  const traps = [];
  const createFocusTrap = () => {
    const t = { activated: false, released: false, activate() { this.activated = true; }, release() { this.released = true; } };
    traps.push(t);
    return t;
  };
  const drawer = createSidebarDrawer({
    body: doc.body,
    toggle: doc.getElementById('toggle'),
    backdrop: doc.getElementById('backdrop'),
    sidebar: doc.getElementById('sidebar'),
    content: doc.getElementById('content'),
    createFocusTrap,
    doc,
  });
  return { doc, drawer, traps };
}

test('open: sets sidebar-open, aria-expanded, shows backdrop, inerts content, traps focus', () => {
  const { doc, drawer, traps } = setup();
  drawer.open();
  assert.equal(doc.body.classList.contains('sidebar-open'), true);
  assert.equal(doc.getElementById('toggle').getAttribute('aria-expanded'), 'true');
  assert.equal(doc.getElementById('backdrop').hidden, false);
  assert.equal(doc.getElementById('content').hasAttribute('inert'), true);
  assert.equal(traps[0].activated, true);
});

test('close: reverses state and releases the focus trap', () => {
  const { doc, drawer, traps } = setup();
  drawer.open();
  drawer.close();
  assert.equal(doc.body.classList.contains('sidebar-open'), false);
  assert.equal(doc.getElementById('toggle').getAttribute('aria-expanded'), 'false');
  assert.equal(doc.getElementById('backdrop').hidden, true);
  assert.equal(doc.getElementById('content').hasAttribute('inert'), false);
  assert.equal(traps[0].released, true);
});

test('veto-keeps-open: a vetoed (never-mounted) navigation never calls onNavigated; drawer stays open', () => {
  const { drawer } = setup();
  drawer.open();
  // A guard-vetoed navigation never mounts, so the post-mount onNavigated() hook
  // is never invoked. The drawer must remain open so the user keeps context.
  assert.equal(drawer.isOpen(), true);
  // An ALLOWED navigation mounts and calls onNavigated(), which closes it.
  drawer.onNavigated();
  assert.equal(drawer.isOpen(), false);
});

test('onNavigated is a no-op when the drawer is already closed', () => {
  const { drawer } = setup();
  drawer.onNavigated();
  assert.equal(drawer.isOpen(), false);
});

test('toggle opens then closes', () => {
  const { drawer } = setup();
  drawer.toggle();
  assert.equal(drawer.isOpen(), true);
  drawer.toggle();
  assert.equal(drawer.isOpen(), false);
});
