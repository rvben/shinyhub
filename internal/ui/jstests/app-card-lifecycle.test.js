import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';

import {
  createAppCardLifecycle,
  requestAppRestart,
  restartConfirmationCopy,
} from '../static/views/app-card-lifecycle.js';

function setup(options = {}) {
  const dom = new JSDOM('<!doctype html><body><article id="card"></article></body>', {
    pretendToBeVisual: true,
  });
  const doc = dom.window.document;
  const calls = [];
  const control = createAppCardLifecycle(doc, {
    appName: options.appName || 'Sales dashboard',
    onConfirm: () => calls.push('confirm'),
    onRetry: () => calls.push('retry'),
    onViewLogs: () => calls.push('logs'),
  });
  doc.getElementById('card').appendChild(control.root);
  return { dom, doc, control, calls };
}

test('restart confirmation names the app and explains the session impact', () => {
  const copy = restartConfirmationCopy('Sales dashboard');
  assert.equal(copy.title, 'Restart Sales dashboard?');
  assert.match(copy.message, /Active viewer sessions will disconnect/);
  assert.match(copy.message, /report progress here/);
});

test('restart request encodes the slug and returns the refreshed app', async () => {
  let request = null;
  const app = { slug: 'sales dashboard', status: 'running' };
  const result = await requestAppRestart(async (path, init) => {
    request = { path, init };
    return { ok: true, status: 200, json: async () => app };
  }, app.slug);

  assert.deepEqual(request, {
    path: '/api/apps/sales%20dashboard/restart',
    init: { method: 'POST' },
  });
  assert.deepEqual(result, { kind: 'success', app });
});

test('restart request distinguishes authentication, network, and server failures', async () => {
  const unauthorized = await requestAppRestart(
    async () => ({ ok: false, status: 401, json: async () => ({ error: 'expired' }) }),
    'sales',
  );
  assert.deepEqual(unauthorized, { kind: 'unauthorized' });

  const offline = await requestAppRestart(async () => { throw new Error('offline'); }, 'sales');
  assert.equal(offline.kind, 'network-error');
  assert.match(offline.message, /Check your connection/);

  const server = await requestAppRestart(
    async () => ({ ok: false, status: 500, json: async () => ({ error: 'process exited' }) }),
    'sales',
  );
  assert.deepEqual(server, { kind: 'error', message: 'process exited' });
});

test('restart request gives a useful fallback for a malformed error response', async () => {
  const result = await requestAppRestart(
    async () => ({ ok: false, status: 502, json: async () => { throw new Error('invalid JSON'); } }),
    'sales',
  );
  assert.equal(result.kind, 'error');
  assert.match(result.message, /Review its logs/);
});

test('confirmation is inline, focuses the decisive action, and remains cancellable', () => {
  const { doc, control, calls } = setup();

  control.showConfirmation();

  assert.equal(control.prompt.hidden, false);
  assert.equal(control.prompt.querySelector('strong').textContent, 'Restart Sales dashboard?');
  assert.equal(doc.activeElement.textContent, 'Restart app');

  doc.activeElement.click();
  assert.deepEqual(calls, ['confirm']);
  assert.equal(control.prompt.hidden, true);

  control.showConfirmation();
  control.prompt.querySelector('[data-lifecycle-cancel]').click();
  assert.deepEqual(calls, ['confirm']);
  assert.equal(control.prompt.hidden, true);
});

test('pending and success states announce precise, card-local progress', () => {
  const { control } = setup();

  control.setPhase('pending');
  assert.equal(control.feedback.hidden, false);
  assert.equal(control.feedback.getAttribute('role'), 'status');
  assert.equal(control.feedback.querySelector('strong').textContent, 'Restarting app…');
  assert.match(control.feedback.textContent, /Health checks are running/);
  assert.equal(control.feedback.querySelectorAll('button').length, 0);

  control.setPhase('success');
  assert.equal(control.feedback.getAttribute('role'), 'status');
  assert.equal(control.feedback.querySelector('strong').textContent, 'Restart complete');
  assert.match(control.feedback.textContent, /healthy and accepting sessions/);
});

test('failure is assertive and offers retry and logs without blocking other cards', () => {
  const { control, calls } = setup();

  control.setPhase('error', 'process exited before becoming ready');

  assert.equal(control.feedback.getAttribute('role'), 'alert');
  assert.equal(control.feedback.querySelector('strong').textContent, 'Restart failed');
  assert.match(control.feedback.textContent, /process exited before becoming ready/);
  const buttons = [...control.feedback.querySelectorAll('button')];
  assert.deepEqual(buttons.map((button) => button.textContent), ['Retry', 'View logs']);

  buttons[0].click();
  buttons[1].click();
  assert.deepEqual(calls, ['retry', 'logs']);
});

test('long and international app names are written as text, never markup', () => {
  const name = 'لوحة المبيعات <img src=x onerror=alert(1)> 🚀'.repeat(4);
  const { control } = setup({ appName: name });

  control.showConfirmation();

  assert.equal(control.prompt.querySelector('strong').textContent, `Restart ${name}?`);
  assert.equal(control.prompt.querySelector('img'), null);
});

test('clear removes stale feedback and confirmation state', () => {
  const { control } = setup();
  control.showConfirmation();
  control.setPhase('error', 'Timed out');

  control.clear();

  assert.equal(control.prompt.hidden, true);
  assert.equal(control.feedback.hidden, true);
  assert.equal(control.feedback.textContent, '');
});
