import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { createNewPersonController } from '../static/views/new-person.js';

function fixture(api) {
  const document = new JSDOM(readFileSync(new URL('../static/index.html', import.meta.url), 'utf8')).window.document;
  let created = 0;
  let copied = '';
  const controller = createNewPersonController({ document, api, origin: 'https://hub.example.com', activate() {}, release() {}, onCreated() { created++; }, onUnauthorized() {}, copy(text) { copied = text; } });
  controller.open();
  document.getElementById('new-user-username').value = 'person';
  return { document, controller, created: () => created, copied: () => copied };
}
const event = { preventDefault() {} };

test('invitation offers all roles, defaults to viewer, then shows a private link', async () => {
  let payload;
  const f = fixture(async (_, options) => { payload = JSON.parse(options.body); return { ok: true, json: async () => ({ token: 'a'.repeat(64), invitation: { id: 'invite', username: 'person', role: 'viewer', expires_at: '2030-01-01T00:00:00Z' } }) }; });
  assert.deepEqual([...f.document.querySelectorAll('input[name="role"]')].map(el => el.value), ['viewer', 'developer', 'operator', 'admin']);
  await f.controller.submit(event);
  assert.equal(payload.role, 'viewer');
  assert.equal(f.created(), 1);
  assert.equal(f.document.getElementById('new-user-success').hidden, false);
  assert.equal(payload.password, undefined);
  assert.equal(f.document.activeElement.id, 'new-user-success-heading');
  f.document.getElementById('new-user-snippet-copy').click();
  assert.match(f.copied(), /https:\/\/hub.example.com/);
  assert.doesNotMatch(f.copied(), /a secure password|shinyhub login/);
  f.controller.close();
  f.controller.open();
  assert.equal(f.document.getElementById('new-user-success').hidden, true);
  assert.equal(f.document.getElementById('new-user-form').hidden, false);
});

test('pending creation cannot be dismissed or submitted twice', async () => {
  let finish;
  let calls = 0;
  const f = fixture(() => { calls++; return new Promise(resolve => { finish = resolve; }); });
  const pending = f.controller.submit(event);
  await f.controller.submit(event);
  f.controller.close();
  assert.equal(calls, 1);
  assert.equal(f.document.getElementById('new-user-modal').hidden, false);
  assert.equal(f.document.getElementById('new-user-close').disabled, true);
  finish({ ok: true, json: async () => ({ token: 'a'.repeat(64), invitation: { id: 'invite', username: 'person', role: 'viewer', expires_at: '2030-01-01T00:00:00Z' } }) });
  await pending;
  assert.equal(f.document.getElementById('new-user-close').disabled, false);
});

test('failed creation retains values and allows recovery without a false success', async () => {
  const f = fixture(async () => ({ ok: false, json: async () => ({ error: 'username already exists' }) }));
  f.document.querySelector('[name="role"][value="operator"]').checked = true;
  await f.controller.submit(event);
  assert.equal(f.created(), 0);
  assert.equal(f.document.getElementById('new-user-success').hidden, true);
  assert.equal(f.document.getElementById('new-user-error').textContent, 'username already exists');
  assert.equal(f.document.querySelector('[name="role"]:checked').value, 'operator');
  assert.equal(f.document.getElementById('new-user-submit').disabled, false);
});


test('replaced invitation opens a fresh dialog with the preserved identity',()=>{
 const f=fixture(async()=>{});
 f.controller.close();
 f.controller.showInvitation({token:'b'.repeat(64),invitation:{username:'person',role:'operator',expires_at:'2030-01-01T00:00:00Z'}},true);
 assert.equal(f.document.getElementById('new-user-modal').hidden,false);
 assert.equal(f.document.getElementById('new-user-heading').textContent,'Invitation replaced');
 assert.match(f.document.getElementById('new-user-result-detail').textContent,/previous link no longer works/);
 assert.match(f.document.getElementById('new-user-success-heading').textContent,/person/);
});
