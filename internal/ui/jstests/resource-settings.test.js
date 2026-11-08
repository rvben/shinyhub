import { test } from 'node:test';
import assert from 'node:assert/strict';
import { resourceSettingsPatch } from '../static/views/resource-settings.js';

test('CPU-only save preserves memory changed on the server since the form loaded', () => {
  assert.deepEqual(resourceSettingsPatch(
    { memory_limit_mb: 256, cpu_quota_percent: 200 },
    { memory_limit_mb: 256, cpu_quota_percent: 100 },
  ), { cpu_quota_percent: 200 });
});

test('clearing memory submits only the edited field and a clean form submits nothing', () => {
  const saved = { memory_limit_mb: null, cpu_quota_percent: 100 };
  assert.deepEqual(resourceSettingsPatch(saved,
    { memory_limit_mb: 256, cpu_quota_percent: 100 }), { memory_limit_mb: null });
  assert.deepEqual(resourceSettingsPatch(saved, saved), {});
});

import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { directSettings, populateDirectSetting, installDirectSetting, readDirectSetting } from '../static/views/direct-setting.js';
const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const appSource = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
const response = (body, status = 200) => ({ok:status < 400, status, json:async () => body});
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return {promise, resolve}; };
function harness({initial = {}, request, confirm = () => true} = {}) {
  const doc = new JSDOM(html).window.document;
  let server = {slug:'test', status:'running', memory_limit_mb:512, cpu_quota_percent:100, ...initial};
  const calls = [], confirmations = [];
  const section = doc.getElementById('configuration-resources');
  const button = doc.getElementById('resources-save-btn'); button.hidden = false;
  const discard = doc.createElement('button'); section.append(discard);
  for (const [id, value, fallback] of [['resources-memory', server.memory_limit_mb, '512'], ['resources-cpu', server.cpu_quota_percent, '1']]) {
    doc.getElementById(id).dataset.defaultValue = fallback;
    populateDirectSetting(doc, id, value, true);
  }
  const rec = {btn:button, dirtyEl:doc.getElementById('resources-dirty'), discard, sectionId:section.id,
    els:['resources-memory','resources-cpu','resources-memory-mode','resources-cpu-mode'].map(id => doc.getElementById(id))};
  const scope = {document:doc, directSettings, readDirectSetting, populateDirectSetting, resourceSettingsPatch,
    window:{confirm:message => { confirmations.push(message); return confirm(message); }},
    setHidden:(el, value) => { el.hidden = value; },
    setError:(el, message) => { el.textContent = message || ''; el.hidden = !message; },
    updateConfigurationDirty:() => {}, updateWorkerCapacity:() => {}, loadApps:async () => {}, refreshDetailFleetState:async () => {},
    handleUnauthorized:async () => {},
    api:async (url, options) => {
      calls.push({url, options});
      const fallback = () => {
        if (options?.method === 'PATCH') { Object.assign(server, JSON.parse(options.body)); return response({app:{...server}}); }
        return response({app:{...server}, resource_revision:'current-revision'});
      };
      return request ? request({url, options, server, fallback}) : fallback();
    },
  };
  const shared = appSource.slice(appSource.indexOf('  function focusSettingsFeedback('), appSource.indexOf('  function anySettingsDirty('));
  const save = appSource.slice(appSource.indexOf('  async function saveResources('), appSource.indexOf('  function updateMinWarmWarning('));
  const controls = new Function(...Object.keys(scope), 'rec', `
    let settingsSlug='test', settingsRevision=0;
    const settingsSections={resources:rec};
    let originalResourceSettings={memory_limit_mb:${JSON.stringify(server.memory_limit_mb)},cpu_quota_percent:${JSON.stringify(server.cpu_quota_percent)}};
    let resourceApplyPending=false;
    ${shared}\n${save}
    snapshotSettingsSection('resources');
    return {
      save:retry=>saveSettingsSection('resources',()=>saveResources(retry)),
      changed:()=>recomputeDirty('resources'),
      context:(slug='other')=>{settingsSlug=slug;settingsRevision++;},
      result:()=>({originalResourceSettings,resourceApplyPending,dirty:isSectionDirty('resources')}),
    };
  `)(...Object.values(scope), rec);
  for (const id of ['resources-memory','resources-cpu']) installDirectSetting(doc, id, controls.changed);
  return {doc, rec, calls, confirmations, ...controls,
    edit(id, value, mode = 'custom') {
      const select = doc.getElementById(`${id}-mode`);
      select.value = mode; select.dispatchEvent(new doc.defaultView.Event('change'));
      if (mode === 'custom') {
        const input = doc.getElementById(id); input.value = value;
        input.dispatchEvent(new doc.defaultView.Event('input'));
      }
    },
    server:values => Object.assign(server, values),
  };
}

test('fractional CPU save preserves a concurrent memory change and adopts server values', async () => {
  const h = harness(); h.edit('resources-cpu', '0.29'); h.server({memory_limit_mb:768});
  await h.save();
  const patch = h.calls.find(call => call.options?.method === 'PATCH');
  assert.deepEqual(JSON.parse(patch.options.body), {cpu_quota_percent:29});
  assert.equal(patch.options.headers['X-Shinyhub-If-Resource-Revision'], 'current-revision');
  assert.deepEqual(h.result().originalResourceSettings, {memory_limit_mb:768, cpu_quota_percent:29});
  assert.equal(h.doc.getElementById('resources-memory').value, '768');
  assert.equal(h.result().dirty, false);
  assert.match(h.doc.getElementById('resources-status').textContent, /Any required restart runs automatically/);
});

test('saved limits whose application fails retry the latest target without overwriting other changes', async () => {
  let failed = true;
  const h = harness({request:({options, fallback, server}) => {
    if (options?.method !== 'PATCH') return fallback();
    Object.assign(server, JSON.parse(options.body));
    if (failed) { failed = false; return response({app:{...server,status:'degraded'},resource_update_error:'Runtime unavailable.'}); }
    return fallback();
  }});
  h.edit('resources-memory', '1024'); await h.save();
  assert.equal(h.result().dirty, false);
  assert.equal(h.result().resourceApplyPending, true);
  assert.equal(h.doc.getElementById('resources-retry-btn').hidden, false);
  assert.match(h.doc.getElementById('resources-error').textContent, /Could not apply saved limits/);
  h.server({memory_limit_mb:2048,cpu_quota_percent:200});
  await h.save(true);
  const patches = h.calls.filter(call => call.options?.method === 'PATCH');
  assert.deepEqual(JSON.parse(patches[1].options.body), {cpu_quota_percent:200});
  assert.deepEqual(h.result().originalResourceSettings, {memory_limit_mb:2048,cpu_quota_percent:200});
  assert.equal(h.result().resourceApplyPending, false);
});

test('save locks the complete form, announces progress, and prevents a duplicate request', async () => {
  const pending = deferred();
  const h = harness({request:({options, fallback}) => options?.method === 'PATCH' ? pending.promise : fallback()});
  h.edit('resources-memory', '1024');
  const saving = h.save(); await new Promise(setImmediate);
  assert.equal(h.doc.getElementById('configuration-resources').getAttribute('aria-busy'), 'true');
  assert.ok([...h.doc.querySelectorAll('#configuration-resources input, #configuration-resources select, #configuration-resources button')].every(el => el.disabled));
  assert.match(h.doc.getElementById('resources-status').textContent, /Saving resource limits/);
  await h.save(); assert.equal(h.calls.length, 2);
  pending.resolve(response({app:{memory_limit_mb:1024,cpu_quota_percent:100,status:'running'}})); await saving;
  assert.equal(h.doc.getElementById('configuration-resources').hasAttribute('aria-busy'), false);
  assert.equal(h.doc.getElementById('resources-memory-mode').disabled, false);
  assert.equal(h.rec.btn.disabled, true);
});

for (const status of [409, 503]) test(`a ${status} response preserves the draft and restores Save`, async () => {
  const h = harness({request:({options, fallback}) => options?.method === 'PATCH' ? response({error:'Try again later.'}, status) : fallback()});
  h.edit('resources-memory', '1024'); await h.save();
  assert.equal(h.doc.getElementById('resources-memory').value, '1024');
  assert.equal(h.result().dirty, true);
  assert.equal(h.rec.btn.disabled, false);
  assert.equal(h.doc.getElementById('resources-status').hidden, true);
  assert.equal(h.doc.getElementById('resources-error').hidden, false);
});

test('malformed save confirmation retains the draft without claiming success', async () => {
  const h = harness({request:({options, fallback}) => options?.method === 'PATCH' ? response({}) : fallback()});
  h.edit('resources-memory', '1024'); await h.save();
  assert.equal(h.result().dirty, true);
  assert.match(h.doc.getElementById('resources-error').textContent, /Could not confirm/);
  assert.equal(h.doc.getElementById('resources-status').hidden, true);
});

test('failed preflight prevents writes and preserves edits', async () => {
  const h = harness({request:() => { throw new Error('offline'); }});
  h.edit('resources-memory', '1024'); await h.save();
  assert.equal(h.calls.length, 1);
  assert.equal(h.result().dirty, true);
  assert.match(h.doc.getElementById('resources-error').textContent, /Could not check/);
});

test('invalid limits focus an associated correction without a network request', async () => {
  const h = harness(); h.edit('resources-cpu', '0.125'); await h.save();
  assert.equal(h.calls.length, 0);
  assert.equal(h.doc.activeElement.id, 'resources-cpu');
  assert.equal(h.doc.activeElement.getAttribute('aria-errormessage'), 'resources-error');
  assert.match(h.doc.getElementById('resources-error').textContent, /increments of 0.01/);
});

test('confirmation uses the current running state and cancellation retains the draft', async () => {
  const h = harness({initial:{status:'stopped'},confirm:() => false});
  h.edit('resources-memory', '1024'); h.server({status:'running'}); await h.save();
  assert.equal(h.calls.length, 1);
  assert.match(h.confirmations[0], /deadline disconnect/);
  assert.equal(h.result().dirty, true);
  assert.equal(h.doc.getElementById('resources-status').hidden, true);
});

test('stopped apps save without interruption confirmation', async () => {
  const h = harness({initial:{status:'stopped'}}); h.edit('resources-memory', '', 'off'); await h.save();
  assert.equal(h.confirmations.length, 0);
  assert.equal(h.result().originalResourceSettings.memory_limit_mb, 0);
  assert.match(h.doc.getElementById('resources-status').textContent, /next starts/);
});

for (const phase of ['read','write']) test(`late ${phase} responses cannot replace a newer settings context`, async () => {
  const pending = deferred();
  const h = harness({request:({options, fallback}) => ((phase === 'write') === (options?.method === 'PATCH')) ? pending.promise : fallback()});
  h.edit('resources-memory', '1024'); const saving = h.save(); await new Promise(setImmediate);
  h.context('test');
  h.doc.getElementById('resources-status').textContent = 'New settings context';
  pending.resolve(response({app:{memory_limit_mb:2048,cpu_quota_percent:100},resource_revision:'new-revision'})); await saving;
  assert.equal(h.doc.getElementById('resources-status').textContent, 'New settings context');
  assert.equal(h.result().originalResourceSettings.memory_limit_mb, 512);
  if (phase === 'read') assert.equal(h.calls.length, 1);
});

test('untouched legacy limits survive a save even when outside current input constraints', async () => {
  const h = harness({initial:{memory_limit_mb:8}}); h.edit('resources-cpu', '2'); await h.save();
  assert.deepEqual(JSON.parse(h.calls.find(call => call.options?.method === 'PATCH').options.body), {cpu_quota_percent:200});
  assert.equal(h.result().originalResourceSettings.memory_limit_mb, 8);
});
