import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { mountAuditLog } from '../static/views/audit-log.js';

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
