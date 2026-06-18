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
export function createMetricsController({ intervalMs = 10000, onMetrics, onError }) {
  let targets = [];
  let timer = null;

  async function tick() {
    const snapshot = targets.slice();
    if (snapshot.length === 0) return;
    // One batch request for every card on screen (GET /api/apps/metrics?slugs=...)
    // instead of one round-trip per app, so the dashboard fills in together rather
    // than one card at a time.
    try {
      const qs = encodeURIComponent(snapshot.join(','));
      const resp = await fetch(`/api/apps/metrics?slugs=${qs}`, { credentials: 'include' });
      if (!resp.ok) {
        if (onError) for (const slug of snapshot) onError(slug, new Error(`status ${resp.status}`));
        return;
      }
      const body = await resp.json();
      const metrics = (body && body.metrics) || {};
      for (const slug of snapshot) {
        if (Object.prototype.hasOwnProperty.call(metrics, slug)) onMetrics(slug, metrics[slug]);
      }
    } catch (e) {
      if (onError) for (const slug of snapshot) onError(slug, e);
    }
  }

  function setTargets(next) {
    targets = Array.isArray(next) ? next.slice() : [];
    if (targets.length === 0) {
      if (timer) { clearInterval(timer); timer = null; }
      return;
    }
    if (!timer) {
      timer = setInterval(tick, intervalMs);
      // Immediate fetch so the UI shows values before the first interval tick.
      tick();
    }
  }

  function stop() {
    if (timer) { clearInterval(timer); timer = null; }
    targets = [];
  }

  return { setTargets, stop };
}
