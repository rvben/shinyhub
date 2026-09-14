import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { createSupportSessionAction } from '../static/views/support-session-settings.js';

function fixture() {
  return new JSDOM(readFileSync(new URL('../static/index.html', import.meta.url), 'utf8')).window.document;
}

test('only eligible people with confirmed enabled status can launch', () => {
  const document = fixture();
  for (const enabled of [true, false, undefined, null]) {
    for (const role of ['viewer', 'developer', 'operator', 'admin']) {
      let starts = 0;
      const action = createSupportSessionAction({ document, user: { id: 2, username: 'person', role }, selfId: 1, enabled, onStart: () => starts++ });
      if (enabled !== true) {
        assert.equal(action, null);
        assert.equal(starts, 0);
        continue;
      }
      document.body.append(action);
      const button = action.querySelector('button');
      button.click();
      const eligible = ['viewer', 'developer', 'operator', 'admin'].includes(role);
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
