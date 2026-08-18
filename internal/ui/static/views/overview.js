// Overview view - the operator dashboard home. A health-first fleet pulse, the
// apps that need attention, fleet resource pressure, and (for admins) recent
// activity. Logic lives in overview-model.js (DOM-free, unit-tested); this file
// is the renderer + the 10s liveness poll. All app/user-supplied text is set via
// textContent / createElement, never innerHTML, so a malicious slug or crash
// traceback can never inject markup.
import { buildOverviewModel, pulseMeta } from './overview-model.js';
import { formatBytes } from './stat-format.js';
import { formatStatus } from './status-label.js';
import { appCardBadge } from './app-card-badge.js';

const POLL_MS = 10000;
const REQUEST_TIMEOUT_MS = 8000;

export function mountOverview(ctx) {
  const view = document.getElementById('overview-view');
  const body = document.getElementById('overview-body');
  view.hidden = false;
  ctx.updateActiveNav(location.pathname);

  let disposed = false;
  let timer = null;
  let loadInFlight = false;
  let lastMetricsSnapshot = null;
  let lastLiveSignature = '';
  let lastLiveResources = null;
  const pendingControllers = new Set();
  let live = document.getElementById('overview-live');
  if (!live) {
    live = el('p', 'sr-only');
    live.id = 'overview-live';
    live.setAttribute('role', 'status');
    live.setAttribute('aria-live', 'polite');
    live.setAttribute('aria-atomic', 'true');
    view.appendChild(live);
  }
  // Server-computed capability: admins always, operators behind the
  // auth.operator_audit_access flag. Gates the recent-activity feed.
  const canReadAudit = !!(ctx.state && ctx.state.canReadAudit);

  // stop ends the liveness poll (on unmount or after a 401), so a logged-out or
  // navigated-away Overview never keeps fetching in the background.
  function stop() {
    disposed = true;
    if (timer) { clearInterval(timer); timer = null; }
    for (const controller of pendingControllers) controller.abort();
    pendingControllers.clear();
  }

  body.replaceChildren(skeleton());

  function requestJSON(path) {
    return apiWithTimeout(ctx.api, path, REQUEST_TIMEOUT_MS, pendingControllers, async (response) => ({
      response,
      body: response.ok ? await response.json() : null,
    }));
  }

  async function load(initial) {
    if (loadInFlight) return;
    loadInFlight = true;
    try {
      let apps = [];
      try {
        const { response: resp, body: ovBody } = await requestJSON('/api/apps');
        if (disposed) return;
        if (resp.status === 401) { stop(); ctx.onUnauthorized(); return; }
        if (!resp.ok) { if (initial) body.replaceChildren(errorState()); return; }
        // Standard {items,...} list envelope; tolerate a bare array for resilience.
        const payload = ovBody || [];
        apps = Array.isArray(payload) ? payload : (Array.isArray(payload.items) ? payload.items : []);
      } catch {
        if (initial) body.replaceChildren(errorState());
        return;
      }
      if (disposed) return;
      ctx.state.apps = apps;
      if (typeof ctx.syncSidebar === 'function') ctx.syncSidebar();

      const [metrics, history, events] = await Promise.all([
        fetchMetrics(apps.length),
        fetchHistory(apps.length),
        canReadAudit && apps.length > 0 ? fetchActivity() : Promise.resolve(null),
      ]);
      if (disposed) return;

      const model = buildOverviewModel(apps, metrics, history);
      replaceOverviewContent(body, render(model, events));
      const signature = resourceLiveSignature(model.resources);
      if (!initial && lastLiveSignature && signature !== lastLiveSignature) {
        live.textContent = resourceLiveSummary(model.resources, lastLiveResources);
      }
      lastLiveSignature = signature;
      lastLiveResources = model.resources;
    } finally {
      loadInFlight = false;
    }
  }

  async function fetchMetrics(appCount) {
    if (appCount === 0) return { state: 'ready', metrics: {}, generatedAt: null };
    try {
      // With no slug filter the endpoint resolves the caller's visible fleet.
      // This keeps large fleets out of request-line limits.
      const { response: resp, body: b } = await requestJSON('/api/apps/metrics');
      if (!resp.ok) throw new Error('metrics request failed');
      lastMetricsSnapshot = {
        state: 'ready',
        metrics: (b && b.metrics) || {},
        generatedAt: (b && b.generated_at) || new Date().toISOString(),
      };
      return lastMetricsSnapshot;
    } catch {
      if (lastMetricsSnapshot) return { ...lastMetricsSnapshot, state: 'stale' };
      return { state: 'unavailable', metrics: {}, generatedAt: null };
    }
  }

  async function fetchHistory(appCount) {
    if (appCount === 0) return { historyBySlug: {}, historyAvailable: true };
    try {
      const { response: resp, body: b } = await requestJSON('/api/apps/metrics/history');
      if (!resp.ok) throw new Error('history request failed');
      return { historyBySlug: (b && b.history) || {}, historyAvailable: true };
    } catch {
      return { historyBySlug: {}, historyAvailable: false };
    }
  }

  async function fetchActivity() {
    try {
      const { response: resp, body: b } = await requestJSON('/api/audit?limit=6');
      if (!resp.ok) return null;
      return (b && Array.isArray(b.events)) ? b.events : null;
    } catch { return null; }
  }

  function render(model, events) {
    const root = el('div', 'ov-grid');
    if (model.total === 0) {
      root.appendChild(renderFirstRun());
      return root;
    }
    root.appendChild(renderPulse(model));
    if (model.attention.length > 0) root.appendChild(renderAttention(model.attention));
    root.appendChild(renderFooter(model, events));
    return root;
  }

  // ── Pulse: the fleet verdict + a proportional status bar (the signature). ──
  function renderPulse(model) {
    const sec = el('section', 'ov-pulse ov-pulse--' + model.verdict.tone);
    sec.setAttribute('aria-label', 'Fleet status');

    const verdict = el('div', 'ov-pulse-verdict');
    verdict.appendChild(el('span', 'ov-pulse-dot'));
    const vtext = el('div', 'ov-pulse-text');
    vtext.appendChild(el('p', 'ov-pulse-headline', model.verdict.headline));
    vtext.appendChild(el('p', 'ov-pulse-detail', model.verdict.detail));
    verdict.appendChild(vtext);
    sec.appendChild(verdict);

    const active = model.segments.filter((s) => s.count > 0);
    if (model.total > 0) {
      const bar = el('div', 'ov-bar');
      bar.setAttribute('role', 'img');
      bar.setAttribute('aria-label',
        active.map((s) => `${s.count} ${s.label.toLowerCase()}`).join(', '));
      for (const seg of active) {
        const s = el('span', 'ov-bar-seg');
        s.style.flexGrow = String(seg.count);
        s.style.setProperty('--seg', `var(${seg.cssVar})`);
        bar.appendChild(s);
      }
      sec.appendChild(bar);

      const legend = el('ul', 'ov-legend');
      for (const seg of active) {
        const li = el('li', 'ov-legend-item');
        const dot = el('span', 'ov-legend-dot');
        dot.style.setProperty('--seg', `var(${seg.cssVar})`);
        li.appendChild(dot);
        li.appendChild(el('span', 'ov-legend-label', seg.label));
        li.appendChild(el('b', 'ov-legend-count', String(seg.count)));
        legend.appendChild(li);
      }
      sec.appendChild(legend);
    }
    return sec;
  }

  // ── Attention: actionable rows for crashed / degraded apps. ──
  function renderAttention(items) {
    const sec = el('section', 'ov-panel ov-attention');
    sec.appendChild(sectionTitle('Needs attention', items.length));
    const list = el('ul', 'ov-attn-list');
    for (const a of items) {
      const li = el('li', 'ov-attn-row');
      const main = el('div', 'ov-attn-main');
      const name = el('a', 'ov-attn-name', a.name);
      name.href = '/apps/' + encodeURIComponent(a.slug);
      name.setAttribute('data-nav', '');
      main.appendChild(name);
      main.appendChild(el('span', 'ov-attn-reason', a.reason || formatStatus(a.status)));
      li.appendChild(main);

      const bi = appCardBadge(a.app, formatStatus);
      li.appendChild(el('span', bi.cls, bi.text));

      const actions = el('div', 'ov-attn-actions');
      // Restart is a management action: only offer it to users who can manage
      // this app (the server would reject it for view-only members anyway), the
      // same gate the Apps grid uses.
      if (ctx.canManageApp(ctx.state.user, a.app)) {
        const restart = el('button', 'ov-btn', 'Restart');
        restart.type = 'button';
        restart.addEventListener('click', () => {
          restart.disabled = true;
          restart.textContent = 'Restarting…';
          Promise.resolve(ctx.restart(a.slug)).finally(() => { if (!disposed) load(false); });
        });
        actions.appendChild(restart);
      }
      const open = el('a', 'ov-btn', 'Open');
      open.href = '/apps/' + encodeURIComponent(a.slug);
      open.setAttribute('data-nav', '');
      actions.appendChild(open);
      li.appendChild(actions);

      list.appendChild(li);
    }
    sec.appendChild(list);
    return sec;
  }

  function renderFirstRun() {
    const sec = el('section', 'ov-panel ov-firstrun');
    const mark = el('span', 'ov-firstrun-mark', '+');
    mark.setAttribute('aria-hidden', 'true');
    sec.appendChild(mark);
    sec.appendChild(el('h2', 'ov-firstrun-title', 'Deploy your first Shiny app'));
    sec.appendChild(el('p', 'ov-firstrun-body',
      'Once it is deployed, Overview will show its health, resource usage, and recent activity.'));
    const cta = el('a', 'btn-primary', 'Go to Apps');
    cta.href = '/apps';
    cta.setAttribute('data-nav', '');
    sec.appendChild(cta);
    return sec;
  }

  // ── Footer: resource pressure + (admin) recent activity. ──
  function renderFooter(model, events) {
    const footer = el('div', 'ov-footer');
    if (!canReadAudit) footer.classList.add('ov-footer--single');
    footer.appendChild(renderResources(model.resources));
    if (canReadAudit) footer.appendChild(renderActivity(events));
    return footer;
  }

  function renderResources(res) {
    return renderResourcePressure(res);
  }

  function renderActivity(events) {
    const sec = el('section', 'ov-panel ov-activity');
    sec.appendChild(sectionTitle('Recent activity'));
    if (!events || events.length === 0) {
      sec.appendChild(el('p', 'ov-empty-note', 'No recent activity.'));
      return sec;
    }
    const list = el('ul', 'ov-timeline');
    for (const ev of events.slice(0, 6)) {
      const li = el('li', 'ov-tl-row');
      li.appendChild(el('span', 'ov-tl-rail'));
      const main = el('div', 'ov-tl-main');
      const head = el('p', 'ov-tl-head');
      head.appendChild(el('b', 'ov-tl-action', humanAction(ev.action)));
      const target = ev.resource_id || ev.resource_type;
      if (target) head.appendChild(el('span', 'ov-tl-target', target));
      main.appendChild(head);
      const meta = el('p', 'ov-tl-meta');
      meta.appendChild(el('span', 'ov-tl-actor', ev.username || 'system'));
      meta.appendChild(el('span', 'ov-tl-time', relTime(ev.created_at)));
      main.appendChild(meta);
      li.appendChild(main);
      list.appendChild(li);
    }
    sec.appendChild(list);
    return sec;
  }

  // ── helpers ──
  function sectionTitle(text, count) {
    const h = el('h2', 'ov-section-title', text);
    if (typeof count === 'number') {
      h.appendChild(el('span', 'ov-section-count', String(count)));
    }
    return h;
  }
  function skeleton() {
    const wrap = el('div', 'ov-grid ov-skeleton');
    wrap.appendChild(el('div', 'ov-skel ov-skel-pulse'));
    const f = el('div', 'ov-footer');
    f.appendChild(el('div', 'ov-skel ov-skel-panel'));
    f.appendChild(el('div', 'ov-skel ov-skel-panel'));
    wrap.appendChild(f);
    wrap.setAttribute('aria-busy', 'true');
    return wrap;
  }
  function errorState() {
    const sec = el('section', 'ov-panel ov-error');
    sec.appendChild(el('p', 'ov-error-text', "Couldn't load the overview."));
    const retry = el('button', 'ov-btn', 'Try again');
    retry.type = 'button';
    retry.addEventListener('click', () => { body.replaceChildren(skeleton()); load(true); });
    sec.appendChild(retry);
    return sec;
  }

  load(true);
  timer = setInterval(() => { if (!disposed) load(false); }, POLL_MS);

  return {
    title: 'Overview',
    unmount() {
      stop();
      view.hidden = true;
    },
  };
}

// Exported as a small, state-complete renderer so its meter semantics and
// unavailable/partial copy can be verified without mounting the whole SPA.
export function renderResourcePressure(res) {
  const sec = el('section', 'ov-panel ov-resources');
  sec.setAttribute('aria-labelledby', 'ov-resources-title');

  const head = el('div', 'ov-res-head');
  const titleWrap = el('div', 'ov-res-heading');
  const title = el('h2', 'ov-section-title', 'App allocation pressure');
  title.id = 'ov-resources-title';
  titleWrap.appendChild(title);
  titleWrap.appendChild(el('p', 'ov-res-scope', 'Against enforced per-replica limits'));
  head.appendChild(titleWrap);
  head.appendChild(el('p', 'ov-res-updated', updatedText(res)));
  sec.appendChild(head);

  if (res.state === 'unavailable') {
    const unavailable = el('div', 'ov-res-message ov-res-message--unavailable');
    unavailable.appendChild(el('b', null, 'Metrics unavailable'));
    unavailable.appendChild(el('span', null, 'CPU and memory pressure could not be verified. Health status remains available above.'));
    sec.appendChild(unavailable);
    return sec;
  }
  if (res.state === 'idle') {
    sec.appendChild(el('p', 'ov-res-message', 'No running replicas to measure.'));
    return sec;
  }

  const rows = el('div', 'ov-capacity-list');
  rows.appendChild(renderCapacityRow('CPU', res.cpu));
  rows.appendChild(renderCapacityRow('Memory', res.memory));
  sec.appendChild(rows);

  if (res.hotspots.length > 0) {
    const hotspotHead = el('div', 'ov-hotspot-head');
    hotspotHead.appendChild(el('h3', 'ov-res-subhead', 'Pressure alerts'));
    hotspotHead.appendChild(el('span', 'ov-section-count', String(res.hotspots.length)));
    sec.appendChild(hotspotHead);
    const list = el('ul', 'ov-hotspot-list');
    list.id = 'ov-pressure-alerts';
    for (const [index, item] of res.hotspots.entries()) {
      const li = renderHotspot(item);
      if (index >= 4) li.hidden = true;
      list.appendChild(li);
    }
    sec.appendChild(list);
    if (res.hiddenHotspotCount > 0) {
      const toggle = el('button', 'ov-res-toggle', `View all ${res.hotspots.length} pressure alerts`);
      toggle.type = 'button';
      toggle.setAttribute('aria-expanded', 'false');
      toggle.setAttribute('aria-controls', list.id);
      toggle.dataset.disclosureKey = 'pressure-alerts';
      toggle.dataset.focusKey = 'pressure-alerts-toggle';
      toggle.addEventListener('click', () => {
        const expanded = toggle.getAttribute('aria-expanded') === 'true';
        toggle.setAttribute('aria-expanded', String(!expanded));
        for (const [index, row] of [...list.children].entries()) {
          if (index >= 4) row.hidden = expanded;
        }
        toggle.textContent = expanded ? `View all ${res.hotspots.length} pressure alerts` : 'Show fewer';
      });
      sec.appendChild(toggle);
    }
  } else {
    const complete = res.cpu.state === 'ready' && res.memory.state === 'ready';
    const text = complete
      ? 'All covered replicas are below the 85% warning threshold.'
      : `No pressure alerts among ${Math.max(res.cpu.coverage.coveredReplicas, res.memory.coverage.coveredReplicas)} of ${res.runningReplicas} covered running replicas.`;
    sec.appendChild(el('p', 'ov-res-clear', text));
  }
  return sec;
}

// Polls replace the Overview snapshot, but a keyboard user following a pressure
// link must not be thrown back to the document. Stable focus keys restore the
// same control when it still exists in the refreshed snapshot.
export function replaceOverviewContent(body, next) {
  const focusKey = focusedKey(body);
  const disclosures = expandedDisclosureKeys(body);
  body.replaceChildren(next);
  restoreDisclosures(body, disclosures);
  restoreFocus(body, focusKey);
}

function renderCapacityRow(label, metric) {
  const row = el('section', 'ov-capacity-row ov-capacity-row--' + metric.severity);
  row.setAttribute('aria-label', label + ' allocation pressure');

  const top = el('div', 'ov-capacity-top');
  top.appendChild(el('h3', 'ov-capacity-label', label));
  top.appendChild(el('span', 'ov-pressure-state ov-pressure-state--' + stateTone(metric), stateLabel(metric)));
  row.appendChild(top);

  const values = el('div', 'ov-capacity-values');
  values.appendChild(el('b', 'ov-capacity-value', capacityValue(metric)));
  const context = el('span', 'ov-capacity-context');
  if (metric.fraction != null) context.appendChild(el('strong', null, `${Math.round(metric.fraction * 100)}%`));
  else context.appendChild(el('strong', null, 'Capacity unavailable'));
  if (metric.peakFraction != null && metric.fraction != null && metric.peakFraction - metric.fraction >= 0.05) {
    context.appendChild(el('span', 'ov-capacity-peak', `Peak replica ${Math.round(metric.peakFraction * 100)}%`));
  }
  context.appendChild(el('span', null, trendText(metric.trend)));
  values.appendChild(context);
  row.appendChild(values);

  if (metric.fraction != null) {
    const meter = document.createElement('meter');
    meter.className = 'ov-capacity-meter ov-capacity-meter--' + metric.severity;
    meter.min = 0;
    meter.max = 1;
    meter.low = 0.85;
    meter.high = 0.95;
    meter.optimum = 0;
    meter.value = Math.min(1, Math.max(0, metric.fraction));
    meter.setAttribute('aria-label', meterLabel(label, metric));
    meter.textContent = `${Math.round(metric.fraction * 100)}%`;
    row.appendChild(meter);
  }
  row.appendChild(el('p', 'ov-capacity-coverage', coverageText(metric)));
  return row;
}

function renderHotspot(item) {
  const li = el('li', 'ov-hotspot-row');
  const main = el('div', 'ov-hotspot-main');
  const link = el('a', 'ov-hotspot-name', item.name);
  link.href = '/apps/' + encodeURIComponent(item.slug) + '/configuration';
  link.setAttribute('data-nav', '');
  link.dataset.focusKey = `pressure:${item.slug}:${item.metric}`;
  main.appendChild(link);
  const replica = item.replicaIndex == null ? '' : ` · Replica ${item.replicaIndex + 1}`;
  main.appendChild(el('span', 'ov-hotspot-meta', `${item.metric === 'cpu' ? 'CPU' : 'Memory'}${replica}`));
  li.appendChild(main);

  const meter = document.createElement('meter');
  meter.className = 'ov-capacity-meter ov-capacity-meter--' + item.severity;
  meter.min = 0;
  meter.max = 1;
  meter.low = 0.85;
  meter.high = 0.95;
  meter.optimum = 0;
  meter.value = Math.min(1, item.fraction);
  meter.setAttribute('aria-label', hotspotLabel(item));
  li.appendChild(meter);

  const values = el('span', 'ov-hotspot-values');
  values.appendChild(el('b', null, `${Math.round(item.fraction * 100)}%`));
  values.appendChild(el('span', null, `${formatResource(item.metric, item.used)} / ${formatResource(item.metric, item.limit)}`));
  li.appendChild(values);
  li.appendChild(el('span', 'ov-pressure-state ov-pressure-state--' + item.severity, titleCase(item.severity)));
  return li;
}

function capacityValue(metric) {
  if (metric.capacity > 0) {
    return `${formatResource(metric.kind, metric.used)} / ${formatResource(metric.kind, metric.capacity)}`;
  }
  if (metric.coverage.observedReplicas > 0) return `${formatResource(metric.kind, metric.observedUsed)} observed`;
  return 'Unavailable';
}

function formatResource(kind, value) {
  if (kind === 'cpu') {
    const cores = Number(value || 0) / 100;
    return `${cores > 0 && cores < 0.1 ? cores.toFixed(2) : cores.toFixed(1)} cores`;
  }
  const bytes = Number(value || 0);
  const gib = 1024 * 1024 * 1024;
  return bytes >= gib ? `${(bytes / gib).toFixed(1)} GB` : formatBytes(bytes);
}

function stateTone(metric) {
  if (metric.state === 'unavailable' || metric.state === 'stale') return metric.state;
  if (metric.severity === 'critical' || metric.severity === 'warning') return metric.severity;
  if (metric.state === 'partial') return metric.state;
  return metric.severity;
}

function stateLabel(metric) {
  if (metric.state === 'unavailable') return 'Unavailable';
  if (metric.state === 'stale') return 'Stale';
  if (metric.state === 'partial') {
    if (metric.severity === 'critical' || metric.severity === 'warning') return `${titleCase(metric.severity)} · Partial`;
    return 'Partial';
  }
  return titleCase(metric.severity === 'unknown' ? 'Unavailable' : metric.severity);
}

function coverageText(metric) {
  const c = metric.coverage;
  const parts = [`${c.coveredReplicas} of ${c.runningReplicas} running replicas covered`];
  if (c.unlimitedReplicas) parts.push(`${c.unlimitedReplicas} unlimited`);
  if (c.unenforcedReplicas) parts.push(`${c.unenforcedReplicas} not enforced`);
  if (c.unknownCapacityReplicas) parts.push(`${c.unknownCapacityReplicas} capacity unknown`);
  if (c.unavailableReplicas) parts.push(`${c.unavailableReplicas} metrics unavailable`);
  return parts.join(' · ');
}

function trendText(trend) {
  if (!trend || trend.state === 'unavailable') return 'Trend unavailable';
  if (trend.state === 'collecting') return 'Collecting trend';
  const window = trend.windowSeconds >= 12 * 60 ? '15m' : `${Math.max(2, Math.round(trend.windowSeconds / 60))}m`;
  if (trend.state === 'steady') return `Steady / ${window}`;
  const points = Math.abs(Math.round(trend.deltaFraction * 100));
  return `${titleCase(trend.state)} ${points} pts / ${window}`;
}

function meterLabel(label, metric) {
  const peak = metric.peakFraction != null && metric.peakFraction - metric.fraction >= 0.05
    ? ` Hottest replica is ${Math.round(metric.peakFraction * 100)} percent.` : '';
  return `${label} allocation, ${Math.round(metric.fraction * 100)} percent of enforced limits.${peak} ${stateLabel(metric).toLowerCase()}. ${coverageText(metric)}.`;
}

function hotspotLabel(item) {
  const replica = item.replicaIndex == null ? '' : `, replica ${item.replicaIndex + 1}`;
  return `${item.name}, ${item.metric}${replica}, ${formatResource(item.metric, item.used)} of ${formatResource(item.metric, item.limit)}, ${Math.round(item.fraction * 100)} percent, ${item.severity}.`;
}

function updatedText(res) {
  if (!res.generatedAt) return res.state === 'unavailable' ? 'Not updated' : 'Waiting for data';
  const age = relativeAge(res.generatedAt);
  return res.state === 'stale' ? `Last updated ${age}` : `Updated ${age}`;
}

function relativeAge(iso) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return 'recently';
  const seconds = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (seconds < 5) return 'just now';
  if (seconds < 60) return `${seconds}s ago`;
  return `${Math.round(seconds / 60)}m ago`;
}

function focusedKey(root) {
  const active = document.activeElement;
  return active && root.contains(active) ? active.dataset.focusKey || null : null;
}

function restoreFocus(root, key) {
  if (!key) return;
  const match = [...root.querySelectorAll('[data-focus-key]')].find((node) => node.dataset.focusKey === key);
  if (match) match.focus({ preventScroll: true });
}

function expandedDisclosureKeys(root) {
  return [...root.querySelectorAll('[data-disclosure-key][aria-expanded="true"]')]
    .map((node) => node.dataset.disclosureKey);
}

function restoreDisclosures(root, keys) {
  for (const key of keys) {
    const match = [...root.querySelectorAll('[data-disclosure-key]')]
      .find((node) => node.dataset.disclosureKey === key);
    if (match && match.getAttribute('aria-expanded') !== 'true') match.click();
  }
}

export function resourceLiveSignature(res) {
  const hotspots = res.hotspots.map((item) =>
    `${item.slug}:${item.metric}:${item.replicaIndex ?? 'x'}:${item.severity}`).sort().join(',');
  return [res.state, res.cpu.state, res.cpu.severity, res.memory.state, res.memory.severity, hotspots].join(':');
}

export function resourceLiveSummary(res, previous = null) {
  if (res.state === 'unavailable') return 'Resource metrics are unavailable.';
  if (res.state === 'stale') return 'Resource metrics are stale; showing the last successful snapshot.';
  const changed = changedHotspot(res.hotspots, previous && previous.hotspots);
  if (changed) {
    if (changed.cleared) return `Resource pressure cleared: ${hotspotDescription(changed.item)}.`;
    return `Resource pressure alert: ${hotspotDescription(changed.item)}.`;
  }
  return `Resource coverage is ${res.state}. CPU ${stateLabel(res.cpu)}. Memory ${stateLabel(res.memory)}.`;
}

function changedHotspot(current, prior) {
  if (!Array.isArray(prior)) return current[0] ? { item: current[0], cleared: false } : null;
  const previousByMetric = new Map(prior.map((item) => [`${item.slug}:${item.metric}`, item]));
  for (const item of current) {
    const old = previousByMetric.get(`${item.slug}:${item.metric}`);
    if (!old || old.replicaIndex !== item.replicaIndex || old.severity !== item.severity) {
      return { item, cleared: false };
    }
  }
  const currentKeys = new Set(current.map((item) => `${item.slug}:${item.metric}`));
  const cleared = prior.find((item) => !currentKeys.has(`${item.slug}:${item.metric}`));
  return cleared ? { item: cleared, cleared: true } : null;
}

function hotspotDescription(item) {
  const replica = item.replicaIndex == null ? '' : `, replica ${item.replicaIndex + 1}`;
  return `${item.name}, ${item.metric}${replica}, ${item.severity}`;
}

// A stuck fetch must not freeze Overview forever. The deadline rejects even if
// a custom API adapter ignores AbortSignal; real fetches are aborted as well.
export async function apiWithTimeout(api, path, timeoutMs = REQUEST_TIMEOUT_MS, controllers = null, consume = (response) => response) {
  const controller = typeof AbortController === 'function' ? new AbortController() : null;
  if (controller && controllers) controllers.add(controller);
  let timeout;
  const deadline = new Promise((_, reject) => {
    timeout = setTimeout(() => {
      if (controller) controller.abort();
      const error = new Error(`Request timed out: ${path}`);
      error.name = 'AbortError';
      reject(error);
    }, timeoutMs);
  });
  try {
    const options = controller ? { signal: controller.signal } : {};
    const operation = Promise.resolve(api(path, options)).then(consume);
    return await Promise.race([operation, deadline]);
  } finally {
    clearTimeout(timeout);
    if (controller && controllers) controllers.delete(controller);
  }
}

function titleCase(value) {
  const text = String(value || '');
  return text.charAt(0).toUpperCase() + text.slice(1);
}

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}

function humanAction(action) {
  if (!action || typeof action !== 'string') return 'Activity';
  const s = action.replace(/[._]/g, ' ');
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// relTime renders an ISO timestamp as a compact "3m ago" / "2h ago" string.
function relTime(iso) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const secs = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (secs < 60) return 'just now';
  const mins = Math.round(secs / 60);
  if (mins < 60) return mins + 'm ago';
  const hrs = Math.round(mins / 60);
  if (hrs < 24) return hrs + 'h ago';
  const days = Math.round(hrs / 24);
  return days + 'd ago';
}
