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

// Readiness collapses the whole operator status vocabulary to the only thing a
// viewer cares about: can I open this or not. A viewer never needs to know that
// an app is sleeping, waking, deploying, or degraded - opening it Just Works
// (a hibernated app wakes transparently, a degraded one routes to a healthy
// replica). Status is the authoritative signal: an app can't be running /
// hibernated / deploying / degraded without a live bundle, and the reverse proxy
// routes purely on status. So openable apps carry no status label at all; only
// an app a viewer genuinely cannot open is flagged "Unavailable".
const OPENABLE = new Set([
  'running', 'healthy', 'hibernated', 'suspended', 'deploying', 'waking', 'degraded',
]);

// "Recently opened" is a shortcut for skipping a scan of a large catalog. When
// the whole catalog already fits in roughly two tile rows, the shortcut only
// echoes tiles the grid shows right below it, so it is suppressed at or under
// this size and the grouped grid stands alone.
const RECENT_MIN_CATALOG = 8;

/**
 * launchReadiness maps an app to its viewer-facing readiness. Openable apps get
 * an empty label (the tile is just a clean, clickable card); only an app the
 * viewer cannot open is labelled.
 * @returns {{openable:boolean, label:string}}
 */
export function launchReadiness(app) {
  const s = (app.status || '').toLowerCase();
  if (OPENABLE.has(s)) return { openable: true, label: '' };
  // crashed / stopped / unknown / never-deployed: nothing a viewer can open
  // (they cannot start or fix it), so the tile is shown, dimmed, and flagged.
  return { openable: false, label: 'Unavailable' };
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
