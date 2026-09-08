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
    canRevokeSessions: !reserved,
    roleHint: reserved ? RESERVED_USER_HINT : (isSelf ? 'You cannot change your own role' : ''),
    deleteHint: reserved ? RESERVED_USER_HINT : (isSelf ? 'You cannot delete yourself' : ''),
    revokeSessionsHint: reserved ? RESERVED_USER_HINT : '',
  };
}

export function supportSessionCaps(user, selfId, enabled = false) {
  const isSelf = user.id === selfId;
  const reserved = isReservedUser(user);
  return {
    canStart: enabled && !isSelf && !reserved && ['viewer', 'developer'].includes(user.role),
    hint: !enabled
      ? 'Support sessions are not enabled'
      : (isSelf ? 'You cannot start a support session as yourself'
        : (reserved || !['viewer', 'developer'].includes(user.role)
          ? 'Support sessions can target only human viewers or developers' : '')),
  };
}

// Effective permissions and their provenance are separate: automatic does not
// imply that a person signs in through SSO.
export function userRolePresentation(user) {
  const role = user.role || 'viewer';
  const label = role.charAt(0).toUpperCase() + role.slice(1);
  const manual = user.manual_role === undefined ? role : user.manual_role;
  return {
    selected: manual || '',
    automaticLabel: manual ? 'Use group/default role' : `${label} · Automatic`,
    sourceLabel: manual ? 'Manual override' : (user.role_source === 'sso' ? 'From group rules' : 'Default role'),
  };
}
