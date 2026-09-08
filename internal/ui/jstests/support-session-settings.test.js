import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { renderSupportSessionSettings, createSupportSessionAction } from '../static/views/support-session-settings.js';

function fixture() {
  return new JSDOM(readFileSync(new URL('../static/index.html', import.meta.url), 'utf8')).window.document;
}

test('status distinguishes disabled, enabled, loading, and missing metadata', () => {
  const document = fixture();
  for (const [enabled, loading, expected] of [[false, false, 'Not enabled'], [true, false, 'Enabled'], [null, true, 'Checking status…'], [undefined, false, 'Status unavailable']]) {
    renderSupportSessionSettings(document, enabled, loading);
    assert.equal(document.getElementById('support-settings-status').textContent, expected);
  }
  assert.match(document.getElementById('support-settings-message').textContent, /Refresh/);
});

test('disabled feature opens setup with keyboard focus and never launches a session', () => {
  const document = fixture();
  let starts = 0;
  const action = createSupportSessionAction({ document, user: { id: 2, username: 'viewer', role: 'viewer' }, selfId: 1, enabled: false, onStart: () => starts++ });
  document.body.append(action);
  action.querySelector('button').click();
  assert.equal(document.getElementById('support-settings-setup').open, true);
  assert.equal(document.activeElement.tagName, 'SUMMARY');
  assert.equal(starts, 0);
});

test('only eligible people with confirmed enabled status can launch', () => {
  const document = fixture();
  for (const enabled of [true, false, undefined, null]) {
    for (const role of ['viewer', 'developer', 'operator', 'admin']) {
      let starts = 0;
      const action = createSupportSessionAction({ document, user: { id: 2, username: 'person', role }, selfId: 1, enabled, onStart: () => starts++ });
      document.body.append(action);
      const button = action.querySelector('button');
      button.click();
      const eligible = ['viewer', 'developer'].includes(role);
      assert.equal(starts, enabled === true && eligible ? 1 : 0);
      if (!eligible || enabled !== true) {
        assert.ok(document.getElementById(button.getAttribute('aria-describedby')).textContent);
      }
      action.remove();
    }
  }
});

test('self and service accounts remain unavailable with visible reasons', () => {
  const document = fixture();
  for (const user of [{ id: 1, role: 'viewer' }, { id: 2, role: 'viewer', principal_type: 'service_account' }]) {
    const action = createSupportSessionAction({ document, user, selfId: 1, enabled: true, onStart: () => assert.fail('must not launch') });
    assert.equal(action.querySelector('button').disabled, true);
    assert.ok(action.querySelector('.users-support-hint').textContent);
  }
});
