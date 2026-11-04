import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  createUsageRequestGate,
  createUsageViewer,
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
  const dom = new JSDOM('<button id="outside">Another action</button><main id="panel"></main>');
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

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

function response(body, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

// DOM event handlers intentionally do not return their request promises. Drain
// their bounded fetch/JSON/render chain without wall-clock timers.
async function settle() {
  for (let index = 0; index < 12; index += 1) await Promise.resolve();
}

function viewerHarness(api, onUnauthorized = () => {}) {
  const dom = setup();
  return {
    ...dom,
    dispose: createUsageViewer({ panel: dom.panel, app: { slug: 'forecast app' }, api, onUnauthorized }),
  };
}

function buttonNamed(panel, name) {
  const button = [...panel.querySelectorAll('button')].find(candidate => name.test(candidate.textContent));
  assert.ok(button, `Expected a button matching ${name}`);
  return button;
}

test('usage controller keeps a valid requested range after the server caps retention', async () => {
  const paths = [];
  const h = viewerHarness(async path => {
    paths.push(path);
    if (paths.length === 2) throw new Error('Offline');
    return response(report({ window_days: 14, aggregate_retention_days: 14 }));
  });
  await settle();
  buttonNamed(h.panel, /^Refresh$/).click();
  await settle();
  buttonNamed(h.panel, /Try again|Retry/i).click();
  await settle();
  assert.deepEqual(paths, [
    '/api/apps/forecast%20app/usage?days=30',
    '/api/apps/forecast%20app/usage?days=30',
    '/api/apps/forecast%20app/usage?days=30',
  ]);
  h.dispose();
});

test('failed range changes retain the displayed report and reset the range selector', async () => {
  const paths = [];
  let fail = false;
  const h = viewerHarness(async path => {
    paths.push(path);
    if (fail) throw new Error('Offline');
    return response(report({ window_days: path.endsWith('days=7') ? 7 : 30 }));
  });
  await settle();
  const previousSummary = h.panel.querySelector('.usage-summary').textContent;
  fail = true;
  const select = h.panel.querySelector('select');
  select.value = '7';
  select.dispatchEvent(new h.document.defaultView.Event('change'));
  await settle();
  assert.equal(h.panel.querySelector('.usage-summary').textContent, previousSummary);
  assert.equal(h.panel.querySelector('select').value, '30');
  assert.equal(paths.at(-1), '/api/apps/forecast%20app/usage?days=7');
  assert.ok(h.panel.querySelector('[role="alert"]'));
  fail = false;
  buttonNamed(h.panel, /Try again|Retry/i).click();
  await settle();
  assert.equal(paths.at(-1), '/api/apps/forecast%20app/usage?days=7');
  assert.equal(h.panel.querySelector('select').value, '7');
  h.dispose();
});

test('access revocation removes a retained identified report and retry can recover', async () => {
  const identified = report({
    identity_mode: 'identified',
    capabilities: { unique_viewers: true, viewer_detail: true, recent_sessions: true },
    viewers: [{ user_id: 7, username: 'private-account', display_name: 'Ada Lovelace', sessions: 1, total_duration_seconds: 60, last_opened_at: '2026-08-31T11:54:12Z' }],
    recent_sessions: [{ id: 'private-session', principal_kind: 'person', user_id: 7, username: 'private-account', display_name: 'Ada Lovelace', started_at: '2026-08-31T11:54:12Z', duration_seconds: 60 }],
  });
  const replies = [response(identified), response({}, 403), response(report())];
  const paths = [];
  const h = viewerHarness(async path => { paths.push(path); return replies.shift(); });
  await settle();
  assert.match(h.panel.textContent, /Ada Lovelace/);
  assert.ok(h.panel.querySelector('.usage-recent-table [title]'));

  buttonNamed(h.panel, /^Refresh$/).click();
  await settle();
  assert.match(h.panel.querySelector('[role="alert"]').textContent, /access.*changed/i);
  assert.equal(h.panel.querySelector('.usage-summary'), null);
  assert.equal(h.panel.querySelector('.usage-detail-grid'), null);
  assert.equal(h.panel.querySelector('[title]'), null);
  assert.doesNotMatch(h.panel.textContent, /Ada Lovelace|private-account|previous.*report/i);

  buttonNamed(h.panel, /Try again|Retry/i).click();
  await settle();
  assert.ok(h.panel.querySelector('.usage-summary'));
  assert.equal(h.panel.querySelector('[role="alert"]'), null);
  assert.doesNotMatch(h.panel.textContent, /Ada Lovelace|private-account/);
  assert.deepEqual(paths, Array(3).fill('/api/apps/forecast%20app/usage?days=30'));
  h.dispose();
});

function selectRange(h, days) {
  const select = h.panel.querySelector('select');
  assert.ok(select, 'A retained report keeps its range control available');
  select.value = String(days);
  select.dispatchEvent(new h.document.defaultView.Event('change'));
}

test('stale fetches cannot replace a newer usage report or report an error', async () => {
  const stale = deferred();
  const signals = [];
  let calls = 0;
  const h = viewerHarness((path, options) => {
    signals.push(options.signal);
    return ++calls === 2 ? stale.promise : Promise.resolve(response(report({ window_days: calls === 3 ? 7 : 30 })));
  });
  await settle();
  buttonNamed(h.panel, /^Refresh$/).click();
  selectRange(h, 7);
  await settle();
  const current = h.panel.textContent;
  assert.equal(signals[1].aborted, true);
  stale.resolve(response({}, 503));
  await settle();
  assert.equal(h.panel.textContent, current);
  assert.equal(h.panel.querySelector('[role="alert"]'), null);
  assert.equal(h.panel.querySelector('select').value, '7');
  h.dispose();
});

test('stale JSON completions cannot replace a newer usage report', async () => {
  const body = deferred();
  let calls = 0;
  const h = viewerHarness(async () => ++calls === 2
    ? { ok: true, status: 200, json: () => body.promise }
    : response(report({ window_days: calls === 3 ? 7 : 30 })));
  await settle();
  buttonNamed(h.panel, /^Refresh$/).click();
  await settle();
  selectRange(h, 7);
  await settle();
  const current = h.panel.textContent;
  body.resolve(report({ summary: { sessions: 999 } }));
  await settle();
  assert.equal(h.panel.textContent, current);
  assert.equal(h.panel.querySelector('select').value, '7');
  h.dispose();
});

for (const phase of ['fetch', 'JSON']) test(`disposing usage during ${phase} cannot repaint the replacement surface`, async () => {
  const pending = deferred();
  let signal;
  let unauthorized = 0;
  const h = viewerHarness((path, options) => {
    signal = options.signal;
    return phase === 'fetch' ? pending.promise : Promise.resolve({ ok: true, status: 200, json: () => pending.promise });
  }, () => { unauthorized += 1; });
  await settle();
  h.dispose();
  h.panel.textContent = 'Replacement app';
  assert.equal(signal.aborted, true);
  pending.resolve(phase === 'fetch' ? response({}, 401) : report());
  await settle();
  assert.equal(h.panel.textContent, 'Replacement app');
  assert.equal(unauthorized, 0);
});

test('usage request failures remain recoverable and successful retry receives focus', async () => {
  let calls = 0;
  const h = viewerHarness(async () => {
    if (++calls === 1) throw new Error('Offline');
    return response(report());
  });
  await settle();
  assert.ok(h.panel.querySelector('[role="alert"]'));
  const retry = buttonNamed(h.panel, /Try again|Retry/i);
  retry.focus();
  retry.click();
  await settle();
  assert.ok(h.panel.querySelector('.usage-summary'));
  assert.equal(h.document.activeElement, buttonNamed(h.panel, /^Refresh$/));
  h.dispose();
});

test('delayed refresh does not steal focus after the viewer moves to another control', async () => {
  const pending = deferred();
  let calls = 0;
  const h = viewerHarness(async () => ++calls === 1 ? response(report()) : pending.promise);
  await settle();
  const refresh = buttonNamed(h.panel, /^Refresh$/);
  refresh.focus();
  refresh.click();
  const outside = h.document.getElementById('outside');
  outside.focus();
  pending.resolve(response(report()));
  await settle();
  assert.equal(h.document.activeElement, outside);
  h.dispose();
});

test('usage refresh and range success preserve focus when their control still owns it', async () => {
  const h = viewerHarness(async path => response(report({ window_days: path.endsWith('days=7') ? 7 : 30 })));
  await settle();
  let refresh = buttonNamed(h.panel, /^Refresh$/);
  refresh.focus();
  refresh.click();
  await settle();
  assert.equal(h.document.activeElement, buttonNamed(h.panel, /^Refresh$/));
  h.panel.querySelector('select').focus();
  selectRange(h, 7);
  await settle();
  assert.equal(h.document.activeElement, h.panel.querySelector('select'));
  assert.equal(h.panel.querySelector('select').value, '7');
  h.dispose();
});

test('usage authorization and malformed JSON have distinct recovery paths', async () => {
  let unauthorized = 0;
  const h = viewerHarness(async () => response({}, 401), () => { unauthorized += 1; });
  await settle();
  assert.equal(unauthorized, 1);
  h.dispose();

  for (const result of [
    response({}, 403),
    { ok: true, status: 200, json: async () => { throw new SyntaxError('Invalid JSON'); } },
    response(null),
    response(report({ generated_at: 'not a date' })),
  ]) {
    const attempt = viewerHarness(async () => result);
    await settle();
    assert.ok(attempt.panel.querySelector('[role="alert"]'));
    assert.ok(buttonNamed(attempt.panel, /Try again|Retry/i));
    assert.equal(attempt.panel.hasAttribute('aria-busy'), false);
    attempt.dispose();
  }
});

test('aggregate dashboard explains its metric and does not render identities', () => {
  const { document, panel } = setup();
  renderUsageDashboard(document, panel, report());
  assert.match(panel.textContent, /live connection opens/i);
  assert.match(panel.textContent, /3Successful connections/);
  assert.match(panel.textContent, /1Authenticated people/);
  assert.match(panel.textContent, /2Simultaneous connections/);
  assert.match(panel.textContent, /Viewer keys are app-scoped/i);
  assert.equal(panel.querySelectorAll('.usage-chart-bar').length, 30);
  assert.equal(panel.querySelector('.usage-chart').getAttribute('role'), 'img');
  assert.equal(panel.querySelector('.usage-chart-bars').getAttribute('aria-hidden'), 'true');
  assert.equal(panel.querySelectorAll('.usage-summary dt').length, 4);
  assert.match(panel.querySelector('.usage-metadata').textContent, /Last opened: 5m ago/);
  assert.match(panel.querySelector('.usage-metadata').textContent, /1 active from this window/);
  assert.equal(panel.querySelector('.usage-metadata time').dateTime, '2026-08-31T12:00:00Z');
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
  assert.match(panel.textContent, /Up to 50 retained accounts, ordered by session count/);
  assert.match(panel.textContent, /Up to 50 newest retained sessions/);
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
  assert.deepEqual([...panel.querySelectorAll('option')].map(option => option.value), ['7', '30']);
  assert.equal(panel.querySelector('select').value, '30');
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
  assert.match(panel.textContent, /Unique viewersUnavailable/);
  assert.match(panel.textContent, /No stable viewer identifier is retained/);
  assert.match(panel.textContent, /Service accounts1/);
});

test('daily data is available in a native disclosure and keeps unavailable distinct from zero', () => {
  const { document, panel } = setup();
  renderUsageDashboard(document, panel, report({
    daily: [
      { date: '2026-08-30', sessions: 0, peak_concurrent_sessions: 0, unique_viewers: 0 },
      { date: '2026-08-31', sessions: 3, peak_concurrent_sessions: 2, unique_viewers: null },
    ],
  }));
  const table = panel.querySelector('.usage-daily-data');
  assert.ok(table.closest('details')?.querySelector('summary'));
  assert.equal(table.classList.contains('sr-only'), false);
  const rows = [...table.querySelectorAll('tbody tr')];
  assert.equal(rows.at(-2).lastElementChild.textContent, '0');
  assert.equal(rows.at(-1).lastElementChild.textContent, 'Unavailable');
});

test('carried-over concurrency remains inspectable when no session opened in the window', () => {
  const { document, panel } = setup();
  renderUsageDashboard(document, panel, report({
    summary: { sessions: 0, peak_concurrent_sessions: 1, active_sessions: 0 },
    daily: [{ date: '2026-08-31', sessions: 0, peak_concurrent_sessions: 1, unique_viewers: null }],
  }));
  assert.ok(panel.querySelector('.usage-chart'));
  const finalDay = panel.querySelector('.usage-daily-data tbody').lastElementChild;
  assert.equal(finalDay.children[1].textContent, '0');
  assert.equal(finalDay.children[2].textContent, '1');
  assert.match(panel.querySelector('.usage-metadata').textContent, /0 active from this window/);
});

test('viewer and recent-session capabilities independently govern identity detail', () => {
  const { document, panel } = setup();
  const identities = {
    identity_mode: 'identified',
    viewers: [{ user_id: 1, username: 'Ada', sessions: 1, total_duration_seconds: 60, last_opened_at: '2026-08-31T11:55:00Z' }],
    recent_sessions: [{ id: 'one', principal_kind: 'person', username: 'Ada', started_at: '2026-08-31T11:55:00Z', duration_seconds: 60 }],
  };
  renderUsageDashboard(document, panel, report({
    ...identities, capabilities: { unique_viewers: true, viewer_detail: true, recent_sessions: false },
  }));
  assert.equal(panel.querySelector('.usage-recent-table'), null);
  assert.equal(panel.querySelectorAll('.usage-table').length, 1);
  renderUsageDashboard(document, panel, report({
    ...identities, capabilities: { unique_viewers: true, viewer_detail: false, recent_sessions: true },
  }));
  assert.ok(panel.querySelector('.usage-recent-table'));
  assert.equal(panel.querySelectorAll('.usage-table').length, 1);
});

test('unnamed person history is not relabeled as anonymous activity', () => {
  const { document, panel } = setup();
  renderUsageDashboard(document, panel, report({
    identity_mode: 'identified',
    capabilities: { unique_viewers: false, viewer_detail: true, recent_sessions: true },
    recent_sessions: [{ id: 'deleted', principal_kind: 'person', started_at: '2026-08-31T11:55:00Z', duration_seconds: 60 }],
  }));
  const who = panel.querySelector('.usage-recent-table tbody td').textContent;
  assert.match(who, /person/i);
  assert.doesNotMatch(who, /anonymous/i);
});

test('missing retention metadata is unknown, never NaN or unlimited', () => {
  const { document, panel } = setup();
  renderUsageDashboard(document, panel, report({ raw_retention_days: undefined, aggregate_retention_days: null }));
  assert.match(panel.querySelector('.usage-privacy').textContent, /retention is unavailable/);
  assert.doesNotMatch(panel.querySelector('.usage-privacy').textContent, /NaN|without a time limit/);
});
