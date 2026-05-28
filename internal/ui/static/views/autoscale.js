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
//
// It also carries the live pool size (app.replicas) and a drift flag set when
// autoscale is enabled and the live pool is outside the configured [min, max]
// band. The controller reconverges on its next tick, but the operator should
// see the transient gap explicitly so an emergency manual scale or a freshly
// lowered bound isn't invisible until the next scan.
export function summariseAutoscale(app, envelope) {
  const a = app && typeof app === 'object' ? app : {};
  const e = envelope && typeof envelope === 'object' ? envelope : {};
  const target = Number(a.autoscale_target) || 0;
  const effective = Number(e.effective_autoscale_target) || 0;
  const min = Number(a.autoscale_min_replicas) || (a.autoscale_min_replicas === 0 ? 0 : 1);
  const max = Number(a.autoscale_max_replicas) || (a.autoscale_max_replicas === 0 ? 0 : 1);
  const current = Number(a.replicas) || (a.replicas === 0 ? 0 : 1);
  const enabled = !!a.autoscale_enabled;
  return {
    enabled,
    current,
    min,
    max,
    target,
    effectiveTarget: effective,
    inheritsTarget: target <= 0,
    drift: enabled && (current < min || current > max),
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
// enabled, the live pool versus its configured band, target (with an
// inheritance hint when the app inherits the runtime default). Stale content
// is cleared so a refresh never double-renders.
//
// When autoscale is enabled the replicas row shows "current / [min–max]" so
// the operator sees the live pool next to the band the controller is steering
// toward; a drift call-out is appended when the live pool is outside that band
// so the imminent reconverge is visible rather than invisible until the next
// scan. When autoscale is disabled the bounds are persisted (so a re-enable
// restores them) but do not govern the pool, so the row shows a bare count to
// avoid implying a relationship that isn't enforced.
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
  let replicasValue;
  if (s.enabled) {
    replicasValue = `${s.current} / [${s.min}–${s.max}]`;
    if (s.drift) {
      replicasValue += ' (drift: controller will reconverge)';
    }
  } else {
    replicasValue = String(s.current);
  }
  row('Replicas', replicasValue);
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

// readAutoscaleForm is the pure validator behind the Configuration tab's
// autoscale fieldset. It reads enabled/min/max/target from the form selectors
// in internal/ui/static/index.html and returns either {payload, error: null}
// or {payload: null, error: '<message>'} so the save wrapper can branch on a
// single result. Validation mirrors handlePatchApp (internal/api/apps.go):
//
//   - bounds are checked against [0,1000] always (the stored column range),
//   - when enabled, min must be >= 1 and max >= min,
//   - the explicit target is checked against [0,1] (0 means "inherit"),
//
// so a clearly worded inline error appears before the PATCH lands instead of
// a generic 400 surfacing the server-side message.
export function readAutoscaleForm(doc) {
  const enabled = !!doc.getElementById('autoscale-enabled').checked;
  const minRaw = doc.getElementById('autoscale-min').value.trim();
  const maxRaw = doc.getElementById('autoscale-max').value.trim();
  const min = parseInt(minRaw, 10);
  const max = parseInt(maxRaw, 10);
  if (!Number.isFinite(min) || min < 0 || min > 1000) {
    return { payload: null, error: 'Min replicas must be a whole number between 0 and 1000.' };
  }
  if (!Number.isFinite(max) || max < 0 || max > 1000) {
    return { payload: null, error: 'Max replicas must be a whole number between 0 and 1000.' };
  }
  if (enabled) {
    if (min < 1) {
      return { payload: null, error: 'Min replicas must be at least 1 when autoscale is enabled.' };
    }
    if (max < min) {
      return { payload: null, error: 'Max replicas must be greater than or equal to min replicas.' };
    }
  }
  const targetMode = doc.querySelector('input[name="autoscale-target-mode"]:checked');
  if (!targetMode) {
    return { payload: null, error: 'Pick a target session load option.' };
  }
  let target;
  if (targetMode.value === 'default') {
    // 0 is the API's sentinel for "inherit the runtime default"; see
    // effective_autoscale_target in internal/api/apps.go handleGetApp.
    target = 0;
  } else {
    const t = Number(doc.getElementById('autoscale-target').value.trim());
    if (!Number.isFinite(t) || t <= 0 || t > 1) {
      return { payload: null, error: 'Target session load must be a number between 0 and 1.' };
    }
    target = t;
  }
  return {
    payload: { enabled, min_replicas: min, max_replicas: max, target },
    error: null,
  };
}
