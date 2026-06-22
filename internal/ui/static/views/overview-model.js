// overview-model.js - DOM-free logic for the operator Overview (the dashboard
// home). Turns the GET /api/apps list (plus optional live metrics) into the
// display model the Overview view renders: a one-line fleet verdict, the
// status distribution for the pulse bar, the apps that need attention, and a
// fleet resource summary. Kept DOM-free so it is unit-testable and so the view
// stays a thin renderer over a tested model.

import { aggregateMetrics } from './stat-format.js';

// Wire statuses grouped into the four pulse buckets. Anything unmapped counts
// as "idle" (stopped/unknown): present but not serving, and not an alarm.
const HEALTHY = new Set(['running', 'healthy']);
const SLEEPING = new Set(['hibernated', 'suspended']);
const TRANSIENT = new Set(['deploying', 'waking']);
const ATTENTION = new Set(['crashed', 'degraded']);

// pulseOrder is the segment order in the status bar: healthy leads, attention
// anchors the far end so trouble reads at the same edge every time.
export const pulseOrder = ['healthy', 'transient', 'sleeping', 'idle', 'attention'];

export const pulseMeta = {
  healthy: { label: 'Running', cssVar: '--green' },
  transient: { label: 'Working', cssVar: '--cyan-bright' },
  sleeping: { label: 'Sleeping', cssVar: '--standby' },
  idle: { label: 'Idle', cssVar: '--text-muted' },
  attention: { label: 'Needs attention', cssVar: '--red' },
};

function bucketOf(status) {
  const s = (status || 'unknown').toLowerCase();
  if (ATTENTION.has(s)) return 'attention';
  if (HEALTHY.has(s)) return 'healthy';
  if (TRANSIENT.has(s)) return 'transient';
  if (SLEEPING.has(s)) return 'sleeping';
  return 'idle';
}

const MIB = 1024 * 1024;

// nearLimitFraction is the usage-vs-limit ratio above which an app is flagged as
// approaching its memory ceiling. 0.85 leaves headroom to act before an OOM.
const nearLimitFraction = 0.85;

/**
 * buildOverviewModel maps the apps list and a per-slug live-metrics map into the
 * Overview display model.
 *
 * @param {Array<object>} apps        GET /api/apps payload
 * @param {Object<string,object>} metricsBySlug  slug -> { cpu_percent, rss_bytes }
 * @returns {{
 *   total:number,
 *   counts:Record<string,number>,
 *   segments:Array<{key:string,label:string,cssVar:string,count:number}>,
 *   verdict:{tone:'nominal'|'warning'|'critical',headline:string,detail:string},
 *   attention:Array<{slug:string,name:string,status:string,reason:string}>,
 *   resources:{cpuPercent:number,rssBytes:number,running:number,
 *              nearLimit:Array<{slug:string,name:string,usedBytes:number,limitBytes:number,fraction:number}>}
 * }}
 */
export function buildOverviewModel(apps, metricsBySlug) {
  const list = Array.isArray(apps) ? apps : [];
  const metrics = metricsBySlug && typeof metricsBySlug === 'object' ? metricsBySlug : {};

  const counts = { healthy: 0, transient: 0, sleeping: 0, idle: 0, attention: 0 };
  const attention = [];
  let cpuPercent = 0;
  let rssBytes = 0;
  let running = 0;
  const nearLimit = [];

  for (const app of list) {
    // An app needs attention when its status is crashed/degraded OR its most
    // recent deployment failed (which leaves the app "stopped" - indistinguish-
    // able from an intentionally-stopped app by status alone).
    const attn = needsAttention(app);
    const bucket = attn ? 'attention' : bucketOf(app.status);
    counts[bucket] += 1;
    if (attn) {
      attention.push({
        slug: app.slug,
        name: app.name || app.slug,
        status: app.status,
        reason: attentionReason(app),
        app,
      });
    }

    const m = metrics[app.slug];
    if (m) {
      const { cpu, rss, runningCount } = aggregateMetrics(m);
      if (cpu > 0 || rss > 0) running += 1;
      cpuPercent += cpu;
      rssBytes += rss;
      // memory_limit_mb is the PER-REPLICA cgroup ceiling, while rss is summed
      // across replicas, so compare against the fleet capacity (limit x running
      // replicas). Otherwise a scaled app reads as over-limit when each replica
      // is well within its own cap.
      const perReplicaBytes = Number(app.memory_limit_mb || 0) * MIB;
      if (perReplicaBytes > 0 && rss > 0) {
        const limitBytes = perReplicaBytes * Math.max(1, runningCount);
        const fraction = rss / limitBytes;
        if (fraction >= nearLimitFraction) {
          nearLimit.push({ slug: app.slug, name: app.name || app.slug, usedBytes: rss, limitBytes, fraction });
        }
      }
    }
  }

  nearLimit.sort((a, b) => b.fraction - a.fraction);

  const segments = pulseOrder.map((key) => ({
    key,
    label: pulseMeta[key].label,
    cssVar: pulseMeta[key].cssVar,
    count: counts[key],
  }));

  return {
    total: list.length,
    counts,
    segments,
    verdict: verdictFor(list.length, counts, attention),
    attention,
    resources: { cpuPercent, rssBytes, running, nearLimit },
  };
}

function verdictFor(total, counts, attention) {
  if (total === 0) {
    return { tone: 'nominal', headline: 'No apps deployed yet', detail: 'Deploy your first Shiny app to see it here.' };
  }
  if (attention.length > 0) {
    const n = attention.length;
    return {
      tone: 'critical',
      headline: n === 1 ? '1 app needs attention' : `${n} apps need attention`,
      detail: attention.map((a) => a.slug).slice(0, 3).join(', ') + (n > 3 ? ` +${n - 3} more` : ''),
    };
  }
  const live = counts.healthy + counts.transient;
  return {
    tone: 'nominal',
    headline: 'All systems nominal',
    detail: summaryLine(total, counts, live),
  };
}

function summaryLine(total, counts, live) {
  const parts = [`${total} ${total === 1 ? 'app' : 'apps'}`];
  if (live) parts.push(`${live} running`);
  if (counts.sleeping) parts.push(`${counts.sleeping} sleeping`);
  if (counts.idle) parts.push(`${counts.idle} idle`);
  return parts.join(' · ');
}

function needsAttention(app) {
  const s = (app.status || '').toLowerCase();
  if (ATTENTION.has(s)) return true;
  return (app.last_deployment_status || '').toLowerCase() === 'failed';
}

// attentionReason is a concise, one-line cause for the attention row. The full
// detail (a crash traceback, the failed-deploy log) lives on the app's detail
// page, which the row links to.
function attentionReason(app) {
  const s = (app.status || '').toLowerCase();
  if (s === 'crashed') return 'Crashed on startup';
  if (s === 'degraded') return 'Replicas lost';
  if ((app.last_deployment_status || '').toLowerCase() === 'failed') {
    return Number(app.deploy_count) > 0 ? 'Last deployment failed' : 'First deployment failed';
  }
  return 'Needs attention';
}
