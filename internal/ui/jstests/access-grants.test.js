import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {JSDOM} from 'jsdom';
import {runInFlight} from '../static/views/in-flight.js';
const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const app = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
const response = items => ({ok: true, status: 200, json: async () => ({items}), headers: new Map()});
const deferred = () => { let resolve; const promise = new Promise(done => resolve = done); return {promise, resolve}; };
const settle = () => new Promise(resolve => setImmediate(resolve));
function harness(api, user = {id:1}) {
  const doc = new JSDOM(html).window.document;
  const source = app.slice(app.indexOf('  let accessPanelRevision ='), app.indexOf('  logPaneClose.addEventListener'));
  const add = app.slice(app.indexOf('  async function addMember('), app.indexOf('  // Danger zone:'));
  const messages = [], navigations = [], state = {user,apps:[{slug:'a',can_manage:true}]};
  const wrappedApi = async (path, options) => {
    const result = await api(path, options);
    if (path === '/api/apps/a' && result?.ok && !('can_manage' in await result.json())) return {ok:true,status:200,json:async()=>({app:{slug:'a'},can_manage:true})};
    return result;
  };
  const h = new Function('document', 'state', 'api', 'runInFlight', 'setError', 'flashToast', 'handleUnauthorized', 'router', `let settingsSlug='a', settingsRevision=0, detailApp={slug:'a',can_manage:true}; const closeEnvForm=()=>{},clearSettingsDirty=()=>{},syncDetailHeaderActions=()=>{},syncAppContext=()=>{}; ${source}\n${add}\nreturn {refreshMemberList,refreshGroupAccessList,addMember,addGroupAccess,reconcileGroupPermissions, switchApp(slug) {settingsSlug=slug;accessPanelRevision++;}, get revision(){return accessPanelRevision;}};`)(doc, state, wrappedApi, runInFlight, (el,msg) => {el.textContent=msg;el.hidden=!msg;}, msg => messages.push(msg), async () => messages.push('unauthorized'), {navigate:(path,options)=>navigations.push({path,options})});
  return {...h, doc, messages, state, navigations};
}
const submit = {preventDefault(){}};
test('list failures show retry, never an empty list that suggests no grants', async () => {
  let fail = true;
  const h = harness(async () => {if (fail) throw Error('offline'); return response([]);});
  await h.refreshMemberList(); await h.refreshGroupAccessList();
  for (const id of ['members-list','group-access-list']) {
    assert.match(h.doc.getElementById(id).textContent, /Could not load/);
    assert.equal(h.doc.getElementById(id).getAttribute('aria-busy'), 'false');
    assert.equal(h.doc.querySelectorAll(`#${id} button`).length,1);
  }
  fail=false;
  h.doc.querySelector('#members-list button').click();
  await settle();
  assert.equal(h.doc.getElementById('members-list').textContent, 'No members added.');
});
test('a delayed list cannot paint a different mounted app or supersede a newer refresh', async () => {
  const pending=deferred(); let calls=0;
  const h=harness(async () => ++calls===1 ? pending.promise : response([{user_id:2,username:'current',role:'manager'}]));
  const first=h.refreshMemberList();
  h.switchApp('b'); await h.refreshMemberList();
  pending.resolve(response([{user_id:3,username:'stale',role:'viewer'}])); await first;
  assert.match(h.doc.getElementById('members-list').textContent,/current/);
  assert.doesNotMatch(h.doc.getElementById('members-list').textContent,/stale/);
  const other=deferred(); calls=0;
  const h2=harness(async()=> ++calls===1 ? other.promise : response([]));
  const earlier=h2.refreshGroupAccessList(); await h2.refreshGroupAccessList();
  other.resolve(response([{group:'outdated',role:'manager'}])); await earlier;
  assert.equal(h2.doc.getElementById('group-access-list').textContent,'No groups added.');
});
test('member roles remain editable; failed changes restore the role and preserve removal', async () => {
  const h=harness(async(_,options)=> options ? {ok:false,status:503} : response([{user_id:2,username:'analyst',role:'viewer'}]));
  await h.refreshMemberList();
  const select=h.doc.querySelector('#members-list select');
  select.value='manager';select.dispatchEvent(new h.doc.defaultView.Event('change'));await settle();
  assert.equal(select.value,'viewer'); assert.equal(select.disabled,false);
  assert.equal(h.doc.querySelector('#members-list button').disabled,false);
  assert.match(h.doc.getElementById('grant-error').textContent,/Could not update/);
});
test('failed group removal keeps the grant visible and explains how to recover', async () => {
  const h=harness(async(_,options)=>options ? {ok:false,status:503} : response([{group:'research',role:'manager',source:'manual'}]));
  await h.refreshGroupAccessList();h.doc.querySelector('#group-access-list button').click();await settle();
  assert.match(h.doc.getElementById('group-access-list').textContent,/research/);
  assert.match(h.doc.getElementById('group-access-error').textContent,/Could not remove research/);
  assert.equal(h.doc.querySelector('#group-access-list button').disabled,false);
});
test('removing the last grant reveals an explicit empty state and returns focus', async () => {
  let removed=false;
  const h=harness(async(_,options)=> {if(options){removed=true;return {ok:true,status:204};}return response(removed?[]:[{user_id:2,username:'analyst',role:'viewer'}]);});
  await h.refreshMemberList();h.doc.querySelector('#members-list button').click();await settle();
  assert.equal(h.doc.getElementById('members-list').textContent,'No members added.');
  assert.equal(h.doc.activeElement,h.doc.querySelector('#member-add summary'));
});
test('adding an existing member preserves their server role and reports no assumed viewer role', async () => {
  let payload;
  const h=harness(async(_,options)=>{if(options){payload=JSON.parse(options.body);return {ok:true,status:204};}return response([{user_id:2,username:'analyst',role:'manager'}]);});
  h.doc.getElementById('member-add').open=true;
  h.doc.getElementById('grant-username').value='analyst';await h.addMember(submit);
  assert.deepEqual(payload,{username:'analyst'});
  assert.equal(h.doc.querySelector('#members-list select').value,'manager');
  assert.equal(h.doc.getElementById('member-add').open,false);
  assert.doesNotMatch(h.messages.join(' '),/as viewer/);
});
test('a delayed group add preserves a newer draft and cannot clear a different app', async () => {
  const pending=deferred(); const h=harness(async(_,options)=>options?pending.promise:response([]));
  const input=h.doc.getElementById('group-access-name'); input.value='first';
  h.doc.getElementById('group-add').open=true;
  const save=h.addGroupAccess(submit);input.value='next';
  pending.resolve({ok:true,status:204,headers:new Map()});await save;
  assert.equal(input.value,'next');assert.equal(h.doc.getElementById('group-add').open,true);
  const later=deferred();const other=harness(async()=>later.promise);
  other.doc.getElementById('group-access-name').value='old';const old=other.addGroupAccess(submit);
  other.switchApp('b');other.doc.getElementById('group-access-name').value='current';
  later.resolve({ok:true,status:204,headers:new Map()});await old;
  assert.equal(other.doc.getElementById('group-access-name').value,'current');assert.deepEqual(other.messages,[]);
});
test('deploy-managed groups explain the unavailable action and untrusted names remain text',async()=>{
  const h=harness(async()=>response([{group:'<img src=x onerror=alert(1)>',role:'viewer',source:'manifest'}]));
  await h.refreshGroupAccessList();
  assert.equal(h.doc.querySelector('#group-access-list img'),null);
  assert.equal(h.doc.querySelector('#group-access-list button'),null);
  assert.match(h.doc.getElementById('group-access-list').textContent,/deploy to change/);
});

test('own membership has a visible explanation associated with disabled controls',async()=>{
  const h=harness(async()=>response([{user_id:1,username:'operator',role:'manager'}]));
  await h.refreshMemberList();
  const controls=[...h.doc.querySelectorAll('#members-list select, #members-list button')];
  assert.ok(controls.every(el=>el.disabled));
  for(const control of controls) assert.match(h.doc.getElementById(control.getAttribute('aria-describedby')).textContent,/Another manager/);
  assert.match(h.doc.querySelector('#members-list').textContent,/operator \(you\)/);
});
test('group role changes persist the exact grant and roll back on failure',async()=>{
  let payload, fail=true;
  const h=harness(async(_,options)=>{
    if(options?.method==='POST'){payload=JSON.parse(options.body);return {ok:!fail,status:fail?503:204};}
    return response([{group:'research',role:'viewer',source:'manual'}]);
  });
  await h.refreshGroupAccessList();
  const select=h.doc.querySelector('#group-access-list select');
  select.value='manager';select.dispatchEvent(new h.doc.defaultView.Event('change'));await settle();
  assert.deepEqual(payload,{group:'research',role:'manager'});assert.equal(select.value,'viewer');
  assert.match(h.doc.getElementById('group-access-error').textContent,/Could not update/);
  fail=false;select.value='manager';select.dispatchEvent(new h.doc.defaultView.Event('change'));await settle();
  assert.equal(select.dataset.previous,'manager');assert.equal(select.disabled,false);
  assert.match(h.messages.join(' '),/Updated group research to manager/);
});
test('duplicate groups focus the existing role without issuing another write',async()=>{
  let writes=0;
  const h=harness(async(_,options)=>{if(options?.method)writes++;return response([{group:'research',role:'manager',source:'manual'}]);});
  await h.refreshGroupAccessList();h.doc.getElementById('group-access-name').value='research';
  await h.addGroupAccess(submit);
  assert.equal(writes,0);assert.equal(h.doc.activeElement,h.doc.querySelector('#group-access-list select'));
  assert.match(h.doc.getElementById('group-access-error').textContent,/Change its role in the list/);
});
test('losing management through a group change hides stale controls and leaves settings',async()=>{
  const h=harness(async()=>({ok:true,status:200,json:async()=>({app:{slug:'a'},can_manage:false})}),{id:1,role:'viewer'});
  h.doc.getElementById('detail-access-panel').hidden=false;
  assert.equal(await h.reconcileGroupPermissions('a',0),false);
  assert.equal(h.doc.getElementById('detail-access-panel').hidden,true);
  assert.equal(h.state.apps[0].can_manage,false);
  assert.deepEqual(h.navigations,[{path:'/apps',options:{replace:true}}]);
  assert.match(h.messages.join(' '),/Group change saved. You no longer manage/);
});
test('an unavailable permission check reports the saved mutation accurately',async()=>{
  const h=harness(async()=>({ok:false,status:503}));
  assert.equal(await h.reconcileGroupPermissions('a',0),false);
  assert.match(h.doc.getElementById('group-access-error').textContent,/Group change saved, but your permissions could not be checked/);
  assert.deepEqual(h.navigations,[]);
});

function visibilityHarness(api) {
  const doc=new JSDOM(html).window.document, state={apps:[{slug:'a',access:'private'}]};
  const note=app.slice(app.indexOf('  function updateAccessViewingNote('),app.indexOf('  function populateAccessPanel('));
  const save=app.slice(app.indexOf('  async function saveVisibility('),app.indexOf('  // --- General tab ---'));
  const h=new Function('document','state','api',`let settingsSlug='a',accessPanelRevision=0;
    const accessRequestCurrent=(slug,revision)=>slug===settingsSlug&&revision===accessPanelRevision;
    const setError=(el,msg)=>{el.textContent=msg;el.hidden=!msg;},setHidden=(el,hidden)=>el.hidden=hidden;
    const handleUnauthorized=async()=>{},recomputeDirty=()=>{},snapshotSettingsSection=()=>{},refreshDetailFleetState=async()=>{},flashToast=()=>{};
    ${note}\n${save}
    return {saveVisibility, updateAccessViewingNote, switchApp(){settingsSlug='b';accessPanelRevision++;}};
  `)(doc,state,api);
  h.updateAccessViewingNote('private');
  doc.querySelector('input[value="public"][name="access-level"]').checked=true;
  return {...h,doc,state};
}
test('visibility consequences describe saved access until the save succeeds',async()=>{
  let fail=true;
  const h=visibilityHarness(async()=>({ok:!fail,status:fail?503:204,json:async()=>({error:'Try again'})}));
  const note=h.doc.getElementById('access-viewing-note');
  assert.doesNotMatch(note.textContent,/Currently public/);
  await h.saveVisibility();assert.doesNotMatch(note.textContent,/Currently public/);
  assert.equal(h.state.apps[0].access,'private');
  fail=false;await h.saveVisibility();
  assert.match(note.textContent,/Currently public: anyone/);assert.equal(h.state.apps[0].access,'public');
  assert.equal(h.doc.getElementById('visibility-status').textContent,'Visibility saved.');
});
test('a delayed visibility save updates its app cache without painting another app',async()=>{
  const pending=deferred(),h=visibilityHarness(()=>pending.promise);
  const save=h.saveVisibility();h.switchApp();h.updateAccessViewingNote('shared');
  pending.resolve({ok:true,status:204});await save;
  assert.equal(h.state.apps[0].access,'public');
  assert.match(h.doc.getElementById('access-viewing-note').textContent,/Currently shared/);
  assert.equal(h.doc.getElementById('visibility-status').hidden,true);
});

test('access loading, empty and error states have one announcement and accessible retries', async () => {
  let failed=false;
  const h=harness(async()=>{if(failed)throw Error('offline');return response([]);});
  await h.refreshMemberList();await h.refreshGroupAccessList();
  for(const id of ['members-list','group-access-list']) {
    assert.equal(h.doc.querySelector(`#${id} .access-list-state`).getAttribute('aria-hidden'),'true');
    assert.match(h.doc.getElementById(id==='members-list'?'members-status':'groups-status').textContent,/No .* added/);
  }
  failed=true;await h.refreshMemberList();
  const row=h.doc.querySelector('#members-list .access-list-state');
  assert.equal(row.hasAttribute('aria-hidden'),false);
  assert.equal(row.querySelector('span').getAttribute('aria-hidden'),'true');
  assert.equal(row.querySelector('button').getAttribute('aria-hidden'),null);
});
