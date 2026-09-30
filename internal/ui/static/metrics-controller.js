// Metrics polling controller. Holds at most one interval timer; callers change
// the set of slugs to poll by calling setTargets(). The router calls this on
// every mount so we stop hammering /metrics for apps the user can't see.
//
// Usage:
//   const metrics = createMetricsController({
//     intervalMs: 10000,
//     onMetrics: (slug, m) => { /* update UI */ },
//     onError: (slug, err) => { /* optional */ },
//   });
//   metrics.setTargets(['demo', 'replica-smoke']); // grid view
//   metrics.setTargets(['replica-smoke']);         // detail view
//   metrics.setTargets([]);                        // login view / logged out
import { startVisiblePoll } from './views/visible-poll.js';

export function createMetricsController({ intervalMs = 10000, onMetrics, onError }) {
  // Guarded rather than a bare reference: this controller is also exercised in
  // plain-Node unit tests with no DOM at all, where startVisiblePoll's fail-open
  // path (doc === null never pauses) keeps those tests' behavior unchanged.
  const doc = typeof document !== 'undefined' ? document : null;
  let targets = [];
  let stopPoll = null;
  let requestVersion = 0;
  let activeRequest = null;

  function cancelRequest() {
    requestVersion++;
    activeRequest?.abort();
    activeRequest = null;
  }

  async function tick() {
    const snapshot = targets.slice();
    if (snapshot.length === 0) return;
    // A retry, visibility refresh, or target change supersedes the old poll.
    // The version also protects against fetch/json implementations that finish
    // despite cancellation, so an older answer cannot repaint newer values.
    cancelRequest();
    const version = requestVersion;
    const request = new AbortController();
    activeRequest = request;
    const isCurrent = () => version === requestVersion;
    // One batch request for every card on screen (GET /api/apps/metrics?slugs=...)
    // instead of one round-trip per app, so the dashboard fills in together rather
    // than one card at a time.
    try {
      const qs = encodeURIComponent(snapshot.join(','));
      // A single-app detail poll also requests the controller's active-session
      // signal for its autoscale proximity view. Grid polls stay batched and
      // avoid a per-card fleet-load query.
      const scaleLoad = snapshot.length === 1 ? '&autoscale_load=1' : '';
      const resp = await fetch(`/api/apps/metrics?slugs=${qs}${scaleLoad}`, { credentials: 'include', signal: request.signal });
      if (!isCurrent()) return;
      if (!resp.ok) {
        if (onError) for (const slug of snapshot) onError(slug, new Error(`status ${resp.status}`));
        return;
      }
      const body = await resp.json();
      if (!isCurrent()) return;
      const metrics = (body && body.metrics) || {};
      for (const slug of snapshot) {
        if (!isCurrent()) return;
        if (Object.prototype.hasOwnProperty.call(metrics, slug)) onMetrics(slug, metrics[slug]);
        else if (onError) onError(slug, new Error('Metrics missing from response'));
      }
    } catch (e) {
      if (isCurrent() && onError) for (const slug of snapshot) onError(slug, e);
    } finally {
      if (activeRequest === request) activeRequest = null;
    }
  }

  function setTargets(next) {
    const requested = Array.isArray(next) ? [...new Set(next)] : [];
    if (requested.length === targets.length && requested.every(slug => targets.includes(slug))) return;
    targets = requested;
    if (requested.length === 0) {
      stop();
      return;
    }
    if (!stopPoll) {
      stopPoll = startVisiblePoll(doc, intervalMs, tick);
    }
    // Initial loads and a changed app both fetch immediately. An unchanged
    // set above preserves the current poll and its measurements on tab changes.
    tick();
  }

  function stop() {
    if (stopPoll) { stopPoll(); stopPoll = null; }
    targets = [];
    cancelRequest();
  }

  return { setTargets, stop, refresh: tick };
}
