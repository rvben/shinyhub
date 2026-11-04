import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { createNewPersonController } from '../static/views/new-person.js';

function fixture(api, requestOptions = {}) {
  const document = new JSDOM(readFileSync(new URL('../static/index.html', import.meta.url), 'utf8')).window.document;
  let created = 0;
  let copied = '';
  const controller = createNewPersonController({ document, api, requestOptions, origin: 'https://hub.example.com', activate() {}, release() {}, onCreated() { created++; }, onUnauthorized() {}, copy(text) { copied = text; } });
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


test('leaving Identity clears invitation secrets and ignores a late creation response', async () => {
  let finish;
  const f = fixture(() => new Promise(resolve => { finish = resolve; }));
  const pending = f.controller.submit(event);
  f.controller.reset();
  finish({ok: true, json: async () => ({token: 'private-invitation', invitation: {username: 'person', role: 'viewer', expires_at: '2030-01-01T00:00:00Z'}})});
  await pending;
  assert.equal(f.created(), 0);
  assert.equal(f.document.getElementById('new-user-modal').hidden, true);
  assert.equal(f.document.getElementById('new-user-snippet').textContent, '');
  f.controller.open();
  assert.equal(f.document.getElementById('new-user-submit').disabled, false);
});

for (const stalledBody of [false,true]) test(`stalled invitation ${stalledBody?'body':'connection'} unlocks with status reconciliation instead of a blind retry`,async()=>{
 let expire,postCount=0,signal;
 const f=fixture(async(_path,options)=>{
  if(options.method==='POST') {postCount++;signal=options.signal;return stalledBody?{ok:true,status:201,json:()=>new Promise(()=>{})}:new Promise(()=>{});}
  return {ok:true,status:200,json:async()=>({items:[{username:'person',role:'viewer'}]})};
 },{setTimer:fn=>{expire=fn;return 1;},clearTimer(){}});
 const pending=f.controller.submit(event);expire();await pending;
 assert.equal(signal.aborted,true);assert.equal(f.document.getElementById('new-user-close').disabled,false);
 assert.equal(f.document.getElementById('new-user-submit').disabled,true);
 assert.equal(f.document.getElementById('new-user-check').hidden,false);
 assert.match(f.document.getElementById('new-user-error').textContent,/may have been created/);
 await f.controller.submit(event);assert.equal(postCount,1);
 await f.controller.checkStatus();
 assert.equal(f.document.getElementById('new-user-view-invitations').hidden,false);
 assert.match(f.document.getElementById('new-user-error').textContent,/already exists/);
 assert.equal(postCount,1);
});
test('a confirmed absent invitation allows retry with the original values',async()=>{
 let expire;
 const f=fixture(async(_path,options)=>options.method==='POST'?new Promise(()=>{}):{ok:true,status:200,json:async()=>({items:[]})},{setTimer:fn=>{expire=fn;return 1;},clearTimer(){}});
 const pending=f.controller.submit(event);expire();await pending;await f.controller.checkStatus();
 assert.equal(f.document.getElementById('new-user-submit').disabled,false);
 assert.equal(f.document.getElementById('new-user-username').value,'person');
 assert.match(f.document.getElementById('new-user-error').textContent,/No pending invitation/);
});
test('failed status reconciliation keeps creation disabled but permits closing',async()=>{
 let expire;
 const f=fixture(async(_path,options)=>options.method==='POST'?new Promise(()=>{}):{ok:false,status:503,json:async()=>({})},{setTimer:fn=>{expire=fn;return 1;},clearTimer(){}});
 const pending=f.controller.submit(event);expire();await pending;await f.controller.checkStatus();
 assert.equal(f.document.getElementById('new-user-submit').disabled,true);
 assert.equal(f.document.getElementById('new-user-close').disabled,false);
 assert.match(f.document.getElementById('new-user-error').textContent,/Could not check/);
 f.controller.close();assert.equal(f.document.getElementById('new-user-modal').hidden,true);
});

test('an ambiguous server failure requires reconciliation and retains the original identity',async()=>{
 const f=fixture(async()=>({ok:false,status:502,json:async()=>({error:'upstream unavailable'})}));
 await f.controller.submit(event);
 assert.equal(f.document.getElementById('new-user-submit').hidden,true);
 assert.equal(f.document.getElementById('new-user-username').disabled,true);
 assert.equal(f.document.getElementById('new-user-cancel').disabled,false);
 assert.equal(f.document.getElementById('new-user-check').hidden,false);
 assert.equal(f.document.getElementById('new-user-username').value,'person');
 f.controller.close();f.controller.open();
 assert.equal(f.document.getElementById('new-user-username').value,'person');
 assert.equal(f.document.getElementById('new-user-check').hidden,false);
});
