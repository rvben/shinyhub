import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  dstAdvisoryText,
  dstAdvisoryMarkup,
  DST_ADVISORY_LABEL,
  afterSuccessLabel,
  afterSuccessDetail,
  activationState,
  activationErrorDetail,
  canCancelActivation,
  deletedScheduleActivations,
  formatAge,
  formatDuration,
  runTriggerLabel,
  scheduleTimingLabel,
  runDurationSeconds,
  deployTriggerLabel,
  producerCompatibilityState,
  scheduleDetailSignature,
  scheduleState,
  scheduleSummary,
  createScheduleRequestGate,
} from '../static/views/schedule-ui.js';

test('dstAdvisoryText: returns the advisory string when present', () => {
  assert.equal(
    dstAdvisoryText({ dst_advisory: 'Schedule fires twice on 2025-10-26.' }),
    'Schedule fires twice on 2025-10-26.',
  );
});

test('dstAdvisoryText: null when missing / empty / blank / non-object', () => {
  assert.equal(dstAdvisoryText({}), null);
  assert.equal(dstAdvisoryText({ dst_advisory: '' }), null);
  assert.equal(dstAdvisoryText({ dst_advisory: '   ' }), null);
  assert.equal(dstAdvisoryText({ dst_advisory: null }), null);
  assert.equal(dstAdvisoryText(null), null);
  assert.equal(dstAdvisoryText(undefined), null);
});

test('dstAdvisoryMarkup: empty string when no advisory', () => {
  assert.equal(dstAdvisoryMarkup({}), '');
  assert.equal(dstAdvisoryMarkup({ dst_advisory: '' }), '');
  assert.equal(dstAdvisoryMarkup(null), '');
});

test('dstAdvisoryMarkup: visible badge carries the full advisory in title + aria-label', () => {
  const html = dstAdvisoryMarkup({ dst_advisory: 'Schedule fires twice on 2025-10-26.' });
  assert.match(html, /dst-advisory/);
  assert.ok(html.includes(DST_ADVISORY_LABEL));
  assert.match(html, /title="Schedule fires twice on 2025-10-26\."/);
  assert.match(html, /aria-label="Schedule fires twice on 2025-10-26\."/);
});

test('dstAdvisoryMarkup: escapes HTML metacharacters in the advisory text', () => {
  const html = dstAdvisoryMarkup({ dst_advisory: 'fires <b>twice</b> & "again"' });
  assert.doesNotMatch(html, /<b>twice<\/b>/);
  assert.match(html, /&lt;b&gt;twice&lt;\/b&gt;/);
  assert.match(html, /&amp;/);
  assert.match(html, /&quot;again&quot;/);
});

test('scheduleState prioritises live work, disabled state, and freshness exceptions', () => {
  assert.deepEqual(scheduleState({ enabled: false, last_run_status: 'failed' }), {
    key: 'paused', label: 'Paused', tone: 'quiet', attention: false,
  });
  assert.equal(scheduleState({ enabled: true, active_run_id: 14, stale: true }).key, 'running');
  assert.equal(scheduleState({ enabled: false, active_run_id: 14 }).key, 'running');
  assert.equal(scheduleState({ enabled: false, refreshing: true }).key, 'running');
  assert.equal(scheduleState({ enabled: false }).key, 'paused');
  assert.equal(scheduleState({ enabled: true, retired: true, active_run_id: 14 }).key, 'retired');
  assert.equal(scheduleState({ enabled: true, stale: true }).key, 'stale');
  assert.equal(scheduleState({ enabled: true, last_run_status: 'failed' }).attention, true);
  assert.equal(scheduleState({ enabled: true, last_run_status: 'skipped_overlap' }).label, 'Skipped');
  assert.equal(scheduleState({ enabled: true }).label, 'Never run');
});

test('run trigger labels match the backend scheduler vocabulary', () => {
  assert.equal(runTriggerLabel('schedule'), 'Scheduled');
  assert.equal(runTriggerLabel('cron'), 'Scheduled');
  assert.equal(runTriggerLabel('manual'), 'Manual');
  assert.equal(runTriggerLabel('deploy'), 'Deployment');
  assert.equal(runTriggerLabel('register'), 'On registration');
  assert.equal(runTriggerLabel('missed'), 'Missed run');
  assert.equal(runTriggerLabel('unexpected'), 'Unknown');
});

test('simple schedule timing labels describe only supported cron patterns', () => {
  for (const [cron, label] of [
    ['* * * * *', 'Every minute'],
    ['*/1 * * * *', 'Every minute'],
    ['*/15 * * * *', 'Every 15 minutes'],
    ['0 5 * * *', 'Daily at 05:00'],
    ['59 23 * * *', 'Daily at 23:59'],
    ['5 9 * * 0', 'Every Sunday at 09:05'],
    ['30 12 * * 6', 'Every Saturday at 12:30'],
    [' 0\t5 * * * ', 'Daily at 05:00'],
  ]) assert.equal(scheduleTimingLabel(cron), label, cron);
});

test('unsupported or invalid timing expressions retain the exact original cron', () => {
  for (const cron of [
    '0 5 * * 1-5', '0 5 1 * *', '0 5 * 1 *', '0,30 * * * *',
    '@daily', '0 0 5 * * *', '*/0 * * * *', '*/59 * * * *', '*/7 * * * *', '*/60 * * * *',
    '60 5 * * *', '0 24 * * *', '0 5 * * 7', '0 5 * * MON',
    ' 0 5 * * 1-5 ', '',
  ]) assert.equal(scheduleTimingLabel(cron), cron);
});

test('detail signature ignores age-only polling changes but tracks real outcomes', () => {
  const base = {
    id: 7,
    name: 'refresh-data',
    cron_expr: '*/15 * * * *',
    command: ['python', 'fetch.py'],
    enabled: true,
    last_run_id: 41,
    last_run_status: 'succeeded',
    last_success_age_s: 30,
  };
  assert.equal(
    scheduleDetailSignature(base),
    scheduleDetailSignature({...base, last_success_age_s: 60}),
  );
  assert.notEqual(
    scheduleDetailSignature(base),
    scheduleDetailSignature({...base, last_run_id: 42, last_run_status: 'failed'}),
  );
  assert.notEqual(
    scheduleDetailSignature(base),
    scheduleDetailSignature({...base, deploy_trigger: 'bundle_change', deploy_trigger_satisfied: false}),
  );
});

test('deploy policy and producer compatibility use plain, fail-closed language', () => {
  assert.equal(deployTriggerLabel('never'), 'Only on its cron');
  assert.equal(deployTriggerLabel('first_deploy'), 'On first deployment');
  assert.equal(deployTriggerLabel('bundle_change'), 'On every bundle change');

  assert.deepEqual(producerCompatibilityState({deploy_trigger: 'never'}), {
    key: 'not-tracked', label: 'Not tracked', tone: 'quiet', attention: false,
    detail: 'This schedule does not promise that its data matches the deployed bundle.',
  });
  assert.equal(producerCompatibilityState({
    deploy_trigger: 'bundle_change', deploy_trigger_satisfied: true,
  }).label, 'Matches current code');
  assert.equal(producerCompatibilityState({
    deploy_trigger: 'bundle_change', deploy_trigger_satisfied: false, convergence_status: 'running',
  }).label, 'Building for current code');
  assert.equal(producerCompatibilityState({
    deploy_trigger: 'bundle_change', producer_repair_required: true,
  }).label, 'Repair required');
  assert.equal(producerCompatibilityState({
    deploy_trigger: 'bundle_change', deploy_trigger_satisfied: false,
  }).attention, true);
});

test('scheduleSummary counts operational state and selects the earliest enabled fire', () => {
  const summary = scheduleSummary([
    { enabled: true, refreshing: true, next_fire: '2026-08-25T13:00:00Z' },
    { enabled: true, stale: true, next_fire: '2026-08-25T12:00:00Z' },
    { enabled: false, stale: true, next_fire: '2026-08-25T11:00:00Z' },
  ]);
  assert.equal(summary.total, 3);
  assert.equal(summary.enabled, 2);
  assert.equal(summary.running, 1);
  assert.equal(summary.activeCount, 1);
  assert.equal(summary.attention, 1);
  assert.equal(summary.compatible, 0);
  assert.equal(summary.nextFire.toISOString(), '2026-08-25T12:00:00.000Z');
});

test('schedule summary counts each active schedule once and excludes retired timers', () => {
  const summary = scheduleSummary([
    { enabled: true, active_run_id: 14, latest_activation: { status: 'pending' }, next_fire: '2026-08-25T13:00:00Z' },
    { enabled: false, active_run_id: 15, next_fire: '2026-08-25T11:00:00Z' },
    { enabled: true, retired: true, next_fire: '2026-08-25T10:00:00Z' },
  ]);
  assert.equal(summary.total, 3);
  assert.equal(summary.enabled, 1);
  assert.equal(summary.running, 2);
  assert.equal(summary.activating, 1);
  assert.equal(summary.activeCount, 2);
  assert.equal(summary.nextFire.toISOString(), '2026-08-25T13:00:00.000Z');
});

test('schedule request gates isolate list and history work and invalidate superseded requests', () => {
  const list = createScheduleRequestGate();
  const history = createScheduleRequestGate();
  const firstList = list.begin();
  const firstHistory = history.begin();
  const latestList = list.begin();
  assert.equal(firstList.signal.aborted, true);
  assert.equal(list.isCurrent(firstList), false);
  assert.equal(list.isCurrent(latestList), true);
  assert.equal(firstHistory.signal.aborted, false);
  assert.equal(history.isCurrent(firstHistory), true);
  history.invalidate();
  assert.equal(firstHistory.signal.aborted, true);
  assert.equal(history.isCurrent(firstHistory), false);
  assert.equal(list.isCurrent(latestList), true);
  list.invalidate();
  assert.equal(latestList.signal.aborted, true);
  assert.equal(list.isCurrent(latestList), false);
  const remounted = list.begin();
  assert.equal(list.isCurrent(remounted), true);
  assert.equal(remounted.signal.aborted, false);
  list.invalidate();
});

test('schedule copy remains honest about post-success behavior', () => {
  assert.equal(afterSuccessLabel({}), 'No future app action');
  assert.equal(afterSuccessLabel({ on_success: 'roll' }), 'Roll app');
  assert.equal(afterSuccessLabel({ on_success: 'signal' }), 'Unsupported action');
  assert.match(afterSuccessDetail({ on_success: 'roll', min_roll_interval_seconds: 3600 }), /at most once every 1h/);
  assert.match(afterSuccessDetail({ on_success: 'roll' }), /temporary surge replica/);
  assert.match(afterSuccessDetail({ on_success: 'roll', roll_fallback: 'restart' }), /restarts the app in place/);
  assert.match(afterSuccessDetail({ on_success: 'roll', max_defer_age_seconds: 21600 }), /fails after 6h/);
});

test('activation state remains distinct from scheduled job state', () => {
  assert.deepEqual(activationState({ on_success: 'none' }), {
    key: 'none', label: 'Not configured', tone: 'quiet', active: false, attention: false,
  });
  assert.equal(activationState({ on_success: 'roll', latest_activation: {status: 'pending'} }).label, 'Queued');
  assert.equal(activationState({ on_success: 'roll', latest_activation: {status: 'deferred_capacity'} }).label, 'Waiting for capacity');
  assert.equal(activationState({ on_success: 'roll', latest_activation: {status: 'repairing', phase: 'starting_slot'} }).label, 'Repairing · starting replica');
  assert.equal(activationState({ on_success: 'none', latest_activation: {status: 'repairing', phase: 'starting_slot'} }).label, 'Repairing · starting replica');
  assert.equal(activationState({ on_success: 'roll', latest_activation: {status: 'deferred_capacity'} }).attention, true);
  assert.equal(activationState({ on_success: 'roll', latest_activation: {status: 'running', phase: 'draining_slot'} }).label, 'Rolling · draining replica');
  assert.equal(activationState({ on_success: 'roll', latest_activation: {status: 'succeeded'} }).label, 'Activated');
  assert.deepEqual(activationState({ on_success: 'roll', latest_activation: {status: 'cancelled'} }), {
    key: 'cancelled', label: 'Activation cancelled', tone: 'warning', active: false, attention: true,
  });
  assert.equal(activationState({ on_success: 'roll', latest_activation: {status: 'failed'} }).attention, true);
});

test('canCancelActivation permits only queued, nondestructive lifecycle states', () => {
  for (const status of ['pending', 'deferred_interval', 'deferred_capacity']) {
    assert.equal(canCancelActivation({latest_activation: {status}}), true, status);
  }
  for (const status of ['running', 'repairing', 'succeeded', 'failed', 'cancelled']) {
    assert.equal(canCancelActivation({latest_activation: {status}}), false, status);
  }
  assert.equal(canCancelActivation({}), false);
});

test('historical activation errors remain available for safe visible rendering', () => {
  assert.equal(activationErrorDetail({activation_error: '  failed <script>alert(1)</script>  '}), 'failed <script>alert(1)</script>');
  assert.equal(activationErrorDetail({activation_error: '   '}), '');
  assert.equal(activationErrorDetail({}), '');
});

test('deleted schedule activations remain distinct from configured schedule rows', () => {
  const activations = [
    {schedule_id: 7, schedule_name: 'still-here'},
    {schedule_id: 8, schedule_name: 'deleted'},
    {schedule_id: null, schedule_name: 'legacy-deleted'},
  ];
  assert.deepEqual(
    deletedScheduleActivations(activations, [{id: 7}]).map(row => row.schedule_name),
    ['deleted', 'legacy-deleted'],
  );
});

test('age and duration formatting cover operator-scale intervals', () => {
  assert.equal(formatAge(20), 'just now');
  assert.equal(formatAge(3700), '1h ago');
  assert.equal(runDurationSeconds({
    started_at: '2026-08-25T12:00:00Z', finished_at: '2026-08-25T12:02:05Z',
  }), 125);
  assert.equal(formatDuration(125), '2m 5s');
  assert.equal(formatDuration(null), '—');
});

test('retired schedules retain history without offering producer repair', () => {
  const retired = { enabled: false, retired: true, deploy_trigger: 'bundle_change', last_run_status: 'failed' };
  assert.deepEqual(scheduleState(retired), { key: 'retired', label: 'Retired', tone: 'quiet', attention: false });
  const compatibility = producerCompatibilityState(retired);
  assert.equal(compatibility.attention, false);
  assert.equal(compatibility.key, 'retired');
  assert.match(compatibility.detail, /history is retained/);
  assert.notEqual(scheduleDetailSignature(retired), scheduleDetailSignature({ ...retired, retired: false }));
});
