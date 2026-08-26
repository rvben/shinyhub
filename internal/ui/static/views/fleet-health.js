// fleet-health.js - pure helper for the admin fleet-health banner. Turns the
// GET /api/fleet/health envelope into a display model: an overall status class,
// a one-line headline, per-tier trouble chips, and actionable detail lists.
// No DOM dependency; importable from jsdom tests and app.js.

/**
 * summariseFleetHealth maps the fleet-health envelope to its display model.
 *
 * statusClass reuses the shared badge-<class> CSS:
 *   running (green) = healthy; stopped (amber) = warning (a worker is down but
 *   no replica is lost yet); lost (red) = degraded (lost replicas or degraded
 *   apps, stale schedules, or serving-data activations that need attention).
 *   The severity order is degraded > warning > healthy.
 *
 * @param {object|null} h  the /api/fleet/health response
 * @returns {{statusClass:string, statusLabel:string, headline:string,
 *            tierChips:Array<{tier:string,lost:number,workersDown:number}>,
 *            degraded:Array<object>, staleSchedules:Array<object>,
 *            activationAttentionItems:Array<object>, activationAttentionCount:number}}
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
  const crashedApps = num(apps.crashed);
  const workersDown = workers ? num(workers.down) : 0;
  const staleSchedules = Array.isArray(env.stale_schedule_list) ? env.stale_schedule_list : [];
  const staleCount = num(env.stale_schedules);
  const activationAttentionItems = Array.isArray(env.activation_attention_list) ? env.activation_attention_list : [];
  // The API count is uncapped while its actionable list is bounded. Be
  // defensive when reading older or partially populated response envelopes.
  const activationAttentionCount = Math.max(num(env.activation_attention), activationAttentionItems.length);
  const complete = env.complete !== false;
  const unavailable = Array.isArray(env.unavailable_components) ? env.unavailable_components : [];

  let statusClass, statusLabel;
  if (!complete) {
    statusClass = 'stopped';
    statusLabel = 'unknown';
  } else if (lostReplicas > 0 || degradedApps > 0 || crashedApps > 0 || staleCount > 0 || activationAttentionCount > 0) {
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
  // Idle: healthy elastic apps (grouped / per_session) with no live worker.
  // They boot one on the first request, so they are neither running nor a
  // problem; without this line a fleet of grouped apps reads "N apps, 0
  // running" with nothing accounting for the gap.
  const idleApps = num(apps.idle);
  if (idleApps > 0) parts.push(`${idleApps} idle`);
  if (crashedApps > 0) parts.push(`${crashedApps} crashed`);
  if (degradedApps > 0) parts.push(`${degradedApps} degraded`);
  if (lostReplicas > 0) parts.push(`${lostReplicas} replicas lost`);
  if (workers) parts.push(`${num(workers.up)}/${num(workers.total)} workers up`);
  if (staleCount > 0) parts.push(`${staleCount} schedule${staleCount === 1 ? '' : 's'} stale`);
  if (activationAttentionCount > 0) {
    parts.push(`${activationAttentionCount} data activation${activationAttentionCount === 1 ? '' : 's'} need${activationAttentionCount === 1 ? 's' : ''} attention`);
  }
  if (!complete) parts.push(`health unavailable: ${unavailable.join(', ') || 'incomplete observation'}`);
  const headline = parts.join(' · ');

  const tierChips = tiers
    .filter((t) => num(t.replicas_lost) > 0 || num(t.workers_down) > 0)
    .map((t) => ({
      tier: t.tier,
      lost: num(t.replicas_lost),
      workersDown: num(t.workers_down),
    }));

  return {
    statusClass,
    statusLabel,
    headline,
    tierChips,
    degraded,
    staleSchedules,
    activationAttentionItems,
    activationAttentionCount,
    complete,
    unavailable,
  };
}

function compactAge(seconds) {
  const age = Number(seconds);
  if (!Number.isFinite(age) || age < 0) return '';
  if (age < 60) return age < 1 ? 'just now' : `${Math.floor(age)}s old`;
  if (age < 3600) return `${Math.floor(age / 60)}m old`;
  if (age < 86400) return `${Math.floor(age / 3600)}h old`;
  return `${Math.floor(age / 86400)}d old`;
}

/**
 * activationAttentionTooltip names each schedule whose serving-data activation
 * needs operator attention. The headline carries the count; this detail is
 * reused by the safe title and accessible name in app.js.
 *
 * @param {{activationAttentionItems:Array<object>, activationAttentionCount:number}} summary
 * @param {number} [max] cap before collapsing the tail into "+N more"
 * @returns {string} empty string when no activation needs attention
 */
export function activationAttentionTooltip(summary, max = 5) {
  const items = summary && Array.isArray(summary.activationAttentionItems)
    ? summary.activationAttentionItems
    : [];
  const total = Math.max(Number(summary?.activationAttentionCount || 0), items.length);
  if (total === 0) return '';

  const shown = items.slice(0, max).map((item) => {
    const status = String(item.status || 'unknown').replaceAll('_', ' ');
    const details = [status];
    if (item.phase) details.push(`phase ${String(item.phase).replaceAll('_', ' ')}`);
    const age = compactAge(item.age_seconds);
    if (age) details.push(age);
    if (item.error) details.push(`error: ${item.error}`);
    return `${item.slug || 'unknown app'}: ${item.schedule || 'unknown schedule'} (${details.join(', ')})`;
  });
  if (total > shown.length) shown.push(`+${total - shown.length} more`);
  return shown.join('; ');
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
