import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM, VirtualConsole } from 'jsdom';

const BANNER_JS = new URL('../../supportui/assets/banner.js', import.meta.url);
const source = readFileSync(BANNER_JS, 'utf8');
if (!source.includes('shinyhub-support-session')) {
  throw new Error(`${BANNER_JS.pathname} does not look like the support-session banner`);
}
if (source.includes('.innerHTML')) {
  throw new Error('support-session banner must remain compatible with Trusted Types enforcement');
}

const flush = () => new Promise(resolve => setImmediate(resolve));

function mount({ remaining = 15 * 60 * 1000 } = {}) {
  const virtualConsole = new VirtualConsole();
  const jsdomErrors = [];
  virtualConsole.on('jsdomError', error => jsdomErrors.push(error.message));
  const dom = new JSDOM('<!doctype html><html><head></head><body><main id="app">App</main></body></html>', {
    runScripts: 'dangerously',
    url: 'https://apps.example.com/app/sales/',
    virtualConsole,
  });
  const { window } = dom;
  const { document } = window;
  const roots = new WeakMap();
  const nativeAttachShadow = window.Element.prototype.attachShadow;
  window.Element.prototype.attachShadow = function attachShadow(options) {
    const root = nativeAttachShadow.call(this, options);
    roots.set(this, root);
    return root;
  };

  let now = 1_800_000_000_000;
  window.Date.now = () => now;
  const intervals = [];
  window.setInterval = fn => { intervals.push(fn); return intervals.length; };
  const submissions = [];
  window.HTMLFormElement.prototype.submit = function submit() { submissions.push(this.action); };

  const tag = document.createElement('script');
  tag.id = 'shinyhub-support-session-loader';
  tag.dataset.stopUrl = '/app/sales/.shinyhub/support-session/stop';
  tag.dataset.actor = 'admin';
  tag.dataset.subject = 'alice';
  tag.dataset.sessionId = 'support-id';
  tag.dataset.expiresAt = String(now + remaining);
  tag.textContent = source;
  document.body.appendChild(tag);

  const host = document.getElementById('shinyhub-support-session');
  return {
    dom, window, document, host, root: roots.get(host), intervals, submissions, jsdomErrors,
    setRemaining(ms) { now = Number(tag.dataset.expiresAt) - ms; },
  };
}

test('the safety rail lives outside SPA body flow and self-heals after removal', async () => {
  const view = mount();
  assert.equal(view.host.parentNode, view.document.documentElement);
  view.document.body.replaceChildren(view.document.createElement('main'));
  assert.equal(view.host.parentNode, view.document.documentElement, 'body replacement must not remove the rail');
  view.host.remove();
  await flush();
  await flush();
  assert.equal(view.host.parentNode, view.document.documentElement, 'direct removal must be repaired');
  assert.deepEqual(view.jsdomErrors, []);
  view.dom.window.close();
});

test('the visible countdown is quiet and announces only meaningful thresholds', () => {
  const view = mount();
  const timer = view.root.querySelector('.timer');
  const status = view.root.querySelector('.sr');
  assert.equal(timer.hasAttribute('aria-live'), false);
  assert.equal(status.getAttribute('aria-live'), 'polite');
  assert.equal(status.textContent, '');

  view.setRemaining(4 * 60 * 1000);
  view.intervals[0]();
  assert.equal(status.textContent, 'Support session ends in five minutes.');
  view.setRemaining(3 * 60 * 1000);
  view.intervals[0]();
  assert.equal(status.textContent, 'Support session ends in five minutes.');
  view.setRemaining(45 * 1000);
  view.intervals[0]();
  assert.equal(status.textContent, 'Support session ends in one minute.');
  view.dom.window.close();
});

test('expiry submits the native stop form once without a timer retry loop', async () => {
  const view = mount({ remaining: 0 });
  await flush();
  assert.equal(view.submissions.length, 1, 'expiry should make one automatic stop attempt');
  view.intervals[0]();
  view.intervals[0]();
  await flush();
  assert.equal(view.submissions.length, 1, 'timer must not create a retry loop');
  view.dom.window.close();
});

test('the manual exit is a same-origin native POST form', () => {
  const view = mount();
  const form = view.root.querySelector('form');
  assert.equal(form.method, 'post');
  assert.equal(form.action, 'https://apps.example.com/app/sales/.shinyhub/support-session/stop');
  view.root.querySelector('button').click();
  assert.equal(view.submissions.length, 1);
  view.dom.window.close();
});
