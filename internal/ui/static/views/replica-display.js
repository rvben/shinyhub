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
 * @param {{ metrics_available?: boolean, cpu_percent?: number, rss_bytes?: number }} replica
 * @returns {{ cpuText: string, ramText: string, note: string|null }}
 */
export function metricsText(replica) {
  if (!replica) {
    return { cpuText: 'n/a', ramText: 'n/a', note: METRICS_NA_NOTE };
  }
  // Pending / not-yet-polled: availability unknown, show neutral dashes.
  if (replica.metrics_available === undefined) {
    return { cpuText: '—', ramText: '—', note: '' };
  }
  // Confirmed PID-less: show n/a with the monitoring hint.
  if (replica.metrics_available !== true) {
    return { cpuText: 'n/a', ramText: 'n/a', note: METRICS_NA_NOTE };
  }
  const cpu = typeof replica.cpu_percent === 'number' ? replica.cpu_percent : 0;
  const rss = Number(replica.rss_bytes || 0);
  const cpuText = `${cpu.toFixed(1)}%`;
  let ramText;
  if (rss <= 0) {
    ramText = '—'; // em-dash, matching existing zero-RAM treatment
  } else if (rss >= 1 << 20) {
    ramText = `${(rss / (1 << 20)).toFixed(0)} MB`;
  } else {
    ramText = `${(rss / 1024).toFixed(0)} KB`;
  }
  return { cpuText, ramText, note: null };
}
