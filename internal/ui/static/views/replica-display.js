// replica-display.js - pure helpers for rendering replica backend/tier labels
// and honest resource metrics for PID-less backends (Fargate, remote_docker).
// No DOM dependency; importable from jsdom tests and app.js/app-detail.js.

/**
 * backendLabel returns a short human-readable backend/tier string.
 * Examples: "native:local", "fargate:burst", "docker:local".
 *
 * @param {{ tier?: string, provider?: string }|null} replica
 * @returns {string}
 */
export function backendLabel(replica) {
  if (!replica || typeof replica !== 'object') return 'unknown';
  const p = replica.provider;
  const t = replica.tier;
  if (p && t) return `${p}:${t}`;
  if (p) return p;
  if (t) return t;
  return 'unknown';
}

/**
 * reasonLabel returns a replica's presentation-only degraded-state reason
 * (e.g. "worker unavailable" for a replica lost to a dead worker), or "" when
 * none applies. The server sets this on lost replicas in both the app envelope
 * (replicas_status) and the metrics poll, so both render paths can surface the
 * cause instead of a bare "Lost" badge.
 *
 * @param {{ reason?: string }|null} replica
 * @returns {string}
 */
export function reasonLabel(replica) {
  if (!replica || typeof replica !== 'object') return '';
  return typeof replica.reason === 'string' ? replica.reason : '';
}

const METRICS_NA_NOTE =
  'Live CPU/RAM not collected for this backend (Fargate/remote tasks: see CloudWatch / the worker host)';

/**
 * metricsText returns display text for a replica's resource metrics.
 * Three states are distinguished:
 *
 *   metrics_available === true   - PID-backed; return real CPU/RAM numbers.
 *   metrics_available === false  - Confirmed PID-less (Fargate/remote_docker);
 *                                  return "n/a" with the CloudWatch note.
 *   metrics_available === undefined (or absent) - Not yet known; the seed
 *                                  payload from GET replicas_status does not
 *                                  carry this field. Return neutral dashes so
 *                                  the initial panel state does not falsely
 *                                  advertise unavailability for a replica that
 *                                  may turn out to be PID-backed.
 *
 * @param {{ metrics_available?: boolean, cpu_percent?: number, rss_bytes?: number, pss_bytes?: number|null, uss_bytes?: number|null, swap_pss_bytes?: number|null, memory_attribution_partial?: boolean }} replica
 * @returns {{ cpuText: string, ramText: string, physicalText: string, attributionNote: string, note: string|null }}
 */
export function metricsText(replica) {
  if (!replica) {
    return { cpuText: 'n/a', ramText: 'n/a', physicalText: 'n/a', attributionNote: '', note: METRICS_NA_NOTE };
  }
  // Pending / not-yet-polled: availability unknown, show neutral dashes.
  if (replica.metrics_available === undefined) {
    return { cpuText: '—', ramText: '—', physicalText: '—', attributionNote: '', note: '' };
  }
  // Confirmed PID-less: show n/a with the monitoring hint.
  if (replica.metrics_available !== true) {
    return { cpuText: 'n/a', ramText: 'n/a', physicalText: 'n/a', attributionNote: '', note: METRICS_NA_NOTE };
  }
  // A null cpu_percent on a PID-backed replica means the rate is not in yet
  // (first poll after it started), which the neutral dash already covers. Zero
  // would claim the replica is doing nothing.
  const cpuText =
    typeof replica.cpu_percent === 'number' ? `${replica.cpu_percent.toFixed(1)}%` : '—';
  const rss = Number(replica.rss_bytes || 0);
  let ramText;
  if (rss <= 0) {
    ramText = '—'; // em-dash, matching existing zero-RAM treatment
  } else if (rss >= 1 << 20) {
    ramText = `${(rss / (1 << 20)).toFixed(0)} MB`;
  } else {
    ramText = `${(rss / 1024).toFixed(0)} KB`;
  }
  const formatBytes = (value) => {
    if (typeof value !== 'number') return '—';
    if (value >= 1 << 20) return `${(value / (1 << 20)).toFixed(0)} MB`;
    return `${(value / 1024).toFixed(0)} KB`;
  };
  const physicalText = formatBytes(replica.pss_bytes);
  const attribution = [];
  if (typeof replica.pss_bytes === 'number') attribution.push(`PSS ${formatBytes(replica.pss_bytes)}`);
  if (typeof replica.uss_bytes === 'number') attribution.push(`private ${formatBytes(replica.uss_bytes)}`);
  if (typeof replica.swap_pss_bytes === 'number') attribution.push(`swap PSS ${formatBytes(replica.swap_pss_bytes)}`);
  let attributionNote = attribution.join(' · ');
  if (attributionNote && replica.memory_attribution_partial === true) attributionNote += ' · partial';
  return { cpuText, ramText, physicalText, attributionNote, note: null };
}
