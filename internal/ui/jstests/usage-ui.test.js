import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  createUsageRequestGate,
  formatDuration,
  relativeUsageTime,
  renderUsageDashboard,
  renderUsageError,
} from '../static/views/usage-ui.js';

test('usage request gate aborts stale work and invalidates on unmount', () => {
  const gate = createUsageRequestGate();
  const first = gate.begin();
  assert.equal(gate.isCurrent(first), true);
  const second = gate.begin();
  assert.equal(first.signal.aborted, true);
  assert.equal(gate.isCurrent(first), false);
  assert.equal(gate.isCurrent(second), true);
  gate.invalidate();
  assert.equal(second.signal.aborted, true);
  assert.equal(gate.isCurrent(second), false);
});

function setup() {
  const dom = new JSDOM('<main id="panel"></main>');
  return { document: dom.window.document, panel: dom.window.document.getElementById('panel') };
}

function report(overrides = {}) {
  return {
    enabled: true,
    window_days: 30,
    raw_retention_days: 30,
    aggregate_retention_days: 365,
    identity_mode: 'pseudonymous',
    policy_source: 'hub',
    capabilities: { unique_viewers: true, viewer_detail: false, recent_sessions: false },
    generated_at: '2026-08-31T12:00:00Z',
    identity_detail: false,
    summary: {
      sessions: 3,
      unique_viewers: 1,
      peak_concurrent_sessions: 2,
      authenticated_sessions: 2,
      anonymous_sessions: 1,
      active_sessions: 1,
      average_duration_seconds: 125,
      last_opened_at: '2026-08-31T11:55:00Z',
    },
    daily: [
      { date: '2026-08-30', sessions: 1, peak_concurrent_sessions: 1, unique_viewers: 1 },
      { date: '2026-08-31', sessions: 2, peak_concurrent_sessions: 2, unique_viewers: 1 },
    ],
    viewers: [],
    recent_sessions: [],
    ...overrides,
  };
}

test('duration and relative-time labels stay compact and human-readable', () => {
  assert.equal(formatDuration(9), '9s');
  assert.equal(formatDuration(125), '2m 5s');
  assert.equal(formatDuration(7500), '2h 5m');
  assert.equal(relativeUsageTime('2026-08-31T11:59:15Z', new Date('2026-08-31T12:00:00Z')), 'Just now');
  assert.equal(relativeUsageTime('2026-08-31T11:55:00Z', new Date('2026-08-31T12:00:00Z')), '5m ago');
  assert.equal(relativeUsageTime(null), 'Never');
});

test('aggregate dashboard explains its metric and does not render identities', () => {
  const { document, panel } = setup();
  renderUsageDashboard(document, panel, report());
  assert.match(panel.textContent, /live connection opens/i);
  assert.match(panel.textContent, /3Successful connections/);
  assert.match(panel.textContent, /1App-scoped identities/);
  assert.match(panel.textContent, /2Simultaneous connections/);
  assert.match(panel.textContent, /Viewer keys are app-scoped/i);
  assert.equal(panel.querySelectorAll('.usage-chart-bar').length, 30);
  assert.equal(panel.querySelector('.usage-chart').getAttribute('role'), 'img');
  assert.equal(panel.querySelectorAll('.usage-daily-data tbody tr').length, 30);
  assert.match(panel.querySelector('.usage-daily-data thead').textContent, /Peak concurrent/);
  assert.match(panel.querySelector('.usage-daily-data caption').textContent, /Daily app usage data/);
  assert.equal(panel.querySelectorAll('.usage-table').length, 0);
  assert.equal(panel.querySelector('option[value="365"]').disabled, false);
});

test('administrator detail renders viewer names safely through text nodes', () => {
  const { document, panel } = setup();
  renderUsageDashboard(document, panel, report({
    identity_mode: 'identified',
    capabilities: { unique_viewers: true, viewer_detail: true, recent_sessions: true },
    viewers: [{
      user_id: 7, username: '<img src=x onerror=alert(1)>', display_name: 'Ada',
      sessions: 2, total_duration_seconds: 300, last_opened_at: '2026-08-31T11:55:00Z',
    }],
    recent_sessions: [{
      id: 'one', user_id: 7, username: 'ada', display_name: 'Ada', deployment_id: 42,
      started_at: '2026-08-31T11:55:00Z', duration_seconds: 300, active: true,
    }],
  }));
  assert.equal(panel.querySelectorAll('.usage-table').length, 2);
  assert.match(panel.textContent, /Administrators can see account names/);
  assert.match(panel.textContent, /Active · 5m 0s/);
  assert.equal(panel.querySelector('img'), null);
  assert.match(panel.textContent, /<img src=x/);
});

test('recent sessions stay compact and can be expanded without losing semantics', () => {
  const { document, panel } = setup();
  const recent = Array.from({ length: 15 }, (_, index) => ({
    id: `session-${index}`,
    username: `viewer-${index}`,
    deployment_id: 42,
    started_at: '2026-08-31T11:55:00Z',
    duration_seconds: 60,
    active: false,
  }));
  renderUsageDashboard(document, panel, report({
    identity_mode: 'identified',
    capabilities: { unique_viewers: true, viewer_detail: true, recent_sessions: true },
    recent_sessions: recent,
  }));

  const button = panel.querySelector('.usage-show-more');
  assert.equal(panel.querySelectorAll('.usage-recent-table tbody tr').length, 12);
  assert.equal(button.getAttribute('aria-expanded'), 'false');
  assert.match(button.textContent, /Show 3 more/);
  button.click();
  assert.equal(panel.querySelectorAll('.usage-recent-table tbody tr').length, 15);
  assert.equal(button.getAttribute('aria-expanded'), 'true');
  assert.equal(button.textContent, 'Show fewer');
});

test('empty, disabled, error, and range-control states are actionable', () => {
  const { document, panel } = setup();
  let selected = 0;
  let refreshed = false;
  renderUsageDashboard(document, panel, report({
    summary: { sessions: 0 }, daily: [], aggregate_retention_days: 14, window_days: 14,
  }), {
    onRangeChange(days) { selected = days; },
    onRefresh() { refreshed = true; },
  });
  assert.match(panel.textContent, /No sessions in this window/);
  assert.match(panel.querySelector('select').textContent, /14 days \(retained\)/);
  assert.equal(panel.querySelector('option[value="365"]').disabled, true);
  panel.querySelector('select').value = '7';
  panel.querySelector('select').dispatchEvent(new document.defaultView.Event('change'));
  panel.querySelector('button').click();
  assert.equal(selected, 7);
  assert.equal(refreshed, true);

  renderUsageDashboard(document, panel, report({ enabled: false, summary: { sessions: 0 } }));
  assert.match(panel.textContent, /New collection is paused/);
  let retried = false;
  renderUsageError(document, panel, 'Connection failed.', () => { retried = true; });
  assert.equal(panel.querySelector('.usage-empty').getAttribute('role'), 'alert');
  panel.querySelector('button').click();
  assert.equal(retried, true);
});

test('unattributed and rolled-up windows never present unavailable identity as zero', () => {
  const { document, panel } = setup();
  renderUsageDashboard(document, panel, report({
    identity_mode: 'unattributed',
    capabilities: { unique_viewers: false, viewer_detail: false, recent_sessions: false },
    summary: { sessions: 8, unique_viewers: null, authenticated_sessions: 5, anonymous_sessions: 2, service_sessions: 1 },
  }));
  assert.match(panel.textContent, /Unique viewersNot collected/);
  assert.match(panel.textContent, /No stable viewer identifier is retained/);
  assert.match(panel.textContent, /Service accounts1/);
});
