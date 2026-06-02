// fleet-health.js - pure helper for the admin fleet-health banner. Turns the
// GET /api/fleet/health envelope into a display model: an overall status class,
// a one-line headline, per-tier trouble chips, and the degraded-app list.
// No DOM dependency; importable from jsdom tests and app.js.

/**
 * summariseFleetHealth maps the fleet-health envelope to its display model.
 *
 * statusClass reuses the shared badge-<class> CSS:
 *   running (green) = healthy; stopped (amber) = warning (a worker is down but
 *   no replica is lost yet); lost (red) = degraded (lost replicas or degraded
 *   apps). The severity order is degraded > warning > healthy.
 *
 * @param {object|null} h  the /api/fleet/health response
 * @returns {{statusClass:string, statusLabel:string, headline:string,
 *            tierChips:Array<{tier:string,lost:number,workersDown:number}>,
 *            degraded:Array<object>}}
 */
export function summariseFleetHealth(h) {
  const env = h && typeof h === 'object' ? h : {};
  const apps = env.apps || {};
  const replicas = env.replicas || {};
  const workers = env.workers || null;
  const tiers = Array.isArray(env.tiers) ? env.tiers : [];
  const degraded = Array.isArray(env.degraded_apps) ? env.degraded_apps : [];

  const num = (v) => Number(v || 0);
  const lostReplicas = num(replicas.lost);
  const degradedApps = num(apps.degraded);
  const workersDown = workers ? num(workers.down) : 0;

  let statusClass, statusLabel;
  if (lostReplicas > 0 || degradedApps > 0) {
    statusClass = 'lost';
    statusLabel = 'degraded';
  } else if (workersDown > 0) {
    statusClass = 'stopped';
    statusLabel = 'warning';
  } else {
    statusClass = 'running';
    statusLabel = 'healthy';
  }

  const parts = [`${num(apps.total)} apps`, `${num(apps.running)} running`];
  if (degradedApps > 0) parts.push(`${degradedApps} degraded`);
  if (lostReplicas > 0) parts.push(`${lostReplicas} replicas lost`);
  if (workers) parts.push(`${num(workers.up)}/${num(workers.total)} workers up`);
  const headline = parts.join(' · ');

  const tierChips = tiers
    .filter((t) => num(t.replicas_lost) > 0 || num(t.workers_down) > 0)
    .map((t) => ({
      tier: t.tier,
      lost: num(t.replicas_lost),
      workersDown: num(t.workers_down),
    }));

  return { statusClass, statusLabel, headline, tierChips, degraded };
}

/**
 * degradedTooltip builds a one-line, human-readable list of the degraded apps
 * (which app, how many replicas lost, on which tier, and why) for the banner's
 * title/aria description. The banner shows tier-level chips at a glance; this
 * surfaces the actionable per-app detail without cluttering the layout.
 *
 * @param {{degraded:Array<{slug:string,tier:string,lost:number,reason:string}>}} summary
 * @param {number} [max]  cap before collapsing the tail into "+N more"
 * @returns {string}  empty string when nothing is degraded
 */
export function degradedTooltip(summary, max = 5) {
  const degraded = summary && Array.isArray(summary.degraded) ? summary.degraded : [];
  if (degraded.length === 0) return '';
  const shown = degraded.slice(0, max).map((d) => {
    let s = `${d.slug}: ${Number(d.lost || 0)} lost`;
    if (d.tier) s += ` on ${d.tier}`;
    if (d.reason) s += ` (${d.reason})`;
    return s;
  });
  let out = shown.join('; ');
  if (degraded.length > max) out += `; +${degraded.length - max} more`;
  return out;
}
