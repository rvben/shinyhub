import { test } from 'node:test';
import assert from 'node:assert/strict';
import { groupApps, compareGroups, projectKeyOf, UNGROUPED } from '../static/views/project-groups.js';

test('projectKeyOf: missing, empty and whitespace slugs are all ungrouped', () => {
  assert.equal(projectKeyOf({ slug: 'a' }), UNGROUPED);
  assert.equal(projectKeyOf({ slug: 'a', project_slug: '' }), UNGROUPED);
  assert.equal(projectKeyOf({ slug: 'a', project_slug: '   ' }), UNGROUPED);
  assert.equal(projectKeyOf({ slug: 'a', project_slug: ' team ' }), 'team');
  assert.equal(projectKeyOf(null), UNGROUPED);
});

test('groupApps: ungrouped first, named projects by display name', () => {
  const apps = [
    { slug: 'c', name: 'C', project_slug: 'zeta', project_name: 'Aaa Reports' },
    { slug: 'a', name: 'A' },
    { slug: 'b', name: 'B', project_slug: 'alpha' },
  ];
  const groups = groupApps(apps);
  // Ordered by DISPLAY name, not slug: as slugs "alpha" sorts before "zeta", but
  // "zeta" is named "Aaa Reports" and so sorts before the unnamed "alpha",
  // whose display name falls back to its slug - proving the group order comes
  // from the name, not the slug.
  assert.deepEqual(groups.map((g) => g.project), [UNGROUPED, 'zeta', 'alpha']);
  assert.equal(groups[1].name, 'Aaa Reports');
  assert.equal(groups[2].name, 'alpha');
});

test('groupApps: no synthetic default project', () => {
  const groups = groupApps([{ slug: 'a', name: 'A' }, { slug: 'b', name: 'B' }]);
  assert.equal(groups.length, 1);
  assert.equal(groups[0].project, UNGROUPED);
  assert.equal(groups[0].name, '');
});

test('groupApps: a real project literally named default is not the ungrouped bucket', () => {
  // The 050 migration collapses the legacy 'default' value, but a server can
  // still hold an app in a project a user deliberately called "default"; it is
  // an ordinary named project, never the ungrouped bucket.
  const groups = groupApps([{ slug: 'a', name: 'A', project_slug: 'default' }]);
  assert.deepEqual(groups.map((g) => g.project), ['default']);
});

test('groupApps: display metadata comes off the app payload, and projects overrides win', () => {
  const apps = [{ slug: 'a', name: 'A', project_slug: 'p', project_name: 'Stale', project_icon_emoji: '📊' }];
  assert.equal(groupApps(apps)[0].name, 'Stale');
  assert.equal(groupApps(apps)[0].iconEmoji, '📊');
  // The overrides let an inline project rename repaint immediately, before the
  // apps list has been refetched.
  const fresh = groupApps(apps, { projects: [{ slug: 'p', name: 'Fresh', icon_emoji: '🚀' }] });
  assert.equal(fresh[0].name, 'Fresh');
  assert.equal(fresh[0].iconEmoji, '🚀');
});

test('groupApps: apps sort by display name within a group by default', () => {
  const groups = groupApps([
    { slug: 'b', name: 'Beta', project_slug: 'p' },
    { slug: 'a', name: 'Alpha', project_slug: 'p' },
  ]);
  assert.deepEqual(groups[0].apps.map((a) => a.slug), ['a', 'b']);
});

test('groupApps: sortWithin replaces the in-group order, never the group order', () => {
  const groups = groupApps(
    [
      { slug: 'b', name: 'Beta', project_slug: 'zz' },
      { slug: 'a', name: 'Alpha', project_slug: 'zz' },
      { slug: 'c', name: 'Gamma' },
    ],
    { sortWithin: null },
  );
  // sortWithin: null means "keep the caller's order" (the grid's Default order).
  assert.deepEqual(groups.map((g) => g.project), [UNGROUPED, 'zz']);
  assert.deepEqual(groups[1].apps.map((a) => a.slug), ['b', 'a']);
});

test('groupApps: does not mutate the caller array even when a group gets sorted', () => {
  // Each bucket is a fresh array (buckets.set(key, []) then push()), so the
  // caller's array is never the one being sorted. This test fails if groupApps
  // ever sorts apps in place instead of building fresh buckets.
  const apps = [
    { slug: 'b', name: 'Beta', project_slug: 'p' },
    { slug: 'a', name: 'Alpha', project_slug: 'p' },
  ];
  const groups = groupApps(apps);
  assert.deepEqual(groups[0].apps.map((a) => a.slug), ['a', 'b']);
  assert.deepEqual(apps.map((a) => a.slug), ['b', 'a']);
});

test('compareGroups: ungrouped wins against any named project', () => {
  assert.ok(compareGroups({ project: UNGROUPED, name: '' }, { project: 'a', name: 'A' }) < 0);
  assert.ok(compareGroups({ project: 'a', name: 'A' }, { project: UNGROUPED, name: '' }) > 0);
});

test('compareGroups: equal display names break the tie on slug so the order is total', () => {
  // Two projects can share a display name; without the tiebreak the order would
  // depend on input order and section headers would swap between renders.
  assert.ok(compareGroups({ project: 'b', name: 'Same' }, { project: 'a', name: 'Same' }) > 0);
});
