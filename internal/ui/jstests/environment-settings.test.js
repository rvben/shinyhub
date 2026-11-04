import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {JSDOM} from 'jsdom';
import {runInFlight} from '../static/views/in-flight.js';
const html=readFileSync(new URL('../static/index.html',import.meta.url),'utf8');
const app=readFileSync(new URL('../static/app.js',import.meta.url),'utf8');
const source=app.slice(app.indexOf('  let envEditingKey ='),app.indexOf('  let accessPanelRevision ='));
const response=(body,status=200,headers={})=>({ok:status<400,status,json:async()=>body,headers:new Map(Object.entries(headers))});
const deferred=()=>{let resolve;return {promise:new Promise(done=>resolve=done),resolve};};
function harness(api) {
  const doc=new JSDOM(html).window.document,messages=[],prompts=[];
  const scope={document:doc,state:{apps:[{slug:'a',status:'running'},{slug:'b',status:'running'}],user:{}},detailApp:null,api,runInFlight,
    canManageApp:()=>true,loadApps:async()=>{},handleUnauthorized:async()=>messages.push('unauthorized'),
    environmentEditValue:v=>v&&!v.secret?v.value??'':'',updateConfigurationDirty:()=>{},
    setError:(el,msg)=>{el.textContent=msg;el.hidden=!msg;},flashToast:message=>messages.push(message),window:{confirm:message=>{prompts.push(message);return true;}}};
  const methods=new Function(...Object.keys(scope),`let settingsSlug='a',settingsRevision=0;${source}
    return {refresh:()=>refreshEnvList(settingsSlug),retry:retryEnvList,open:openEnvForm,close:closeEnvForm,submit:submitEnvForm,apply:applyEnvChanges,remove:deleteEnvVar,
      switchApp(slug){closeEnvForm(true);settingsSlug=slug;settingsRevision++;}, get saving(){return envSaving;}};
  `)(...Object.values(scope));
  return {...methods,doc,messages,prompts};
}
const submit={preventDefault(){}};
const key=(h,value)=>h.doc.getElementById('env-form-key').value=value;
const value=(h,text)=>h.doc.getElementById('env-form-value').value=text;

test('load failure is recoverable, never presented as an empty saved list',async()=>{
  let fail=true;
  const h=harness(async()=>{if(fail)throw Error('offline');return response({items:[]});});
  await h.refresh();
  assert.equal(h.doc.getElementById('env-empty').hidden,true);
  assert.equal(h.doc.getElementById('env-retry-btn').hidden,false);
  assert.match(h.doc.getElementById('env-load-error').textContent,/could not be loaded/);
  assert.equal(h.doc.getElementById('env-add-btn').disabled,true);
  fail=false;await h.refresh();
  assert.equal(h.doc.getElementById('env-empty').hidden,false);
  assert.equal(h.doc.getElementById('env-retry-btn').hidden,true);
  assert.equal(h.doc.getElementById('env-add-btn').disabled,false);
});
test('malformed lists fail explicitly instead of claiming no variables',async()=>{
  const h=harness(async()=>response({unexpected:[]}));await h.refresh();
  assert.equal(h.doc.getElementById('env-retry-btn').hidden,false);
  assert.equal(h.doc.getElementById('env-empty').hidden,true);
});
test('late success and late failure cannot overwrite another app or a newer list',async()=>{
  const pending=deferred();let calls=0;
  const h=harness(async()=>++calls===1?pending.promise:response({items:[{key:'CURRENT',value:'b',secret:false}]}));
  const old=h.refresh();h.switchApp('b');await h.refresh();
  pending.resolve(response({},503));await old;
  assert.match(h.doc.getElementById('env-list').textContent,/CURRENT/);
  assert.equal(h.doc.getElementById('env-load-error').hidden,true);
  const delayed=deferred();calls=0;
  const other=harness(async()=>++calls===1?delayed.promise:response({items:[]}));
  const first=other.refresh();await other.refresh();delayed.resolve(response({items:[{key:'STALE'}]}));await first;
  assert.equal(other.doc.getElementById('env-empty').hidden,false);
  assert.doesNotMatch(other.doc.getElementById('env-list').textContent,/STALE/);
});
test('list distinguishes empty and secret values and protects bundle variables',async()=>{
  const h=harness(async()=>response({items:[{key:'EMPTY',value:''},{key:'SECRET',secret:true,value:'not-for-display'},{key:'BUNDLED',value:'fixed',source:'bundle'},{key:'<img>',value:'<script>'}]}));
  await h.refresh();
  assert.match(h.doc.getElementById('env-list').textContent,/Empty value/);
  assert.match(h.doc.getElementById('env-list').textContent,/Secret value/);
  assert.doesNotMatch(h.doc.getElementById('env-list').textContent,/not-for-display/);
  const row=[...h.doc.querySelectorAll('#env-list tbody tr')].find(row=>row.textContent.includes('BUNDLED'));
  assert.equal(row.querySelector('button'),null);assert.match(row.textContent,/Edit shinyhub.toml/);
  assert.equal(h.doc.querySelector('#env-list img, #env-list script'),null);
});
test('discarded and saved secrets are cleared from the hidden editor',async()=>{
  const h=harness(async(_,options)=>options?.method==='PUT'?response({changed:true,restart_required:true}):response({items:[]}));
  h.open({key:'SECRET',secret:true});value(h,'discarded');h.close(true);
  assert.equal(h.doc.getElementById('env-form-value').value,'');
  h.open({key:'SECRET',secret:true});value(h,'saved');await h.submit(submit);
  assert.equal(h.doc.getElementById('env-form-value').value,'');
  assert.equal(h.doc.getElementById('env-form').hidden,true);
  assert.match(h.doc.getElementById('env-apply-note').textContent,/waiting for a restart/);
  assert.equal(h.doc.getElementById('env-apply-btn').textContent,'Restart to apply');
  h.switchApp('b');await h.refresh();assert.equal(h.doc.getElementById('env-apply-note').textContent,'');
});
test('saved change with a failed list refresh stays saved and keeps a recovery route',async()=>{
  const h=harness(async(_,options)=>options?.method==='PUT'?response({changed:true,restart_required:true}):response({},503));
  h.open();key(h,'REGION');value(h,'europe');await h.submit(submit);
  assert.equal(h.doc.getElementById('env-form').hidden,true);
  assert.equal(h.doc.getElementById('env-retry-btn').hidden,false);
  assert.equal(h.doc.getElementById('env-add-btn').disabled,true);
  assert.match(h.messages.at(-1),/Variable saved/);
});
test('reserved names, oversize UTF-8 and blank replacement secrets fail before writing',async()=>{
  let writes=0;const h=harness(async()=>{writes++;return response({});});
  h.open();key(h,'SHINYHUB_TEST');await h.submit(submit);
  assert.match(h.doc.getElementById('env-form-error').textContent,/reserved/);
  key(h,'REGION');value(h,'€'.repeat(22000));await h.submit(submit);
  assert.match(h.doc.getElementById('env-form-error').textContent,/64 KB/);
  h.open({key:'SECRET',secret:true});await h.submit(submit);
  assert.match(h.doc.getElementById('env-form-error').textContent,/new secret value/);
  assert.equal(writes,0);
});
test('save and restart applies previously saved changes even when the PUT is unchanged',async()=>{
  const calls=[];
  const h=harness(async(path,options)=>{calls.push({path,method:options?.method});return options?.method==='PUT'?response({changed:false,restarted:false}):options?.method==='POST'?response({restarted:true}):response({items:[]});});
  h.open({key:'REGION',value:'europe',secret:false});h.doc.getElementById('env-form-restart').checked=true;
  await h.submit(submit);
  assert.ok(calls.some(call=>call.path==='/api/apps/a/env/apply'&&call.method==='POST'));
  assert.match(h.doc.getElementById('env-apply-note').textContent,/applied by the last restart/);
});
test('applying saved values preserves the unsaved draft and blocks concurrent writes',async()=>{
  const pending=deferred();const calls=[];
  const h=harness(async(path,options)=>{calls.push(path);return options?.method==='POST'?pending.promise:response({items:[]});});
  h.open();key(h,'DRAFT');value(h,'keep this');
  const applying=h.apply();await h.submit(submit);
  assert.equal(calls.length,1);assert.match(h.prompts[0],/Unsaved edits are not included/);
  pending.resolve(response({restarted:true}));await applying;
  assert.equal(h.doc.getElementById('env-form-value').value,'keep this');
  assert.equal(h.doc.getElementById('env-form').hidden,false);
  assert.equal(h.doc.getElementById('env-form-save').disabled,false);
});
test('late mutation cannot clear a newer draft after leaving and returning to the same app',async()=>{
  const pending=deferred(),h=harness(()=>pending.promise);
  h.open();key(h,'OLD');value(h,'old');const saving=h.submit(submit);
  h.switchApp('b');h.switchApp('a');
  pending.resolve(response({changed:true,restart_required:true}));await saving;
  h.open();key(h,'NEW');value(h,'new');
  assert.equal(h.doc.getElementById('env-form').hidden,false);
  assert.equal(h.doc.getElementById('env-form-value').value,'new');
  assert.deepEqual(h.messages,[]);
});
test('deletion clears the matching editor and reports pending removal',async()=>{
  const h=harness(async(_,options)=>options?.method==='DELETE'?response(null,204,{'X-Shinyhub-Restart-Required':'true'}):response({items:[]}));
  h.open({key:'REGION',value:'europe'});const btn=h.doc.createElement('button');
  await h.remove('a','REGION',btn);
  assert.equal(h.doc.getElementById('env-form').hidden,true);
  assert.match(h.messages.at(-1),/Variable deleted. Restart to apply the removal/);
  assert.match(h.doc.getElementById('env-apply-note').textContent,/waiting for a restart/);
});

test('load retry returns keyboard focus to a usable recovery or add action',async()=>{
  let fail=true;const h=harness(async()=>response(fail?{}:{items:[]},fail?503:200));
  await h.retry();assert.equal(h.doc.activeElement,h.doc.getElementById('env-retry-btn'));
  fail=false;await h.retry();assert.equal(h.doc.activeElement,h.doc.getElementById('env-add-btn'));
});
