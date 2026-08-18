// overview-model.js - DOM-free logic for the operator Overview (the dashboard
// home). Turns the GET /api/apps list (plus optional live metrics) into the
// display model the Overview view renders: a one-line fleet verdict, the
// status distribution for the pulse bar, the apps that need attention, and a
// fleet resource summary. Kept DOM-free so it is unit-testable and so the view
// stays a thin renderer over a tested model.

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
const WARNING_FRACTION = 0.85;
const CRITICAL_FRACTION = 0.95;
const TREND_WINDOW_SECONDS = 15 * 60;
const TREND_MIN_SPAN_SECONDS = 2 * 60;

/**
 * buildOverviewModel maps the apps list and a per-slug live-metrics map into the
 * Overview display model.
 *
 * @param {Array<object>} apps        GET /api/apps payload
 * @param {Object<string,object>|{state:string,metrics:Object,generatedAt?:string}} metricsInput
 * @param {{historyBySlug?:Object,historyAvailable?:boolean}} historyInput
 * @returns {{
 *   total:number,
 *   counts:Record<string,number>,
 *   segments:Array<{key:string,label:string,cssVar:string,count:number}>,
 *   verdict:{tone:'empty'|'nominal'|'warning'|'critical',headline:string,detail:string},
 *   attention:Array<{slug:string,name:string,status:string,reason:string}>,
 *   resources:{cpuPercent:number,rssBytes:number,running:number,
 *              nearLimit:Array<{slug:string,name:string,usedBytes:number,limitBytes:number,fraction:number}>}
 * }}
 */
export function buildOverviewModel(apps, metricsInput, historyInput = {}) {
  const list = Array.isArray(apps) ? apps : [];
  const metricsState = normalizeMetricsState(metricsInput);
  const metrics = metricsState.metrics;

  const counts = { healthy: 0, transient: 0, sleeping: 0, idle: 0, attention: 0 };
  const attention = [];

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
  }

  const resources = buildResources(list, metricsState, historyInput);

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
    verdict: verdictFor(list.length, counts, attention, resources),
    attention,
    resources,
  };
}

function normalizeMetricsState(input) {
  if (input && typeof input === 'object' && ('metrics' in input || 'state' in input)) {
    return {
      state: ['ready', 'stale', 'unavailable'].includes(input.state) ? input.state : 'ready',
      metrics: input.metrics && typeof input.metrics === 'object' ? input.metrics : {},
      generatedAt: input.generatedAt || null,
    };
  }
  return {
    state: 'ready',
    metrics: input && typeof input === 'object' ? input : {},
    generatedAt: null,
  };
}

function buildResources(apps, metricsState, historyInput) {
  const cpu = newPressureMetric('cpu');
  const memory = newPressureMetric('memory');
  const hotspots = [];
  let runningApps = 0;
  let runningReplicas = 0;

  for (const app of apps) {
    const replicas = runningReplicasFor(app, metricsState.metrics[app.slug]);
    if (replicas.length === 0) continue;
    runningApps += 1;
    runningReplicas += replicas.length;
    collectAppPressure(app, replicas, cpu, hotspots);
    collectAppPressure(app, replicas, memory, hotspots);
  }

  finishMetric(cpu, runningApps, runningReplicas, metricsState.state);
  finishMetric(memory, runningApps, runningReplicas, metricsState.state);
  cpu.trend = trendFor(cpu, historyInput);
  memory.trend = trendFor(memory, historyInput);
  delete cpu.trendInputs;
  delete memory.trendInputs;

  hotspots.sort((a, b) => {
    const severity = { critical: 2, warning: 1 };
    return severity[b.severity] - severity[a.severity] || b.fraction - a.fraction || a.name.localeCompare(b.name);
  });

  let state = 'ready';
  if (runningReplicas === 0) state = 'idle';
  else if (metricsState.state === 'unavailable') state = 'unavailable';
  else if (metricsState.state === 'stale') state = 'stale';
  else if (cpu.state !== 'ready' || memory.state !== 'ready') state = 'partial';

  return {
    state,
    generatedAt: metricsState.generatedAt,
    runningApps,
    runningReplicas,
    cpu,
    memory,
    hotspots,
    hiddenHotspotCount: Math.max(0, hotspots.length - 4),
  };
}

function newPressureMetric(kind) {
  return {
    kind,
    used: 0,
    observedUsed: 0,
    capacity: 0,
    fraction: null,
    severity: 'unknown',
    peakFraction: null,
    state: 'unavailable',
    coverage: {
      runningApps: 0,
      runningReplicas: 0,
      completeApps: 0,
      coveredReplicas: 0,
      observedReplicas: 0,
      unlimitedReplicas: 0,
      unenforcedReplicas: 0,
      unknownCapacityReplicas: 0,
      unavailableReplicas: 0,
    },
    trendInputs: [],
    trend: { state: 'unavailable', deltaFraction: null, windowSeconds: null },
  };
}

function runningReplicasFor(app, metrics) {
  if (metrics && Array.isArray(metrics.replicas) && metrics.replicas.length > 0) {
    return metrics.replicas.filter((replica) => isRunning(replica.status));
  }
  if (!isRunning(metrics && metrics.status) && !isRunning(app.status)) return [];
  const count = Math.max(1, Number(metrics && metrics.replicas_running) || Number(app.replicas_running) || Number(app.replicas) || 1);
  return Array.from({ length: count }, (_, index) => {
    if (count !== 1 || !metrics) return { index, status: 'running', metrics_available: false };
    return {
      ...metrics,
      index,
      status: 'running',
      effective_memory_limit_mb: metrics.effective_memory_limit_mb ?? app.effective_memory_limit_mb,
      effective_cpu_quota_percent: metrics.effective_cpu_quota_percent ?? app.effective_cpu_quota_percent,
      memory_limit_enforced: metrics.memory_limit_enforced ?? app.memory_limit_enforced,
      cpu_quota_enforced: metrics.cpu_quota_enforced ?? app.cpu_quota_enforced,
      resource_enforcement_known: metrics.resource_enforcement_known ?? app.resource_enforcement_known,
    };
  });
}

function collectAppPressure(app, replicas, metric, hotspots) {
  let complete = true;
  let hottest = null;
  let appCapacity = 0;
  let appLimitTotal = 0;

  for (const replica of replicas) {
    const facts = replicaFacts(app, replica, metric.kind);
    if (facts.observed) {
      metric.observedUsed += facts.used;
      metric.coverage.observedReplicas += 1;
    }

    if (facts.limit == null) {
      metric.coverage.unknownCapacityReplicas += 1;
      complete = false;
      continue;
    }
    if (facts.limit === 0) {
      metric.coverage.unlimitedReplicas += 1;
      complete = false;
      continue;
    }
    if (!facts.enforcementKnown) {
      metric.coverage.unknownCapacityReplicas += 1;
      complete = false;
      continue;
    }
    if (!facts.enforced) {
      metric.coverage.unenforcedReplicas += 1;
      complete = false;
      continue;
    }
    if (!facts.observed) {
      metric.coverage.unavailableReplicas += 1;
      complete = false;
      continue;
    }

    metric.used += facts.used;
    metric.capacity += facts.limit;
    metric.coverage.coveredReplicas += 1;
    appCapacity += facts.limit;
    appLimitTotal += facts.limit;
    const fraction = facts.used / facts.limit;
    const severity = severityFor(fraction);
    metric.peakFraction = metric.peakFraction == null ? fraction : Math.max(metric.peakFraction, fraction);
    if (severity !== 'normal' && (!hottest || fraction > hottest.fraction)) {
      hottest = {
        slug: app.slug,
        name: app.name || app.slug,
        metric: metric.kind,
        replicaIndex: Number.isFinite(Number(replica.index)) ? Number(replica.index) : null,
        used: facts.used,
        limit: facts.limit,
        fraction,
        severity,
      };
    }
  }

  if (complete) metric.coverage.completeApps += 1;
  if (hottest) hotspots.push(hottest);
  if (complete && appCapacity > 0) {
    metric.trendInputs.push({
      slug: app.slug,
      capacity: appCapacity,
      perReplicaLimit: appLimitTotal / replicas.length,
    });
  }
}

function replicaFacts(app, replica, kind) {
  const available = replica.metrics_available === true;
  const cpu = Number(replica.cpu_percent);
  const rss = Number(replica.rss_bytes);
  const observed = kind === 'cpu'
    ? available && replica.cpu_percent != null && Number.isFinite(cpu)
    : available && Number.isFinite(rss) && rss >= 0;
  const used = observed ? (kind === 'cpu' ? Math.max(0, cpu) : rss) : 0;
  const limitKey = kind === 'cpu' ? 'effective_cpu_quota_percent' : 'effective_memory_limit_mb';
  const appLimit = app[limitKey];
  const rawLimit = replica[limitKey] ?? appLimit;
  const limitValue = Number(rawLimit);
  const limit = Number.isFinite(limitValue) && limitValue >= 0
    ? (kind === 'memory' ? limitValue * MIB : limitValue)
    : null;
  const enforcedKey = kind === 'cpu' ? 'cpu_quota_enforced' : 'memory_limit_enforced';
  const enforcementKnown = replica.resource_enforcement_known === true || typeof app[enforcedKey] === 'boolean';
  const enforced = replica[enforcedKey] === true || (replica[enforcedKey] == null && app[enforcedKey] === true);
  return { observed, used, limit, enforcementKnown, enforced };
}

function finishMetric(metric, runningApps, runningReplicas, metricsState) {
  metric.coverage.runningApps = runningApps;
  metric.coverage.runningReplicas = runningReplicas;
  metric.fraction = metric.capacity > 0 ? metric.used / metric.capacity : null;
  metric.severity = severityFor(metric.peakFraction);

  if (runningReplicas === 0) metric.state = 'idle';
  else if (metricsState === 'unavailable') metric.state = 'unavailable';
  else if (metric.coverage.coveredReplicas === runningReplicas) metric.state = metricsState === 'stale' ? 'stale' : 'ready';
  else if (metricsState === 'stale') metric.state = 'stale';
  else if (metric.coverage.observedReplicas > 0) metric.state = 'partial';
  else metric.state = 'unavailable';
}

function severityFor(fraction) {
  if (!Number.isFinite(fraction)) return 'unknown';
  if (fraction >= CRITICAL_FRACTION) return 'critical';
  if (fraction >= WARNING_FRACTION) return 'warning';
  return 'normal';
}

function trendFor(metric, historyInput) {
  if (metric.state !== 'ready') return { state: 'unavailable', deltaFraction: null, windowSeconds: null };
  if (historyInput.historyAvailable === false) return { state: 'unavailable', deltaFraction: null, windowSeconds: null };
  const history = historyInput.historyBySlug && typeof historyInput.historyBySlug === 'object'
    ? historyInput.historyBySlug : {};
  let earlyWeighted = 0;
  let lateWeighted = 0;
  let totalWeight = 0;
  let shortestSpan = TREND_WINDOW_SECONDS;

  for (const input of metric.trendInputs) {
    const series = history[input.slug];
    const points = historyPoints(series, metric.kind, input.perReplicaLimit);
    if (points.length < 3 || points.at(-1).ts - points[0].ts < TREND_MIN_SPAN_SECONDS) {
      return { state: 'collecting', deltaFraction: null, windowSeconds: null };
    }
    const latest = points.at(-1).ts;
    const windowed = points.filter((point) => point.ts >= latest - TREND_WINDOW_SECONDS);
    if (windowed.length < 3) return { state: 'collecting', deltaFraction: null, windowSeconds: null };
    const early = median(windowed.slice(0, 3).map((point) => point.fraction));
    const late = median(windowed.slice(-3).map((point) => point.fraction));
    earlyWeighted += early * input.capacity;
    lateWeighted += late * input.capacity;
    totalWeight += input.capacity;
    shortestSpan = Math.min(shortestSpan, windowed.at(-1).ts - windowed[0].ts);
  }
  if (totalWeight === 0) return { state: 'collecting', deltaFraction: null, windowSeconds: null };
  const delta = (lateWeighted - earlyWeighted) / totalWeight;
  const state = delta >= 0.05 ? 'rising' : delta <= -0.05 ? 'falling' : 'steady';
  return { state, deltaFraction: delta, windowSeconds: shortestSpan };
}

function historyPoints(series, kind, perReplicaLimit) {
  if (!series || !Array.isArray(series.ts) || perReplicaLimit <= 0) return [];
  const values = kind === 'cpu' ? series.cpu : series.rss;
  const instances = series.instances;
  if (!Array.isArray(values) || !Array.isArray(instances)) return [];
  const points = [];
  for (let i = 0; i < series.ts.length; i += 1) {
    const ts = Number(series.ts[i]);
    if (values[i] == null) continue;
    const value = Number(values[i]);
    const count = Number(instances[i]);
    if (!Number.isFinite(ts) || !Number.isFinite(value) || !Number.isFinite(count) || count <= 0) continue;
    points.push({ ts, fraction: value / (perReplicaLimit * count) });
  }
  return points.sort((a, b) => a.ts - b.ts);
}

function median(values) {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor(sorted.length / 2)];
}

function isRunning(status) {
  return HEALTHY.has(String(status || '').toLowerCase());
}

function verdictFor(total, counts, attention, resources) {
  if (total === 0) {
    return { tone: 'empty', headline: 'Fleet not configured', detail: 'Deploy your first Shiny app to begin monitoring.' };
  }
  if (attention.length > 0) {
    const n = attention.length;
    return {
      tone: 'critical',
      headline: n === 1 ? '1 app needs attention' : `${n} apps need attention`,
      detail: attention.map((a) => a.slug).slice(0, 3).join(', ') + (n > 3 ? ` +${n - 3} more` : ''),
    };
  }

  const critical = resources.hotspots.find((item) => item.severity === 'critical');
  if (critical) {
    return {
      tone: 'critical',
      headline: 'Resource limit critical',
      detail: `${critical.name} ${critical.metric} is at ${Math.round(critical.fraction * 100)}% on replica ${(critical.replicaIndex ?? 0) + 1}.`,
    };
  }
  const warning = resources.hotspots.find((item) => item.severity === 'warning');
  if (warning) {
    return {
      tone: 'warning',
      headline: 'Resource headroom is tightening',
      detail: `${warning.name} ${warning.metric} is at ${Math.round(warning.fraction * 100)}% on replica ${(warning.replicaIndex ?? 0) + 1}.`,
    };
  }
  if (resources.state === 'unavailable') {
    return {
      tone: 'warning',
      headline: 'Resource metrics unavailable',
      detail: 'App health is visible, but current CPU and memory pressure cannot be verified.',
    };
  }
  if (resources.state === 'stale') {
    return {
      tone: 'warning',
      headline: 'Resource data is stale',
      detail: 'Showing the last successful resource snapshot while live metrics recover.',
    };
  }
  if (resources.state === 'partial') {
    return {
      tone: 'warning',
      headline: 'Resource coverage is partial',
      detail: 'Some running replicas have unavailable, unlimited, or unenforced capacity.',
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
