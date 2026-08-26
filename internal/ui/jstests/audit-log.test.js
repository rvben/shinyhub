import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import {
  auditEmptyMessage,
  auditListPath,
  auditLoadError,
  auditLoadingMessage,
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
  assert.deepEqual(auditSelection('?event=42'), { event: '42', run: '', action: '' });
  assert.deepEqual(auditSelection('?run=run%2Fone'), { event: '', run: 'run/one', action: '' });
  assert.deepEqual(auditSelection('?action=env.set'), { event: '', run: '', action: 'env.set' });
  assert.deepEqual(auditSelection('?event=bad'), { event: '', run: '', action: '' });

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

  assert.deepEqual(loadCalls, [[0, { event: '42', run: '', action: '' }]]);
});

test('filtered audit empty and failure copy preserves investigation context', () => {
  assert.equal(auditEmptyMessage({ event: '42' }), 'Audit event 42 is no longer available.');
  assert.equal(auditEmptyMessage({ run: 'run-1' }), 'No audit events were found for this run.');
  assert.equal(auditEmptyMessage({}), 'No audit events recorded yet — every mutating action will appear here.');
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
