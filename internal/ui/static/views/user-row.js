// Reserved / synthetic accounts the dashboard must not let an admin edit or
// delete. `__deploy__` is the identity the pre-shared deploy token authenticates
// as: it has no password and its role is governed by SHINYHUB_DEPLOY_TOKEN_ROLE,
// so "Reset password", role changes and "Delete" are meaningless (or actively
// misleading) for it. Kept here as a tiny pure module so it can be unit-tested.

export const RESERVED_USERNAMES = ['__deploy__'];

export function isReservedUser(username) {
  return RESERVED_USERNAMES.includes(username);
}

export const RESERVED_USER_HINT =
  'Token identity (SHINYHUB_DEPLOY_TOKEN) — managed via environment, not here.';

// userRowCaps decides what a Users-table row may do, given the row's user and
// the signed-in user's id. Centralising this keeps the self-protection and the
// reserved-account protection consistent and testable.
export function userRowCaps(user, selfId) {
  const isSelf = user.id === selfId;
  const reserved = isReservedUser(user.username);
  return {
    isSelf,
    reserved,
    canChangeRole: !isSelf && !reserved,
    canDelete: !isSelf && !reserved,
    canResetPassword: !reserved,
    roleHint: reserved ? RESERVED_USER_HINT : (isSelf ? 'You cannot change your own role' : ''),
    deleteHint: reserved ? RESERVED_USER_HINT : (isSelf ? 'You cannot delete yourself' : ''),
  };
}
