// Service accounts the People table must not let an admin edit or delete.
// Explicit principal metadata is authoritative; the reserved username remains
// as a compatibility fallback for older API responses. Credential role and
// scope are managed on the separate Service accounts surface.

export const RESERVED_USERNAMES = ['__deploy__'];

export function isReservedUser(user) {
  if (user && typeof user === 'object') {
    return user.principal_type === 'service_account' || RESERVED_USERNAMES.includes(user.username);
  }
  return RESERVED_USERNAMES.includes(user);
}

export const RESERVED_USER_HINT =
  'Service accounts are managed separately from people.';

// userRowCaps decides what a Users-table row may do, given the row's user and
// the signed-in user's id. Centralising this keeps the self-protection and the
// reserved-account protection consistent and testable.
export function userRowCaps(user, selfId) {
  const isSelf = user.id === selfId;
  const reserved = isReservedUser(user);
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
