// project-groups.js - the single grouping and group-ordering rule shared by the
// Launchpad, the sidebar and the operator grid. Three views group the same apps
// by project; without one shared rule they drift, and they already had (the
// sidebar put ungrouped first while the Launchpad invented a synthetic
// "default" project that sorted among the real ones).
//
// DOM-free by design so it is unit-testable and every consumer stays a thin
// renderer.

// Ungrouped is the empty slug. It is a real state, not a project: an app is
// ungrouped until someone puts it in a project. Never render it as a project
// named "default" - migration 050 exists precisely to retire that value.
export const UNGROUPED = '';

// projectKeyOf normalizes an app's project slug. Missing, empty and
// whitespace-only all mean ungrouped, matching the server, which stores "".
export function projectKeyOf(app) {
  const raw = app && app.project_slug ? String(app.project_slug) : '';
  return raw.trim();
}

// displayName falls back to the slug so an unnamed project still shows
// something meaningful, and an ungrouped bucket has no name at all (its heading
// text is the caller's business: the Launchpad says "All apps", the sidebar
// suppresses it).
function displayName(key, name) {
  if (key === UNGROUPED) return '';
  const n = (name || '').trim();
  return n || key;
}

// compareGroups is THE group-ordering rule. Ungrouped first, then named
// projects by display name. The slug tiebreak makes the order total: two
// projects may share a display name, and without it the order would depend on
// input order and section headings would swap between renders.
export function compareGroups(a, b) {
  const au = a.project === UNGROUPED;
  const bu = b.project === UNGROUPED;
  if (au !== bu) return au ? -1 : 1;
  const byName = String(a.name || '').localeCompare(String(b.name || ''));
  return byName !== 0 ? byName : String(a.project).localeCompare(String(b.project));
}

const byDisplayName = (a, b) =>
  String((a && (a.name || a.slug)) || '').localeCompare(String((b && (b.name || b.slug)) || ''));

/**
 * groupApps partitions apps by project and orders the groups by the shared rule.
 * @param {Array<object>} apps  apps carrying project_slug, project_name and
 *   project_icon_emoji (the GET /api/apps payload)
 * @param {object} [opts]
 * @param {Array<object>} [opts.projects]  optional GET /api/projects rows whose
 *   name/icon_emoji override the app payload's copy, so an inline rename
 *   repaints before the apps list is refetched
 * @param {?function} [opts.sortWithin]  in-group comparator; null keeps the
 *   caller's order. Defaults to display name.
 * @returns {Array<{project:string,name:string,iconEmoji:string,apps:Array<object>}>}
 */
export function groupApps(apps, opts) {
  const o = opts || {};
  const overrides = new Map();
  for (const p of o.projects || []) {
    if (p && p.slug) overrides.set(String(p.slug), p);
  }
  const sortWithin = o.sortWithin === undefined ? byDisplayName : o.sortWithin;

  const buckets = new Map();
  for (const app of apps || []) {
    if (!app) continue;
    const key = projectKeyOf(app);
    if (!buckets.has(key)) buckets.set(key, []);
    buckets.get(key).push(app);
  }

  const groups = [];
  for (const [key, list] of buckets) {
    const ov = overrides.get(key);
    const first = list[0] || {};
    const name = ov ? ov.name : first.project_name;
    const icon = ov ? ov.icon_emoji : first.project_icon_emoji;
    groups.push({
      project: key,
      name: displayName(key, name),
      iconEmoji: key === UNGROUPED ? '' : (icon || ''),
      // Each bucket is a fresh array (buckets.set(key, []) then push()), so the
      // list sorted here is never the caller's apps array; that protection
      // comes from the bucketing, not from slice(). slice() is a defensive copy
      // at the module boundary: nothing re-reads a bucket once its group is
      // returned, so it guards nothing today and keeps that true if anything
      // ever does.
      apps: sortWithin ? list.slice().sort(sortWithin) : list.slice(),
    });
  }
  return groups.sort(compareGroups);
}
