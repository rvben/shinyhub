import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import {
  auditEmptyMessage,
  auditListPath,
  auditLoadError,
  auditLoadingMessage,
  auditRangeSuffix,
  auditSelection,
  createLatestRequestGate,
  mountAuditLog,
} from '../static/views/audit-log.js';

function fixture() {
  const dom = new JSDOM('<!DOCTYPE html><body><section id="audit-view" hidden></section></body>', {
    url: 'http://localhost/audit-log',
  });
  global.document = dom.window.document;
  global.location = dom.window.location;
  return dom;
}

test('mountAuditLog shows the view, loads the first page, updates nav, and unmount hides it', () => {
  fixture();
  const loadCalls = [];
  let navUpdated = 0;
  const view = document.getElementById('audit-view');

  const handle = mountAuditLog({
    loadAuditEvents: (offset) => loadCalls.push(offset),
    updateActiveNav: () => navUpdated++,
  });

  assert.equal(view.hidden, false, 'view must be revealed on mount');
  assert.deepEqual(loadCalls, [0], 'loadAuditEvents must be called with the first-page offset 0');
  assert.equal(navUpdated, 1, 'updateActiveNav must be called once');
  assert.equal(handle.title, 'Audit Log');

  handle.unmount();
  assert.equal(view.hidden, true, 'view must be hidden on unmount');
});

test('audit selection parses exact event and run filters and builds encoded API paths', () => {
  assert.deepEqual(auditSelection('?event=42'), { event: '42', run: '', action: '', since: '', until: '' });
  assert.deepEqual(auditSelection('?run=run%2Fone'), { event: '', run: 'run/one', action: '', since: '', until: '' });
  assert.deepEqual(auditSelection('?action=env.set'), { event: '', run: '', action: 'env.set', since: '', until: '' });
  assert.deepEqual(auditSelection('?event=bad'), { event: '', run: '', action: '', since: '', until: '' });

  assert.equal(auditListPath(0, { event: '42', run: '', action: '' }), '/api/audit?limit=100&offset=0&event=42');
  assert.equal(auditListPath(2, { event: '', run: 'run/one', action: '' }), '/api/audit?limit=100&offset=200&run=run%2Fone');
  assert.equal(auditListPath(1, { event: '', run: '', action: 'env.set' }), '/api/audit?limit=100&offset=100&action=env.set');
});

test('mountAuditLog forwards the current selection to the first load', () => {
  fixture();
  const loadCalls = [];
  mountAuditLog({
    loadAuditEvents: (page, selection) => loadCalls.push([page, selection]),
    updateActiveNav() {},
  }, '?event=42');

  assert.deepEqual(loadCalls, [[0, { event: '42', run: '', action: '', since: '', until: '' }]]);
});

test('filtered audit empty and failure copy preserves investigation context', () => {
  assert.equal(auditEmptyMessage({ event: '42' }), 'Audit event 42 is no longer available.');
  assert.equal(auditEmptyMessage({ run: 'run-1' }), 'No audit events were found for this run.');
  assert.equal(auditEmptyMessage({}), 'No audit events recorded yet. Every mutating action will appear here.');
  assert.equal(auditLoadError({ run: 'run-1' }), 'Failed to load the selected audit context.');
  assert.equal(auditLoadError({}), 'Failed to load audit log.');
  assert.equal(auditLoadingMessage({ event: '42' }), 'Loading audit event 42…');
  assert.equal(auditLoadingMessage({ run: 'run-1' }), 'Loading events for this run…');
});

test('audit request gate rejects responses from an older selection', () => {
  const gate = createLatestRequestGate();
  const first = gate.begin();
  const second = gate.begin();
  assert.equal(gate.isCurrent(first), false);
  assert.equal(gate.isCurrent(second), true);
  gate.invalidate();
  assert.equal(gate.isCurrent(second), false);
});

test('audit loading and asynchronous failures have announcement semantics', () => {
  const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
  assert.match(html, /id="audit-loading"[^>]*role="status"/);
  assert.match(html, /id="audit-error"[^>]*role="alert"/);
});

test('audit selection reads a date range and drops anything that is not a calendar date', () => {
  assert.deepEqual(
    auditSelection('?since=2026-09-01&until=2026-09-06'),
    { event: '', run: '', action: '', since: '2026-09-01', until: '2026-09-06' },
  );
  // Either end stands alone: "everything since the incident" is a real ask.
  assert.equal(auditSelection('?since=2026-09-01').since, '2026-09-01');
  assert.equal(auditSelection('?until=2026-09-06').until, '2026-09-06');
  // A malformed bound is dropped rather than forwarded to the server.
  assert.equal(auditSelection('?since=last-tuesday').since, '');
  assert.equal(auditSelection('?until=2026-9-6').until, '');
  // An inverted range matches nothing; ignore it instead of showing an error
  // for a link the operator was handed.
  assert.deepEqual(
    auditSelection('?since=2026-09-06&until=2026-09-01'),
    { event: '', run: '', action: '', since: '', until: '' },
  );
});

test('audit list path combines action with a date range but not with a deep link', () => {
  assert.equal(
    auditListPath(0, { action: 'env.set', since: '2026-09-01', until: '2026-09-06' }),
    '/api/audit?limit=100&offset=0&action=env.set&since=2026-09-01&until=2026-09-06',
  );
  assert.equal(
    auditListPath(1, { since: '2026-09-01' }),
    '/api/audit?limit=100&offset=100&since=2026-09-01',
  );
  // A specific event replaces the browsing filters; sending both would ask the
  // server for one event that is also inside a window, which is not the
  // question the deep link asks.
  assert.equal(
    auditListPath(0, { event: '42', action: 'env.set', since: '2026-09-01' }),
    '/api/audit?limit=100&offset=0&event=42',
  );
  assert.equal(
    auditListPath(0, { run: 'run-1', until: '2026-09-06' }),
    '/api/audit?limit=100&offset=0&run=run-1',
  );
  // A junk bound never reaches the query string even if it is in the selection.
  assert.equal(
    auditListPath(0, { since: 'yesterday' }),
    '/api/audit?limit=100&offset=0',
  );
});

test('audit copy names the window that was searched, so empty does not read as "nothing ever happened"', () => {
  assert.equal(auditRangeSuffix({}), '');
  assert.equal(auditRangeSuffix({ since: '2026-09-01', until: '2026-09-06' }), ' between 2026-09-01 and 2026-09-06');
  assert.equal(auditRangeSuffix({ since: '2026-09-01' }), ' on or after 2026-09-01');
  assert.equal(auditRangeSuffix({ until: '2026-09-06' }), ' on or before 2026-09-06');

  assert.equal(
    auditEmptyMessage({ since: '2026-09-01', until: '2026-09-06' }),
    'No audit events were recorded between 2026-09-01 and 2026-09-06.',
  );
  assert.equal(
    auditEmptyMessage({ action: 'env.set', since: '2026-09-01' }),
    'No env.set audit events were found on or after 2026-09-01.',
  );
  assert.equal(
    auditLoadingMessage({ until: '2026-09-06' }),
    'Loading audit events on or before 2026-09-06…',
  );
  assert.equal(auditLoadError({ since: '2026-09-01' }), 'Failed to load the selected audit context.');
});

test('the audit date range is labelled, and the API contract it queries is the one the server exposes', () => {
  const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
  // Two bare date inputs do not say which end is which, so each carries its own
  // label and an aria-label naming the direction and the zone.
  assert.match(html, /<label for="audit-since">/);
  assert.match(html, /<label for="audit-until">/);
  assert.match(html, /id="audit-since"[^>]*aria-label="Show audit events on or after this date \(UTC\)"/);
  assert.match(html, /id="audit-until"[^>]*aria-label="Show audit events on or before this date \(UTC\)"/);
  assert.match(html, /id="audit-range-clear"/);
});
