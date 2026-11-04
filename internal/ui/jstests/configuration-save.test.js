import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { directSettings, populateDirectSetting, refreshDirectSetting } from '../static/views/direct-setting.js';
import { applyConfigurationContext, markConfigurationBehaviorStale, updateConfigurationDirty } from '../static/views/configuration-context.js';
import { renderAppAvatar, avatarView } from '../static/views/app-avatar.js';
import { isSingleEmoji } from '../static/views/emoji-picker.js';
import { scalingSettingsSnapshot, scalingSettingsPatch } from '../static/views/scaling-settings.js';
import { runInFlight } from '../static/views/in-flight.js';
const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const app = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
test('server default refresh and discard do not mark inherited settings as unsaved', () => {
  const doc = new JSDOM(html).window.document;
  const id = 'resources-memory'; const input = doc.getElementById(id); const mode = doc.getElementById(`${id}-mode`);
  input.dataset.defaultValue = '512'; populateDirectSetting(doc, id, null, true);
  const source = app.slice(app.indexOf('  function sectionSnapshot('), app.indexOf('  function snapshotSettingsSection('));
  const snapshot = new Function('document', 'directSettings', `${source};return sectionSnapshot;`)(doc, directSettings);
  const rec = { els: [input, mode] }; const baseline = snapshot(rec);
  input.dataset.defaultValue = '1024'; refreshDirectSetting(doc, id);
  assert.equal(input.value, '1024');
  assert.equal(snapshot(rec), baseline);
  mode.value = 'custom';
  assert.notEqual(snapshot(rec), baseline, 'an explicit override is dirty even when it equals the default');
  input.value = '768';
  // Discard restores the saved mode/value, then refreshes the current default.
  mode.value = 'inherit'; input.value = '512'; refreshDirectSetting(doc, id, { resetDraft: true });
  assert.equal(input.value, '1024');
  assert.equal(snapshot(rec), baseline);
  mode.value = 'off';
  assert.notEqual(snapshot(rec), baseline, 'explicit unlimited remains a distinct saved policy');
});
test('manual replica change confirms against the latest pool size, not the mount baseline',async()=>{
 const doc=new JSDOM(html).window.document;
 doc.getElementById('scaling-replicas').value='3';doc.getElementById('scaling-replicas').dataset.original='2';doc.getElementById('scaling-replicas').dataset.appStatus='running';
 doc.getElementById('worker-warm-spares').value='0';doc.getElementById('worker-max-workers').value='0';doc.getElementById('worker-grouped-size').value='1';
 doc.getElementById('scaling-cap').value='20';doc.getElementById('scaling-cap-mode').value='custom';
 const start=app.indexOf('  async function saveScalingSettings('),end=app.indexOf('  async function saveRenderPacing(',start);
 let confirmation='',reads=0;
 const run=new Function('document','api','window','setError','setHidden','scalingSettingsSnapshot','scalingSettingsPatch',`let settingsSlug='test', settingsRevision=0;const originalScalingSettings={replicas:2}; const currentIsolationChoice=()=> 'multiplex';${app.slice(start,end)};return saveScalingSettings;`)(doc,async()=>{reads++;return{ok:true,json:async()=>({app:{replicas:5,status:'running'}})};},{confirm:message=>{confirmation=message;return false;}},()=>{},()=>{},scalingSettingsSnapshot,scalingSettingsPatch);
 await run();assert.equal(reads,1);assert.match(confirmation,/from 5 to 3/);
});
test('an icon result updates its cache without repainting another app',()=>{
 const source=app.slice(app.indexOf('  function applyIconChange('),app.indexOf('  async function saveGeneralInfo('));
 const current={slug:'b',icon_emoji:'B'};const saved={slug:'a',icon_emoji:'A'};let paints=0;
 const run=new Function('state','detailApp','renderIconPicker','renderDetailHeaderAvatar',`let settingsSlug='b';${source};return applyIconChange;`)({apps:[saved]},current,()=>paints++,()=>paints++);
 run(saved,{mime:'',emoji:'C'});assert.equal(saved.icon_emoji,'C');assert.equal(current.icon_emoji,'B');assert.equal(paints,0);
});

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return {promise, resolve};
}
const iconFile = () => {
  const bytes = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=', 'base64');
  return {size:bytes.length, type:'image/png', arrayBuffer:async () => Uint8Array.from(bytes).buffer};
};
const iconResponse = body => ({ok:true, status:200, json:async () => body});
const iconConfirmation = (emoji = '📊', mime = '') => ({app_id:1, icon_emoji:emoji, icon_mime:mime, revision:'revision-1'});
const flushIconPreparation = () => new Promise(resolve => setImmediate(resolve));
function iconHarness({mutate, read, snapshot, initialEmoji = '', initialImage = null} = {}) {
  const doc = new JSDOM(html).window.document;
  const initial = {id:1, slug:'test', name:'Test', icon_mime:initialImage?.mime || '', icon_emoji:initialEmoji, replicas:1};
  const state = {user:{canEdit:true}, apps:[initial]};
  const serverStates = new Map([['test', {app_id:1, mime:initial.icon_mime, data:initialImage?.data || null, emoji:initialEmoji, revision:'revision-0'}]]);
  let revision = 0;
  const calls = [], messages = [];
  let current = initial;
  const envelope = () => ({app:{...current}, configuration_sources:{
    bundle_status:'available', fleet_status:'unmanaged', saved_toml:`[app]\nicon = ${JSON.stringify(current.icon_emoji)}`,
    fields:{icon:{source:'bundle', current:JSON.stringify(current.icon_emoji), declared:'"📈"', differs:current.icon_emoji !== '📈'}},
  }});
  const scope = {
    document:doc, state, initial,
    setHidden:(el, hidden) => {el.hidden = hidden;},
    setError:(el, message) => {el.textContent = message || ''; el.hidden = !message;},
    canManageApp:user => user.canEdit,
    renderAppAvatar, avatarView, isSingleEmoji, runInFlight,
    applyConfigurationContext, markConfigurationBehaviorStale,
    refreshFleetSurfaces:() => {}, updateWorkerCapacity:() => {}, handleUnauthorized:async () => {},
    flashToast:message => messages.push(message),
    api:async (url, options) => {
      calls.push({url, options});
      if (url.endsWith('/icon/state')) {
        const slug = decodeURIComponent(url.split('/')[3]);
        if (options?.method !== 'PUT') return snapshot ? snapshot(url, options) : iconResponse({...serverStates.get(slug)});
        const write = () => {
          const target = JSON.parse(options.body);
          const saved = {...target, revision:`revision-${++revision}`};
          serverStates.set(slug, saved);
          return iconResponse({app_id:saved.app_id, icon_mime:saved.mime, icon_emoji:saved.emoji, revision:saved.revision});
        };
        return mutate ? mutate(url, options, write) : write();
      }
      if (!options?.method) return read ? read() : {ok:true, status:200, json:async () => envelope()};
      throw new Error(`Unexpected icon request: ${url}`);
    },
  };
  const refresh = app.slice(app.indexOf('  async function refreshDetailFleetState('), app.indexOf('  // --- Settings dirty-state tracking'));
  const icons = app.slice(app.indexOf('  const ICON_MAX_BYTES ='), app.indexOf('  async function saveGeneralInfo('));
  const controls = new Function(...Object.keys(scope), `
    let settingsSlug=initial.slug; let detailApp=initial; let detailLastEnvelope={};
    ${refresh}\n${icons}
    renderIconPicker(detailApp, true);
    return {
      set:value => withIconSave(() => setEmojiIcon(detailApp, value)),
      clear:() => withIconSave(() => clearEmojiIcon(detailApp)),
      upload:file => withIconSave(() => uploadIcon(detailApp, file, document.getElementById('general-icon-upload'))),
      remove:() => withIconSave(() => removeIcon(detailApp, document.getElementById('general-icon-remove'))),
      undo:() => withIconSave(() => undoIconChange(detailApp), 'Restoring icon…'),
      apply:(target, change) => applyIconChange(target, change),
      mount:(next, canEdit) => { settingsSlug=next.slug; detailApp=next; state.user.canEdit=canEdit; renderIconPicker(next, canEdit); },
    };
  `)(...Object.values(scope));
  applyConfigurationContext(doc, initial, envelope());
  return {doc, state, calls, messages, serverStates, ...controls, mount(next, canEdit = true) {
    next.id ??= serverStates.size + 1;
    if (!serverStates.has(next.slug)) serverStates.set(next.slug, {app_id:next.id, mime:next.icon_mime || '', data:null, emoji:next.icon_emoji || '', revision:'revision-0'});
    current = next; controls.mount(next, canEdit);
  }};
}

test('an immediate icon save locks controls, restores focus, and refreshes saved settings without replacing drafts', async () => {
  const response = deferred();
  const h = iconHarness({mutate:() => response.promise, initialEmoji:'📈'});
  const doc = h.doc, trigger = doc.getElementById('general-icon-emoji-btn');
  const general = doc.getElementById('general-name'), cpu = doc.getElementById('resources-cpu');
  general.value = 'Unsaved title'; cpu.value = '0.29'; doc.getElementById('resources-cpu-mode').value = 'custom';
  doc.getElementById('general-dirty').hidden = false;
  const group = trigger.closest('.icon-picker'); trigger.focus();
  const saving = h.set('📊');
  assert.equal(group.getAttribute('aria-busy'), 'true');
  assert.ok([...group.querySelectorAll('button, input')].every(control => control.disabled));
  assert.equal(doc.getElementById('general-icon-status').textContent, 'Saving icon…');
  await flushIconPreparation();
  await h.set('📈');
  assert.equal(h.calls.length, 2, 'a duplicate attempt must not issue another snapshot or mutation');
  assert.match(h.messages[0], /still saving/);
  // Browsers may drop focus when its control becomes disabled.
  trigger.blur();
  response.resolve(iconResponse(iconConfirmation()));
  await saving;
  assert.equal(doc.activeElement, trigger);
  assert.equal(group.hasAttribute('aria-busy'), false);
  assert.equal(trigger.disabled, false);
  assert.equal(doc.getElementById('general-icon-remove').disabled, false);
  assert.equal(doc.getElementById('general-icon-status').textContent, 'Icon updated.');
  assert.equal(doc.getElementById('general-icon-status').hidden, false);
  assert.equal(h.calls.length, 3, 'snapshot and mutation are followed by fresh saved settings');
  assert.equal(doc.getElementById('general-name'), general);
  assert.equal(general.value, 'Unsaved title'); assert.equal(cpu.value, '0.29');
  assert.equal(doc.getElementById('general-dirty').hidden, false);
});

test('invalid immediate icon input clears old success without a request or lingering pending state', async () => {
  const h = iconHarness(), status = h.doc.getElementById('general-icon-status');
  for (const attempt of [() => h.set('plain text'), () => h.upload({size:513 * 1024, type:'image/png'})]) {
    status.textContent = 'Icon updated.'; status.hidden = false;
    await attempt();
    assert.equal(status.hidden, true);
    assert.equal(h.doc.getElementById('general-icon-error').hidden, false);
    assert.equal(h.doc.querySelector('.icon-picker').hasAttribute('aria-busy'), false);
    assert.equal(h.doc.getElementById('general-icon-emoji-btn').disabled, false);
  }
  assert.equal(h.calls.length, 0);
  assert.equal(h.doc.getElementById('general-icon-remove').disabled, true, 'an iconless app still cannot remove an icon');
});

for (const operation of ['set', 'clear', 'upload', 'remove']) {
  test(`a failed ${operation} icon attempt clears prior success and permits a retry`, async () => {
    let failed = true;
    const h = iconHarness({mutate:async () => {
      if (failed) throw new Error('offline');
      return iconResponse(iconConfirmation('📊', 'image/png'));
    }});
    const attempt = () => operation === 'set' ? h.set('📊') : operation === 'upload' ? h.upload(iconFile()) : h[operation]();
    const status = h.doc.getElementById('general-icon-status'); status.textContent = 'Icon updated.'; status.hidden = false;
    await attempt();
    assert.equal(status.hidden, true, 'neither prior success nor Saving icon may remain after failure');
    assert.match(h.doc.getElementById('general-icon-error').textContent, /Check your connection/);
    assert.equal(h.doc.getElementById('general-icon-emoji-btn').disabled, false);
    assert.equal(h.doc.querySelector('.icon-picker').hasAttribute('aria-busy'), false);
    assert.equal(h.calls.length, 2, 'a failed mutation follows a snapshot but does not refresh provenance');
    failed = false; await attempt();
    assert.equal(h.doc.getElementById('general-icon-error').hidden, true);
    assert.equal(status.hidden, false);
    assert.match(status.textContent, /Icon (updated|removed)\./);
    assert.equal(h.calls.length, 5, 'successful retry snapshots, mutates, and refreshes provenance');
  });
}

test('a rejected emoji retains the attempted value and last saved icon for correction', async () => {
  const h = iconHarness({mutate:async () => ({ok:false, status:400, json:async () => ({error:'Choose one emoji, not a sequence.'})})});
  h.state.apps[0].icon_emoji = '📈';
  const input = h.doc.getElementById('general-icon-emoji-input'); input.value = '📊';
  await h.set(input.value);
  assert.equal(input.value, '📊');
  assert.equal(h.state.apps[0].icon_emoji, '📈');
  assert.equal(h.doc.getElementById('general-icon-error').textContent, 'Choose one emoji, not a sequence.');
  assert.equal(h.doc.getElementById('general-icon-status').hidden, true);
  assert.equal(h.doc.getElementById('general-icon-emoji-btn').disabled, false);
  assert.equal(h.calls.length, 2);
});

test('a saved icon with a failed settings refresh keeps success and drafts while hiding stale runtime claims', async () => {
  const h = iconHarness({read:async () => {throw new Error('metadata unavailable');}});
  const general = h.doc.getElementById('general-name'); general.value = 'Unsaved title';
  await h.set('📊');
  assert.equal(h.doc.getElementById('general-icon-status').textContent, 'Icon updated.');
  assert.equal(h.doc.getElementById('general-icon-status').hidden, false);
  assert.equal(h.doc.getElementById('general-icon-error').hidden, true);
  assert.equal(h.doc.getElementById('configuration-behavior').hidden, true);
  assert.equal(general.value, 'Unsaved title');
});

test('a late icon result leaves another app’s controls, feedback, icon, and focus alone', async () => {
  const response = deferred(), h = iconHarness({mutate:() => response.promise});
  const saving = h.set('📊');
  await flushIconPreparation();
  const other = {slug:'other', name:'Other', icon_emoji:'📈', icon_mime:''};
  h.mount(other);
  const status = h.doc.getElementById('general-icon-status');
  const focus = h.doc.getElementById('general-name'); focus.focus();
  assert.equal(h.doc.getElementById('general-icon-emoji-btn').disabled, false);
  assert.equal(h.doc.querySelector('.icon-picker').hasAttribute('aria-busy'), false);
  response.resolve(iconResponse(iconConfirmation()));
  await saving;
  assert.equal(other.icon_emoji, '📈');
  assert.equal(status.hidden, true);
  assert.equal(h.doc.activeElement, focus);
  assert.equal(h.calls.length, 2, 'an obsolete result must not refresh the newly mounted app');
});

test('a late removal cannot unlock another app’s icon controls or reclaim its focus', async () => {
  const response = deferred(), h = iconHarness({mutate:() => response.promise});
  const first = h.state.apps[0]; first.icon_mime = 'image/png'; h.mount(first);
  const remove = h.doc.getElementById('general-icon-remove');
  assert.equal(remove.disabled, false); remove.focus();
  const saving = h.remove();
  await flushIconPreparation();
  const other = {slug:'other', name:'Other', icon_emoji:'', icon_mime:''};
  h.mount(other, false);
  const focus = h.doc.getElementById('general-name'); focus.focus();
  response.resolve(iconResponse(iconConfirmation('')));
  await saving;
  assert.ok([...h.doc.querySelector('.icon-picker').querySelectorAll('button, input')].every(control => control.disabled));
  assert.equal(remove.disabled, true, 'the new app has neither permission nor an icon to remove');
  assert.equal(h.doc.activeElement, focus);
  assert.equal(h.doc.getElementById('general-icon-status').hidden, true);
  assert.equal(h.calls.length, 2);
});

test('finishing an icon request respects permissions changed while it was pending', async () => {
  const response = deferred(), h = iconHarness({mutate:() => response.promise});
  const saving = h.set('📊'); await flushIconPreparation(); h.state.user.canEdit = false;
  response.resolve(iconResponse(iconConfirmation()));
  await saving;
  assert.equal(h.doc.querySelector('.icon-picker').hasAttribute('aria-busy'), false);
  assert.ok([...h.doc.querySelector('.icon-picker').querySelectorAll('button, input')].every(control => control.disabled));
  assert.equal(h.doc.getElementById('general-icon-upload').hidden, true);
});

test('Undo restores exact retained image bytes and its deployment-managed emoji together', async () => {
  const data = Buffer.from(await iconFile().arrayBuffer()).toString('base64');
  const h = iconHarness({initialEmoji:'📈', initialImage:{mime:'image/png', data}});
  const general = h.doc.getElementById('general-name'), cpu = h.doc.getElementById('resources-cpu');
  general.value = 'Unsaved title'; cpu.value = '0.29'; h.doc.getElementById('resources-cpu-mode').value = 'custom';
  await h.remove();
  assert.deepEqual(JSON.parse(h.calls[1].options.body), {app_id:1, mime:'', data:null, emoji:'', revision:'revision-0'});
  const undo = h.doc.getElementById('general-icon-undo');
  assert.equal(undo.hidden, false);
  undo.focus();
  await h.undo();
  const restored = h.serverStates.get('test');
  assert.deepEqual(restored, {app_id:1, mime:'image/png', data, emoji:'📈', revision:'revision-2'});
  const put = h.calls.filter(call => call.options?.method === 'PUT');
  assert.deepEqual(JSON.parse(put[1].options.body), {app_id:1, mime:'image/png', data, emoji:'📈', revision:'revision-1'});
  assert.equal(h.calls.filter(call => call.url.endsWith('/icon/state') && call.options?.method !== 'PUT').length, 1, 'Undo restores the saved snapshot without replacing it');
  assert.equal(h.state.apps[0].icon_mime, 'image/png');
  assert.equal(h.state.apps[0].icon_emoji, '📈');
  assert.equal(undo.hidden, true, 'Undo is consumed after restoration');
  assert.equal(h.doc.getElementById('general-icon-status').textContent, 'Previous icon restored.');
  assert.equal(h.doc.getElementById('general-name'), general);
  assert.equal(general.value, 'Unsaved title');
  assert.equal(cpu.value, '0.29');
});

test('clearing an emoji retains uploaded image bytes and Undo restores the emoji', async () => {
  const data = Buffer.from(await iconFile().arrayBuffer()).toString('base64');
  const h = iconHarness({initialEmoji:'📈', initialImage:{mime:'image/png', data}});
  await h.clear();
  assert.deepEqual(h.serverStates.get('test'), {app_id:1, mime:'image/png', data, emoji:'', revision:'revision-1'});
  await h.undo();
  assert.equal(h.serverStates.get('test').emoji, '📈');
  assert.equal(h.serverStates.get('test').data, data);
});

test('a consumed Undo returns keyboard focus to Choose emoji when its trigger disappears', async () => {
  const h = iconHarness({initialEmoji:'📈'});
  await h.set('📊');
  const undo = h.doc.getElementById('general-icon-undo');
  undo.focus();
  const restoring = h.undo();
  assert.equal(h.doc.getElementById('general-icon-status').textContent, 'Restoring icon…');
  // jsdom leaves focus on disabled controls; emulate the browser dropping it.
  h.doc.body.tabIndex = -1; h.doc.body.focus();
  await restoring;
  assert.equal(undo.hidden, true);
  assert.equal(h.doc.activeElement.id, 'general-icon-emoji-btn');
});

test('removing the final icon returns focus to Choose emoji when Remove becomes disabled', async () => {
  const h = iconHarness({initialEmoji:'📈'});
  const remove = h.doc.getElementById('general-icon-remove');
  remove.focus();
  const removing = h.remove();
  h.doc.body.tabIndex = -1; h.doc.body.focus();
  await removing;
  assert.equal(remove.disabled, true);
  assert.equal(h.doc.activeElement.id, 'general-icon-emoji-btn');
});

test('a failed Undo retains the same safe snapshot for retry and clears pending feedback', async () => {
  let failUndo = false;
  const h = iconHarness({initialEmoji:'📈', mutate:async (_url, _options, write) => {
    if (failUndo) throw new Error('offline');
    return write();
  }});
  await h.set('📊');
  failUndo = true;
  await h.undo();
  const undo = h.doc.getElementById('general-icon-undo');
  assert.equal(undo.hidden, false);
  assert.equal(undo.disabled, false);
  assert.equal(h.doc.querySelector('.icon-picker').hasAttribute('aria-busy'), false);
  assert.equal(h.doc.getElementById('general-icon-status').hidden, true);
  assert.match(h.doc.getElementById('general-icon-error').textContent, /Check your connection/);
  assert.equal(h.serverStates.get('test').emoji, '📊');
  const failedPayload = h.calls.at(-1).options.body;
  failUndo = false;
  await h.undo();
  assert.equal(h.calls.at(-2).options.body, failedPayload, 'retry retains the original bytes and expected revision');
  assert.equal(h.serverStates.get('test').emoji, '📈');
  assert.equal(h.doc.getElementById('general-icon-error').hidden, true);
  assert.equal(undo.hidden, true);
});

test('a stale Undo is removed after a revision conflict and cannot overwrite a newer icon', async () => {
  let conflict = false;
  const h = iconHarness({initialEmoji:'📈', mutate:async (_url, _options, write) => conflict
    ? {ok:false, status:409, json:async () => ({error:'Icon changed elsewhere. Reload and try again.'})}
    : write()});
  await h.set('📊');
  h.serverStates.set('test', {...h.serverStates.get('test'), emoji:'🚀', revision:'external-revision'});
  conflict = true;
  await h.undo();
  assert.equal(h.doc.getElementById('general-icon-undo').hidden, true);
  assert.match(h.doc.getElementById('general-icon-error').textContent, /changed elsewhere/);
  assert.equal(h.doc.getElementById('general-icon-status').hidden, true);
  const count = h.calls.length;
  await h.undo();
  assert.equal(h.calls.length, count, 'an unsafe snapshot is no longer submitted');
  assert.equal(h.serverStates.get('test').emoji, '🚀');
});

for (const failure of ['network', 'server', 'wrong-app']) {
  test(`a ${failure} snapshot failure aborts icon mutation`, async () => {
    const h = iconHarness({snapshot:async () => {
      if (failure === 'network') throw new Error('offline');
      if (failure === 'server') return {ok:false, status:503};
      return iconResponse({app_id:999, mime:'', data:null, emoji:'', revision:'revision-0'});
    }});
    await h.set('📊');
    assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0].options.method, undefined);
    assert.equal(h.doc.getElementById('general-icon-undo').hidden, true);
    assert.match(h.doc.getElementById('general-icon-error').textContent, /Could not prepare/);
    assert.equal(h.doc.getElementById('general-icon-status').hidden, true);
    assert.equal(h.doc.getElementById('general-icon-emoji-btn').disabled, false);
    assert.equal(h.serverStates.get('test').emoji, '');
  });
}

test('consecutive icon changes Undo only the latest change', async () => {
  const h = iconHarness({initialEmoji:'📈'});
  await h.set('📊');
  await h.set('🚀');
  await h.undo();
  assert.equal(h.serverStates.get('test').emoji, '📊');
  assert.equal(h.state.apps[0].icon_emoji, '📊');
  assert.equal(h.doc.getElementById('general-icon-undo').hidden, true);
  const count = h.calls.length;
  await h.undo();
  assert.equal(h.calls.length, count);
});

test('Undo history belongs to each app and cannot restore a deleted app into its replacement', async () => {
  const h = iconHarness({initialEmoji:'📈'}), first = h.state.apps[0];
  await h.set('📊');
  const other = {id:2, slug:'other', name:'Other', icon_emoji:'🌱', icon_mime:''};
  h.mount(other);
  assert.equal(h.doc.getElementById('general-icon-undo').hidden, true);
  await h.set('🚀');
  await h.undo();
  assert.equal(h.serverStates.get('other').emoji, '🌱');
  assert.equal(h.serverStates.get('test').emoji, '📊');
  h.mount(first);
  assert.equal(h.doc.getElementById('general-icon-undo').hidden, false);
  await h.undo();
  assert.equal(h.serverStates.get('test').emoji, '📈');
  assert.equal(h.serverStates.get('other').emoji, '🌱');
  await h.set('📊');
  h.mount({...first, id:99});
  assert.equal(h.doc.getElementById('general-icon-undo').hidden, true, 'a slug reused for a different app cannot expose the old Undo');
});

test('leaving an app while its snapshot loads aborts the later mutation', async () => {
  const snapshot = deferred(), h = iconHarness({snapshot:() => snapshot.promise});
  const saving = h.set('📊');
  h.mount({slug:'other', name:'Other', icon_emoji:'🌱', icon_mime:''});
  snapshot.resolve(iconResponse({app_id:1, mime:'', data:null, emoji:'', revision:'revision-0'}));
  await saving;
  assert.equal(h.calls.length, 1);
  assert.equal(h.doc.getElementById('general-icon-status').hidden, true);
  assert.equal(h.serverStates.get('test').emoji, '');
});

for (const destination of ['another app', 'a recreated app with the same slug']) {
  test(`leaving for ${destination} while an upload file is read aborts the mutation`, async () => {
    const fileRead = deferred(), h = iconHarness({initialEmoji:'📈'});
    const file = {...iconFile(), arrayBuffer:() => fileRead.promise};
    const saving = h.upload(file);
    await flushIconPreparation();
    assert.equal(h.calls.length, 1, 'the original app snapshot is read before the file');
    const next = {id:99, slug:destination === 'another app' ? 'other' : 'test', name:'Replacement', icon_emoji:'🌱', icon_mime:''};
    h.state.apps = [next];
    h.mount(next);
    const draft = h.doc.getElementById('general-icon-emoji-input'); draft.value = '🚀';
    fileRead.resolve(await iconFile().arrayBuffer());
    await saving;
    assert.equal(h.calls.filter(call => call.options?.method === 'PUT').length, 0, 'an upload must not begin writing after its app is left or replaced');
    assert.equal(h.serverStates.get('test').emoji, '📈');
    assert.equal(next.icon_emoji, '🌱');
    assert.equal(draft.value, '🚀');
    assert.equal(h.doc.getElementById('general-icon-status').hidden, true);
    assert.equal(h.doc.getElementById('general-icon-undo').hidden, true);
  });
}

test('a delayed icon success cannot repaint or clear drafts on a recreated app with the same slug', async () => {
  const response = deferred(), h = iconHarness({initialEmoji:'📈', mutate:() => response.promise});
  const original = h.state.apps[0];
  const saving = h.set('📊');
  await flushIconPreparation();
  assert.equal(h.calls.length, 2, 'the original mutation is already in flight');
  const replacement = {id:99, slug:'test', name:'Replacement', icon_emoji:'🌱', icon_mime:''};
  h.state.apps = [replacement];
  h.serverStates.set('test', {app_id:99, mime:'', data:null, emoji:'🌱', revision:'replacement-revision'});
  h.mount(replacement);
  const doc = h.doc, draft = doc.getElementById('general-icon-emoji-input');
  draft.value = '🚀'; doc.getElementById('general-name').value = 'Replacement draft'; draft.focus();
  const status = doc.getElementById('general-icon-status'); status.textContent = 'Replacement feedback'; status.hidden = false;
  const error = doc.getElementById('general-icon-error'); error.textContent = ''; error.hidden = true;
  const preview = doc.getElementById('general-icon-preview').innerHTML;
  const behavior = doc.getElementById('configuration-behavior').outerHTML;
  response.resolve(iconResponse(iconConfirmation()));
  await saving;
  assert.equal(replacement.icon_emoji, '🌱');
  assert.equal(h.state.apps[0], replacement);
  assert.equal(h.state.apps[0].icon_emoji, '🌱', 'the sidebar/list cache must retain the replacement icon');
  assert.equal(doc.getElementById('general-icon-preview').innerHTML, preview);
  assert.equal(doc.getElementById('configuration-behavior').outerHTML, behavior);
  assert.equal(draft.value, '🚀');
  assert.equal(doc.getElementById('general-name').value, 'Replacement draft');
  assert.equal(doc.activeElement, draft);
  assert.equal(status.textContent, 'Replacement feedback'); assert.equal(status.hidden, false);
  assert.equal(error.textContent, ''); assert.equal(error.hidden, true);
  assert.equal(doc.getElementById('general-icon-undo').hidden, true, 'the original app Undo must not be offered on its replacement');
  assert.equal(h.calls.length, 2, 'an obsolete success must not refresh replacement metadata');
  assert.equal(h.serverStates.get('test').app_id, 99);
  assert.equal(h.serverStates.get('test').emoji, '🌱');
  assert.equal(original.id, 1);
});

test('applying an old app icon cannot overwrite a same-slug replacement in detail or list caches', () => {
  const h = iconHarness({initialEmoji:'📈'}), original = h.state.apps[0];
  const replacement = {id:99, slug:'test', name:'Replacement', icon_emoji:'🌱', icon_mime:''};
  h.state.apps = [replacement]; h.mount(replacement);
  const preview = h.doc.getElementById('general-icon-preview').innerHTML;
  h.apply(original, {mime:'image/png', emoji:''});
  assert.equal(original.icon_mime, 'image/png', 'the completed action may update its original app object');
  assert.equal(replacement.icon_emoji, '🌱');
  assert.equal(replacement.icon_mime, '');
  assert.equal(h.state.apps[0].icon_emoji, '🌱');
  assert.equal(h.doc.getElementById('general-icon-preview').innerHTML, preview);
});

test('a delayed settings refresh after icon success cannot replace a recreated app or its runtime summary', async () => {
  const metadata = deferred(), h = iconHarness({initialEmoji:'📈', read:() => metadata.promise});
  const saving = h.set('📊');
  await flushIconPreparation();
  assert.equal(h.calls.length, 3, 'the icon was confirmed and its metadata refresh is pending');
  const replacement = {id:99, slug:'test', name:'Replacement', icon_emoji:'🌱', icon_mime:''};
  h.state.apps = [replacement]; h.mount(replacement);
  applyConfigurationContext(h.doc, replacement, {});
  const behavior = h.doc.getElementById('configuration-behavior').outerHTML;
  const draft = h.doc.getElementById('general-name'); draft.value = 'Replacement draft';
  metadata.resolve(iconResponse({app:{id:1, slug:'test', name:'Stale original name', icon_emoji:'📊', icon_mime:''}, configuration_sources:{
    bundle_status:'available', fleet_status:'unmanaged', saved_toml:'[app]\nicon = "📊"',
    fields:{icon:{source:'bundle', current:'"📊"', declared:'"📈"', differs:true}},
  }}));
  await saving;
  assert.deepEqual(replacement, {id:99, slug:'test', name:'Replacement', icon_emoji:'🌱', icon_mime:''});
  assert.equal(h.state.apps[0], replacement);
  assert.equal(h.doc.getElementById('configuration-behavior').outerHTML, behavior);
  assert.equal(draft.value, 'Replacement draft');
  assert.equal(h.doc.getElementById('general-icon-status').hidden, true);
  assert.equal(h.calls.length, 3);
});

test('save feedback preserves focus in another section',()=>{
 const doc=new JSDOM(html).window.document;
 const source=app.slice(app.indexOf('  function focusSettingsFeedback('),app.indexOf('  async function saveSettingsSection('));
 const run=new Function('document',`${source};return focusSettingsFeedback;`)(doc);
 const other=doc.getElementById('scaling-replicas');other.focus();
 run({btn:doc.getElementById('resources-save-btn')});assert.equal(doc.activeElement,other);
});
test('unknown hub privacy has explicit feedback rather than an invented effective policy',()=>{
 const doc=new JSDOM(html).window.document;
 const select=doc.getElementById('usage-privacy-mode');select.dataset.hubMode='';select.dataset.originalMode='inherit';select.value='inherit';
 const source=app.slice(app.indexOf('  const usageModeRank ='),app.indexOf('  function populateGeneralTab('));
 const run=new Function('document','usageModeLabel','setHidden',`${source};return updateUsagePrivacyHelp;`)(doc,mode=>mode||'Policy unavailable',(el,hidden)=>{el.hidden=hidden;});
 run();assert.match(doc.getElementById('usage-privacy-help').textContent,/unavailable/);assert.doesNotMatch(doc.getElementById('usage-privacy-help').textContent,/undefined/);
 select.dataset.originalMode='pseudonymous';select.value='pseudonymous';run();assert.equal(doc.getElementById('usage-privacy-warning').hidden,true,'an unchanged override must not show a destructive warning');
 select.value='unattributed';run();assert.match(doc.getElementById('usage-privacy-help').textContent,/session totals/);assert.equal(doc.getElementById('usage-privacy-warning').hidden,false);
});
test('worker advice uses saved memory and does not repeat an unchanged live announcement',async()=>{
 const {workerCapacityLine}=await import('../static/views/worker-isolation.js');
 const {updateWorkerFields}=await import('../static/views/configuration-context.js');
 const doc=new JSDOM(html).window.document;
 doc.getElementById('worker-isolation').value='per_session';doc.getElementById('worker-max-workers').value='20';doc.getElementById('worker-grouped-size').value='1';doc.getElementById('resources-memory').value='1024';
 const source=app.slice(app.indexOf('  function updateWorkerCapacity('),app.indexOf('  async function saveHibernateSettings('));
 const stable=app.slice(app.indexOf('  function setStableText('),app.indexOf('  function updateRenderPacingAdvice('));
 const run=new Function('document','workerCapacityLine','updateWorkerFields',`const originalResourceSettings={memory_limit_mb:512};const currentIsolationChoice=()=> 'per_session';const isSectionDirty=()=>true;${stable}${source};return updateWorkerCapacity;`)(doc,workerCapacityLine,updateWorkerFields);
 run();const el=doc.getElementById('worker-capacity');assert.match(el.textContent,/~13 GB/);assert.match(el.textContent,/saved memory limit/);
 const observer=new doc.defaultView.MutationObserver(()=>{});observer.observe(el,{childList:true,characterData:true,subtree:true});run();assert.equal(observer.takeRecords().length,0);observer.disconnect();
});

function settingsFeedback(doc) {
  const source = app.slice(app.indexOf('  function focusSettingsFeedback('), app.indexOf('  async function saveSettingsSection('));
  return new Function('document', `${source};return focusSettingsFeedback;`)(doc);
}

test('worker validation reveals and focuses the invalid field with its error', async () => {
  const doc = new JSDOM(html).window.document;
  const values = {'scaling-replicas':'2', 'scaling-cap':'20', 'scaling-cap-mode':'custom', 'worker-isolation':'grouped', 'worker-grouped-size':'2', 'worker-max-workers':'0', 'worker-warm-spares':'0'};
  for (const [id, value] of Object.entries(values)) doc.getElementById(id).value = value;
  const start = app.indexOf('  async function saveScalingSettings(');
  const end = app.indexOf('  async function saveRenderPacing(', start);
  const run = new Function('document', 'setError', 'setHidden', `let settingsSlug='test', settingsRevision=0;const originalScalingSettings={replicas:2};const currentIsolationChoice=()=> 'grouped';${app.slice(start,end)};return saveScalingSettings;`)(doc, (el, message) => { el.textContent = message; el.hidden = !message; }, (el, hidden) => { el.hidden = hidden; });
  const button = doc.getElementById('scaling-save-btn');
  button.hidden = false; button.disabled = false; button.focus();
  await run();
  settingsFeedback(doc)({btn:button});
  const invalid = doc.getElementById('worker-max-workers');
  assert.equal(doc.activeElement, invalid);
  assert.equal(doc.getElementById('worker-settings').open, true);
  assert.equal(invalid.getAttribute('aria-invalid'), 'true');
  assert.equal(invalid.getAttribute('aria-errormessage'), 'scaling-error');
  assert.match(doc.getElementById('scaling-error').textContent, /1 to 1000/);
});

test('autoscale validation identifies the field to correct', async () => {
  const {readAutoscaleForm} = await import('../static/views/autoscale.js');
  const doc = new JSDOM(html).window.document;
  doc.getElementById('autoscale-enabled').checked = true;
  doc.getElementById('autoscale-min').value = '3';
  doc.getElementById('autoscale-max').value = '2';
  assert.equal(readAutoscaleForm(doc).fieldId, 'autoscale-max');
  doc.getElementById('autoscale-min').value = '';
  assert.equal(readAutoscaleForm(doc).fieldId, 'autoscale-min');
  doc.getElementById('autoscale-min').value = '1';
  doc.getElementById('autoscale-target-mode').value = 'custom';
  doc.getElementById('autoscale-target').value = '101';
  assert.equal(readAutoscaleForm(doc).fieldId, 'autoscale-target');
});

function environmentHarness({confirm = () => false, save = async () => ({ok:true, json:async () => ({})})} = {}) {
  const doc = new JSDOM(html).window.document;
  const messages = [];
  const source = app.slice(app.indexOf('  let envEditingKey ='), app.indexOf('  async function deleteEnvVar('));
  const scope = {
    document:doc, updateConfigurationDirty, window:{confirm}, environmentEditValue: v => v?.secret ? '' : v?.value ?? '',
    setError:(el, message) => {el.textContent=message;el.hidden=!message;},
    runInFlight:async (_, action) => action(), flashToast:message => messages.push(message),
    api:async (_, options) => options?.method === 'PUT' ? save(options) : {ok:true,json:async()=>({items:[{key:'API_KEY',secret:true}]})},
    state:{apps:[],user:{}}, detailApp:{slug:'test',status:'stopped'},canManageApp:()=>true,
  };
  const methods = new Function(...Object.keys(scope), `let settingsSlug='test',settingsRevision=0;${source};return {open:openEnvForm,close:closeEnvForm,submit:submitEnvForm,dirty:envFormDirty,refresh:()=>refreshEnvList(settingsSlug)};`)(...Object.values(scope));
  return {doc,messages,...methods};
}

test('declining a variable discard preserves the draft; accepting clears its dirty state', () => {
  let accepted = false;
  const h = environmentHarness({confirm:()=>accepted});
  h.open({key:'REGION',value:'europe',secret:false});
  h.doc.getElementById('env-form-value').value = 'test-region';
  h.close(new h.doc.defaultView.MouseEvent('click'));
  assert.equal(h.doc.getElementById('env-form').hidden, false);
  assert.equal(h.doc.getElementById('env-form-value').value, 'test-region');
  assert.equal(h.dirty(), true);
  accepted = true; h.close();
  assert.equal(h.doc.getElementById('env-form').hidden, true);
  assert.equal(h.dirty(), false);
});

test('a failed secret replacement keeps the masked draft and can be retried', async () => {
  const payloads = [];
  const h = environmentHarness({save:async options => {
    payloads.push(JSON.parse(options.body));
    // Browsers blur a focused control when the request locks it.
    h.doc.body.tabIndex = -1; h.doc.body.focus();
    return payloads.length === 1 ? {ok:false,json:async()=>({error:'Temporary save failure'})} : {ok:true,json:async()=>({restart_required:false})};
  }});
  h.open({key:'API_KEY',secret:true});
  const value = h.doc.getElementById('env-form-value');
  assert.equal(value.value, ''); assert.equal(value.type, 'password'); assert.equal(value.required, true);
  value.value = 'synthetic-test-value';
  await h.submit({preventDefault(){}});
  assert.equal(h.doc.getElementById('env-form').hidden, false);
  assert.equal(value.value, 'synthetic-test-value'); assert.equal(value.type, 'password');
  assert.equal(h.doc.getElementById('env-form-save').disabled, false);
  assert.equal(h.doc.activeElement, h.doc.getElementById('env-form-save'));
  assert.equal(h.dirty(), true);
  assert.match(h.doc.getElementById('env-form-error').textContent, /Temporary save failure/);
  await h.submit({preventDefault(){}});
  assert.equal(h.doc.getElementById('env-form').hidden, true);
  assert.equal(h.dirty(), false);
  assert.deepEqual(payloads, Array(2).fill({value:'synthetic-test-value',secret:true}));
  assert.equal(h.messages.at(-1), 'Variable saved.');
});

test('a saved variable with a failed restart exits the editor and explains how to apply it', async () => {
  const h = environmentHarness({save:async()=>({ok:true,json:async()=>({restart_error:'Runtime unavailable'})})});
  h.open({key:'API_KEY',secret:true});
  h.doc.getElementById('env-form-value').value = 'synthetic-test-value';
  await h.submit({preventDefault(){}});
  assert.equal(h.doc.getElementById('env-form').hidden, true);
  assert.equal(h.dirty(), false);
  assert.match(h.doc.getElementById('env-form-error').textContent, /Variable saved, but applying it failed.*Restart to apply/);
});


test('validation errors stay associated with their fields without stealing another section’s focus', () => {
  const doc = new JSDOM(html).window.document;
  const field = doc.getElementById('resources-memory');
  field.setAttribute('aria-invalid', 'true');
  const error = doc.getElementById('resources-error');
  error.hidden = false; error.textContent = 'Memory limit must be at least 16 MB.';
  const other = doc.getElementById('general-name'); other.focus();
  settingsFeedback(doc)({btn:doc.getElementById('resources-save-btn')});
  assert.equal(doc.activeElement, other);
  assert.equal(field.getAttribute('aria-errormessage'), error.id);
});

test('duplicate environment keys focus the correction and reopening clears validation', async () => {
  const h = environmentHarness();
  await h.refresh(); h.open();
  const key = h.doc.getElementById('env-form-key');
  key.value = 'API_KEY';
  await h.submit({preventDefault(){}});
  assert.equal(h.doc.activeElement, key);
  assert.equal(key.getAttribute('aria-invalid'), 'true');
  assert.equal(key.getAttribute('aria-errormessage'), 'env-form-error');
  assert.match(h.doc.getElementById('env-form-error').textContent, /already exists/);
  h.close(true);
  assert.equal(h.doc.getElementById('env-form-error').hidden, true);
  h.open();
  assert.equal(key.hasAttribute('aria-invalid'), false);
  assert.equal(key.hasAttribute('aria-errormessage'), false);
});
