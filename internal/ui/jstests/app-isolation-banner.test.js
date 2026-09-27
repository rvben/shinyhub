import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { shouldShowAppIsolationBanner, wireAppIsolationBanner } from '../static/views/app-isolation-banner.js';

function memoryStorage() {
  const values = new Map();
  return {
    getItem: (key) => (values.has(key) ? values.get(key) : null),
    setItem: (key, value) => values.set(key, String(value)),
  };
}

test('the banner is hidden when the server sends no warning', () => {
  const storage = memoryStorage();
  assert.equal(shouldShowAppIsolationBanner(false, storage), false);
  assert.equal(shouldShowAppIsolationBanner(undefined, storage), false);
});

test('the banner shows once the server warns and storage has no dismissal', () => {
  const storage = memoryStorage();
  assert.equal(shouldShowAppIsolationBanner(true, storage), true);
});

test('a stored dismissal suppresses the banner even though the server still warns', () => {
  const storage = memoryStorage();
  storage.setItem('shinyhub.dismissedAppIsolationWarning', '1');
  assert.equal(shouldShowAppIsolationBanner(true, storage), false);
});

test('a storage that throws on read is treated as not dismissed', () => {
  const storage = {
    getItem() { throw new Error('storage disabled'); },
    setItem() { throw new Error('storage disabled'); },
  };
  assert.equal(shouldShowAppIsolationBanner(true, storage), true);
});

test('clicking dismiss hides the banner and persists the dismissal', () => {
  const doc = new JSDOM('<!doctype html><section id="root"><button id="dismiss"></button></section>').window.document;
  const root = doc.getElementById('root');
  const dismissButton = doc.getElementById('dismiss');
  const storage = memoryStorage();

  wireAppIsolationBanner({ root, dismissButton, storage });
  assert.equal(root.hidden, false);
  dismissButton.click();
  assert.equal(root.hidden, true);
  assert.equal(shouldShowAppIsolationBanner(true, storage), false);
});

test('wiring is a no-op when the markup is missing', () => {
  // Must not throw: an SPA shell mid-deploy, or a test fixture without the
  // banner markup, should not break the caller that wires it unconditionally.
  assert.doesNotThrow(() => wireAppIsolationBanner({}));
  assert.doesNotThrow(() => wireAppIsolationBanner());
});

test('a dismiss click never throws when storage is unavailable', () => {
  const doc = new JSDOM('<!doctype html><section id="root"><button id="dismiss"></button></section>').window.document;
  const root = doc.getElementById('root');
  const dismissButton = doc.getElementById('dismiss');
  const storage = {
    getItem() { throw new Error('storage disabled'); },
    setItem() { throw new Error('storage disabled'); },
  };

  wireAppIsolationBanner({ root, dismissButton, storage });
  assert.doesNotThrow(() => dismissButton.click());
  assert.equal(root.hidden, true);
});
