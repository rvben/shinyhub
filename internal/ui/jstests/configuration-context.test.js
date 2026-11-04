import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { populateDirectSetting } from '../static/views/direct-setting.js';
import { configurationDefaults, applyConfigurationContext, updateWorkerFields, environmentEditValue, CONFIGURATION_PAGES, configurationPageForSection, revealConfigurationSection, updateConfigurationDirty } from '../static/views/configuration-context.js';
const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const defaults = { hibernate_timeout_minutes: 30, max_sessions_per_replica: 12, autoscale_target: .65, memory_limit_mb: 512, cpu_quota_percent: 150, worker_isolation: 'grouped', schedule_timezone: 'Europe/Amsterdam' };

test('inheritance describes server defaults even when the app has overrides', () => {
  const app = { hibernate_timeout_minutes: 5, max_sessions_per_replica: 99, autoscale_target: .9, memory_limit_mb: 4096, cpu_quota_percent: 400, worker_isolation: 'multiplex' };
  const doc = new JSDOM(html).window.document;
  applyConfigurationContext(doc, app, { configuration_defaults: defaults, effective_max_sessions_per_replica: 99 });
  for (const [id, value, expected] of [
    ['hibernate-custom-minutes', 5, /30 minutes/], ['scaling-cap', 99, /12 sessions/],
    ['autoscale-target', .9, /65%/], ['resources-memory', 4096, /512 MB/], ['resources-cpu', 400, /1.5 cores/],
  ]) { populateDirectSetting(doc, id, value, true); assert.match(doc.getElementById(`${id}-hint`).textContent, expected); }
  assert.match(doc.querySelector('#worker-isolation option[value=""]').textContent, /Grouped/);
  assert.match(doc.getElementById('sched-timezone-help').textContent, /Europe\/Amsterdam/);
});

test('zero defaults mean disabled or unlimited, while absent defaults stay unknown', () => {
  const doc = new JSDOM(html).window.document;
  applyConfigurationContext(doc, {}, { configuration_defaults: { hibernate_timeout_minutes: 0, max_sessions_per_replica: 0, memory_limit_mb: 0, cpu_quota_percent: 0 } });
  populateDirectSetting(doc, 'resources-memory', null, true);
  assert.match(doc.getElementById('resources-memory-hint').textContent, /No limit/);
  populateDirectSetting(doc, 'hibernate-custom-minutes', null, true);
  assert.match(doc.getElementById('hibernate-custom-minutes-hint').textContent, /Never hibernate/);
  applyConfigurationContext(doc, {}, {});
  populateDirectSetting(doc, 'resources-memory', null, true);
  assert.match(doc.getElementById('resources-memory-hint').textContent, /not reported/);
  assert.equal(doc.getElementById('scaling-cap').dataset.defaultValue, '');
});

test('refreshed defaults update inherited values and guidance while preserving explicit drafts', () => {
  const doc = new JSDOM(html).window.document;
  applyConfigurationContext(doc, {}, { configuration_defaults: defaults });
  populateDirectSetting(doc, 'resources-memory', null, true);
  populateDirectSetting(doc, 'resources-cpu', 200, false);
  const memory = doc.getElementById('resources-memory');
  const cpu = doc.getElementById('resources-cpu');
  cpu.value = '3.5';
  applyConfigurationContext(doc, {}, { configuration_defaults: { ...defaults, memory_limit_mb: 1024, cpu_quota_percent: 250 } });
  assert.equal(memory.value, '1024');
  assert.equal(doc.getElementById('resources-memory-mode').value, 'inherit');
  assert.match(doc.getElementById('resources-memory-hint').textContent, /Follows server default: 1024 MB/);
  assert.equal(cpu.value, '3.5');
  assert.equal(cpu.disabled, true);
  assert.equal(doc.getElementById('resources-cpu-mode').value, 'custom');
  assert.match(doc.getElementById('resources-cpu-hint').textContent, /Server default: 2.5 cores/);
});

test('older servers supply effective values only for actually inherited settings', () => {
  const envelope = { effective_max_sessions_per_replica: 20, effective_autoscale_target: .8, effective_hibernate_timeout_minutes: 45 };
  assert.equal(configurationDefaults({}, envelope).sessions, 20);
  assert.equal(configurationDefaults({ max_sessions_per_replica: 20 }, envelope).sessions, null);
  assert.equal(configurationDefaults({ autoscale_target: .8 }, envelope).target, null);
  assert.equal(configurationDefaults({ hibernate_timeout_minutes: 45 }, envelope).hibernation, null);
});

test('worker fields follow explicit and inherited isolation without discarding values or granting edit access', () => {
  const doc = new JSDOM(html).window.document;
  const select = doc.getElementById('worker-isolation');
  const grouped = doc.getElementById('worker-grouped-size');
  const max = doc.getElementById('worker-max-workers');
  grouped.value = '7';
  select.value = 'multiplex';
  updateWorkerFields(doc);
  assert.equal(grouped.closest('.scaling-row').hidden, true);
  assert.equal(grouped.disabled, true);
  assert.equal(max.disabled, true);
  select.value = ''; select.dataset.defaultIsolation = 'grouped';
  updateWorkerFields(doc);
  assert.equal(grouped.closest('.scaling-row').hidden, false);
  assert.equal(grouped.value, '7');
  assert.equal(max.disabled, false);
  select.value = 'per_session'; updateWorkerFields(doc);
  assert.equal(grouped.closest('.scaling-row').hidden, true);
  assert.equal(grouped.disabled, true);
  assert.equal(max.closest('.scaling-row').hidden, false);
  select.disabled = true; updateWorkerFields(doc);
  assert.equal(max.disabled, true);
});

test('environment editing preserves non-secret values and never fills secret values', () => {
  assert.equal(environmentEditValue({ value: 'europe', secret: false }), 'europe');
  assert.equal(environmentEditValue({ value: 'not-for-display', secret: true }), '');
  assert.equal(environmentEditValue(null), '');
});

test('saved behavior does not read unsaved form values and failed refresh hides stale claims', async () => {
  const {markConfigurationBehaviorStale} = await import('../static/views/configuration-context.js');
  const doc = new JSDOM(html).window.document;
  doc.getElementById('scaling-replicas').value = '99';
  const app = {replicas:2,worker_isolation:'multiplex',hibernate_timeout_minutes:0};
  applyConfigurationContext(doc,app,{});
  assert.match(doc.getElementById('configuration-behavior').textContent,/2 replicas/);
  assert.doesNotMatch(doc.getElementById('configuration-behavior').textContent,/99/);
  markConfigurationBehaviorStale(doc);
  assert.equal(doc.getElementById('configuration-behavior').hidden,true);
  assert.match(doc.getElementById('configuration-behavior-note').textContent,/Reload/);
  applyConfigurationContext(doc,{...app,replicas:3},{});
  assert.equal(doc.getElementById('configuration-behavior').hidden,false);
  assert.match(doc.getElementById('configuration-behavior').textContent,/3 replicas/);
});

test('worker explanations and resource units follow the actual isolation policy', () => {
  const doc = new JSDOM(html).window.document;
  applyConfigurationContext(doc,{worker_isolation:'per_session'},{});
  assert.match(doc.getElementById('resources-description').textContent,/each worker/);
  assert.match(doc.getElementById('resources-memory-help').textContent,/each worker/);
  const select = doc.getElementById('worker-isolation'); select.value='per_session';
  updateWorkerFields(doc);
  assert.match(doc.getElementById('worker-mode-help').textContent,/tabs in the same browser profile share a worker/);
  select.value='grouped'; updateWorkerFields(doc);
  assert.match(doc.getElementById('worker-mode-help').textContent,/same group/);
  select.value='multiplex'; updateWorkerFields(doc);
  assert.match(doc.getElementById('worker-mode-help').textContent,/fewer processes/);
});

test('unchanged dirty state does not repeat live announcements', async () => {
  const {updateConfigurationDirty} = await import('../static/views/configuration-context.js');
  const dom = new JSDOM(html); const doc = dom.window.document;
  updateConfigurationDirty(doc,'configuration-resources',true);
  const observer = new dom.window.MutationObserver(() => {});
  observer.observe(doc.getElementById('configuration-pending'),{childList:true,characterData:true,subtree:true});
  updateConfigurationDirty(doc,'configuration-resources',true);
  assert.equal(observer.takeRecords().length,0);
  updateConfigurationDirty(doc,'configuration-resources',false);
  assert.ok(observer.takeRecords().length > 0);
  observer.disconnect();
});

test('CPU defaults convert to cores and capacity is clearly shared or unknown', () => {
  const doc = new JSDOM(html).window.document;
  applyConfigurationContext(doc,{}, {configuration_defaults:{cpu_quota_percent:150},configuration_host_cpu_cores:8});
  populateDirectSetting(doc,'resources-cpu',null,true);
  assert.equal(doc.getElementById('resources-cpu').value,'1.5');
  assert.match(doc.getElementById('resources-cpu-hint').textContent,/Follows server default: 1.5 cores/);
  assert.match(doc.getElementById('resources-cpu-capacity').textContent,/8 cores total, shared/);
  applyConfigurationContext(doc,{}, {configuration_capacity_scope:'per_host'});
  assert.match(doc.getElementById('resources-cpu-capacity').textContent,/depends on placement/);
  assert.doesNotMatch(doc.getElementById('resources-cpu-capacity').textContent,/8 cores/);
  applyConfigurationContext(doc,{}, {});
  assert.match(doc.getElementById('resources-cpu-capacity').textContent,/not reported/);
});

function contextualLinks(doc) {
  const nav = doc.createElement('nav');
  for (const page of [...Object.keys(CONFIGURATION_PAGES), 'access']) {
    const link = doc.createElement('a'); link.href = `/apps/demo/settings/${page}`;
    link.dataset.settingsPage = page; link.textContent = page;
    nav.append(link);
  }
  doc.getElementById('app-detail-view').prepend(nav);
  return nav;
}

test('settings pages group the existing sections while retaining every form node and draft', () => {
  const doc = new JSDOM(html).window.document, view = doc.getElementById('app-detail-view');
  const expected = {
    general:['configuration-general', 'danger-zone'], environment:['configuration-environment'],
    resources:['configuration-resources', 'configuration-pacing'],
    scaling:['configuration-scaling', 'autoscale-options', 'configuration-hibernation'], privacy:['usage-privacy-settings'],
  };
  assert.deepEqual(CONFIGURATION_PAGES, expected);
  const fields = ['general-name', 'env-form-key', 'resources-cpu', 'worker-max-workers'].map(id => doc.getElementById(id));
  fields.forEach((field, index) => { field.value = ['Unsaved title', 'REGION', '0.29', '7'][index]; });
  const values = fields.map(field => field.value);
  doc.getElementById('resources-dirty').hidden = false;
  fields[2].disabled = true;
  let recentered = 0;
  const panel = doc.getElementById('detail-configuration-panel');
  panel.querySelectorAll(':scope > .settings-block').forEach(section => { section.scrollIntoView = () => recentered++; });
  fields[0].focus();
  for (const [page, ids] of Object.entries(expected)) {
    revealConfigurationSection(view, page);
    assert.equal(panel.dataset.settingsPage, page);
    assert.deepEqual([...panel.querySelectorAll(':scope > [data-current-section]')].map(section => section.id), ids);
    for (const id of ids) {
      assert.equal(configurationPageForSection(id), page);
      revealConfigurationSection(view, id);
      assert.equal(panel.dataset.settingsPage, page);
    }
    assert.deepEqual(fields.map(field => doc.getElementById(field.id)), fields);
    assert.deepEqual(fields.map(field => field.value), values);
  }
  assert.equal(doc.activeElement, fields[0], 'rendering a page must not steal field focus');
  assert.equal(fields[2].disabled, true);
  assert.equal(doc.getElementById('resources-dirty').hidden, false);
  assert.equal(recentered, 0, 'rendering a settings page must not recenter the layout');
  revealConfigurationSection(view, 'unknown-page');
  assert.equal(panel.dataset.settingsPage, 'general');
});

test('dirty indicators aggregate grouped sections into contextual page links and pending guidance', () => {
  const doc = new JSDOM(html).window.document, nav = contextualLinks(doc);
  const view = doc.getElementById('app-detail-view'), panel = doc.getElementById('detail-configuration-panel');
  revealConfigurationSection(view, 'resources');
  const link = page => nav.querySelector(`[data-settings-page="${page}"]`);
  updateConfigurationDirty(doc, 'configuration-resources', true);
  updateConfigurationDirty(doc, 'configuration-pacing', true);
  updateConfigurationDirty(doc, 'configuration-resources', false);
  assert.equal(link('resources').hasAttribute('data-dirty'), true, 'runtime changes keep Resources dirty after resource limits are saved');
  assert.equal(link('resources').getAttribute('aria-label'), 'Resources (unsaved)');
  updateConfigurationDirty(doc, 'autoscale-options', true);
  updateConfigurationDirty(doc, 'configuration-hibernation', true);
  updateConfigurationDirty(doc, 'access-visibility', true);
  updateConfigurationDirty(doc, 'danger-zone', true);
  const pending = doc.getElementById('configuration-pending');
  assert.equal(pending.textContent, 'Unsaved: General, Resources, Scaling, Access. Open each page to save or discard.');
  assert.equal(pending.hidden, false);
  assert.equal(link('scaling').hasAttribute('data-dirty'), true);
  assert.equal(link('access').getAttribute('aria-label'), 'Access (unsaved)');
  assert.equal(panel.getAttribute('aria-label'), 'Resources settings', 'dirty-link updates must not overwrite the mounted region label');
  assert.equal(panel.hasAttribute('data-dirty'), false, 'only changed sections and contextual links carry dirty markers');
  for (const id of ['configuration-pacing', 'autoscale-options', 'configuration-hibernation', 'access-visibility', 'danger-zone']) updateConfigurationDirty(doc, id, false);
  assert.equal(pending.hidden, true);
  assert.equal(pending.textContent, '');
  assert.equal(link('resources').getAttribute('aria-label'), 'Resources');
});

test('saved runtime behavior stays with the Scaling controls', () => {
  const doc = new JSDOM(html).window.document;
  assert.equal(doc.getElementById('configuration-behavior').closest('.settings-block').id, 'configuration-hibernation');
  const view = doc.getElementById('app-detail-view');
  revealConfigurationSection(view, 'scaling');
  assert.equal(doc.getElementById('configuration-hibernation').hasAttribute('data-current-section'), true);
  revealConfigurationSection(view, 'general');
  assert.equal(doc.getElementById('configuration-hibernation').hasAttribute('data-current-section'), false);
});
