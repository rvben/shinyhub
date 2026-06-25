// Sidebar app navigator: groups the visible apps by project and renders a
// quick-switch list under the primary section nav.
//
// Pure + DOM-free-by-default so the grouping and active-state logic are
// unit-testable; renderSidebarApps takes an explicit document. The status badge
// is INJECTED (badgeFor) rather than imported so this module stays a leaf with
// no /static import (matching the codebase's testable-module convention). app.js
// injects the shared appCardBadge model, so a failed-only deploy shows "Failed"
// and a never-deployed app shows "Awaiting deploy" — never mislabelled.

// Group apps by project_slug. Apps with no project_slug render UNGROUPED at the
// top (project: null); named projects follow under their heading, sorted by
// name; apps are sorted by display name within each group.
export function groupAppsByProject(apps) {
  const ungrouped = [];
  const byProject = new Map();
  for (const app of apps || []) {
    const proj = (app && app.project_slug ? String(app.project_slug) : '').trim();
    if (!proj) { ungrouped.push(app); continue; }
    if (!byProject.has(proj)) byProject.set(proj, []);
    byProject.get(proj).push(app);
  }
  const label = (a) => String((a && (a.name || a.slug)) || '');
  const byName = (a, b) => label(a).localeCompare(label(b));
  const groups = [];
  if (ungrouped.length) groups.push({ project: null, apps: ungrouped.slice().sort(byName) });
  for (const proj of [...byProject.keys()].sort((a, b) => a.localeCompare(b))) {
    groups.push({ project: proj, apps: byProject.get(proj).slice().sort(byName) });
  }
  return groups;
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
    const empty = d.createElement('p');
    empty.className = 'sidebar-apps-empty';
    empty.textContent = 'No apps yet';
    container.appendChild(empty);
    return;
  }

  const heading = d.createElement('p');
  heading.className = 'sidebar-apps-heading';
  heading.textContent = 'Apps';
  container.appendChild(heading);

  for (const group of groupAppsByProject(list)) {
    if (group.project) {
      const gh = d.createElement('p');
      gh.className = 'sidebar-project';
      gh.textContent = group.project;
      gh.title = group.project;
      container.appendChild(gh);
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
      container.appendChild(a);
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
  }
}
