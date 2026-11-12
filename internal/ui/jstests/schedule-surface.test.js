import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import * as scheduleHelpers from '../static/views/schedule-ui.js';
import { escapeHtml } from '../static/views/html-escape.js';

const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
const start = source.indexOf('  let mountedScheduleSurface = null;');
const end = source.indexOf('  // Load and render the shared-data mounts list.', start);
assert.ok(start >= 0 && end > start, 'Schedule controller extraction must match shipped source');
const controller = source.slice(start, end);

function deferred() {
  let resolve, reject;
  const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

const response = (body, status = 200) => ({ ok: status >= 200 && status < 300, status, json: async () => body });
async function settle() { for (let index = 0; index < 20; index += 1) await Promise.resolve(); }
const schedule = (overrides = {}) => ({
  id: 7, app_id: 1, name: 'Refresh data', cron_expr: '0 5 * * *', command: ['python', 'refresh.py'],
  enabled: true, effective_timezone: 'Europe/Amsterdam', timeout_seconds: 3600,
  overlap_policy: 'skip', missed_policy: 'skip', on_success: 'none', deploy_trigger: 'never',
  next_fire: '2026-11-04T04:00:00Z', last_run_status: 'succeeded', ...overrides,
});
const run = (overrides = {}) => ({
  id: 41, schedule_id: 7, status: 'succeeded', trigger: 'deploy', exit_code: 0,
  started_at: '2026-11-03T04:00:00Z', finished_at: '2026-11-03T04:01:00Z', ...overrides,
});

// Exercise the real list, detail, history and lifecycle functions together.
// Only unrelated mutation handlers, menu wiring and wall-clock timers are
// replaced; no request, rendering or ownership logic is recreated here.
function harness(t, api, overrides = {}) {
  const dom = new JSDOM(html, { pretendToBeVisual: true });
  const doc = dom.window.document;
  doc.getElementById('app-detail-view').hidden = false;
  doc.getElementById('detail-schedules-panel').hidden = false;
  const outside = doc.createElement('button');
  outside.textContent = 'Outside schedules';
  doc.body.append(outside);
  const calls = [], timers = new Map(), navigations = [], toasts = [];
  let timerID = 0, unauthorized = 0;
  const scope = {
    ...scheduleHelpers,
    escapeHtml,
    document: doc,
    api: (path, options) => { calls.push({ path, options }); return api(path, options); },
    setTimeout: (callback, delay) => { const id = ++timerID; timers.set(id, { callback, delay }); return id; },
    clearTimeout: id => timers.delete(id),
    handleUnauthorized: async () => { unauthorized += 1; },
    router: { navigate: (...args) => { navigations.push(args); } },
    flashToast: (...args) => toasts.push(args),
    kebabControls: new WeakMap(), wireKebab: () => ({ close() {} }),
    runScheduleNow() {}, deleteSchedule() {}, openScheduleForm() {}, cancelScheduleActivation() {},
    retryScheduleConvergence() {}, openScheduleRunLogs() {}, cancelScheduleRun() {},
    ...overrides,
  };
  const controls = new Function(...Object.keys(scope), `
    ${controller}
    return {load: loadSchedules, stop: stopScheduleSurface,
      refresh: () => refreshScheduleSurface(mountedScheduleSurface, {announce:true}),
      poll: () => refreshScheduleSurface(mountedScheduleSurface),
      get surface() {return mountedScheduleSurface;}};
  `)(...Object.values(scope));
  t.after(() => { controls.stop(); dom.window.close(); });
  return { ...controls, get surface() { return controls.surface; }, doc, outside, calls, timers, navigations, toasts,
    get unauthorized() { return unauthorized; },
    mount(slug = 'forecast', options = {}) { return controls.load(slug, { canManage: true, canReadLogs: true, canCancel: true, ...options }); },
  };
}

test('schedule mount returns cleanup immediately and aborts pending work on navigation', async t => {
  const pending = deferred();
  const h = harness(t, () => pending.promise);
  const cleanup = h.mount();
  assert.equal(typeof cleanup, 'function');
  cleanup();
  h.doc.getElementById('schedules-list').textContent = 'Replacement view';
  assert.equal(h.calls[0].options.signal.aborted, true);
  pending.resolve(response([schedule()]));
  await settle();
  assert.equal(h.doc.getElementById('schedules-list').textContent, 'Replacement view');
  assert.equal(h.timers.size, 0);
  assert.equal(h.load('forecast'), undefined, 'A late mutation refresh must not remount an unmounted app');
});

for (const phase of ['fetch', 'JSON']) test(`a stale schedule ${phase} completion cannot replace the latest list`, async t => {
  const pending = deferred();
  let calls = 0;
  const h = harness(t, async () => {
    if (++calls === 1) return phase === 'fetch' ? pending.promise : {ok:true, status:200, json:() => pending.promise};
    return response([schedule({name:'Latest result'})]);
  });
  h.mount();
  await settle();
  await h.refresh();
  assert.equal(h.calls[0].options.signal.aborted, true);
  pending.resolve(phase === 'fetch' ? response([schedule({name:'Stale result'})]) : [schedule({name:'Stale result'})]);
  await settle();
  assert.equal(h.doc.querySelector('.schedule-name-link').textContent, 'Latest result');
  assert.equal(h.doc.getElementById('schedules-error').hidden, true);
});

test('refresh failures retain the previous schedule report and retry restores live status', async t => {
  let calls = 0;
  const h = harness(t, async () => {
    calls += 1;
    if (calls === 2) throw new Error('Offline');
    return response([schedule({name:calls === 1 ? 'Previous report' : 'Recovered report'})]);
  });
  h.mount();
  await settle();
  await h.refresh();
  assert.equal(h.doc.querySelector('.schedule-name-link').textContent, 'Previous report');
  assert.equal(h.doc.getElementById('schedules-summary-total').textContent, '1');
  const error = h.doc.getElementById('schedules-error');
  assert.equal(error.hidden, false);
  assert.match(error.textContent, /previous status is still shown/i);
  error.querySelector('button').click();
  await settle();
  assert.equal(h.doc.querySelector('.schedule-name-link').textContent, 'Recovered report');
  assert.equal(error.hidden, true);
});

test('invalid schedule JSON reports a recoverable error instead of claiming an empty list', async t => {
  const h = harness(t, async () => response({unexpected:true}));
  h.mount();
  await settle();
  assert.equal(h.doc.getElementById('schedules-error').hidden, false);
  assert.equal(h.doc.querySelector('.schedules-empty'), null);
  assert.equal(h.doc.getElementById('schedules-list').getAttribute('aria-busy'), 'false');
  assert.equal(h.doc.getElementById('schedules-refresh-btn').disabled, false);
});

test('old schedule history cannot paint a replacement app or invoke stale authorization handling', async t => {
  const oldHistory = deferred();
  const h = harness(t, async path => {
    if (path.includes('/old/')) return path.includes('/runs?') ? oldHistory.promise : response([schedule()]);
    return path.includes('/runs?') ? response({items:[run({id:99, schedule_id:8, trigger:'manual'})], total:1}) : response([schedule({id:8, name:'New schedule'})]);
  });
  h.mount('old', {selectedScheduleID:7});
  await settle();
  const oldRequest = h.calls.find(call => call.path.includes('/old/') && call.path.includes('/runs?'));
  assert.ok(oldRequest);
  h.mount('new', {selectedScheduleID:8});
  await settle();
  assert.equal(oldRequest.options.signal.aborted, true);
  oldHistory.resolve(response({}, 401));
  await settle();
  assert.equal(h.doc.getElementById('schedule-detail-title').textContent, 'New schedule');
  assert.ok(h.doc.querySelector('[data-run-id="99"]'));
  assert.equal(h.unauthorized, 0);
});

test('a stale history JSON body cannot overwrite the current selected schedule', async t => {
  const oldBody = deferred();
  let historyCalls = 0;
  const h = harness(t, async path => path.includes('/runs?')
    ? (++historyCalls === 1 ? {ok:true, status:200, json:() => oldBody.promise} : response({items:[run({id:99})], total:1}))
    : response([schedule()]));
  h.mount('forecast', {selectedScheduleID:7});
  await settle();
  await h.refresh();
  oldBody.resolve({items:[run({id:41})], total:1});
  await settle();
  assert.ok(h.doc.querySelector('[data-run-id="99"]'));
  assert.equal(h.doc.querySelector('[data-run-id="41"]'), null);
});

test('schedule refresh restores retained row focus without stealing outside focus', async t => {
  let pending = null;
  const h = harness(t, async () => pending ? pending.promise : response([schedule()]));
  h.mount();
  await settle();
  h.doc.querySelector('.schedule-name-link').focus();
  await h.refresh();
  assert.equal(h.doc.activeElement, h.doc.querySelector('.schedule-name-link'));
  pending = deferred();
  const refreshing = h.refresh();
  h.outside.focus();
  pending.resolve(response([schedule()]));
  await refreshing;
  assert.equal(h.doc.activeElement, h.outside);
});

test('metadata readers can inspect run outcomes without manager actions or logs', async t => {
  const h = harness(t, async path => path.includes('/runs?')
    ? response({items:[run({status:'running', finished_at:null})], total:1}) : response([schedule()]));
  h.mount('forecast', {selectedScheduleID:7, canManage:false, canReadLogs:false, canCancel:false});
  await settle();
  assert.equal(h.doc.getElementById('schedules-add-btn').hidden, true);
  assert.equal(h.doc.querySelector('[data-action], [data-detail-action], [data-run-action]'), null);
  assert.match(h.doc.querySelector('[data-schedule-history]').textContent, /Running/);
  assert.match(h.doc.querySelector('[data-schedule-history]').textContent, /Deployment/);
});

test('active summary counts schedules once and paused live jobs retain rapid polling', async t => {
  const h = harness(t, async () => response([schedule({enabled:false, active_run_id:41, latest_activation:{status:'pending'}})]));
  h.mount();
  await settle();
  assert.equal(h.doc.getElementById('schedules-summary-active').textContent, '1');
  assert.equal(h.doc.getElementById('schedules-summary-enabled').textContent, '0');
  assert.match(h.doc.querySelector('[data-label="Job status"]').textContent, /Running/);
  assert.match(h.doc.querySelector('[data-label="Next run"]').textContent, /Paused/);
  assert.deepEqual([...h.timers.values()].map(timer => timer.delay), [5000]);
});

test('forbidden refresh removes selected schedule details and a later poll can recover', async t => {
  let forbidden = false;
  const h = harness(t, async path => forbidden ? response({}, 403)
    : path.includes('/runs?') ? response({items:[run()], total:1}) : response([schedule()]));
  h.mount('forecast', {selectedScheduleID:7});
  await settle();
  assert.ok(h.doc.querySelector('[data-schedule-history] table'));
  forbidden = true;
  await h.refresh();
  assert.equal(h.doc.querySelector('.schedule-name-link'), null);
  assert.equal(h.doc.getElementById('schedule-detail').textContent, '');
  assert.equal(h.doc.getElementById('schedule-detail').hidden, true);
  assert.equal(h.doc.getElementById('schedules-summary').hidden, true);
  forbidden = false;
  await h.poll();
  assert.ok(h.doc.getElementById('schedule-detail-title'));
  assert.ok(h.doc.querySelector('[data-schedule-history] table'));
});

test('history access revocation clears all schedule controls and explicit retry can recover', async t => {
  let forbidden = true;
  const h = harness(t, async path => path.includes('/runs?')
    ? forbidden ? response({}, 403) : response({items:[run()], total:1})
    : response([schedule()]));
  h.mount('forecast', {selectedScheduleID:7});
  await settle();
  assert.equal(h.doc.querySelector('.schedule-name-link'), null);
  assert.equal(h.doc.getElementById('schedule-detail').textContent, '');
  assert.equal(h.doc.getElementById('schedules-add-btn').hidden, true);
  assert.equal(h.timers.size, 0, 'Revoked access must not leave a background poll running');
  const error = h.doc.getElementById('schedules-error');
  assert.equal(error.hidden, false);
  assert.match(error.textContent, /access.*changed/i);
  forbidden = false;
  error.querySelector('button').click();
  await settle();
  assert.equal(error.hidden, true);
  assert.ok(h.doc.getElementById('schedule-detail-title'));
  assert.ok(h.doc.querySelector('[data-schedule-history] table'));
});

test('retired schedules preserve their timing definition without advertising manual runs', async t => {
  const h = harness(t, async () => response([schedule({retired:true, enabled:false, cron_expr:'*/59 * * * *'})]));
  h.mount();
  await settle();
  const row = h.doc.querySelector('#schedules-list tbody tr');
  assert.match(row.querySelector('[data-label="Schedule"]').textContent, /\*\/59 \* \* \* \*/);
  assert.match(row.querySelector('[data-label="Schedule"]').textContent, /Europe\/Amsterdam/);
  assert.doesNotMatch(row.querySelector('[data-label="Next run"]').textContent, /Manual runs available/);
  assert.equal(row.querySelector('[data-action="run"], [data-action="edit"]'), null);
  assert.ok(row.querySelector('[data-action="delete"]'));
});

for (const action of ['pagination', 'retry']) {
  for (const moveOutside of [false, true]) {
    test(`history ${action} ${moveOutside ? 'respects outside focus' : 'returns removed-button focus to history'}`, async t => {
      const pending = deferred();
      let historyCalls = 0;
      const firstPage = Array.from({length:10}, (_, index) => run({id:index + 1}));
      const h = harness(t, async path => {
        if (!path.includes('/runs?')) return response([schedule()]);
        if (++historyCalls === 1) return action === 'pagination'
          ? response({items:firstPage, total:11}) : response({}, 503);
        return pending.promise;
      });
      h.mount('forecast', {selectedScheduleID:7});
      await settle();
      const host = h.doc.querySelector('[data-schedule-history]');
      const button = host.querySelector(action === 'pagination' ? '[data-load-more-runs]' : '[data-run-history-retry]');
      assert.ok(button);
      if (action === 'pagination') {
        assert.equal(host.querySelectorAll('tbody tr').length, 10);
        assert.equal(button.textContent, 'Load 1 more');
      }
      button.focus();
      button.click();
      if (moveOutside) h.outside.focus();
      pending.resolve(response({items:[run({id:11})], total:action === 'pagination' ? 11 : 1}));
      await settle();
      assert.equal(button.isConnected, false);
      assert.equal(h.doc.activeElement, moveOutside ? h.outside : host);
      assert.equal(host.getAttribute('aria-busy'), 'false');
      assert.equal(host.querySelectorAll('tbody tr').length, action === 'pagination' ? 11 : 1);
      const historyPaths = h.calls.filter(call => call.path.includes('/runs?')).map(call => call.path);
      assert.deepEqual(historyPaths, [
        '/api/apps/forecast/schedules/7/runs?limit=10&offset=0',
        `/api/apps/forecast/schedules/7/runs?limit=10&offset=${action === 'pagination' ? 10 : 0}`,
      ]);
    });
  }
}

test('an unchanged poll keeps the rendered rows so an open actions menu survives it', async t => {
  // The success age advances on every real poll without changing anything the
  // row shows, so the fixture advances it too.
  let age = 600;
  let current = [schedule({last_success_age_s: age})];
  const edited = [];
  const h = harness(t, async () => response(current.map(item => ({...item, last_success_age_s: age += 30}))),
    {openScheduleForm: (slug, item) => edited.push(item)});
  h.mount();
  await settle();
  const row = h.doc.querySelector('#schedules-list tbody tr');
  const toggle = row.querySelector('.schedule-actions-menu > button');
  const menu = row.querySelector('.kebab-list');
  toggle.setAttribute('aria-expanded', 'true');
  menu.hidden = false;

  await h.poll();
  assert.equal(h.doc.querySelector('#schedules-list tbody tr'), row, 'identical data must not rebuild the row');
  assert.equal(toggle.isConnected && toggle.getAttribute('aria-expanded'), 'true');
  assert.equal(menu.isConnected && menu.hidden, false);
  row.querySelector('[data-action="edit"]').click();
  assert.equal(edited.length, 1);
  assert.equal(edited[0].last_success_age_s, age, 'a surviving row must edit the schedule as last reported');

  current = [schedule({name: 'Renamed job'})];
  await h.poll();
  assert.notEqual(h.doc.querySelector('#schedules-list tbody tr'), row, 'changed data must rebuild the row');
  assert.equal(h.doc.querySelector('.schedule-name-link').textContent, 'Renamed job');
});

test('a list replaced from outside is rendered again even when the data is unchanged', async t => {
  const h = harness(t, async () => response([schedule()]));
  h.mount();
  await settle();
  h.doc.getElementById('schedules-list').innerHTML = '<p>Replaced</p>';
  await h.poll();
  assert.equal(h.doc.querySelector('.schedule-name-link').textContent, 'Refresh data');
});
