// Routing and permission rules for the app detail and settings pages.

// Internal renderer keys retained while the contextual sidebar exposes settings
// as individual pages. Manager-only keys keep their existing access rules.
export const TAB_ROUTES = ['overview', 'usage', 'logs', 'traces', 'deployments', 'schedules', 'configuration', 'data', 'access'];
export const MANAGER_ONLY_TABS = new Set(['logs', 'usage', 'configuration', 'data', 'access']);

// resolveDetailAccess decides which tab to render and whether the visitor must
// be redirected away. Returns { tab, redirect } where redirect is null (allowed)
// or { path, replace: true } (send them there instead).
//
// Rules, in the same precedence the mount used:
//   1. An unknown requested tab falls back to 'overview'.
//   2. A pure viewer (global role 'viewer') who does NOT manage this app has no
//      business on the operator detail page — return to the canonical Apps page.
//   3. A non-manager on a manager-only tab (logs/usage/settings/data) is sent
//      to the app root '/apps/<slug>'.
// Rule 2 is checked before rule 3, so a pure viewer requesting a manager-only
// tab is returned to '/apps', not to the app root.
export function resolveDetailAccess({ user, canManage, requestedTab, slug }) {
  const tab = TAB_ROUTES.includes(requestedTab) ? requestedTab : 'overview';

  if (user && user.role === 'viewer' && !canManage) {
    return { tab, redirect: { path: '/apps', replace: true } };
  }
  if (!canManage && MANAGER_ONLY_TABS.has(tab)) {
    return { tab, redirect: { path: `/apps/${slug}`, replace: true } };
  }
  return { tab, redirect: null };
}

// Settings pages share one mounted form so moving between them keeps drafts.
export const SETTINGS_PAGES = ['general', 'environment', 'resources', 'scaling', 'access', 'privacy'];

export function settingsRouteParams(params = {}) {
  const section = SETTINGS_PAGES.includes(params.section) ? params.section : 'general';
  return { ...params, tab: section === 'access' ? 'access' : 'configuration', section };
}

// Router destinations are rooted internal paths. Decode the slug exactly once,
// just as the router does, while ignoring query strings and fragment markers.
export function isSameAppSettingsDestination(path, slug) {
  if (typeof path !== 'string' || typeof slug !== 'string' || !slug || !path.startsWith('/') || path.startsWith('//') || path.includes('\\')) return false;
  try {
    const url = new URL(path, 'https://shinyhub.invalid');
    if (url.origin !== 'https://shinyhub.invalid') return false;
    const match = /^\/apps\/([^/]+)\/(?:settings\/([^/]+)|(configuration|access))$/.exec(url.pathname);
    if (!match || decodeURIComponent(match[1]) !== slug) return false;
    if (match[2] != null) {
      const section = decodeURIComponent(match[2]);
      // Unknown page names mount General; a nested path is not a settings page.
      return !!section && !section.includes('/');
    }
    return true;
  } catch {
    return false;
  }
}
