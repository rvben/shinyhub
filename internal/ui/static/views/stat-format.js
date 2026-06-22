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
  let cpu = 0, rss = 0, sessions = 0, runningCount = 0;
  if (replicas !== null) {
    for (const r of replicas) {
      if (r && r.status === 'running') runningCount++;
      cpu += Number(r && r.cpu_percent) || 0;
      rss += Number(r && r.rss_bytes) || 0;
      sessions += Number(r && r.sessions) || 0;
    }
  } else {
    cpu = Number(m && m.cpu_percent) || 0;
    rss = Number(m && m.rss_bytes) || 0;
    sessions = Number(m && m.sessions) || 0;
    runningCount = running ? 1 : 0;
  }
  return {
    running,
    metricsAvailable,
    cpu,
    rss,
    sessions,
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
  const { running, metricsAvailable, cpu, rss, sessions, runningCount } = agg;
  const cfg = Number(configured) || agg.replicaCount || 1;
  return {
    running,
    metricsAvailable,
    cpu: !running ? '—' : (metricsAvailable ? cpu.toFixed(1) + '%' : 'n/a'),
    ram: !running ? '—' : (metricsAvailable ? formatBytes(rss) : 'n/a'),
    sessions: running ? String(sessions) : '—',
    replicas: runningCount + ' / ' + cfg,
    multiReplica: agg.replicaCount > 1,
  };
}

// Class set for the header status pill: a state modifier (drives dot + text
// colour) plus is-live (the running pulse).
export function statusPillClass(status) {
  return 'status-pill status-' + status + (status === 'running' ? ' is-live' : '');
}
