import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  collapsedGroups,
  createGroupDisclosure,
  isGroupCollapsed,
  persistGroupCollapsed,
} from '../static/views/group-disclosure.js';

function memoryStorage() {
  const values = new Map();
  return {
    getItem: (key) => values.has(key) ? values.get(key) : null,
    setItem: (key, value) => values.set(key, String(value)),
  };
}

function disclosure(options = {}) {
  const doc = new JSDOM('<!doctype html>').window.document;
  return createGroupDisclosure(doc, {
    view: 'sidebar', groupKey: 'team', label: 'Team Tools', count: 2,
    iconEmoji: '🧰', classPrefix: 'sidebar-project', ...options,
  });
}

test('persisted collapse state is independent per view', () => {
  const storage = memoryStorage();
  persistGroupCollapsed('sidebar', 'team', true, storage);
  assert.equal(isGroupCollapsed('sidebar', 'team', storage), true);
  assert.equal(isGroupCollapsed('apps', 'team', storage), false);
  assert.deepEqual([...collapsedGroups('sidebar', storage)], ['team']);
});

test('disclosure exposes a labelled button and controlled body', () => {
  const d = disclosure({ storage: memoryStorage() });
  assert.equal(d.heading.tagName, 'H2');
  assert.equal(d.toggle.getAttribute('aria-expanded'), 'true');
  assert.equal(d.toggle.getAttribute('aria-label'), 'Team Tools, 2 apps');
  assert.equal(d.toggle.getAttribute('aria-controls'), d.body.id);
  assert.equal(d.body.hidden, false);
  assert.equal(d.root.querySelector('.sidebar-project-group-icon').textContent, '🧰');
});

test('click toggles the body and persists the preference', () => {
  const storage = memoryStorage();
  const d = disclosure({ storage });
  d.toggle.click();
  assert.equal(d.body.hidden, true);
  assert.equal(d.toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(isGroupCollapsed('sidebar', 'team', storage), true);
  d.toggle.click();
  assert.equal(d.body.hidden, false);
  assert.equal(isGroupCollapsed('sidebar', 'team', storage), false);
});

test('forceExpanded reveals the active group without erasing its saved preference', () => {
  const storage = memoryStorage();
  persistGroupCollapsed('sidebar', 'team', true, storage);
  const d = disclosure({ storage, forceExpanded: true });
  assert.equal(d.body.hidden, false);
  assert.equal(d.toggle.getAttribute('aria-expanded'), 'true');
  assert.equal(isGroupCollapsed('sidebar', 'team', storage), true);
});

test('malformed storage is ignored safely', () => {
  const storage = memoryStorage();
  storage.setItem('shinyhub.collapsedProjectGroups.v1.sidebar', '{bad');
  assert.deepEqual([...collapsedGroups('sidebar', storage)], []);
});
