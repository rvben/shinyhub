import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { installSelectPickerGuard } from '../static/views/select-picker.js';

test('Escape belongs to the open picker, then to its parent dialog after closing', () => {
  const { window } = new JSDOM('<div role="dialog"><select><option>A</option></select><input></div>');
  const doc = window.document;
  window.CSS = { supports: () => true };
  const select = doc.querySelector('select');
  // jsdom has no popup rendering. Model only its open pseudo-class; events,
  // capture/bubbling, and parent handlers use the real DOM event system.
  let open = true;
  select.matches = selector => selector === ':open' && open;
  let dismissals = 0;
  doc.addEventListener('keydown', event => { if (event.key === 'Escape') dismissals++; });
  const dispose = installSelectPickerGuard(doc);
  const key = (target, key = 'Escape') => {
    const event = new window.KeyboardEvent('keydown', { key, bubbles: true, cancelable: true });
    target.dispatchEvent(event);
    assert.equal(event.defaultPrevented, false, 'native closing/keyboard behavior must remain enabled');
  };
  key(select.querySelector('option'));
  assert.equal(dismissals, 0);
  open = false;
  key(select);
  assert.equal(dismissals, 1);
  open = true;
  key(doc.querySelector('input'));
  assert.equal(dismissals, 2, 'other fields must not swallow Escape');
  dispose();
  key(select);
  assert.equal(dismissals, 3);
});

test('standard selects need no event interception in unsupported browsers', () => {
  const { window } = new JSDOM('<select><option>A</option></select>');
  assert.doesNotThrow(() => installSelectPickerGuard(window.document)());
});
