// Pure formatting + aggregation for the app-detail header metric tiles.
// DOM-free so the logic is jsdom/unit-testable.

// Human-readable RSS: MB above 1 MiB, else KB.
export function formatBytes(bytes) {
  const n = Number(bytes) || 0;
  if (n <= 0) return '0 KB';
  return n >= (1 << 20)
    ? (n / (1 << 20)).toFixed(0) + ' MB'
    : (n / 1024).toFixed(0) + ' KB';
}

export function formatCoreCount(cores) {
  if (cores == null || !Number.isFinite(Number(cores)) || Number(cores) < 0) return '—';
  const value = Number(cores);
  // Significant digits keep tiny real measurements visible without making
  // ordinary CPU values noisy. Never round a nonzero rate down to idle.
  const text = Number.isInteger(value) ? value.toFixed(1)
    : value.toLocaleString('en-US', { maximumSignificantDigits: 3, useGrouping: false });
  return `${text} cores`;
}

export function formatCoreCapacity(used, capacity) {
  if (formatCoreCount(used) === '—') return '—';
  if (!(Number.isFinite(Number(capacity)) && Number(capacity) > 0)) return formatCoreCount(used);
  const ceiling = Number(capacity);
  const ceilingText = String(ceiling);
  return `${formatCoreCount(used).replace(/ cores$/, '')} / ${ceilingText} cores`;
}

export function cpuSeverity(fraction) {
  if (!Number.isFinite(fraction)) return '';
  if (fraction >= 0.9) return 'critical';
  if (fraction >= 0.7) return 'warning';
  return 'normal';
}

// aggregateMetrics sums per-replica metrics (m.replicas[]) into raw fleet totals,
// falling back to the legacy top-level cpu_percent/rss_bytes scalars when
// m.replicas is absent. Returns raw numbers; both the app-detail header tiles and
// the Overview build their displays from these, so the aggregation lives in one
// place and cannot drift between them.
export function aggregateMetrics(m) {
  // Distinguish "no replicas array" (use the legacy top-level scalars) from an
  // empty array (genuinely zero tracked replicas → zero counts, never a false 1).
  const replicas = Array.isArray(m && m.replicas) ? m.replicas : null;
  const running = !!(m && m.status === 'running');
  // metrics_available is false when running replicas are PID-less (Fargate /
  // remote_docker); callers render "n/a" there.
  const metricsAvailable = !(m && m.metrics_available === false);
  // cpu_percent is null when the server has no rate for a replica yet, which is
  // the case on the first poll after it starts. Summing the rest would understate
  // the fleet at exactly that moment, so a running replica without a rate makes
  // the whole total unavailable and callers render a neutral dash until the next
  // poll fills it in.
  let cpu = 0, rss = 0, sessions = 0, runningCount = 0;
  let cpuAvailable = true;
  let memoryAvailable = true;
  let metricsTransient = false;
  let liveCount = 0;
  let sessionsAvailable = true;
  if (replicas !== null) {
    for (const r of replicas) {
      const isRunning = !!(r && r.status === 'running');
      // A draining worker is still a live process serving its bound sessions
      // until it stops, so its memory and sessions stay in the totals. A
      // stopped, crashed or lost replica has no process, and any numbers it
      // still carries are stale.
      const isLive = isRunning || !!(r && r.status === 'draining');
      if (isRunning) runningCount++;
      if (!isLive) continue;
      liveCount++;
      if (r.metrics_available === false && r.pid > 0) metricsTransient = true;
      // CPU is measured against the running replicas' capacity (the server's
      // cpu_cores sums the same set), so a draining worker does not count here.
      if (isRunning) {
        if (!Number.isFinite(r.cpu_percent) || r.cpu_percent < 0 || r.metrics_available === false) {
          cpuAvailable = false;
        } else {
          cpu += Number(r.cpu_percent) || 0;
        }
      }
      // A failed sample must not turn a complete memory total into a partial
      // sum. Successful per-replica API samples omit rss_bytes when it is zero.
      const memoryMeasured = Number.isFinite(r.rss_bytes) && r.rss_bytes >= 0
        || r.rss_bytes === undefined && r.metrics_available === true;
      if (r.metrics_available === false || !memoryMeasured) memoryAvailable = false;
      else rss += r.rss_bytes || 0;
      if (Number.isFinite(r.sessions) && r.sessions >= 0) sessions += r.sessions;
      else sessionsAvailable = false;
    }
  } else {
    cpuAvailable = Number.isFinite(m?.cpu_percent) && m.cpu_percent >= 0;
    cpu = Number(m && m.cpu_percent) || 0;
    rss = Number(m && m.rss_bytes) || 0;
    memoryAvailable = metricsAvailable && Number.isFinite(m?.rss_bytes) && m.rss_bytes >= 0;
    metricsTransient = !metricsAvailable && m?.pid > 0;
    sessionsAvailable = Number.isFinite(m?.sessions) && m.sessions >= 0;
    sessions = sessionsAvailable ? m.sessions : 0;
    runningCount = running ? 1 : 0;
  }
  if (runningCount === 0) cpuAvailable = false;
  if (replicas !== null && liveCount === 0) memoryAvailable = false;
  return {
    running,
    metricsAvailable,
    metricsTransient,
    memoryAvailable,
    cpu,
    cpuAvailable,
    rss,
    sessions,
    sessionsAvailable,
    runningCount,
    replicaCount: replicas ? replicas.length : running ? 1 : 0,
  };
}

// Aggregate per-replica metrics (m.replicas[]) into the header tiles. The header
// shows FLEET TOTALS; per-replica detail lives in the Overview replicas panel.
//
// CPU/Memory/Sessions read "—" when the app isn't running; Replicas always shows
// running / configured so a hibernated app still reads "0 / N".
export function headerStats(m, configured) {
  const agg = aggregateMetrics(m);
  const { running, metricsAvailable, metricsTransient, memoryAvailable, cpu, rss, sessions, sessionsAvailable } = agg;
  const workerIsolation = m?.worker_isolation || 'multiplex';
  const isElastic = workerIsolation === 'grouped' || workerIsolation === 'per_session';
  const runningCount = isElastic && Number.isInteger(m?.workers_running) && m.workers_running >= 0
    ? m.workers_running : agg.runningCount;
  const workerMax = isElastic && Number.isInteger(m?.max_workers) && m.max_workers > 0 ? m.max_workers : null;
  const cfg = typeof m?.replicas_desired === 'number' && Number.isInteger(m.replicas_desired) && m.replicas_desired >= 0
    ? m.replicas_desired : Number(configured) || agg.replicaCount || 1;

  const capacity = Number(m?.cpu_capacity_cores);
  const host = Number(m?.cpu_host_cores);
  const explicitCores = m != null && Object.hasOwn(m, 'cpu_cores');
  const used = explicitCores ? m.cpu_cores : cpu / 100;
  const cpuAvailable = metricsAvailable && (explicitCores ? Number.isFinite(used) && used >= 0 : agg.cpuAvailable);
  const hasCPU = running && metricsAvailable && cpuAvailable;
  const hasCapacity = Number.isFinite(capacity) && capacity > 0;
  const fraction = hasCPU && hasCapacity ? used / capacity : null;
  const hostShare = Number.isFinite(host) && host > 0 ? used / host * 100 : null;
  // The dash and "n/a" say different things: "n/a" is a tier that will never
  // report live CPU, the dash is a number that is not in yet.
  return {
    running,
    metricsAvailable,
    metricsTransient,
    memoryAvailable,
    cpu: !running ? '—' : (!metricsAvailable && !metricsTransient ? 'n/a' : (cpuAvailable
      ? formatCoreCapacity(used, hasCapacity ? capacity : null) : '—')),
    cpuFraction: fraction,
    cpuSeverity: cpuSeverity(fraction),
    cpuContext: hasCPU
      ? [hostShare !== null ? `${hostShare > 0 && hostShare < 0.5 ? '<1' : Math.round(hostShare)}% of host` : '',
        m?.cpu_capacity_source === 'quota' && hasCapacity ? `limit: ${capacity} cores (quota)` : '',
        !hasCapacity ? 'Capacity unknown' : ''].filter(Boolean).join(' · ')
      : metricsTransient ? 'Live CPU/memory sampling failed. Retry metrics or wait for the next check.' : '',
    ram: !running ? '—' : (!metricsAvailable && !metricsTransient ? 'n/a' : memoryAvailable ? formatBytes(rss) : '—'),
    sessions: running && sessionsAvailable ? String(sessions) : '—',
    workerIsolation,
    isElastic,
    capacityLabel: isElastic ? 'Workers' : 'Replicas',
    workerMax,
    runningCount,
    desiredCount: isElastic ? null : cfg,
    replicas: isElastic ? `${runningCount}${workerMax ? ' / ' + workerMax : ''}` : runningCount + ' / ' + cfg,
    multiReplica: agg.replicaCount > 1,
  };
}

// Class set for the header status pill: a state modifier (drives dot + text
// colour) plus is-live (the running pulse).
export function statusPillClass(status) {
  return 'status-pill status-' + status + (status === 'running' ? ' is-live' : '');
}
