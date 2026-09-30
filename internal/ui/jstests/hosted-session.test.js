import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM, VirtualConsole } from 'jsdom';

// These are the same concatenated bytes sessionui embeds into hosted pages.
const controller = readFileSync(new URL('../static/views/session-controller.js', import.meta.url), 'utf8');
const loader = readFileSync(new URL('../../sessionui/assets/session.js', import.meta.url), 'utf8');
const script = `(function () {\n${controller.replace('export function createSessionController', 'function createSessionController')}${loader}\n})();`;

function mount(response) {
  const errors = [];
  const console = new VirtualConsole();
  console.on('jsdomError', error => errors.push(error.message));
  const dom = new JSDOM('<!doctype html><html><body><input id="work" value="unsaved"></body></html>', { runScripts: 'dangerously', pretendToBeVisual: true, url: 'https://apps.example.com/app/demo/', virtualConsole: console });
  const requests = [];
  dom.window.fetch = async (...args) => { requests.push(args); return response; };
  let shadow;
  const attach = dom.window.Element.prototype.attachShadow;
  dom.window.Element.prototype.attachShadow = function(options) { shadow = attach.call(this, options); return shadow; };
  const tag = dom.window.document.createElement('script');
  tag.id = 'shinyhub-browser-session';
  tag.dataset.sessionUrl = '/app/demo/.shinyhub/session.json';
  tag.dataset.signInUrl = 'https://hub.example.com/app/demo/';
  tag.dataset.userId = '1';
  tag.textContent = script;
  dom.window.document.body.append(tag);
  return { dom, requests, errors, get shadow() { return shadow; } };
}
const flush = () => new Promise(resolve => setImmediate(resolve));

test('hosted renewal uses its app-local endpoint without exposing the credential', async () => {
  const f = mount({ status: 200, ok: true, json: async () => ({ user: { id: 1 }, session: { refresh_after_seconds: 60 } }) });
  try {
    await flush();
    assert.equal(f.requests.length, 1);
    assert.equal(f.requests[0][0], '/app/demo/.shinyhub/session.json');
    assert.equal(f.requests[0][1].credentials, 'same-origin');
    assert.equal(f.requests[0][1].cache, 'no-store');
    assert.equal(f.dom.window.document.getElementById('shinyhub-session-notice'), null);
    assert.deepEqual(f.errors, []);
  } finally { f.dom.window.close(); }
});

for (const status of [401, 403]) {
  test(`hosted ${status} preserves work and provides accessible new-tab recovery`, async () => {
    const f = mount({ status, ok: false });
    try {
      await flush();
      assert.equal(f.dom.window.document.getElementById('work').value, 'unsaved');
      assert.ok(f.shadow);
      assert.equal(f.shadow.querySelector('.notice').getAttribute('role'), 'status');
      const link = f.shadow.querySelector('a');
      assert.equal(link.href, 'https://hub.example.com/app/demo/');
      assert.equal(link.target, '_blank');
      assert.equal(link.rel, 'noopener');
      assert.match(f.shadow.textContent, status === 403 ? /Access to this app has changed/ : /sign-in session ended/);
      assert.deepEqual(f.errors, []);
    } finally { f.dom.window.close(); }
  });
}

test('hosted temporary server failure keeps the page intact without sign-in UI', async () => {
  const f = mount({ status: 503, ok: false });
  try {
    await flush();
    assert.equal(f.dom.window.document.getElementById('work').value, 'unsaved');
    assert.equal(f.dom.window.document.getElementById('shinyhub-session-notice'), null);
    assert.deepEqual(f.errors, []);
  } finally { f.dom.window.close(); }
});
