import { afterEach, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { activityTime, actorLabel, buildActivityBrief } from '../static/views/overview-activity.js';
import {
  activityLiveMessage,
  appendOverviewAnnouncement,
  renderActivityBrief,
  replaceOverviewContent,
} from '../static/views/overview.js';

let dom;

beforeEach(() => {
  dom = new JSDOM('<!doctype html><body></body>', { url: 'http://localhost/' });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
});

afterEach(() => {
  dom.window.close();
  delete globalThis.window;
  delete globalThis.document;
});

function event(id, action, resourceID, createdAt, extra = {}) {
  return {
    id,
    action,
    resource_type: 'app',
    resource_id: resourceID,
    username: '__deploy__',
    created_at: createdAt,
    ...extra,
  };
}

test('activity model groups a fleet run into one operation with chronological children', () => {
  const runID = '0123456789abcdef';
  const brief = buildActivityBrief([
    event(5, 'fleet_apply_finished', 'homelab-dashboards', '2026-08-22T14:35:00Z', {
      run_id: runID,
      resource_type: 'fleet',
    }),
    event(4, 'deploy', 'energy', '2026-08-22T14:34:00Z', { run_id: runID }),
    event(3, 'update_app', 'observability', '2026-08-22T14:33:00Z', { run_id: runID }),
    event(2, 'deploy', 'observability', '2026-08-22T14:32:00Z', { run_id: runID }),
    event(1, 'fleet_apply_started', 'homelab-dashboards', '2026-08-22T14:31:00Z', {
      run_id: runID,
      resource_type: 'fleet',
    }),
  ], 4, null, true);

  assert.equal(brief.operations.length, 1);
  assert.equal(brief.grouped, true);
  const operation = brief.operations[0];
  assert.equal(operation.kind, 'group');
  assert.equal(operation.actionLabel, 'Fleet apply');
  assert.equal(operation.target, 'homelab-dashboards');
  assert.deepEqual(operation.children.map((child) => child.actionLabel), ['Deployed', 'Updated', 'Deployed']);
  assert.deepEqual(operation.children.map((child) => child.target), ['observability', 'observability', 'energy']);
  assert.equal(operation.tone.label, 'Completed');
  assert.equal(operation.tone.name, 'neutral');
  assert.equal(operation.auditHref, `/audit-log?run=${runID}`);
  assert.equal(operation.createdAt, '2026-08-22T14:35:00Z');
  assert.equal(operation.truncated, false);
});

test('activity model translates reserved actors, creates app destinations, and ignores malformed rows', () => {
  const brief = buildActivityBrief([
    null,
    'bad row',
    event(1, 'restart', 'observability', '2026-08-22T14:42:00Z'),
  ]);

  assert.equal(brief.sourceCount, 1);
  assert.equal(brief.operations[0].actorLabel, 'Deployment automation');
  assert.equal(brief.operations[0].targetHref, '/apps/observability/overview');
  assert.equal(brief.operations[0].auditHref, '/audit-log?event=1');
  assert.equal(actorLabel(null), 'System');
  assert.equal(actorLabel('alice'), 'alice');
});

test('a lifecycle-only run links to its audit run without offering an empty disclosure', () => {
  const runID = 'run-without-changes';
  const brief = buildActivityBrief([
    event(1, 'fleet_apply_started', 'fleet', '2026-08-22T14:31:00Z', {
      run_id: runID,
      resource_type: 'fleet',
    }),
  ]);

  assert.equal(brief.grouped, false);
  assert.equal(brief.operations[0].kind, 'event');
  assert.equal(brief.operations[0].auditHref, `/audit-log?run=${runID}`);

  const node = renderActivityBrief({ state: 'ready', updatedAt: new Date().toISOString(), events: [
    event(1, 'fleet_apply_started', 'fleet', '2026-08-22T14:31:00Z', {
      run_id: runID,
      resource_type: 'fleet',
    }),
  ] });
  assert.equal(node.querySelector('.ov-activity-disclosure'), null);
  assert.equal(node.querySelector('.ov-activity-action-link').getAttribute('aria-label'), 'Open audit run for Fleet apply');
  assert.ok(node.querySelector('.ov-activity-action-link .ov-activity-link-icon--history'));
  assert.equal(node.querySelector('.ov-activity-target--static .ov-activity-link-icon'), null);
});

test('a truncated mutation-only run synthesizes a fleet parent and counts every visible change', () => {
  const runID = 'truncated-run';
  const brief = buildActivityBrief([
    event(2, 'deploy', 'energy', '2026-08-22T14:34:00Z', { run_id: runID }),
    event(1, 'update_app', 'observability', '2026-08-22T14:33:00Z', { run_id: runID }),
  ], 4, null, true);

  const operation = brief.operations[0];
  assert.equal(operation.actionLabel, 'Fleet apply');
  assert.equal(operation.kind, 'group');
  assert.equal(operation.children.length, 2);
  assert.equal(operation.truncated, true);
  assert.equal(operation.auditHref, `/audit-log?run=${runID}`);

  const node = renderActivityBrief({
    state: 'ready', hasMore: true, updatedAt: new Date().toISOString(), events: [
      event(2, 'deploy', 'energy', '2026-08-22T14:34:00Z', { run_id: runID }),
      event(1, 'update_app', 'observability', '2026-08-22T14:33:00Z', { run_id: runID }),
    ],
  });
  assert.match(node.querySelector('.ov-activity-meta').textContent, /2 recent app changes/);
  assert.match(node.querySelector('.ov-activity-disclosure').textContent, /Show 2 recent changes/);
});

test('activity destinations follow the action and never link deleted or unavailable apps', () => {
  const available = new Set(['observability']);
  const cases = [
    ['deploy', 'deployments'],
    ['rollback', 'deployments'],
    ['restart', 'overview'],
    ['env.set', 'configuration'],
    ['data.push', 'data'],
    ['set_access', 'access'],
    ['schedule_activation_roll', 'schedules'],
  ];
  for (const [index, [action, tab]] of cases.entries()) {
    const brief = buildActivityBrief([
      event(index + 1, action, 'observability', '2026-08-22T14:42:00Z'),
    ], 4, available);
    assert.equal(brief.operations[0].targetHref, `/apps/observability/${tab}`);
  }

  const deleted = buildActivityBrief([
    event(20, 'delete_app', 'observability', '2026-08-22T14:42:00Z'),
  ], 4, available);
  assert.equal(deleted.operations[0].targetHref, '');
  assert.equal(deleted.operations[0].auditHref, '/audit-log?event=20');

  const missing = buildActivityBrief([
    event(21, 'deploy', 'missing', '2026-08-22T14:42:00Z'),
  ], 4, available);
  assert.equal(missing.operations[0].targetHref, '');

  const scheduleResource = buildActivityBrief([{
    ...event(22, 'schedule_update', 'nightly', '2026-08-22T14:42:00Z'),
    resource_type: 'schedule',
  }], 4, available);
  assert.equal(scheduleResource.operations[0].targetHref, '');
  assert.equal(scheduleResource.operations[0].auditHref, '/audit-log?event=22');
});

test('activity time exposes exact semantic time and an honest invalid fallback', () => {
  const valid = activityTime('2026-08-22T14:42:00Z', Date.parse('2026-08-24T14:42:00Z'));
  assert.equal(valid.valid, true);
  assert.equal(valid.datetime, '2026-08-22T14:42:00.000Z');
  assert.equal(valid.relative, '2d ago');
  assert.ok(valid.exact);
  assert.ok(valid.title);

  assert.deepEqual(activityTime('not-a-time'), {
    valid: false,
    datetime: '',
    exact: 'Unknown time',
    relative: '',
  });
});

test('activity renderer names the region and provides real audit and app links', () => {
  const node = renderActivityBrief({
    state: 'ready',
    updatedAt: new Date().toISOString(),
    events: [event(1, 'restart', 'observability', '2026-08-22T14:42:00Z')],
  });
  document.body.appendChild(node);

  assert.equal(node.getAttribute('aria-labelledby'), 'ov-activity-title');
  assert.equal(node.querySelector('#ov-activity-title').textContent, 'Recent changes');
  assert.equal(node.querySelector('.ov-activity-audit').getAttribute('href'), '/audit-log');
  const actionLink = node.querySelector('.ov-activity-action-link');
  assert.equal(actionLink.getAttribute('href'), '/audit-log?event=1');
  assert.equal(actionLink.getAttribute('aria-label'), 'Open audit event for Restarted');
  assert.ok(actionLink.querySelector('.ov-activity-link-icon--history'));
  const targetLink = node.querySelector('.ov-activity-target');
  assert.equal(targetLink.getAttribute('href'), '/apps/observability/overview');
  assert.equal(targetLink.getAttribute('aria-label'), 'Open Overview for the observability app');
  assert.ok(targetLink.querySelector('.ov-activity-link-icon--destination'));
  assert.equal(node.querySelector('.ov-activity-item').getAttribute('role'), null);
  assert.equal(node.querySelector('.ov-activity-item').getAttribute('tabindex'), null);
  assert.equal(node.querySelector('a a, a button, button a, button button'), null);
  assert.match(node.textContent, /Deployment automation/);
  const time = node.querySelector('time');
  assert.equal(time.getAttribute('datetime'), '2026-08-22T14:42:00.000Z');
  assert.match(time.getAttribute('aria-label'), /2d ago|d ago/);
});

test('group disclosure is operable and survives an Overview poll replacement', () => {
  const events = [
    event(2, 'deploy', 'energy', '2026-08-22T14:34:00Z', { run_id: 'run-1' }),
    event(1, 'fleet_apply_started', 'fleet', '2026-08-22T14:31:00Z', {
      run_id: 'run-1', resource_type: 'fleet',
    }),
  ];
  const body = document.createElement('div');
  document.body.appendChild(body);
  replaceOverviewContent(body, renderActivityBrief({ state: 'ready', updatedAt: new Date().toISOString(), events }));
  const toggle = body.querySelector('.ov-activity-disclosure');
  assert.equal(toggle.textContent.trim(), 'Show 1 change');
  assert.equal(toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(body.querySelector('.ov-activity-children').hidden, true);
  toggle.click();
  toggle.focus();
  assert.equal(toggle.getAttribute('aria-expanded'), 'true');
  assert.equal(toggle.textContent.trim(), 'Hide 1 change');
  assert.equal(body.querySelector('.ov-activity-children').hidden, false);

  replaceOverviewContent(body, renderActivityBrief({ state: 'ready', updatedAt: new Date().toISOString(), events }));
  const restored = body.querySelector('.ov-activity-disclosure');
  assert.equal(restored.getAttribute('aria-expanded'), 'true');
  assert.equal(restored.textContent.trim(), 'Hide 1 change');
  assert.equal(body.querySelector('.ov-activity-children').hidden, false);
  assert.equal(document.activeElement, restored);
});

test('group children render independently actionable audit links and readable severity', () => {
  const events = [
    event(3, 'deploy_rejected_quota', 'energy', '2026-08-22T14:34:00Z', { run_id: 'run-1' }),
    event(2, 'deploy', 'observability', '2026-08-22T14:33:00Z', { run_id: 'run-1' }),
    event(1, 'fleet_apply_started', 'fleet', '2026-08-22T14:31:00Z', {
      run_id: 'run-1', resource_type: 'fleet',
    }),
  ];
  const node = renderActivityBrief({ state: 'ready', updatedAt: new Date().toISOString(), events });
  node.querySelector('.ov-activity-disclosure').click();

  const children = [...node.querySelectorAll('.ov-activity-child')];
  assert.equal(children.length, 2);
  assert.equal(children[0].querySelector('.ov-activity-action-link').getAttribute('href'), '/audit-log?event=2');
  assert.equal(children[0].querySelector('.ov-activity-status'), null);
  assert.equal(children[1].querySelector('.ov-activity-action-link').getAttribute('href'), '/audit-log?event=3');
  assert.match(children[1].querySelector('.ov-activity-status').textContent, /Attention/);
});

test('mixed fleet operations do not label every child as an app change', () => {
  const events = [
    event(3, 'deploy', 'energy', '2026-08-22T14:34:00Z', { run_id: 'run-1' }),
    event(2, 'set_access', 'operators', '2026-08-22T14:33:00Z', {
      run_id: 'run-1', resource_type: 'access',
    }),
    event(1, 'fleet_apply_started', 'fleet', '2026-08-22T14:31:00Z', {
      run_id: 'run-1', resource_type: 'fleet',
    }),
  ];
  const node = renderActivityBrief({ state: 'ready', updatedAt: new Date().toISOString(), events });

  assert.match(node.querySelector('.ov-activity-meta').textContent, /^2 changes/);
  assert.doesNotMatch(node.querySelector('.ov-activity-meta').textContent, /app changes/);
});

test('unavailable and stale states stay truthful and recoverable', () => {
  let retries = 0;
  const unavailable = renderActivityBrief({ state: 'unavailable', events: [], updatedAt: null }, () => { retries += 1; });
  assert.match(unavailable.textContent, /Activity unavailable/);
  assert.match(unavailable.textContent, /Fleet health is still current/);
  assert.doesNotMatch(unavailable.textContent, /No changes/);
  unavailable.querySelector('.ov-activity-retry').click();
  assert.equal(retries, 1);

  const stale = renderActivityBrief({
    state: 'stale',
    updatedAt: new Date().toISOString(),
    events: [event(1, 'deploy', 'energy', '2026-08-22T14:34:00Z')],
  }, () => { retries += 1; });
  assert.match(stale.textContent, /could not be refreshed/);
  assert.match(stale.textContent, /Deployedenergy/);
  stale.querySelector('.ov-activity-retry').click();
  assert.equal(retries, 2);
});

test('loading and empty states are distinct', () => {
  const loading = renderActivityBrief({ state: 'loading', events: [], updatedAt: null });
  assert.equal(loading.getAttribute('aria-busy'), 'true');
  assert.match(loading.querySelector('[role="status"]').getAttribute('aria-label'), /Loading recent changes/);

  const empty = renderActivityBrief({ state: 'ready', events: [], updatedAt: new Date().toISOString() });
  assert.match(empty.textContent, /No changes recorded yet/);
  assert.equal(empty.getAttribute('aria-busy'), null);
});

test('activity announcements are meaningful and suppress initial or unchanged polling noise', () => {
  const ready = { state: 'ready', events: [event(1, 'deploy', 'energy', '2026-08-22T14:34:00Z')] };
  assert.equal(activityLiveMessage({ state: 'loading', events: [] }, ready), '');
  assert.equal(activityLiveMessage(ready, { ...ready, updatedAt: new Date().toISOString() }), '');
  assert.equal(activityLiveMessage(ready, {
    state: 'ready',
    events: [event(2, 'rollback', 'energy', '2026-08-22T14:35:00Z'), ...ready.events],
  }), '1 new recent change.');
  assert.match(activityLiveMessage(ready, { ...ready, state: 'stale' }), /could not be refreshed/);
  assert.match(activityLiveMessage({ ...ready, state: 'stale' }, ready), /available again/);
  assert.equal(activityLiveMessage({ state: 'unavailable', events: [] }, ready, true), 'Recent changes updated.');
});

test('resource and activity announcements share one atomic message without overwriting', () => {
  const live = document.createElement('p');
  appendOverviewAnnouncement(live, 'Memory pressure is now critical.');
  appendOverviewAnnouncement(live, '2 new recent changes.');
  appendOverviewAnnouncement(live, '2 new recent changes.');
  assert.equal(live.textContent, 'Memory pressure is now critical. 2 new recent changes.');
});

test('poll replacement moves focus to the activity heading when the focused event ages out', () => {
  const body = document.createElement('div');
  document.body.appendChild(body);
  body.appendChild(renderActivityBrief({
    state: 'ready', updatedAt: new Date().toISOString(), events: [
      event(1, 'deploy', 'energy', '2026-08-22T14:34:00Z'),
    ],
  }));
  body.querySelector('.ov-activity-action-link').focus();

  replaceOverviewContent(body, renderActivityBrief({
    state: 'ready', updatedAt: new Date().toISOString(), events: [
      event(2, 'deploy', 'forecast', '2026-08-22T14:35:00Z'),
    ],
  }));
  assert.equal(document.activeElement, body.querySelector('#ov-activity-title'));
});

test('rendered retry state survives a surrounding Overview replacement', () => {
  const pending = renderActivityBrief(
    { state: 'unavailable', events: [], updatedAt: null },
    () => {},
    { retryPending: true },
  );
  assert.equal(pending.getAttribute('aria-busy'), 'true');
  assert.equal(pending.querySelector('.ov-activity-retry').disabled, true);
  assert.equal(pending.querySelector('.ov-activity-retry').textContent, 'Trying again…');
});

test('activity CSS keeps rows inert and gives coarse-pointer controls 44px targets', () => {
  const css = readFileSync(new URL('../static/style.css', import.meta.url), 'utf8');
  assert.doesNotMatch(css, /\.ov-activity-item:hover/);
  assert.match(css, /\.ov-activity-action-link\s*\{[^}]*text-decoration: none/s);
  assert.match(css, /\.ov-activity-target\s*\{[^}]*text-decoration: none/s);
  assert.match(css, /\.ov-activity-link-icon\s*\{[^}]*display: inline-grid/s);
  const touchRules = css.match(/@media \(hover: none\), \(pointer: coarse\), \(max-width: 760px\) \{[\s\S]*?\n\}/)?.[0] || '';
  for (const selector of ['.ov-activity-audit', '.ov-activity-disclosure', '.ov-activity-retry', '.ov-activity-action-link', 'a.ov-activity-target']) {
    assert.match(touchRules, new RegExp(selector.replace('.', '\\.')));
  }
  assert.match(touchRules, /min-height: 44px/);

  const reducedMotion = css.match(/@media \(prefers-reduced-motion: reduce\) \{\s*\.ov-pulse-dot[\s\S]*?\n\}/)?.[0] || '';
  assert.match(reducedMotion, /\.ov-activity-audit \.ov-activity-icon/);
  assert.match(reducedMotion, /\.ov-activity-disclosure \.ov-activity-icon \{ transition: none; \}/);
  assert.match(reducedMotion, /\.ov-activity-audit:hover \.ov-activity-icon \{ transform: none; \}/);
});
