import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { escapeHtml } from '../static/views/html-escape.js';

const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
function section(startMarker, endMarker) {
  const start = source.indexOf(startMarker);
  const end = source.indexOf(endMarker, start);
  assert.ok(start >= 0 && end > start, `Must extract actual ${startMarker.trim()} source`);
  return source.slice(start, end);
}
const functions = [
  section('  function updateCronPreview(', '  const scheduleSurface = createScheduleSurface({'),
  section('  function openScheduleForm(', '  async function runScheduleNow('),
].join('\n');
const existing = {
  id:7, name:'nightly-refresh', cron_expr:'0 5 * * *', command:['python', 'refresh.py', '--full'],
  timeout_seconds:900, overlap_policy:'queue', missed_policy:'run_once', enabled:false,
  timezone:'Europe/Amsterdam', deploy_trigger:'never', on_success:'roll',
  min_roll_interval_seconds:1800, roll_fallback:'restart', max_defer_age_seconds:7200,
};

async function settle() { for (let index = 0; index < 12; index += 1) await Promise.resolve(); }

// Run the shipped form lifecycle and submit handler against the real markup.
// Network and surrounding modal infrastructure are the only dependencies stubbed.
function harness(t) {
  const dom = new JSDOM(html);
  const doc = dom.window.document;
  const requests = [], refreshed = [];
  const scope = {
    document:doc,
    escapeHtml,
    api:async (path, options) => { requests.push({path, options}); return {ok:true, status:200}; },
    loadSchedules:async slug => refreshed.push(slug),
    modalTrap:() => ({activate() {}, release() {}}),
    setError:(element, message) => { element.textContent = message; element.hidden = !message; },
    handleUnauthorized:async () => {},
    errorMessage:async () => 'Unexpected server error',
  };
  const controls = new Function(...Object.keys(scope), `${functions}\nreturn {open:openScheduleForm, close:closeScheduleForm};`)(...Object.values(scope));
  t.after(() => dom.window.close());
  return { ...controls, doc, requests, refreshed,
    field:id => doc.getElementById(id),
    submit() { doc.getElementById('schedule-form').dispatchEvent(new dom.window.Event('submit', {bubbles:true, cancelable:true})); },
  };
}

function assertSavedPolicies(h) {
  for (const [id, value] of [
    ['sched-deploy-trigger', 'never'], ['sched-on-success', 'roll'],
    ['sched-overlap', 'queue'], ['sched-missed', 'run_once'], ['sched-roll-fallback', 'restart'],
    ['sched-timeout', '900'], ['sched-min-roll-interval', '1800'], ['sched-max-defer-age', '7200'],
    ['sched-timezone', 'Europe/Amsterdam'],
  ]) assert.equal(h.field(id).value, value, id);
  assert.equal(h.field('sched-enabled').checked, false);
  assert.equal(h.field('sched-roll-options').hidden, false);
  assert.equal(h.field('sched-min-roll-interval').disabled, false);
  assert.equal(h.doc.querySelector('.schedule-advanced').open, true);
  assert.match(h.field('schedule-consequence').textContent, /Deployments never run this job/);
  assert.match(h.field('schedule-consequence').textContent, /serving replicas roll/);
}

test('opening and reopening an existing schedule preserve every saved policy selection', t => {
  const h = harness(t);
  for (let attempt = 0; attempt < 2; attempt += 1) {
    h.open('forecast', existing);
    assertSavedPolicies(h);
    assert.equal(h.field('sched-name').value, 'nightly-refresh');
    assert.equal(h.field('sched-command').value, 'python\nrefresh.py\n--full');
    h.close();
  }
  assert.equal(h.requests.length, 0);
});

test('editing only cron submits the existing deployment, rollout and execution policies unchanged', async t => {
  const h = harness(t);
  h.open('forecast', existing);
  h.field('sched-cron').value = '30 6 * * *';
  h.field('sched-cron').dispatchEvent(new h.doc.defaultView.Event('input', {bubbles:true}));
  assert.match(h.field('cron-preview').textContent, /Europe\/Amsterdam/);
  h.submit();
  await settle();
  assert.equal(h.requests.length, 1);
  assert.equal(h.requests[0].path, '/api/apps/forecast/schedules/7');
  assert.equal(h.requests[0].options.method, 'PATCH');
  const {id, ...saved} = existing;
  assert.deepEqual(JSON.parse(h.requests[0].options.body), {...saved, cron_expr:'30 6 * * *'});
  assert.deepEqual(h.refreshed, ['forecast']);
  assert.equal(h.field('schedule-form-modal').hidden, true);
});

test('switching from edit to create resets advanced values and submits a new schedule', async t => {
  const h = harness(t);
  h.open('forecast', existing);
  assertSavedPolicies(h);
  h.close();
  h.open('another app', null);
  for (const id of ['sched-name', 'sched-command', 'sched-cron', 'sched-timezone']) assert.equal(h.field(id).value, '', id);
  for (const [id, value] of [
    ['sched-deploy-trigger', 'bundle_change'], ['sched-on-success', 'none'],
    ['sched-overlap', 'skip'], ['sched-missed', 'skip'], ['sched-roll-fallback', 'defer'],
    ['sched-timeout', '3600'], ['sched-min-roll-interval', '0'], ['sched-max-defer-age', '0'],
  ]) assert.equal(h.field(id).value, value, id);
  assert.equal(h.field('sched-enabled').checked, true);
  assert.equal(h.doc.querySelector('.schedule-advanced').open, false);
  assert.equal(h.field('sched-roll-options').hidden, true);
  assert.equal(h.field('sched-min-roll-interval').disabled, true);
  assert.match(h.field('schedule-consequence').textContent, /Every changed bundle/);
  assert.match(h.field('schedule-consequence').textContent, /do not reload serving replicas/);
  h.field('sched-name').value = 'new-job';
  h.field('sched-command').value = 'python\nnew.py';
  h.field('sched-cron').value = '0 8 * * *';
  h.submit();
  await settle();
  assert.equal(h.requests.length, 1, 'A replaced edit handler must not submit too');
  assert.equal(h.requests[0].path, '/api/apps/another%20app/schedules');
  assert.equal(h.requests[0].options.method, 'POST');
  assert.deepEqual(JSON.parse(h.requests[0].options.body), {
    name:'new-job', cron_expr:'0 8 * * *', command:['python', 'new.py'],
    timeout_seconds:3600, overlap_policy:'skip', missed_policy:'skip', enabled:true, timezone:'',
    deploy_trigger:'bundle_change', on_success:'none', min_roll_interval_seconds:0,
    roll_fallback:'defer', max_defer_age_seconds:0,
  });
});
