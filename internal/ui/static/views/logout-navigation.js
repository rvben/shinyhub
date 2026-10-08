const LOGOUT_BRIDGE = '/api/auth/forward-auth/logout';

export function logoutTarget(providers, fallback = '/') {
  return providers?.forward_auth_logout === true ? LOGOUT_BRIDGE : fallback;
}

// The edge may redirect a fetch to its IdP. Full navigation completes that
// sign-in without clearing ShinyHub's opt-out or silently granting identity.
export async function reconnectForwardAuth(api, providers) {
  if (providers?.forward_auth_resume !== true) return 'reload';
  let response;
  try {
    response = await api('/api/auth/forward-auth/resume', { method: 'POST', redirect: 'manual' });
  } catch {
    return 'upstream';
  }
  if (response.type === 'opaqueredirect' || response.status === 401) return 'upstream';
  if (!response.ok) throw new Error(`Reconnect failed (${response.status})`);
  return 'reload';
}
