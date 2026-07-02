// Pure routing/visibility decisions for the app-detail page. Kept DOM-free so it
// can be unit-tested with node:test; the view (app-detail.js) applies the result
// to the tab elements. These decisions used to live inline in mountAppDetail.

// The tab order shown in the settings tab strip, and the tabs that require
// manage rights on the app. Single source of truth for both app-detail.js and
// this module's callers.
export const TAB_ROUTES = ['overview', 'logs', 'traces', 'deployments', 'configuration', 'data', 'access'];
export const MANAGER_ONLY_TABS = new Set(['configuration', 'data', 'access']);

// resolveDetailAccess decides which tab to render and whether the visitor must
// be redirected away. Returns { tab, redirect } where redirect is null (allowed)
// or { path, replace: true } (send them there instead).
//
// Rules, in the same precedence the mount used:
//   1. An unknown requested tab falls back to 'overview'.
//   2. A pure viewer (global role 'viewer') who does NOT manage this app has no
//      business on the operator detail page — bounce to '/' (the Launchpad).
//   3. A non-manager on a manager-only tab (configuration/data/access) is sent
//      to the app root '/apps/<slug>'.
// Rule 2 is checked before rule 3, so a pure viewer requesting a manager-only
// tab is bounced to '/', not to the app root.
export function resolveDetailAccess({ user, canManage, requestedTab, slug }) {
  const tab = TAB_ROUTES.includes(requestedTab) ? requestedTab : 'overview';

  if (user && user.role === 'viewer' && !canManage) {
    return { tab, redirect: { path: '/', replace: true } };
  }
  if (!canManage && MANAGER_ONLY_TABS.has(tab)) {
    return { tab, redirect: { path: `/apps/${slug}`, replace: true } };
  }
  return { tab, redirect: null };
}

// tabViewModels builds the per-tab render model for the settings tab strip, in
// TAB_ROUTES order. The view applies each field to the matching tab element:
//   hidden       — manager-only tabs are hidden from non-managers
//   href         — overview keeps the bare /apps/<slug>; others carry the segment
//                  (so middle-click / cmd-click open real URLs)
//   active        — the current tab
//   ariaSelected  — mirrors active for the tablist ARIA state
//   tabindex      — roving tabindex: only the active tab is in the page Tab order
//   ariaCurrent   — 'page' on the active tab, null otherwise
export function tabViewModels(slug, activeTab, canManage) {
  return TAB_ROUTES.map((route) => {
    const active = route === activeTab;
    return {
      route,
      hidden: !canManage && MANAGER_ONLY_TABS.has(route),
      href: route === 'overview' ? `/apps/${slug}` : `/apps/${slug}/${route}`,
      active,
      ariaSelected: active,
      tabindex: active ? '0' : '-1',
      ariaCurrent: active ? 'page' : null,
    };
  });
}
