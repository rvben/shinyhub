import { afterEach, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { activityTime, actorLabel, buildActivityBrief } from '../static/views/overview-activity.js';
import { renderActivityBrief, replaceOverviewContent } from '../static/views/overview.js';

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
    event(4, 'deploy', 'energy', '2026-08-22T14:34:00Z', { run_id: runID }),
    event(3, 'update_app', 'observability', '2026-08-22T14:33:00Z', { run_id: runID }),
    event(2, 'deploy', 'observability', '2026-08-22T14:32:00Z', { run_id: runID }),
    event(1, 'fleet_apply_started', 'homelab-dashboards', '2026-08-22T14:31:00Z', {
      run_id: runID,
      resource_type: 'fleet',
    }),
  ]);

  assert.equal(brief.operations.length, 1);
  assert.equal(brief.grouped, true);
  const operation = brief.operations[0];
  assert.equal(operation.kind, 'group');
  assert.equal(operation.actionLabel, 'Fleet apply');
  assert.equal(operation.target, 'homelab-dashboards');
  assert.deepEqual(operation.children.map((child) => child.actionLabel), ['Deployed', 'Updated', 'Deployed']);
  assert.deepEqual(operation.children.map((child) => child.target), ['observability', 'observability', 'energy']);
  assert.equal(operation.tone.label, 'Started');
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
  assert.equal(actorLabel(null), 'System');
  assert.equal(actorLabel('alice'), 'alice');
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
  assert.equal(node.querySelector('.ov-activity-target').getAttribute('href'), '/apps/observability/overview');
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
  assert.equal(toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(body.querySelector('.ov-activity-children').hidden, true);
  toggle.click();
  toggle.focus();
  assert.equal(toggle.getAttribute('aria-expanded'), 'true');
  assert.equal(body.querySelector('.ov-activity-children').hidden, false);

  replaceOverviewContent(body, renderActivityBrief({ state: 'ready', updatedAt: new Date().toISOString(), events }));
  const restored = body.querySelector('.ov-activity-disclosure');
  assert.equal(restored.getAttribute('aria-expanded'), 'true');
  assert.equal(body.querySelector('.ov-activity-children').hidden, false);
  assert.equal(document.activeElement, restored);
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
