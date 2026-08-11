import { test } from 'node:test';
import assert from 'node:assert/strict';
import { groupAppsForGrid } from '../static/views/app-grid-groups.js';
import { groupApps } from '../static/views/project-groups.js';
import { GROUP_ORDER_FIXTURE as FIXTURE, GROUP_ORDER_EXPECTED } from './group-order-fixture.js';

for (const sortKey of ['default', 'name', 'deploy', 'status']) {
  test(`groupAppsForGrid: group order is identical under sort "${sortKey}"`, () => {
    const groups = groupAppsForGrid(FIXTURE, { sortKey });
    assert.deepEqual(groups.map((g) => g.project), GROUP_ORDER_EXPECTED);
  });
}

test('groupAppsForGrid: the sort applies within a group, not across groups', () => {
  const groups = groupAppsForGrid(FIXTURE, { sortKey: 'deploy' });
  // new-b is the newest deploy in the whole list, yet it stays in group bbb
  // BELOW group aaa's much older app. Across-group sorting would hoist it.
  assert.deepEqual(groups.map((g) => g.apps.map((a) => a.slug)), [['loose'], ['old-a'], ['new-b']]);
});

test('groupAppsForGrid: name sort orders within a group', () => {
  const apps = [
    { slug: 'z', name: 'Zulu', project_slug: 'p' },
    { slug: 'a', name: 'Alpha', project_slug: 'p' },
  ];
  assert.deepEqual(groupAppsForGrid(apps, { sortKey: 'name' })[0].apps.map((a) => a.slug), ['a', 'z']);
});

test('groupAppsForGrid: default sort preserves server order within a group', () => {
  const apps = [
    { slug: 'z', name: 'Zulu', project_slug: 'p' },
    { slug: 'a', name: 'Alpha', project_slug: 'p' },
  ];
  assert.deepEqual(groupAppsForGrid(apps, { sortKey: 'default' })[0].apps.map((a) => a.slug), ['z', 'a']);
});

test('groupAppsForGrid: status sort keeps the crashed-first order within a group', () => {
  const apps = [
    { slug: 'ok', name: 'Ok', project_slug: 'p', status: 'running' },
    { slug: 'bad', name: 'Bad', project_slug: 'p', status: 'crashed' },
  ];
  assert.deepEqual(groupAppsForGrid(apps, { sortKey: 'status' })[0].apps.map((a) => a.slug), ['bad', 'ok']);
});

test('groupAppsForGrid: an unknown sort key falls back to server order', () => {
  const apps = [{ slug: 'z', name: 'Z', project_slug: 'p' }, { slug: 'a', name: 'A', project_slug: 'p' }];
  assert.deepEqual(groupAppsForGrid(apps, { sortKey: 'nonsense' })[0].apps.map((a) => a.slug), ['z', 'a']);
});

test('groupAppsForGrid: same group order as the shared rule for the same input', () => {
  // The spec requires the grid and the Launchpad to agree. Both go through
  // groupApps, and this pins that they do rather than merely happening to match.
  // The Launchpad half of the same claim, over the same fixture, is asserted in
  // launchpad-model.test.js (Task 16).
  assert.deepEqual(
    groupAppsForGrid(FIXTURE, { sortKey: 'name' }).map((g) => g.project),
    groupApps(FIXTURE).map((g) => g.project),
  );
  assert.deepEqual(groupApps(FIXTURE).map((g) => g.project), GROUP_ORDER_EXPECTED);
});
