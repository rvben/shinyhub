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
import { activityTime, buildActivityBrief } from './overview-activity.js';
import { focusedKey, restoreFocus } from './focus-restore.js';

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
  let activityInFlight = false;
  let activitySnapshot = null;
  let activityRetryPending = false;
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
  live.textContent = '';
  // Server-computed capability: admins always, operators behind the
  // auth.operator_audit_access flag. Gates the recent-activity feed.
  const canReadAudit = !!(ctx.state && ctx.state.canReadAudit);
  if (canReadAudit) activitySnapshot = { state: 'loading', events: [], updatedAt: null };

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
    if (!initial) live.textContent = '';
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

      if (canReadAudit && apps.length > 0) void refreshActivity();
      const [metrics, history] = await Promise.all([
        fetchMetrics(apps.length),
        fetchHistory(apps.length),
      ]);
      if (disposed) return;

      const model = buildOverviewModel(apps, metrics, history);
      replaceOverviewContent(body, render(model, activitySnapshot));
      const signature = resourceLiveSignature(model.resources);
      if (!initial && lastLiveSignature && signature !== lastLiveSignature) {
        appendOverviewAnnouncement(live, resourceLiveSummary(model.resources, lastLiveResources));
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
        // The host block is the scale the fleet is measured against when no app
        // carries an enforced limit. Absent when the server could not size the
        // box, which the model reports as unknown rather than as a full one.
        host: (b && b.host) || null,
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

  async function refreshActivity() {
    if (activityInFlight || disposed) return;
    activityInFlight = true;
    const previous = activitySnapshot;
    try {
      const { response: resp, body: b } = await requestJSON('/api/audit?limit=12');
      if (disposed) return;
      if (resp.status === 401) { stop(); ctx.onUnauthorized(); return; }
      if (!resp.ok || !b || !Array.isArray(b.events)) throw new Error('activity request failed');
      activitySnapshot = {
        state: 'ready',
        events: b.events,
        hasMore: !!b.has_more,
        updatedAt: new Date().toISOString(),
      };
    } catch {
      if (disposed) return;
      activitySnapshot = previous && Array.isArray(previous.events) && previous.events.length > 0
        ? { ...previous, state: 'stale' }
        : { state: 'unavailable', events: [], updatedAt: null };
    } finally {
      activityInFlight = false;
    }
    if (!disposed) {
      const wasRetry = activityRetryPending;
      const announcement = activityLiveMessage(previous, activitySnapshot, wasRetry);
      activityRetryPending = false;
      replaceActivityPanel(wasRetry ? 'activity:heading' : '');
      appendOverviewAnnouncement(live, announcement);
    }
  }

  function retryActivity() {
    if (activityInFlight || disposed) return;
    activityRetryPending = true;
    live.textContent = '';
    const current = body.querySelector('.ov-activity');
    if (current) {
      current.setAttribute('aria-busy', 'true');
      const retry = current.querySelector('.ov-activity-retry');
      if (retry) {
        retry.disabled = true;
        retry.textContent = 'Trying again…';
      }
    }
    void refreshActivity();
  }

  function replaceActivityPanel(fallbackFocusKey = '') {
    const current = body.querySelector('.ov-activity');
    if (!current || !activitySnapshot) return;
    const focusKey = focusedKey(current);
    const disclosures = expandedDisclosureKeys(current);
    const next = renderActivityBrief(activitySnapshot, retryActivity, {
      availableAppSlugs: new Set((ctx.state.apps || []).map((app) => app.slug)),
      retryPending: activityRetryPending,
    });
    current.replaceWith(next);
    restoreDisclosures(next, disclosures);
    if (!restoreFocus(next, focusKey) && (fallbackFocusKey || focusKey)) restoreFocus(next, fallbackFocusKey || 'activity:heading');
  }

  function render(model, activity) {
    const root = el('div', 'ov-grid');
    if (model.total === 0) {
      root.appendChild(renderFirstRun());
      return root;
    }
    root.appendChild(renderPulse(model));
    if (model.attention.length > 0) root.appendChild(renderAttention(model.attention));
    root.appendChild(renderFooter(model, activity));
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
  function renderFooter(model, activity) {
    const footer = el('div', 'ov-footer');
    if (!canReadAudit) footer.classList.add('ov-footer--single');
    footer.appendChild(renderResources(model.resources));
    if (canReadAudit) footer.appendChild(renderActivityBrief(activity, retryActivity, {
      availableAppSlugs: new Set((ctx.state.apps || []).map((app) => app.slug)),
      retryPending: activityRetryPending,
    }));
    return footer;
  }

  function renderResources(res) {
    return renderResourcePressure(res);
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

// renderActivityBrief turns the audit tail into a compact operational summary.
// It stays exported so grouping, states, semantics, and disclosure behavior can
// be verified without mounting the complete Overview route.
export function renderActivityBrief(snapshot, onRetry = null, options = {}) {
  const state = snapshot && snapshot.state ? snapshot.state : 'loading';
  const sec = el('section', 'ov-panel ov-activity ov-activity--' + state);
  sec.setAttribute('aria-labelledby', 'ov-activity-title');
  if (options.retryPending) sec.setAttribute('aria-busy', 'true');

  const head = el('div', 'ov-activity-head');
  const heading = el('div', 'ov-activity-heading');
  const title = el('h2', 'ov-section-title', 'Recent changes');
  title.id = 'ov-activity-title';
  title.tabIndex = -1;
  title.dataset.focusKey = 'activity:heading';
  heading.appendChild(title);
  if ((state === 'ready' || state === 'stale') && snapshot.updatedAt) {
    const freshness = el('p', 'ov-activity-freshness');
    const dot = el('span', 'ov-activity-freshness-dot');
    dot.setAttribute('aria-hidden', 'true');
    freshness.appendChild(dot);
    const prefix = state === 'stale' ? 'Last updated' : 'Updated';
    freshness.appendChild(document.createTextNode(`${prefix} ${relativeAge(snapshot.updatedAt)}`));
    heading.appendChild(freshness);
  }
  head.appendChild(heading);

  const audit = el('a', 'ov-activity-audit', 'View audit log');
  audit.href = '/audit-log';
  audit.setAttribute('data-nav', '');
  audit.dataset.focusKey = 'activity:audit-log';
  audit.appendChild(activityIcon('arrow'));
  head.appendChild(audit);
  sec.appendChild(head);

  if (state === 'loading') {
    sec.setAttribute('aria-busy', 'true');
    const loading = el('div', 'ov-activity-loading');
    loading.setAttribute('role', 'status');
    loading.setAttribute('aria-label', 'Loading recent changes');
    for (let i = 0; i < 3; i += 1) loading.appendChild(el('span', 'ov-activity-loading-row'));
    sec.appendChild(loading);
    return sec;
  }

  if (state === 'unavailable') {
    sec.appendChild(activityUnavailable(onRetry, options.retryPending));
    return sec;
  }

  if (state === 'stale') {
    const stale = el('div', 'ov-activity-stale');
    const copy = el('p', null, 'Recent changes could not be refreshed. Showing the last successful update.');
    stale.appendChild(copy);
    if (typeof onRetry === 'function') stale.appendChild(activityRetry(onRetry, options.retryPending));
    sec.appendChild(stale);
  }

  const brief = buildActivityBrief(snapshot.events, undefined, options.availableAppSlugs || null, !!snapshot.hasMore);
  if (brief.operations.length === 0) {
    sec.appendChild(el('p', 'ov-empty-note', 'No changes recorded yet.'));
    return sec;
  }

  const list = el('ul', 'ov-activity-list');
  for (const [index, operation] of brief.operations.entries()) {
    list.appendChild(renderActivityOperation(operation, index));
  }
  sec.appendChild(list);

  return sec;
}

function renderActivityOperation(operation, index) {
  const li = el('li', 'ov-activity-item');
  const row = el('div', 'ov-activity-operation');
  row.appendChild(activityIcon(operation.icon));

  const copy = el('div', 'ov-activity-copy');
  const title = el('p', 'ov-activity-titleline');
  title.appendChild(activityActionLink(
    operation.actionLabel,
    operation.auditHref,
    `${operation.key}:audit`,
    '',
    operation.runID ? 'run' : 'event',
  ));
  appendActivityTarget(title, operation.target, operation.targetHref, `${operation.key}:target`);
  if (operation.tone) {
    title.appendChild(el('span', `ov-activity-status ov-activity-status--${operation.tone.name}`, operation.tone.label));
  }
  copy.appendChild(title);

  const meta = el('p', 'ov-activity-meta');
  if (operation.kind === 'group') {
    const count = operation.children.length;
    const allAppChanges = operation.children.every((child) => child.resourceType === 'app');
    const changeLabel = count === 1 ? 'change' : 'changes';
    const recent = operation.truncated ? 'recent ' : '';
    meta.appendChild(el('span', null, `${count} ${recent}${allAppChanges ? `app ${changeLabel}` : changeLabel}`));
    meta.appendChild(el('span', 'ov-activity-separator', '·'));
  }
  meta.appendChild(el('span', 'ov-activity-actor', operation.actorLabel));
  if (operation.kind !== 'group' && operation.resourceTypeLabel) {
    meta.appendChild(el('span', 'ov-activity-separator', '·'));
    meta.appendChild(el('span', null, operation.resourceTypeLabel));
  }
  copy.appendChild(meta);

  if (operation.kind === 'group') {
    const childrenID = `ov-activity-children-${index}-${safeDOMID(operation.runID || operation.key)}`;
    const toggle = el('button', 'ov-activity-disclosure', disclosureLabel('Show', operation.children.length, operation.truncated));
    toggle.type = 'button';
    toggle.setAttribute('aria-expanded', 'false');
    toggle.setAttribute('aria-controls', childrenID);
    toggle.dataset.disclosureKey = `activity:${operation.runID || operation.key}`;
    toggle.dataset.focusKey = `activity:${operation.runID || operation.key}:toggle`;
    toggle.dataset.changeCount = String(operation.children.length);
    toggle.dataset.truncated = String(operation.truncated);
    toggle.appendChild(activityIcon('chevron'));
    copy.appendChild(toggle);

    const children = el('ul', 'ov-activity-children');
    children.id = childrenID;
    children.hidden = true;
    for (const child of operation.children) children.appendChild(renderActivityChild(child, operation.key));
    li.appendChild(row);
    li.appendChild(children);
    toggle.addEventListener('click', () => {
      const expanded = toggle.getAttribute('aria-expanded') === 'true';
      toggle.setAttribute('aria-expanded', String(!expanded));
      children.hidden = expanded;
      toggle.firstChild.textContent = expanded
        ? disclosureLabel('Show', operation.children.length, operation.truncated)
        : disclosureLabel('Hide', operation.children.length, operation.truncated);
    });
  } else {
    li.appendChild(row);
  }

  row.appendChild(copy);
  row.appendChild(renderActivityTime(operation.createdAt));
  return li;
}

function renderActivityChild(event, operationKey) {
  const li = el('li', 'ov-activity-child');
  const main = el('p', 'ov-activity-child-main');
  main.appendChild(activityActionLink(
    event.actionLabel,
    event.auditHref,
    `${operationKey}:${event.id}:audit`,
    'ov-activity-child-action',
  ));
  appendActivityTarget(main, event.target, event.targetHref, `${operationKey}:${event.id}:target`);
  if (event.tone) {
    main.appendChild(el('span', `ov-activity-status ov-activity-status--${event.tone.name}`, event.tone.label));
  }
  li.appendChild(main);
  li.appendChild(renderActivityTime(event.createdAt, 'ov-activity-child-time'));
  return li;
}

function activityActionLink(label, href, focusKey, extraClass = '', auditScope = 'event') {
  const link = el('a', `ov-activity-action ov-activity-action-link ${extraClass}`.trim(), label);
  link.href = href || '/audit-log';
  link.setAttribute('data-nav', '');
  link.dataset.focusKey = `activity:${focusKey}`;
  const destinationLabel = `Open audit ${auditScope} for ${label}`;
  link.setAttribute('aria-label', destinationLabel);
  link.title = destinationLabel;
  link.appendChild(activityLinkIcon('history'));
  return link;
}

function disclosureLabel(verb, count, truncated = false) {
  return `${verb} ${count} ${truncated ? 'recent ' : ''}${count === 1 ? 'change' : 'changes'}`;
}

function appendActivityTarget(parent, target, href, focusKey) {
  if (!target) return;
  if (href) {
    const link = el('a', 'ov-activity-target', target);
    link.href = href;
    link.setAttribute('data-nav', '');
    link.dataset.focusKey = `activity:${focusKey}`;
    const tab = decodeURIComponent(href.split('/').filter(Boolean).at(-1) || 'overview');
    const destinationLabel = `Open ${titleCase(tab)} for the ${target} app`;
    link.setAttribute('aria-label', destinationLabel);
    link.title = destinationLabel;
    link.appendChild(activityLinkIcon('destination'));
    parent.appendChild(link);
    return;
  }
  parent.appendChild(el('span', 'ov-activity-target ov-activity-target--static', target));
}

function renderActivityTime(createdAt, cls = 'ov-activity-time') {
  const formatted = activityTime(createdAt);
  if (!formatted.valid) return el('span', `${cls} ov-activity-time--unknown`, formatted.exact);
  const time = el('time', cls, formatted.exact);
  time.dateTime = formatted.datetime;
  time.title = formatted.title;
  time.setAttribute('aria-label', `${formatted.title}, ${formatted.relative}`);
  time.appendChild(el('span', null, formatted.relative));
  return time;
}

function activityUnavailable(onRetry, retryPending = false) {
  const state = el('div', 'ov-activity-unavailable');
  state.appendChild(activityIcon('attention'));
  const copy = el('div', 'ov-activity-unavailable-copy');
  copy.appendChild(el('b', null, 'Activity unavailable'));
  copy.appendChild(el('p', null, 'Fleet health is still current. Recent changes could not be loaded.'));
  state.appendChild(copy);
  if (typeof onRetry === 'function') state.appendChild(activityRetry(onRetry, retryPending));
  return state;
}

function activityRetry(onRetry, pending = false) {
  const retry = el('button', 'ov-btn ov-activity-retry', pending ? 'Trying again…' : 'Try again');
  retry.type = 'button';
  retry.disabled = pending;
  retry.dataset.focusKey = 'activity:retry';
  retry.addEventListener('click', onRetry);
  return retry;
}

function activityIcon(kind) {
  const span = el('span', `ov-activity-icon ov-activity-icon--${kind}`);
  span.setAttribute('aria-hidden', 'true');
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 20 20');
  svg.setAttribute('fill', 'none');
  const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  path.setAttribute('d', activityIconPath(kind));
  path.setAttribute('stroke', 'currentColor');
  path.setAttribute('stroke-width', '1.5');
  path.setAttribute('stroke-linecap', 'round');
  path.setAttribute('stroke-linejoin', 'round');
  svg.appendChild(path);
  span.appendChild(svg);
  return span;
}

function activityLinkIcon(kind) {
  const icon = activityIcon(kind);
  icon.className = `ov-activity-link-icon ov-activity-link-icon--${kind}`;
  return icon;
}

function activityIconPath(kind) {
  if (kind === 'restart') return 'M15.2 7.1A5.8 5.8 0 1 0 15.6 13M15.2 3.8v3.7h-3.7';
  if (kind === 'deploy') return 'M10 3v9m0 0 3-3m-3 3L7 9M4 14.5h12';
  if (kind === 'fleet') return 'm10 3 6 3-6 3-6-3 6-3Zm6 7-6 3-6-3m12 4-6 3-6-3';
  if (kind === 'attention') return 'M10 6.2v4.4m0 3v.1M3.4 15.5l5.2-10a1.57 1.57 0 0 1 2.8 0l5.2 10A1 1 0 0 1 15.7 17H4.3a1 1 0 0 1-.9-1.5Z';
  if (kind === 'security') return 'M10 2.8 15 5v4.1c0 3.4-2 6.3-5 8.1-3-1.8-5-4.7-5-8.1V5l5-2.2Z';
  if (kind === 'history') return 'M3.8 6.5V3.4m0 3.1h3.1M4.2 6.1A6.5 6.5 0 1 1 3.7 12M10 6.4V10l2.4 1.5';
  if (kind === 'destination') return 'M4.5 10h11m-4-4 4 4-4 4';
  if (kind === 'arrow') return 'M3 10h12m-4-4 4 4-4 4';
  if (kind === 'chevron') return 'm5 8 5 5 5-5';
  if (kind === 'info') return 'M10 9v5m0-8v.2M3.5 10a6.5 6.5 0 1 0 13 0 6.5 6.5 0 0 0-13 0Z';
  return 'M4 6.5h12M4 10h12M4 13.5h8';
}

function safeDOMID(value) {
  return String(value || 'activity').replace(/[^a-zA-Z0-9_-]/g, '-');
}

// Exported as a small, state-complete renderer so its meter semantics and
// unavailable/partial copy can be verified without mounting the whole SPA.
export function renderResourcePressure(res) {
  const sec = el('section', 'ov-panel ov-resources');
  sec.setAttribute('aria-labelledby', 'ov-resources-title');

  const head = el('div', 'ov-res-head');
  const titleWrap = el('div', 'ov-res-heading');
  const title = el('h2', 'ov-section-title', panelTitle(res.scale));
  title.id = 'ov-resources-title';
  titleWrap.appendChild(title);
  titleWrap.appendChild(el('p', 'ov-res-scope', panelScope(res.scale)));
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
  rows.appendChild(renderCapacityRow('CPU', res.cpu, res.runningReplicas));
  rows.appendChild(renderCapacityRow('Memory', res.memory, res.runningReplicas));
  sec.appendChild(rows);

  // With no limits anywhere there are no per-replica thresholds to alert on, so
  // the panel answers the next question instead: which apps account for this.
  if (res.scale !== 'limits') {
    if (res.topConsumers) sec.appendChild(renderTopConsumers(res.topConsumers));
    return sec;
  }

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
  if (!restoreFocus(body, focusKey) && focusKey && focusKey.startsWith('activity:')) {
    restoreFocus(body, 'activity:heading');
  }
}

function renderCapacityRow(label, metric, runningReplicas) {
  const row = el('section', 'ov-capacity-row ov-capacity-row--' + metric.severity);
  row.setAttribute('aria-label', `${label} ${metric.scale === 'limits' ? 'allocation pressure' : 'usage'}`);

  const top = el('div', 'ov-capacity-top');
  top.appendChild(el('h3', 'ov-capacity-label', label));
  top.appendChild(el('span', 'ov-pressure-state ov-pressure-state--' + stateTone(metric), stateLabel(metric)));
  row.appendChild(top);

  const values = el('div', 'ov-capacity-values');
  values.appendChild(el('b', 'ov-capacity-value', capacityValue(metric)));
  const context = el('span', 'ov-capacity-context');
  // A percentage needs a denominator. Without one the value beside it already
  // says everything that is known, so nothing stands in for the missing share.
  if (metric.fraction != null) context.appendChild(el('strong', null, `${Math.round(metric.fraction * 100)}%`));
  if (metric.peakFraction != null && metric.fraction != null && metric.peakFraction - metric.fraction >= 0.05) {
    context.appendChild(el('span', 'ov-capacity-peak', `Peak replica ${Math.round(metric.peakFraction * 100)}%`));
  }
  context.appendChild(el('span', null, trendText(metric.trend, metric.kind)));
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
    meter.setAttribute('aria-label', meterLabel(label, metric, runningReplicas));
    meter.textContent = `${Math.round(metric.fraction * 100)}%`;
    row.appendChild(meter);
  }
  row.appendChild(el('p', 'ov-capacity-coverage', coverageText(metric, runningReplicas)));
  return row;
}

// renderTopConsumers attributes fleet usage to the apps behind it. It replaces
// the pressure alerts on a fleet with no limits: nothing can be near a
// threshold that does not exist, so the useful question is who is using it.
function renderTopConsumers(top) {
  const wrap = document.createDocumentFragment();
  const head = el('div', 'ov-hotspot-head');
  head.appendChild(el('h3', 'ov-res-subhead', `Top ${top.kind === 'cpu' ? 'CPU' : 'memory'} consumers`));
  wrap.appendChild(head);

  const list = el('ul', 'ov-consumer-list');
  for (const item of top.items) {
    const li = el('li', 'ov-consumer-row');
    const link = el('a', 'ov-hotspot-name', item.name);
    link.href = '/apps/' + encodeURIComponent(item.slug) + '/configuration';
    link.setAttribute('data-nav', '');
    link.dataset.focusKey = `consumer:${item.slug}:${top.kind}`;
    li.appendChild(link);

    const share = el('span', 'ov-consumer-share');
    share.setAttribute('role', 'img');
    share.setAttribute('aria-label', `${Math.round(item.fraction * 100)} percent of fleet ${top.kind}`);
    const fill = el('span', 'ov-consumer-fill');
    fill.style.width = `${Math.max(2, Math.round(item.fraction * 100))}%`;
    share.appendChild(fill);
    li.appendChild(share);

    const values = el('span', 'ov-consumer-values');
    values.appendChild(el('b', null, formatResource(top.kind, item.used)));
    values.appendChild(el('span', null, `${Math.round(item.fraction * 100)}% of fleet`));
    li.appendChild(values);
    list.appendChild(li);
  }
  wrap.appendChild(list);
  return wrap;
}

function panelTitle(scale) {
  return scale === 'limits' ? 'App allocation pressure' : 'Fleet resource usage';
}

// The scope line says what the numbers are measured against, and on a fleet
// with no limits it also says why the panel is answering a different question.
function panelScope(scale) {
  if (scale === 'limits') return 'Against enforced per-replica limits';
  if (scale === 'host') return 'No per-app limits set · measured against host capacity';
  return 'No per-app limits set · host capacity unknown';
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
  if (metric.coverage.observedReplicas > 0) return `${formatResource(metric.kind, metric.observedUsed)} in use`;
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
  // With no denominator there is no threshold to be inside of, so the pill
  // reports that the number is live rather than claiming it is normal.
  if (metric.scale === 'none') return 'live';
  return metric.severity;
}

function stateLabel(metric) {
  if (metric.state === 'unavailable') return 'Unavailable';
  if (metric.state === 'stale') return 'Stale';
  if (metric.state === 'partial') {
    if (metric.severity === 'critical' || metric.severity === 'warning') return `${titleCase(metric.severity)} · Partial`;
    return 'Partial';
  }
  if (metric.scale === 'none') return 'Live';
  return titleCase(metric.severity === 'unknown' ? 'Unavailable' : metric.severity);
}

/**
 * coverageText says how much of the fleet the row's number actually covers.
 * What counts as covered depends on the scale: against enforced limits it is
 * the replicas that have one, and against the host (or against nothing) it is
 * the replicas that are reporting, since an unlimited replica is fully measured
 * on those scales.
 */
function coverageText(metric, runningReplicas) {
  const c = metric.coverage;
  if (metric.scale !== 'limits') {
    const total = runningReplicas ?? c.runningReplicas;
    if (c.observedReplicas === total) {
      return `Across ${total} running ${total === 1 ? 'replica' : 'replicas'}`;
    }
    return `${c.observedReplicas} of ${total} running replicas reporting`;
  }
  const parts = [`${c.coveredReplicas} of ${c.runningReplicas} running replicas covered`];
  if (c.unlimitedReplicas) parts.push(`${c.unlimitedReplicas} unlimited`);
  if (c.unenforcedReplicas) parts.push(`${c.unenforcedReplicas} not enforced`);
  if (c.unknownCapacityReplicas) parts.push(`${c.unknownCapacityReplicas} capacity unknown`);
  if (c.unavailableReplicas) parts.push(`${c.unavailableReplicas} metrics unavailable`);
  return parts.join(' · ');
}

/**
 * trendText renders the direction in whichever unit the trend was measured in:
 * percentage points where there is a denominator, and the quantity itself where
 * there is not. A trend with no number attached would read as more certain than
 * it is, so the delta is always shown.
 */
function trendText(trend, kind) {
  if (!trend || trend.state === 'unavailable') return 'Trend unavailable';
  if (trend.state === 'collecting') return 'Collecting trend';
  const window = trend.windowSeconds >= 12 * 60 ? '15m' : `${Math.max(2, Math.round(trend.windowSeconds / 60))}m`;
  if (trend.state === 'steady') return `Steady / ${window}`;
  const delta = trend.deltaFraction != null
    ? `${Math.abs(Math.round(trend.deltaFraction * 100))} pts`
    : formatResource(kind, Math.abs(trend.deltaValue));
  return `${titleCase(trend.state)} ${delta} / ${window}`;
}

function meterLabel(label, metric, runningReplicas) {
  const peak = metric.peakFraction != null && metric.peakFraction - metric.fraction >= 0.05
    ? ` Hottest replica is ${Math.round(metric.peakFraction * 100)} percent.` : '';
  const against = metric.scale === 'limits' ? 'enforced limits' : 'host capacity';
  return `${label} allocation, ${Math.round(metric.fraction * 100)} percent of ${against}.${peak} ${stateLabel(metric).toLowerCase()}. ${coverageText(metric, runningReplicas)}.`;
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

function expandedDisclosureKeys(root) {
  return [...root.querySelectorAll('[data-disclosure-key][aria-expanded="true"]')]
    .map((node) => node.dataset.disclosureKey);
}

function restoreDisclosures(root, keys) {
  for (const key of keys) {
    const match = [...root.querySelectorAll('[data-disclosure-key]')]
      .find((node) => node.dataset.disclosureKey === key);
    if (!match || match.getAttribute('aria-expanded') === 'true') continue;
    // Each disclosure owns the details of expanding its controlled content.
    // Replaying that behavior keeps restoration correct for both activity
    // groups (one hidden list) and pressure alerts (individually hidden rows).
    match.click();
  }
}

export function activityLiveMessage(previous, next, retry = false) {
  const before = previous && previous.state ? previous : null;
  const after = next && next.state ? next : null;
  if (!after) return '';
  if (!before || before.state === 'loading') {
    if (after.state === 'unavailable') return 'Recent changes are unavailable.';
    return retry && after.state === 'ready' ? 'Recent changes updated.' : '';
  }
  if (retry) {
    if (after.state === 'ready') return 'Recent changes updated.';
    if (after.state === 'stale') return 'Recent changes could not be refreshed. Showing the last successful update.';
    if (after.state === 'unavailable') return 'Recent changes are still unavailable.';
  }
  if (before.state === 'ready' && after.state === 'stale') {
    return 'Recent changes could not be refreshed. Showing the last successful update.';
  }
  if ((before.state === 'stale' || before.state === 'unavailable') && after.state === 'ready') {
    return 'Recent changes are available again.';
  }
  if (before.state === 'ready' && after.state === 'ready') {
    const priorIDs = new Set((before.events || []).map((event) => String(event && event.id)));
    const newCount = (after.events || []).filter((event) => event && !priorIDs.has(String(event.id))).length;
    if (newCount === 1) return '1 new recent change.';
    if (newCount > 1) return `${newCount} new recent changes.`;
  }
  return '';
}

export function appendOverviewAnnouncement(live, message) {
  if (!live || !message) return;
  const current = live.textContent.trim();
  let messages = [];
  if (current) {
    try { messages = JSON.parse(live.dataset.overviewMessages || '[]'); } catch { messages = [current]; }
    if (!Array.isArray(messages) || messages.length === 0) messages = [current];
  }
  if (!messages.includes(message)) messages.push(message);
  live.dataset.overviewMessages = JSON.stringify(messages);
  live.textContent = messages.join(' ');
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
