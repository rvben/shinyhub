import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { crashBanner } from '../static/views/crash-banner.js';

function doc() {
  return new JSDOM('<!DOCTYPE html><body></body>').window.document;
}

test('non-crashed app yields no banner', () => {
  for (const status of ['running', 'hibernated', 'stopped', 'degraded']) {
    assert.equal(crashBanner(doc(), { status, last_error: 'x' }, { canManage: true }), null);
  }
  assert.equal(crashBanner(doc(), null, {}), null);
});

test('crashed app shows the failure reason and a manager Restart button', () => {
  let restarted = 0;
  const el = crashBanner(doc(), {
    status: 'crashed',
    last_error: "ModuleNotFoundError: No module named 'pandas'",
  }, { canManage: true, onRestart: () => { restarted++; } });

  assert.ok(el);
  assert.equal(el.getAttribute('role'), 'alert');
  assert.match(el.textContent, /This app crashed/);
  // The reason renders in a <pre> so the traceback keeps its formatting.
  const reason = el.querySelector('.crash-banner-reason');
  assert.equal(reason.tagName, 'PRE');
  assert.match(reason.textContent, /ModuleNotFoundError/);

  const btn = el.querySelector('.crash-banner-restart');
  assert.ok(btn, 'manager sees a Restart button');
  btn.click();
  assert.equal(restarted, 1, 'Restart wires to onRestart');
});

test('a viewer (cannot manage) sees the reason but no Restart button', () => {
  const el = crashBanner(doc(), { status: 'crashed', last_error: 'boom' }, { canManage: false });
  assert.ok(el);
  assert.match(el.textContent, /boom/);
  assert.equal(el.querySelector('.crash-banner-restart'), null);
});

test('a crashed app with no recorded reason still explains itself', () => {
  const el = crashBanner(doc(), { status: 'crashed', last_error: '' }, { canManage: true });
  const reason = el.querySelector('.crash-banner-reason');
  // No traceback to preserve -> a plain paragraph with a fallback message.
  assert.equal(reason.tagName, 'P');
  assert.match(reason.textContent, /could not be started/);
});
