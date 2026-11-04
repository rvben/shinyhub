import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { createAccessLists } from '../static/views/access-lists.js';
const ok = items => ({ ok: true, status: 200, json: async () => ({ items }) });
const deferred = () => { let resolve; return { promise: new Promise(r => { resolve = r; }), resolve: value => resolve(value) }; };
const flush = () => new Promise(r => setImmediate(r));
function fixture(request) {
  const dom = new JSDOM('<ul id="members-list"></ul><ul id="group-access-list"></ul><p id="grant-error" role="alert"></p><p id="group-access-error" role="alert"></p><input id="grant-username"><input id="group-access-name">');
  let unauthorized = 0;
  const lists = createAccessLists({ document: dom.window.document, request, onUnauthorized: () => { unauthorized++; } });
  return { lists, doc: dom.window.document, dom, unauthorized: () => unauthorized };
}
const member = { user_id: 7, username: 'Analyst', role: 'viewer' };
test('late reads cannot cross app routes, even A to B to A', async t => {
  const pending = deferred(); let n = 0;
  const f = fixture(() => ++n === 1 ? pending.promise : Promise.resolve(ok([{ ...member, username: 'Current' }])));
  t.after(() => f.dom.window.close());
  f.lists.setApp('a'); const old = f.lists.refreshMembers();
  f.lists.setApp('b'); f.lists.setApp('a'); await f.lists.refreshMembers();
  pending.resolve(ok([member])); await old;
  assert.match(f.doc.querySelector('ul').textContent, /Current/);
  assert.doesNotMatch(f.doc.querySelector('ul').textContent, /Analyst/);
});
test('detached access actions cannot mutate another app', async t => {
  const calls = []; const f = fixture(async (path, options) => { calls.push([path, options]); return ok([member]); });
  t.after(() => f.dom.window.close());
  f.lists.setApp('a'); await f.lists.refreshMembers();
  const button = f.doc.querySelector('button'); f.lists.setApp('b'); button.click(); await flush();
  assert.equal(calls.length, 1);
});
test('failed refresh retains known entries, announces failure and offers retry', async t => {
  let fail = false; const f = fixture(async () => { if (fail) throw Error('offline'); return ok([member]); });
  t.after(() => f.dom.window.close());
  f.lists.setApp('a'); await f.lists.refreshMembers(); fail = true; await f.lists.refreshMembers();
  assert.equal(f.doc.querySelector('.member-name').textContent, 'Analyst');
  assert.match(f.doc.querySelector('[role=alert]').textContent, /last known entries/);
  fail = false; f.doc.querySelector('[data-access-status] button').click(); await flush();
  assert.equal(f.doc.querySelector('[data-access-status]'), null);
});
test('successful removal invalidates an older pending refresh', async t => {
  const pending = deferred(); let reads = 0; const calls = [];
  const f = fixture(async (path, options) => {
    calls.push([path, options]);
    if (options.method === 'DELETE') return ok([]);
    return ++reads === 1 ? ok([member]) : pending.promise;
  }); t.after(() => f.dom.window.close());
  f.lists.setApp('a'); await f.lists.refreshMembers(); const late = f.lists.refreshMembers();
  f.doc.querySelector('button').click(); await flush();
  pending.resolve(ok([member])); await late;
  assert.equal(f.doc.querySelector('.member-name'), null);
  assert.equal(calls.at(-1)[0], '/api/apps/a/members');
  assert.equal(f.doc.querySelector('ul').hasAttribute('aria-busy'), false);
});
test('failed role change restores saved value and announces error', async t => {
  const f = fixture(async (_, options) => options.method ? { ok: false, status: 500 } : ok([member]));
  t.after(() => f.dom.window.close()); f.lists.setApp('a'); await f.lists.refreshMembers();
  const role = f.doc.querySelector('select'); role.value = 'manager'; role.dispatchEvent(new f.dom.window.Event('change')); await flush();
  assert.equal(role.value, 'viewer'); assert.equal(role.disabled, false);
  assert.match(f.doc.querySelector('#grant-error').textContent, /Could not update Analyst/);
});
test('group removal failures stay visible and URL components are encoded', async t => {
  let target; const f = fixture(async (path, options) => {
    if (options.method) { target = path; throw Error('offline'); }
    return ok([{ group: 'team/a', role: 'viewer' }]);
  }); t.after(() => f.dom.window.close()); f.lists.setApp('a'); await f.lists.refreshGroups();
  f.doc.querySelector('button').click(); await flush();
  assert.equal(target, '/api/apps/a/group-access/team%2Fa');
  assert.match(f.doc.querySelector('#group-access-error').textContent, /Could not confirm removal/);
  assert.equal(f.doc.querySelectorAll('.member-name').length, 1);
});
test('malformed list responses produce a retry, not false empty access', async t => {
  const f = fixture(async () => ({ ok: true, json: async () => ({ wrong: [] }) }));
  t.after(() => f.dom.window.close()); f.lists.setApp('a'); await f.lists.refreshMembers();
  assert.match(f.doc.querySelector('ul').textContent, /Could not check/);
});
test('unauthorized responses expire the session', async t => {
  const f = fixture(async () => ({ status: 401 })); t.after(() => f.dom.window.close());
  f.lists.setApp('a'); await f.lists.refreshGroups(); assert.equal(f.unauthorized(), 1);
});
test('refresh preserves the specific action focused while the request was pending', async t => {
  const pending = deferred(); let n = 0; const f = fixture(() => ++n === 1 ? ok([member]) : pending.promise);
  t.after(() => f.dom.window.close()); f.lists.setApp('a'); await f.lists.refreshMembers();
  const refresh = f.lists.refreshMembers(); f.doc.querySelector('[data-focus-key="revoke:7"]').focus();
  pending.resolve(ok([member])); await refresh;
  assert.equal(f.doc.activeElement.dataset.focusKey, 'revoke:7');
});
test('a pending mutation on A cannot suppress loading B or release B’s own pending mutation', async t => {
  const oldMutation = deferred(), newMutation = deferred(); const calls = [];
  const f = fixture(async (path, options) => {
    calls.push(path);
    if (options.method) return path.includes('/a/') ? oldMutation.promise : newMutation.promise;
    return ok([{ ...member, username: path.includes('/a/') ? 'Member A' : 'Member B' }]);
  }); t.after(() => f.dom.window.close());
  f.lists.setApp('a'); await f.lists.refreshMembers(); f.doc.querySelector('button').click();
  f.lists.setApp('b'); await f.lists.refreshMembers(); assert.match(f.doc.querySelector('ul').textContent, /Member B/);
  f.doc.querySelector('button').click(); oldMutation.resolve(ok([])); await flush();
  const before = calls.length; await f.lists.refreshMembers(); assert.equal(calls.length, before);
  newMutation.resolve(ok([])); await flush(); await f.lists.refreshMembers(); assert.equal(calls.length, before + 1);
});
