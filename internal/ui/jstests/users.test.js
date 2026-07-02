import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { mountUsers } from '../static/views/users.js';

function fixture() {
  const dom = new JSDOM('<!DOCTYPE html><body><section id="users-view" hidden></section></body>', {
    url: 'http://localhost/users',
  });
  global.document = dom.window.document;
  global.location = dom.window.location;
  return dom;
}

test('mountUsers shows the view, loads users, updates nav, and unmount hides it', () => {
  fixture();
  let loaded = 0;
  let navUpdated = 0;
  const view = document.getElementById('users-view');

  const handle = mountUsers({
    loadUsers: () => loaded++,
    updateActiveNav: () => navUpdated++,
  });

  assert.equal(view.hidden, false, 'view must be revealed on mount');
  assert.equal(loaded, 1, 'loadUsers must be called once');
  assert.equal(navUpdated, 1, 'updateActiveNav must be called once');
  assert.equal(handle.title, 'Users');

  handle.unmount();
  assert.equal(view.hidden, true, 'view must be hidden on unmount');
});
