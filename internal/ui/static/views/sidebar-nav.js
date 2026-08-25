// Sidebar app navigator: groups the visible apps by project and renders a
// quick-switch list under the primary section nav.
//
// Pure + DOM-free-by-default so the grouping and active-state logic are
// unit-testable; renderSidebarApps takes an explicit document. The status badge
// is INJECTED (badgeFor) rather than imported so this module stays a leaf with
// no DOM-module /static import (matching the codebase's testable-module
// convention); it does still import the pure, DOM-free project-groups.js
// comparator, the same class of dependency as launchpad-model.js importing
// app-avatar.js. app.js injects the shared appCardBadge model, so a
// failed-only deploy shows "Failed" and a never-deployed app shows "Awaiting
// deploy" - never mislabelled.
import { groupApps, UNGROUPED } from './project-groups.js';
import { createGroupDisclosure } from './group-disclosure.js';

// The server computes canManageApps because a global viewer can still manage an
// individual app through ownership, direct membership, or an IdP group grant.
// Keeping this policy pure makes every role/session combination testable.
export function appsSurfaceForSession(role, canManageApps = false) {
  return role === 'viewer' && !canManageApps ? 'viewer' : 'manage';
}

export function homeSurfaceForSession(role) {
  return role === 'admin' || role === 'operator' ? 'overview' : 'apps';
}

// Section navigation has a contextual home: operators see Overview at `/`,
// while viewers and developers land on their role-appropriate Apps surface.
// `/home` is the stable alias used by branded landing pages. `/launchpad` is a
// backwards-compatible alias that redirects to Apps.
export function isPrimaryNavActive(hrefPath, currentPath, role) {
  const href = hrefPath || '';
  const current = currentPath || '/';
  if (current === '/' || current === '/home') {
    return homeSurfaceForSession(role) === 'overview' ? href === '/' : href === '/apps';
  }
  if (current === '/launchpad') return href === '/apps';
  if (current.startsWith('/apps/') && href === '/apps') return true;
  return href === current;
}

// Group apps by project_slug. Apps with no project_slug render UNGROUPED at the
// top (project: null); named projects follow under their display-name heading;
// apps are sorted by display name within each group. The ordering is the shared
// rule in project-groups.js, so the sidebar, the Launchpad and the operator
// grid cannot disagree about group order.
export function groupAppsByProject(apps) {
  return groupApps(apps).map((g) => ({
    // null rather than "" for the ungrouped group: the renderer below and this
    // module's callers already branch on it.
    project: g.project === UNGROUPED ? null : g.project,
    name: g.name,
    iconEmoji: g.iconEmoji,
    apps: g.apps,
  }));
}

// An app row is active for its own page and any nested tab. The trailing-slash
// check keeps the boundary at a path segment so /apps/foo does not match
// /apps/foobar.
export function isSidebarAppActive(href, currentPath) {
  const p = currentPath || '';
  return p === href || p.startsWith(href + '/');
}

// View model for one app row. badgeFor(app) -> {cls, text} is injected (app.js
// passes the shared appCardBadge model) so this module stays a testable leaf.
export function sidebarAppModel(app, currentPath, badgeFor) {
  const badge = (typeof badgeFor === 'function')
    ? badgeFor(app)
    : { cls: `badge badge-${app.status || ''}`, text: app.status || '' };
  const statusKey = String(badge.cls).split(/\s+/).pop().replace('badge-', '');
  const href = `/apps/${app.slug}`;
  return {
    slug: app.slug,
    name: app.name || app.slug,
    href,
    dotClass: `sb-dot sb-dot-${statusKey}`,
    statusLabel: badge.text,
    active: isSidebarAppActive(href, currentPath || ''),
  };
}

// Render the grouped app list into container. The renderer OWNS the initial
// active state (marked from currentPath) so a deep-link load highlights the
// right row even when the list arrives after the route has mounted.
export function renderSidebarApps(container, apps, currentPath, badgeFor, doc) {
  const d = doc || (typeof document !== 'undefined' ? document : null);
  container.textContent = '';
  const list = (apps || []).filter(Boolean);
  if (!list.length) {
    // The active Overview/Launchpad owns the useful first-run message and CTA.
    // Leave this secondary navigation region empty instead of repeating it.
    return;
  }

  const heading = d.createElement('p');
  heading.className = 'sidebar-apps-heading';
  const headingLabel = d.createElement('span');
  headingLabel.textContent = 'Apps';
  const headingCount = d.createElement('span');
  headingCount.className = 'sidebar-apps-count';
  headingCount.textContent = String(list.length);
  headingCount.setAttribute('aria-hidden', 'true');
  heading.appendChild(headingLabel);
  heading.appendChild(headingCount);
  container.appendChild(heading);

  const groups = groupAppsByProject(list);
  for (const group of groups) {
    // Keep a lone ungrouped list quiet. Once projects exist, name the loose
    // bucket "Other apps" so its place in the tree is unambiguous.
    const grouped = !!group.project || groups.length > 1;
    let groupBody = container;
    if (grouped) {
      const label = group.project ? group.name : 'Other apps';
      const active = group.apps.some((app) =>
        isSidebarAppActive(`/apps/${app.slug}`, currentPath || ''));
      const disclosure = createGroupDisclosure(d, {
        view: 'sidebar',
        groupKey: group.project || UNGROUPED,
        label,
        count: group.apps.length,
        iconEmoji: group.iconEmoji,
        classPrefix: 'sidebar-project',
        forceExpanded: active,
      });
      disclosure.toggle.title = group.project && group.name !== group.project
        ? `${group.name} (${group.project})`
        : label;
      container.appendChild(disclosure.root);
      groupBody = disclosure.body;
    }
    for (const app of group.apps) {
      const m = sidebarAppModel(app, currentPath, badgeFor);
      const a = d.createElement('a');
      a.setAttribute('href', m.href);
      a.setAttribute('data-nav', '');
      a.setAttribute('data-app-slug', m.slug);
      a.className = 'sidebar-app' + (m.active ? ' active' : '');
      if (m.active) a.setAttribute('aria-current', 'page');
      // The status dot is decorative (aria-hidden); fold any status into the
      // link's accessible name so screen readers announce e.g. "demo, Failed".
      // When there is no status word (viewer rows for openable apps), the name
      // stands alone - no trailing comma, no status tooltip.
      a.setAttribute('aria-label', m.statusLabel ? `${m.name}, ${m.statusLabel}` : m.name);

      const dot = d.createElement('span');
      dot.className = m.dotClass;
      if (m.statusLabel) dot.title = m.statusLabel;
      dot.setAttribute('aria-hidden', 'true');

      const name = d.createElement('span');
      name.className = 'sidebar-app-name';
      name.textContent = m.name;
      name.title = m.name;

      a.appendChild(dot);
      a.appendChild(name);
      groupBody.appendChild(a);
    }
  }
}

// Update the active row in place on navigation (no rebuild). Scoped to the
// container's own app links so it never touches section nav, cards, or folder
// tabs.
export function highlightSidebarApp(container, currentPath) {
  if (!container) return;
  for (const a of container.querySelectorAll('a[data-nav]')) {
    const href = a.getAttribute('href') || '';
    const active = isSidebarAppActive(href, currentPath || '');
    a.classList.toggle('active', active);
    if (active) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
    if (active) {
      const group = a.closest('.group-disclosure');
      if (group) {
        const body = group.querySelector('.group-disclosure-body');
        const toggle = group.querySelector('.group-disclosure-toggle');
        if (body) body.hidden = false;
        if (toggle) toggle.setAttribute('aria-expanded', 'true');
        group.classList.remove('is-collapsed');
      }
    }
  }
}
