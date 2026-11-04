import { test } from 'node:test';
import assert from 'node:assert/strict';
import { usagePrivacyErrorMessage } from '../static/views/usage-privacy.js';

// Usage privacy's PATCH /api/apps/{slug} validation errors are server-authored
// strings naming the wire field usage_identity_mode (internal/api/apps.go),
// which reads as internal/technical next to the section's plain-language
// labels ("Usage identity granularity"). usagePrivacyErrorMessage rewrites the
// known messages and passes through anything it does not recognize.

test('rewrites the hub-policy-ceiling message to name the on-screen label', () => {
  const rewritten = usagePrivacyErrorMessage('usage_identity_mode cannot collect more identity than the hub policy');
  assert.doesNotMatch(rewritten, /usage_identity_mode/, 'the wire field name must not appear in the rewritten message');
  assert.match(rewritten, /hub policy/);
});

test('rewrites the invalid-enum message to name the on-screen label', () => {
  const rewritten = usagePrivacyErrorMessage('usage_identity_mode must be disabled, unattributed, pseudonymous, identified, or null');
  assert.doesNotMatch(rewritten, /usage_identity_mode/);
});

test('passes through an unrecognized message unchanged rather than guessing', () => {
  assert.equal(usagePrivacyErrorMessage('usage privacy policy unavailable'), 'usage privacy policy unavailable');
  assert.equal(usagePrivacyErrorMessage('Network error'), 'Network error');
  assert.equal(usagePrivacyErrorMessage(''), '');
});

import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
const response = (body, status = 200) => ({ok:status < 400, status, json:async () => body});
const deferred = () => {let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve};};
function privacyHarness({original='inherit', hub='identified', request, confirm=()=>true}={}) {
  const doc = new JSDOM(html).window.document, select = doc.getElementById('usage-privacy-mode');
  select.dataset.originalMode = original; select.dataset.hubMode = hub; select.value = original;
  const button = doc.getElementById('usage-privacy-save-btn'); button.hidden = false;
  const discard = doc.createElement('button'); button.after(discard);
  const rec = {btn:button, discard, dirtyEl:doc.getElementById('usage-privacy-dirty'), els:[select]};
  const calls=[], confirmations=[], app={slug:'test',id:1,usage_identity_mode:original === 'inherit' ? null : original};
  const scope = {document:doc,window:{confirm:message=>{confirmations.push(message);return confirm(message);}},usagePrivacyErrorMessage,
    setError:(el,message)=>{el.textContent=message;el.hidden=!message;},setHidden:(el,hidden)=>{el.hidden=hidden;},
    directSettings:{},updateConfigurationDirty:()=>{},refreshDetailFleetState:async()=>{},handleUnauthorized:async()=>{},
    api:async(url,options)=>{calls.push({url,options});return request ? request({url,options:options?.method === 'PATCH' ? options : undefined}) : response(options?.method === 'PATCH' ? {app:{...app,...JSON.parse(options.body)}} : {app,usage_policy:{hub_identity_mode:hub}});},
  };
  const shared=source.slice(source.indexOf('  function focusSettingsFeedback('),source.indexOf('  function anySettingsDirty('));
  const help=source.slice(source.indexOf('  const usageModeRank ='),source.indexOf('  function populateGeneralTab('));
  const save=source.slice(source.indexOf('  async function saveUsagePrivacy('),source.indexOf('  async function saveResources('));
  const controls = new Function(...Object.keys(scope),'rec','app',`
    let settingsSlug='test',settingsRevision=0,detailApp=app;
    const settingsSections={'usage-privacy':rec};
    ${shared}\n${help}\n${save}
    snapshotSettingsSection('usage-privacy'); updateUsagePrivacyHelp();
    return {save:()=>saveSettingsSection('usage-privacy',saveUsagePrivacy),dirty:()=>isSectionDirty('usage-privacy'),
      update:()=>{recomputeDirty('usage-privacy');updateUsagePrivacyHelp();}, context:(slug='other')=>{settingsSlug=slug;settingsRevision++;detailApp={slug,id:2};return detailApp;}};
  `)(...Object.values(scope),rec,app);
  return {doc,select,app,calls,confirmations,rec,...controls,edit(value){select.value=value;controls.update();}};
}

test('privacy explains totals, unique counts, named access and disabled collection without reserved space',()=>{
  const h=privacyHarness();
  for(const [mode,description] of [['unattributed',/without an account/],['pseudonymous',/Names are not retained/],['identified',/Only platform administrators/],['disabled',/Previously collected totals remain/]]){
    h.edit(mode);assert.match(h.doc.getElementById('usage-privacy-help').textContent,description);
  }
  h.select.dataset.collectionEnabled='false';h.update();
  assert.match(h.doc.getElementById('usage-privacy-limit').textContent,/turned off for the hub/);
  assert.ok(h.doc.getElementById('usage-privacy-warning').compareDocumentPosition(h.rec.btn)&h.doc.defaultView.Node.DOCUMENT_POSITION_FOLLOWING);
});

test('privacy upgrade describes prospective collection without an irreversible warning',()=>{
  const h=privacyHarness({original:'unattributed'});h.edit('pseudonymous');
  assert.match(h.doc.getElementById('usage-privacy-help').textContent,/After saving:.*new sessions/);
  assert.equal(h.doc.getElementById('usage-privacy-warning').hidden,true);
});

test('pausing totals-only collection does not claim to delete identity',()=>{
  const h=privacyHarness({original:'unattributed'});h.edit('disabled');
  assert.equal(h.doc.getElementById('usage-privacy-warning').hidden,true);
});

test('privacy checks latest policy before irreversible confirmation and declining preserves draft',async()=>{
  const h=privacyHarness({original:'disabled',confirm:()=>false,request:()=>response({app:{slug:'test',id:1,usage_identity_mode:'identified'},usage_policy:{hub_identity_mode:'identified'}})});
  h.edit('unattributed');await h.save();
  assert.equal(h.confirmations.length,1);assert.match(h.confirmations[0],/permanently removes.*keys/);
  assert.equal(h.calls.length,1);assert.equal(h.dirty(),true);assert.equal(h.rec.btn.disabled,false);
});

test('privacy save locks controls and duplicates, then adopts the confirmed policy',async()=>{
  const pending=deferred(), patchStarted=deferred();
  const h=privacyHarness({request:({options})=>{if(options){patchStarted.resolve();return pending.promise;}return response({app:{slug:'test',id:1},usage_policy:{hub_identity_mode:'identified'}});}});
  h.edit('pseudonymous');const saving=h.save();
  assert.equal(h.select.disabled,true);assert.equal(h.rec.discard.disabled,true);
  assert.equal(h.rec.btn.closest('.settings-block').getAttribute('aria-busy'),'true');
  await h.save();await patchStarted.promise;assert.equal(h.calls.length,2);
  assert.equal(h.calls[0].options.cache,'no-store');
  pending.resolve(response({app:{slug:'test',id:1,usage_identity_mode:'unattributed'}}));await saving;
  assert.equal(h.select.value,'unattributed');assert.equal(h.select.dataset.originalMode,'unattributed');
  assert.equal(h.app.usage_identity_mode,'unattributed');assert.equal(h.dirty(),false);assert.equal(h.select.disabled,false);
  assert.equal(h.doc.getElementById('usage-privacy-status').textContent,'Privacy saved.');
});

for(const phase of ['preflight','success body','error body']) for(const slug of ['other','test']) test(`privacy ${phase} cannot mutate a replacement ${slug} context`,async()=>{
  const pending=deferred(), boundary=deferred();
  const latest={app:{slug:'test',id:1},usage_policy:{hub_identity_mode:'identified'}};
  const h=privacyHarness({request:({options})=>{
    if(!options){if(phase==='preflight'){boundary.resolve();return pending.promise;}return response(latest);}
    return {ok:phase==='success body',status:phase==='success body'?200:503,json:()=>{boundary.resolve();return pending.promise;}};
  }});
  h.edit('pseudonymous');const saving=h.save();
  await boundary.promise;
  const replacement=h.context(slug);h.doc.getElementById('usage-privacy-status').textContent='Replacement feedback';
  pending.resolve(phase==='preflight'?response(latest):{app:{slug:'test',id:1,usage_identity_mode:'pseudonymous'},error:'Old error'});
  await saving;
  assert.equal(h.doc.getElementById('usage-privacy-status').textContent,'Replacement feedback');
  assert.equal(replacement.usage_identity_mode,undefined);assert.equal(h.select.dataset.originalMode,'inherit');assert.equal(h.dirty(),true);
  if(phase==='preflight')assert.equal(h.calls.length,1);
});

for(const kind of ['network','server','malformed','replacement','ceiling']) test(`privacy ${kind} failure retains the draft and allows recovery`,async()=>{
  const h=privacyHarness({request:({options})=>{
    if(!options){if(kind==='network')throw new Error('Offline');return response({app:{slug:'test',id:1},usage_policy:{hub_identity_mode:kind==='ceiling'?'unattributed':'identified'}});}
    if(kind==='server')return response({error:'Service unavailable.'},503);
    if(kind==='malformed')return {ok:true,status:200,json:async()=>{throw new Error('Invalid JSON');}};
    return response({app:{slug:'test',id:99,usage_identity_mode:'pseudonymous'}});
  }});
  h.edit('pseudonymous');await h.save();
  assert.equal(h.dirty(),true);assert.equal(h.select.value,'pseudonymous');assert.equal(h.app.usage_identity_mode,null);
  assert.equal(h.rec.btn.disabled,false);assert.equal(h.doc.getElementById('usage-privacy-status').hidden,true);
  assert.equal(h.doc.getElementById('usage-privacy-error').hidden,false);
  if(kind==='ceiling')assert.equal(h.calls.length,1);
});

test('restoring inheritance accepts the server’s omitted optional mode',async()=>{
  const h=privacyHarness({original:'pseudonymous',request:({options})=>response(options?{app:{slug:'test',id:1}}:{app:{slug:'test',id:1,usage_identity_mode:'pseudonymous'},usage_policy:{hub_identity_mode:'identified'}})});
  h.edit('inherit');await h.save();assert.equal(h.dirty(),false);assert.equal(h.select.value,'inherit');assert.equal(h.app.usage_identity_mode,null);
});
