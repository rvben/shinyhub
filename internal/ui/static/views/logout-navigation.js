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

// Local revocation succeeds before the optional provider redirect is returned.
export async function browserLogoutTarget(response, providers, fallback = '/', { providerLogout = true } = {}) {
  if (!response.ok && response.status !== 401) throw new Error(`Logout failed (${response.status})`);
  const data = response.status === 200 ? await response.json().catch(() => ({})) : {};
  if (providerLogout && typeof data?.logout_url === 'string' && /^https?:\/\//.test(data.logout_url)) return data.logout_url;
  if (providers?.oidc?.auto_redirect === true) {
    const target = new URL(fallback, 'https://shinyhub.invalid');
    target.searchParams.set('signed_out', '1');
    return target.pathname + target.search + target.hash;
  }
  return logoutTarget(providers, fallback);
}
