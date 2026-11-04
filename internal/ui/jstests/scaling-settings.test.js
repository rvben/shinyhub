import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { scalingSettingsSnapshot, scalingSettingsPatch } from '../static/views/scaling-settings.js';

function form() {
  return new JSDOM(`<input id="scaling-replicas" value="2">
    <input id="scaling-cap" value="10">
    <select id="worker-isolation"><option value="multiplex">Multiplex</option><option value="grouped">Grouped</option></select>
    <input id="worker-grouped-size" value="1">
    <input id="worker-max-workers" value="0">
    <input id="worker-warm-spares" value="0">`).window.document;
}

test('cap-only save omits stale replica count and untouched displayed worker defaults', () => {
  const doc = form();
  const baseline = scalingSettingsSnapshot(doc);
  // Autoscaling may have changed the server count since this form loaded.
  doc.getElementById('scaling-cap').value = '20';
  assert.deepEqual(scalingSettingsPatch(scalingSettingsSnapshot(doc), baseline), {
    max_sessions_per_replica: 20,
  });
});

test('replica increase submits just the requested count', () => {
  const doc = form();
  const baseline = scalingSettingsSnapshot(doc);
  doc.getElementById('scaling-replicas').value = '4';
  assert.deepEqual(scalingSettingsPatch(scalingSettingsSnapshot(doc), baseline), { replicas: 4 });
});

test('worker changes submit only edited fields and subsequent save uses new baseline', () => {
  const doc = form();
  const baseline = scalingSettingsSnapshot(doc);
  doc.getElementById('worker-isolation').value = 'grouped';
  doc.getElementById('worker-grouped-size').value = '5';
  const current = scalingSettingsSnapshot(doc);
  assert.deepEqual(scalingSettingsPatch(current, baseline), {
    worker_isolation: 'grouped', worker_grouped_size: 5,
  });
  assert.deepEqual(scalingSettingsPatch(scalingSettingsSnapshot(doc), current), {});
});

import { readFileSync } from 'node:fs';
import { directSettings, populateDirectSetting, refreshDirectSetting, readDirectSetting } from '../static/views/direct-setting.js';
import { updateWorkerFields, switchWorkerFields } from '../static/views/configuration-context.js';
import { readAutoscaleForm } from '../static/views/autoscale.js';
const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
const response = (body = {}, status = 200) => ({ok:status < 400,status,json:async () => body});
const deferred = () => {let resolve;const promise=new Promise(done=>{resolve=done;});return {promise,resolve};};
function settingsForm() {
  const doc = new JSDOM(html).window.document;
  for (const [id,value] of Object.entries({'scaling-replicas':'2','worker-isolation':'multiplex','worker-grouped-size':'2','worker-max-workers':'4','worker-warm-spares':'0','min-warm-replicas':'0','autoscale-min':'1','autoscale-max':'4'})) doc.getElementById(id).value=value;
  doc.getElementById('scaling-replicas').dataset.appStatus='running';
  doc.getElementById('worker-isolation').dataset.original='multiplex';
  for (const [id,value,fallback] of [['scaling-cap',20,'20'],['hibernate-custom-minutes',null,'30'],['autoscale-target',0,'75']]) {
    doc.getElementById(id).dataset.defaultValue=fallback;populateDirectSetting(doc,id,value,true);
  }
  updateWorkerFields(doc);
  return doc;
}
function harness(name,{request,confirm=()=>true}={}) {
  const doc=settingsForm();
  const ids={scaling:['scaling-replicas','scaling-cap','scaling-cap-mode','worker-isolation','worker-grouped-size','worker-max-workers','worker-warm-spares'],hibernate:['hibernate-custom-minutes','hibernate-custom-minutes-mode','min-warm-replicas'],autoscale:['autoscale-enabled','autoscale-min','autoscale-max','autoscale-target','autoscale-target-mode']};
  const button=doc.getElementById(`${name}-save-btn`);button.hidden=false;
  const discard=doc.createElement('button');button.after(discard);
  const rec={btn:button,discard,dirtyEl:doc.getElementById(`${name}-dirty`),els:ids[name].map(id=>doc.getElementById(id))};
  const calls=[],confirmations=[];
  const scope={document:doc,directSettings,readDirectSetting,readAutoscaleForm,scalingSettingsSnapshot,scalingSettingsPatch,refreshDirectSetting,
    window:{confirm:message=>{confirmations.push(message);return confirm(message);}},
    setError:(el,message)=>{el.textContent=message;el.hidden=!message;},setHidden:(el,hidden)=>{el.hidden=hidden;},
    currentIsolationChoice:()=>doc.getElementById('worker-isolation').value||'multiplex',
    updateConfigurationDirty:()=>{},refreshKeepWarmNote:()=>{},updateMinWarmWarning:()=>{},loadApps:async()=>{},refreshDetailFleetState:async()=>{},handleUnauthorized:async()=>{},
    api:async(url,options)=>{calls.push({url,options});return request?request({url,options}):response({app:{replicas:2,status:'running',worker_isolation:'multiplex'}});},
  };
  const shared=source.slice(source.indexOf('  function focusSettingsFeedback('),source.indexOf('  function anySettingsDirty('));
  const scaling=source.slice(source.indexOf('  async function saveHibernateSettings('),source.indexOf('  async function saveRenderPacing('));
  const autoscale=source.slice(source.indexOf('  async function saveAutoscaleSettings('),source.indexOf('  function resetDangerZone('));
  const controls=new Function(...Object.keys(scope),'rec','name',`
    let settingsSlug='test',settingsRevision=0,detailApp=null;
    let originalScalingSettings=scalingSettingsSnapshot(document);
    const settingsSections={[name]:rec};
    ${shared}\n${scaling}\n${autoscale}
    snapshotSettingsSection(name);
    const actions={scaling:saveScalingSettings,hibernate:saveHibernateSettings,autoscale:saveAutoscaleSettings};
    return {save:()=>saveSettingsSection(name,actions[name]),changed:()=>recomputeDirty(name),dirty:()=>isSectionDirty(name),context:(slug='other')=>{settingsSlug=slug;settingsRevision++;}};
  `)(...Object.values(scope),rec,name);
  return {doc,calls,confirmations,rec,...controls,edit(id,value){doc.getElementById(id).value=value;controls.changed();}};
}
for(const [name,id,value] of [['scaling','scaling-cap','35'],['hibernate','min-warm-replicas','1'],['autoscale','autoscale-max','8']]) {
  test(`${name} save locks all controls, announces progress, and rejects duplicates`,async()=>{
    const pending=deferred(),h=harness(name,{request:()=>pending.promise});h.edit(id,value);
    const saving=h.save();
    assert.equal(h.rec.btn.closest('.settings-block').getAttribute('aria-busy'),'true');
    assert.ok([...h.rec.btn.closest('.settings-block').querySelectorAll('input,select,button')].every(el=>el.disabled));
    assert.match(h.doc.getElementById(`${name}-status`).textContent,/Saving/);
    await h.save();assert.equal(h.calls.length,1);
    pending.resolve(response());await saving;
    assert.equal(h.dirty(),false);assert.equal(h.rec.btn.disabled,true);
    assert.equal(h.doc.getElementById(`${name}-status`).hidden,false);
  });
  test(`${name} failure preserves edits and allows retry`,async()=>{
    const h=harness(name,{request:()=>response({error:'Try again later.'},503)});h.edit(id,value);await h.save();
    assert.equal(h.doc.getElementById(id).value,value);assert.equal(h.dirty(),true);assert.equal(h.rec.btn.disabled,false);
    assert.equal(h.doc.getElementById(`${name}-status`).hidden,true);
    assert.match(h.doc.getElementById(`${name}-error`).textContent,/Try again/);
  });
  for(const slug of ['other','test']) test(`${name} completion cannot mark a replacement ${slug} settings context saved`,async()=>{
    const pending=deferred(),h=harness(name,{request:()=>pending.promise});h.edit(id,value);const saving=h.save();
    h.context(slug);h.doc.getElementById(`${name}-status`).textContent='Replacement app';
    pending.resolve(response());await saving;
    assert.equal(h.doc.getElementById(`${name}-status`).textContent,'Replacement app');assert.equal(h.dirty(),true);
  });
}

test('switching process models suspends irrelevant fields and restores drafts when returning',()=>{
  const doc=settingsForm(),baseline=scalingSettingsSnapshot(doc),select=doc.getElementById('worker-isolation');
  doc.getElementById('scaling-replicas').value='5';doc.getElementById('scaling-cap').value='35';
  select.value='grouped';switchWorkerFields(doc,baseline);
  assert.equal(doc.getElementById('scaling-replica-fields').hidden,true);
  assert.deepEqual(scalingSettingsPatch(scalingSettingsSnapshot(doc),baseline),{worker_isolation:'grouped'});
  doc.getElementById('worker-grouped-size').value='8';doc.getElementById('worker-max-workers').value='7';
  select.value='per_session';switchWorkerFields(doc,baseline);
  assert.equal(doc.getElementById('worker-grouped-size').closest('.scaling-row').hidden,true);
  select.value='multiplex';switchWorkerFields(doc,baseline);
  assert.equal(doc.getElementById('scaling-replicas').value,'5');assert.equal(doc.getElementById('scaling-cap').value,'35');
  assert.equal(doc.getElementById('worker-max-workers').value,'4');
  select.value='grouped';switchWorkerFields(doc,baseline);
  assert.equal(doc.getElementById('worker-grouped-size').value,'8');assert.equal(doc.getElementById('worker-max-workers').value,'7');
});

test('an isolation-only save never warns about or overwrites an autoscaled replica count',async()=>{
  const h=harness('scaling',{request:({options})=>response(options?{}:{app:{replicas:5,status:'running',worker_isolation:'per_session'}})});
  h.edit('worker-isolation','grouped');await h.save();
  assert.equal(h.confirmations.length,1);assert.match(h.confirmations[0],/from per_session to grouped/);
  assert.deepEqual(JSON.parse(h.calls[1].options.body),{worker_isolation:'grouped'});
});

test('a replaced context during replica preflight cannot write or ask for a stale confirmation',async()=>{
  const pending=deferred(),h=harness('scaling',{request:()=>pending.promise});h.edit('scaling-replicas','1');
  const saving=h.save();h.context('test');pending.resolve(response({app:{replicas:5,status:'running'}}));await saving;
  assert.equal(h.calls.length,1);assert.equal(h.confirmations.length,0);assert.equal(h.dirty(),true);
});

test('validation opens a collapsed autoscale policy and focuses its invalid bound',async()=>{
  const h=harness('autoscale');h.edit('autoscale-max','');h.rec.btn.focus();await h.save();
  assert.equal(h.doc.getElementById('autoscale-policy-fields').open,true);
  assert.equal(h.doc.activeElement.id,'autoscale-max');assert.equal(h.calls.length,0);
});

test('post-save detail refresh respects a replaced settings context for the same app',async()=>{
  const pending=deferred(),doc=settingsForm(),initial={slug:'test',id:1};let active=true,paints=0;
  const refresh=source.slice(source.indexOf('  async function refreshDetailFleetState('),source.indexOf('  // --- Settings dirty-state tracking'));
  const run=new Function('document','api','markConfigurationBehaviorStale','refreshFleetSurfaces','applyConfigurationContext','updateWorkerCapacity',`let detailApp={slug:'test',id:1},detailLastEnvelope={};${refresh};return refreshDetailFleetState;`)(doc,()=>pending.promise,()=>paints++,()=>paints++,()=>paints++,()=>paints++);
  const result=run('test',()=>active);active=false;pending.resolve(response({app:{...initial,worker_isolation:'grouped'}}));await result;
  assert.equal(paints,0);
});
