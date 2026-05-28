// Autoscale view helpers: keep the autoscale section of app-detail pure and
// testable by exposing flat data shapes and DOM helpers that take an explicit
// container/document. The wiring inside views/app-detail.js is pinned by
// internal/ui/contract_test.go because the SPA bundle cannot be imported under
// jsdom.

// summariseAutoscale flattens the GET /api/apps/:slug envelope slice that
// describes autoscale into a single object the view can render without
// re-deriving the inherited-target fallback. The server stores 0 to mean
// "inherit the runtime default" and resolves it server-side into
// effective_autoscale_target (see internal/api/apps.go handleGetApp), so the
// summary carries both values plus an inheritsTarget flag.
export function summariseAutoscale(app, envelope) {
  const a = app && typeof app === 'object' ? app : {};
  const e = envelope && typeof envelope === 'object' ? envelope : {};
  const target = Number(a.autoscale_target) || 0;
  const effective = Number(e.effective_autoscale_target) || 0;
  return {
    enabled: !!a.autoscale_enabled,
    min: Number(a.autoscale_min_replicas) || (a.autoscale_min_replicas === 0 ? 0 : 1),
    max: Number(a.autoscale_max_replicas) || (a.autoscale_max_replicas === 0 ? 0 : 1),
    target,
    effectiveTarget: effective,
    inheritsTarget: target <= 0,
  };
}

// formatRejectsByReason normalises the optional rejects_by_reason rollup
// (internal/api/apps.go handleGetApp) into a deterministically sorted list:
// highest count first, ties broken by reason name ascending so a repeating
// poll never reorders a steady-state set. Returns [] for an absent or empty
// rollup so the caller can hide the block.
export function formatRejectsByReason(rollup) {
  if (!rollup || typeof rollup !== 'object') return [];
  const counts = rollup.counts;
  if (!counts || typeof counts !== 'object') return [];
  const rows = Object.entries(counts).map(([reason, count]) => ({
    reason,
    count: Number(count) || 0,
  }));
  if (rows.length === 0) return [];
  rows.sort((a, b) => {
    if (b.count !== a.count) return b.count - a.count;
    return a.reason < b.reason ? -1 : a.reason > b.reason ? 1 : 0;
  });
  return rows;
}

// renderAutoscaleSummary fills a <dl> with one <dt>/<dd> pair per fact:
// enabled, replica bounds, target (with an inheritance hint when the app
// inherits the runtime default). Stale content is cleared so a refresh never
// double-renders.
export function renderAutoscaleSummary(dl, s) {
  dl.innerHTML = '';
  const doc = dl.ownerDocument;
  const row = (label, value) => {
    const dt = doc.createElement('dt');
    dt.textContent = label;
    const dd = doc.createElement('dd');
    dd.textContent = value;
    dl.appendChild(dt);
    dl.appendChild(dd);
  };
  row('Autoscale', s.enabled ? 'enabled' : 'disabled');
  row('Replicas', `${s.min}–${s.max}`);
  const targetLabel = s.inheritsTarget
    ? `${formatTarget(s.effectiveTarget)} (inherited)`
    : formatTarget(s.target);
  row('Target session load', targetLabel);
}

// renderRejectsByReason populates a <ul> with one <li> per reason and reveals
// the surrounding section. An empty list hides the section so a healthy app
// shows no rollup at all.
export function renderRejectsByReason(section, list, rows) {
  list.innerHTML = '';
  if (!rows || rows.length === 0) {
    section.hidden = true;
    return;
  }
  const doc = list.ownerDocument;
  for (const r of rows) {
    const li = doc.createElement('li');
    li.textContent = `${r.reason}: ${r.count}`;
    list.appendChild(li);
  }
  section.hidden = false;
}

function formatTarget(n) {
  // Match the CLI's two-decimal rendering so dashboard and CLI agree at a
  // glance; trailing zero is preserved on the form (0.80, not 0.8).
  return Number(n).toFixed(2);
}
