// launchpad-model.js - DOM-free logic for the viewer Launchpad (the consumer
// home). Turns the GET /api/apps list (already access-scoped server-side) plus
// a recently-opened slug list into the display model the Launchpad renders:
// per-app launch readiness, a visual identity (uploaded icon or monogram), a
// "recently opened" shortlist, and apps grouped by project. Kept DOM-free so it
// is unit-testable and the view stays a thin renderer.
import { appAvatar, appIconUrl } from './app-avatar.js';

// Re-export appAvatar so existing importers (and tests) keep their entry point
// while the implementation lives in the shared app-avatar module.
export { appAvatar } from './app-avatar.js';

// Readiness collapses the operator status vocabulary into the few things a
// viewer cares about: can I open it now, will it wake, or is it down. Status is
// the authoritative signal - an app can't be running/hibernated/degraded
// without a live bundle, and the reverse proxy routes purely on status - so we
// derive readiness from it directly rather than from soft counters
// (deploy_count) or a digest that legacy/pre-migration deployments may lack.
const READY = new Set(['running', 'healthy']);
const SLEEPING = new Set(['hibernated', 'suspended']);
const STARTING = new Set(['deploying', 'waking']);

// "Recently opened" is a shortcut for skipping a scan of a large catalog. When
// the whole catalog already fits in roughly two tile rows, the shortcut only
// echoes tiles the grid shows right below it, so it is suppressed at or under
// this size and the grouped grid stands alone.
const RECENT_MIN_CATALOG = 8;
// degraded is still routable (at least one healthy replica), so it stays
// openable but carries a warning.

/**
 * launchReadiness maps an app to its viewer-facing readiness.
 * @returns {{state:'ready'|'sleeping'|'starting'|'degraded'|'unavailable', label:string, openable:boolean}}
 */
export function launchReadiness(app) {
  const s = (app.status || '').toLowerCase();
  if (s === 'degraded') return { state: 'degraded', label: 'Degraded', openable: true };
  if (READY.has(s)) return { state: 'ready', label: 'Ready', openable: true };
  if (SLEEPING.has(s)) return { state: 'sleeping', label: 'Sleeping · opens in ~1s', openable: true };
  if (STARTING.has(s)) return { state: 'starting', label: 'Starting…', openable: true };
  // crashed / stopped / unknown / never-deployed: nothing a viewer can open
  // (they cannot start or fix it), so the tile is shown but not launchable.
  return { state: 'unavailable', label: 'Unavailable', openable: false };
}

/**
 * buildLaunchpadModel turns the apps list + recently-opened slugs into the
 * Launchpad model.
 * @param {Array<object>} apps         GET /api/apps payload (access-scoped)
 * @param {Array<string>} recentSlugs  most-recent-first slugs from localStorage
 * @returns {{
 *   total:number,
 *   recent:Array<object>,
 *   groups:Array<{project:string,apps:Array<object>}>,
 * }}
 */
export function buildLaunchpadModel(apps, recentSlugs) {
  const list = Array.isArray(apps) ? apps : [];
  const recent = Array.isArray(recentSlugs) ? recentSlugs : [];

  const tiles = list.map((app) => ({
    slug: app.slug,
    name: app.name || app.slug,
    description: app.description || '',
    project: app.project_slug || 'default',
    readiness: launchReadiness(app),
    avatar: appAvatar(app),
    // iconUrl is set when the app has an uploaded icon; the tile renders it in
    // place of the monogram (falling back to the monogram if it fails to load).
    iconUrl: appIconUrl(app),
  }));

  const bySlug = new Map(tiles.map((t) => [t.slug, t]));
  const recentTiles = tiles.length > RECENT_MIN_CATALOG
    ? recent.map((slug) => bySlug.get(slug)).filter(Boolean).slice(0, 6)
    : [];

  // Group by project, projects alphabetical, apps alphabetical within a project.
  const byProject = new Map();
  for (const t of tiles) {
    if (!byProject.has(t.project)) byProject.set(t.project, []);
    byProject.get(t.project).push(t);
  }
  const groups = [...byProject.keys()].sort().map((project) => ({
    project,
    apps: byProject.get(project).sort((a, b) => a.name.localeCompare(b.name)),
  }));

  return { total: tiles.length, recent: recentTiles, groups };
}
