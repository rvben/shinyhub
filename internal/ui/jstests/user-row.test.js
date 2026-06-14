import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isReservedUser, userRowCaps, RESERVED_USER_HINT } from '../static/views/user-row.js';

// The Users table must not let an admin "manage" the synthetic deploy-token
// identity (__deploy__): it has no password and its role comes from the
// environment, so role change / reset-password / delete are meaningless there.
// Self-protection (can't demote or delete yourself) is preserved alongside it.

test('isReservedUser only matches the deploy-token identity', () => {
  assert.equal(isReservedUser('__deploy__'), true);
  assert.equal(isReservedUser('ruben'), false);
  assert.equal(isReservedUser('admin'), false);
  assert.equal(isReservedUser(''), false);
});

test('reserved account row is fully read-only', () => {
  const caps = userRowCaps({ id: 5, username: '__deploy__' }, 1);
  assert.equal(caps.reserved, true);
  assert.equal(caps.canChangeRole, false);
  assert.equal(caps.canDelete, false);
  assert.equal(caps.canResetPassword, false);
  assert.equal(caps.roleHint, RESERVED_USER_HINT);
  assert.equal(caps.deleteHint, RESERVED_USER_HINT);
});

test('your own row cannot self-demote or self-delete but can reset password', () => {
  const caps = userRowCaps({ id: 1, username: 'admin' }, 1);
  assert.equal(caps.isSelf, true);
  assert.equal(caps.canChangeRole, false);
  assert.equal(caps.canDelete, false);
  assert.equal(caps.canResetPassword, true);
  assert.equal(caps.roleHint, 'You cannot change your own role');
  assert.equal(caps.deleteHint, 'You cannot delete yourself');
});

test('an ordinary other user is fully manageable', () => {
  const caps = userRowCaps({ id: 2, username: 'alice' }, 1);
  assert.equal(caps.canChangeRole, true);
  assert.equal(caps.canDelete, true);
  assert.equal(caps.canResetPassword, true);
  assert.equal(caps.roleHint, '');
  assert.equal(caps.deleteHint, '');
});
