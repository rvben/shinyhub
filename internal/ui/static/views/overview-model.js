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
// A trend has to move by 5 percentage points of the denominator before it is
// called rising or falling, so a metric hovering in place reads as steady.
const TREND_DELTA_FRACTION = 0.05;
// With no denominator the same 5% is measured against the fleet's own earlier
// usage, floored so a nearly-idle fleet does not report a rising trend off a
// few megabytes of noise. CPU is in percent-of-one-core, memory in bytes.
const TREND_CPU_FLOOR = 5;
const TREND_MEMORY_FLOOR = 32 * MIB;
// How many apps the top-consumers list names before it stops.
const TOP_CONSUMERS = 3;

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
 *   resources:{state:string,scale:'limits'|'host'|'none',runningApps:number,
 *              runningReplicas:number,cpu:object,memory:object,
 *              hotspots:Array<object>,hiddenHotspotCount:number,
 *              topConsumers:{kind:string,items:Array<object>}|null}
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
      host: input.host && typeof input.host === 'object' ? input.host : null,
    };
  }
  return {
    state: 'ready',
    metrics: input && typeof input === 'object' ? input : {},
    generatedAt: null,
    host: null,
  };
}

/**
 * hostCapacityFor reads the denominator one metric can borrow from the host
 * block on GET /api/apps/metrics, in that metric's own units: CPU in
 * percent-of-one-core (100 per core, matching cpu_percent), memory in bytes.
 *
 * A key the server omitted means it could not measure that dimension, which is
 * not the same as a host with none of it, so this reports null rather than 0.
 */
function hostCapacityFor(host, kind) {
  if (!host) return null;
  if (kind === 'cpu') {
    const cores = Number(host.cores);
    if (!Number.isFinite(cores) || cores <= 0) return null;
    return { value: cores * 100, source: host.cores_source || null };
  }
  const mb = Number(host.memory_mb);
  if (!Number.isFinite(mb) || mb <= 0) return null;
  return { value: mb * MIB, source: host.memory_source || null };
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
  // A metric with no enforced limit anywhere has no allocation to measure
  // against. Rather than report the fleet as immeasurable, fall back to the
  // host's own size, and to plain usage when even that is unknown.
  applyHostScale(cpu, hostCapacityFor(metricsState.host, 'cpu'), runningReplicas, metricsState.state);
  applyHostScale(memory, hostCapacityFor(metricsState.host, 'memory'), runningReplicas, metricsState.state);
  cpu.trend = trendFor(cpu, historyInput);
  memory.trend = trendFor(memory, historyInput);

  hotspots.sort((a, b) => {
    const severity = { critical: 2, warning: 1 };
    return severity[b.severity] - severity[a.severity] || b.fraction - a.fraction || a.name.localeCompare(b.name);
  });

  let state = 'ready';
  if (runningReplicas === 0) state = 'idle';
  else if (metricsState.state === 'unavailable') state = 'unavailable';
  else if (metricsState.state === 'stale') state = 'stale';
  else if (cpu.state !== 'ready' || memory.state !== 'ready') state = 'partial';

  // The panel's scale is the strongest denominator any row has: one row on
  // enforced limits still makes this an allocation-pressure panel.
  let scale = 'none';
  if (cpu.scale === 'limits' || memory.scale === 'limits') scale = 'limits';
  else if (cpu.scale === 'host' || memory.scale === 'host') scale = 'host';

  const topConsumers = scale === 'limits' ? null : topConsumersFor(memory, cpu);
  delete cpu.trendInputs;
  delete memory.trendInputs;
  delete cpu.usageInputs;
  delete memory.usageInputs;

  return {
    state,
    scale,
    generatedAt: metricsState.generatedAt,
    runningApps,
    runningReplicas,
    cpu,
    memory,
    hotspots,
    hiddenHotspotCount: Math.max(0, hotspots.length - 4),
    topConsumers,
  };
}

/**
 * applyHostScale re-bases a metric that has no enforced limit on any running
 * replica. Nothing changes for a metric that has one: a fleet with limits set
 * is still measured against them, including a mixed fleet where only some apps
 * carry them.
 *
 * The re-based metric measures observed usage against the host's own capacity
 * ('host'), or reports usage with no denominator at all ('none') when the host
 * size is unknown. In neither case does it invent a limit.
 */
function applyHostScale(metric, host, runningReplicas, metricsState) {
  if (metric.coverage.coveredReplicas > 0) return;
  metric.scale = host ? 'host' : 'none';
  metric.used = metric.observedUsed;
  metric.capacity = host ? host.value : 0;
  metric.capacitySource = host ? host.source : null;
  metric.fraction = host ? metric.observedUsed / host.value : null;
  // There are no per-replica denominators here, so there is no hottest replica
  // to name; the fraction covers the whole fleet at once.
  metric.peakFraction = null;
  // Without a capacity there is no threshold to be near, so severity says
  // normal and the row's state label says "live" instead of claiming a verdict.
  metric.severity = host ? severityFor(metric.fraction) : 'normal';

  // Coverage now means "reported", not "has a limit": an unlimited replica is
  // fully measured on this scale.
  if (runningReplicas === 0) metric.state = 'idle';
  else if (metricsState === 'unavailable' || metric.coverage.observedReplicas === 0) metric.state = 'unavailable';
  else if (metricsState === 'stale') metric.state = 'stale';
  else if (metric.coverage.observedReplicas === runningReplicas) metric.state = 'ready';
  else metric.state = 'partial';
}

/**
 * topConsumersFor ranks the apps behind the fleet's usage, so a panel with no
 * limits still answers "who is using it". It ranks by memory, falling back to
 * CPU only when nothing reported memory: memory is what exhausts a host, and a
 * 10-second CPU sample reshuffles the order on every poll.
 *
 * A single app is the whole fleet, so ranking it against itself would repeat
 * the row above rather than add to it. That case reports nothing.
 */
function topConsumersFor(memory, cpu) {
  const metric = memory.coverage.observedReplicas > 0 ? memory : cpu;
  const total = metric.observedUsed;
  if (metric.usageInputs.length < 2 || !(total > 0)) return null;
  const items = [...metric.usageInputs]
    .sort((a, b) => b.used - a.used || a.name.localeCompare(b.name))
    .slice(0, TOP_CONSUMERS)
    .map((entry) => ({ slug: entry.slug, name: entry.name, used: entry.used, fraction: entry.used / total }));
  return { kind: metric.kind, items };
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
    // What the fraction is measured against: 'limits' (enforced per-replica
    // limits), 'host' (the size of the box), or 'none' (usage only, no
    // denominator). applyHostScale moves a metric off 'limits'.
    scale: 'limits',
    capacitySource: null,
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
    // trendInputs feeds the limits-scale trend (one entry per app whose replicas
    // are all covered by an enforced limit); usageInputs feeds the host and
    // none scales, and doubles as the top-consumers ranking.
    trendInputs: [],
    usageInputs: [],
    trend: noTrend(),
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
  let appObserved = 0;
  let appObservedUsed = 0;

  for (const replica of replicas) {
    const facts = replicaFacts(app, replica, metric.kind);
    if (facts.observed) {
      metric.observedUsed += facts.used;
      metric.coverage.observedReplicas += 1;
      appObserved += 1;
      appObservedUsed += facts.used;
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
  // An app contributes to fleet usage as soon as one replica reports, whether or
  // not it carries a limit. allObserved records whether the number is the whole
  // app or only the replicas that answered, which the trend needs and the
  // top-consumers list does not.
  if (appObserved > 0) {
    metric.usageInputs.push({
      slug: app.slug,
      name: app.name || app.slug,
      used: appObservedUsed,
      allObserved: appObserved === replicas.length,
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

/**
 * trendFor reads the 15-minute history feed and says which way the metric is
 * moving. Which question it answers depends on the scale the metric ended up
 * on: against enforced limits it tracks the pressure fraction, and against the
 * host (or against nothing) it tracks absolute usage, since there is no
 * per-replica denominator to take a fraction of.
 */
function trendFor(metric, historyInput) {
  if (metric.state !== 'ready') return noTrend();
  if (historyInput.historyAvailable === false) return noTrend();
  const history = historyInput.historyBySlug && typeof historyInput.historyBySlug === 'object'
    ? historyInput.historyBySlug : {};
  return metric.scale === 'limits' ? pressureTrend(metric, history) : usageTrend(metric, history);
}

// pressureTrend moves with the fraction of the enforced limit in use, weighted
// by each app's share of the fleet's total capacity so a large app moves the
// fleet number more than a small one.
function pressureTrend(metric, history) {
  let earlyWeighted = 0;
  let lateWeighted = 0;
  let totalWeight = 0;
  let shortestSpan = TREND_WINDOW_SECONDS;

  for (const input of metric.trendInputs) {
    const windowed = trendWindow(historyPoints(history[input.slug], metric.kind, input.perReplicaLimit));
    if (!windowed) return collectingTrend();
    earlyWeighted += median(windowed.slice(0, 3).map((point) => point.value)) * input.capacity;
    lateWeighted += median(windowed.slice(-3).map((point) => point.value)) * input.capacity;
    totalWeight += input.capacity;
    shortestSpan = Math.min(shortestSpan, windowed.at(-1).ts - windowed[0].ts);
  }
  if (totalWeight === 0) return collectingTrend();
  const deltaFraction = (lateWeighted - earlyWeighted) / totalWeight;
  return {
    state: directionFor(deltaFraction, TREND_DELTA_FRACTION),
    deltaFraction,
    deltaValue: null,
    windowSeconds: shortestSpan,
  };
}

/**
 * usageTrend moves with what the fleet actually consumes, in the metric's own
 * units, summed across every app that is reporting. It is the trend for a fleet
 * with no enforced limits: there is no fraction to track, so it tracks the
 * quantity and reports the delta both ways it can be read.
 *
 * With a host capacity the delta converts to percentage points of that host, so
 * "rising" means the same thing it does on the limits scale. Without one the
 * delta is judged against the fleet's own earlier usage, floored so a nearly
 * idle fleet does not call a few megabytes of noise a rising trend.
 */
function usageTrend(metric, history) {
  let earlyTotal = 0;
  let lateTotal = 0;
  let counted = 0;
  let shortestSpan = TREND_WINDOW_SECONDS;

  for (const input of metric.usageInputs) {
    const windowed = trendWindow(absoluteHistoryPoints(history[input.slug], metric.kind));
    if (!windowed) return collectingTrend();
    earlyTotal += median(windowed.slice(0, 3).map((point) => point.value));
    lateTotal += median(windowed.slice(-3).map((point) => point.value));
    counted += 1;
    shortestSpan = Math.min(shortestSpan, windowed.at(-1).ts - windowed[0].ts);
  }
  if (counted === 0) return collectingTrend();

  const deltaValue = lateTotal - earlyTotal;
  if (metric.capacity > 0) {
    const deltaFraction = deltaValue / metric.capacity;
    return {
      state: directionFor(deltaFraction, TREND_DELTA_FRACTION),
      deltaFraction,
      deltaValue,
      windowSeconds: shortestSpan,
    };
  }
  const floor = metric.kind === 'cpu' ? TREND_CPU_FLOOR : TREND_MEMORY_FLOOR;
  return {
    state: directionFor(deltaValue, Math.max(floor, earlyTotal * TREND_DELTA_FRACTION)),
    deltaFraction: null,
    deltaValue,
    windowSeconds: shortestSpan,
  };
}

/**
 * trendWindow narrows a series to the last TREND_WINDOW_SECONDS and returns it
 * only when it is long enough to say anything: three samples spanning at least
 * TREND_MIN_SPAN_SECONDS. Null means "still collecting", never "steady" - a
 * freshly started app has no trend rather than a flat one.
 */
function trendWindow(points) {
  if (points.length < 3 || points.at(-1).ts - points[0].ts < TREND_MIN_SPAN_SECONDS) return null;
  const latest = points.at(-1).ts;
  const windowed = points.filter((point) => point.ts >= latest - TREND_WINDOW_SECONDS);
  return windowed.length >= 3 ? windowed : null;
}

function directionFor(delta, threshold) {
  if (delta >= threshold) return 'rising';
  if (delta <= -threshold) return 'falling';
  return 'steady';
}

function noTrend() {
  return { state: 'unavailable', deltaFraction: null, deltaValue: null, windowSeconds: null };
}

function collectingTrend() {
  return { state: 'collecting', deltaFraction: null, deltaValue: null, windowSeconds: null };
}

/**
 * absoluteHistoryPoints reads the history series as the quantity the fleet
 * consumed, in the metric's own units. The series values are already summed
 * across an app's replicas, so nothing is divided by the instance count here -
 * that division is what turns them into the per-replica fractions historyPoints
 * needs, and it is wrong for a total.
 */
function absoluteHistoryPoints(series, kind) {
  if (!series || !Array.isArray(series.ts)) return [];
  const values = kind === 'cpu' ? series.cpu : series.rss;
  if (!Array.isArray(values)) return [];
  const points = [];
  for (let i = 0; i < series.ts.length; i += 1) {
    const ts = Number(series.ts[i]);
    if (values[i] == null) continue;
    const value = Number(values[i]);
    if (!Number.isFinite(ts) || !Number.isFinite(value) || value < 0) continue;
    points.push({ ts, value });
  }
  return points.sort((a, b) => a.ts - b.ts);
}

// historyPoints reads the same series as a fraction of one replica's limit: the
// values are summed across replicas, so dividing by the instance count at each
// sample keeps the fraction comparable as an app scales up or down mid-window.
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
    points.push({ ts, value: value / (perReplicaLimit * count) });
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
  const criticalHost = hostScaleMetric(resources, 'critical');
  if (criticalHost) {
    return {
      tone: 'critical',
      headline: 'Host capacity is nearly exhausted',
      detail: hostScaleDetail(criticalHost, resources.runningReplicas),
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
  const warningHost = hostScaleMetric(resources, 'warning');
  if (warningHost) {
    return {
      tone: 'warning',
      headline: 'Host headroom is tightening',
      detail: hostScaleDetail(warningHost, resources.runningReplicas),
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
      detail: partialDetail(resources),
    };
  }
  const live = counts.healthy + counts.transient;
  return {
    tone: 'nominal',
    headline: 'All systems nominal',
    detail: summaryLine(total, counts, live),
  };
}

/**
 * hostScaleMetric finds a row measured against the host that has reached the
 * given severity. Only a row on the host scale qualifies: a row with no
 * denominator has nothing to be near, and a row on enforced limits is already
 * covered by the hotspot list, which can name the replica.
 */
function hostScaleMetric(resources, severity) {
  return [resources.memory, resources.cpu].find(
    (metric) => metric.scale === 'host' && metric.severity === severity,
  ) || null;
}

// hostScaleDetail names the fleet-wide reading. There is no replica to point at
// here, so it says how much of the fleet the number covers instead.
function hostScaleDetail(metric, runningReplicas) {
  const label = metric.kind === 'cpu' ? 'CPU' : 'Memory';
  const scope = `${runningReplicas} running ${runningReplicas === 1 ? 'replica' : 'replicas'}`;
  return `${label} is at ${Math.round(metric.fraction * 100)}% of host capacity across ${scope}.`;
}

// partialDetail says which kind of gap left coverage partial: replicas that are
// not reporting at all, or replicas that report but carry no enforced limit to
// measure against. They call for different fixes, so they get different words.
function partialDetail(resources) {
  const silent = [resources.cpu, resources.memory].some(
    (metric) => metric.coverage.observedReplicas < resources.runningReplicas,
  );
  return silent
    ? 'Some running replicas are not reporting CPU or memory.'
    : 'Some running replicas have unlimited or unenforced capacity.';
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
