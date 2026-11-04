// Overview view - the operator dashboard home. A health-first fleet pulse, the
// apps that need attention, fleet resource pressure, and (for admins) recent
// activity. Logic lives in overview-model.js (DOM-free, unit-tested); this file
// is the renderer + the 10s liveness poll. All app/user-supplied text is set via
// textContent / createElement, never innerHTML, so a malicious slug or crash
// traceback can never inject markup.
import { buildOverviewWorkspace, pulseMeta } from './overview-model.js';
import { formatBytes, formatCoreCapacity, formatCoreCount } from './stat-format.js';
import { updateCPUMeter } from './cpu-capacity.js';
import { formatStatus } from './status-label.js';
import { activityTime, buildActivityBrief } from './overview-activity.js';
import { focusedKey, restoreFocus } from './focus-restore.js';
import { setMetricValue } from './metric-value.js';
import { appCardActions } from './app-card-actions.js';
import { startVisiblePoll } from './visible-poll.js';

const POLL_MS = 10000;
const REQUEST_TIMEOUT_MS = 8000;
// Keep navigation context only for this signed-in session, not across users.
const overviewContexts = new WeakMap();

export function mountOverview(ctx) {
  const view = document.getElementById('overview-view');
  const body = document.getElementById('overview-body');
  view.hidden = false;
  ctx.updateActiveNav(location.pathname);

  const savedContext = overviewContexts.get(ctx.state);
  const sessionIdentity = ctx.state.sessionIdentity ?? ctx.state.user;
  const sessionUserID = ctx.state.user?.id;
  let returnContext = savedContext?.session === sessionIdentity && savedContext?.userID === sessionUserID ? savedContext : null;
  let selectedScope = returnContext?.scope || 'all';
  let overviewSnapshot = null;
  let overviewStale = false;
  let overviewRetryPending = false;
  let resourceRetryPending = false;
  let scopeNotice = '';
  const pendingRestarts = new Set();
  const restartErrors = new Map();
  let disposed = false;
  let stopPoll = null;
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
    if (!disposed && overviewSnapshot && (ctx.state.sessionIdentity ?? ctx.state.user) === sessionIdentity
      && ctx.state.user?.id === sessionUserID) overviewContexts.set(ctx.state, {
      session: sessionIdentity, userID: sessionUserID, scope: selectedScope,
      focusKey: focusedKey(body), disclosures: expandedDisclosureKeys(body),
    });
    disposed = true;
    if (stopPoll) { stopPoll(); stopPoll = null; }
    for (const controller of pendingControllers) controller.abort();
    pendingControllers.clear();
  }

  body.replaceChildren(skeleton());
  const initialScrollY = window.scrollY;

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
    const wasResourceRetry = resourceRetryPending;
    try {
      let apps = [];
      try {
        const { response: resp, body: ovBody } = await requestJSON('/api/apps');
        if (disposed) return;
        if (resp.status === 401) { stop(); ctx.onUnauthorized(); return; }
        if (!resp.ok) { showRefreshFailure(initial); return; }
        // Standard {items,...} list envelope; tolerate a bare array for resilience.
        if (!Array.isArray(ovBody) && !Array.isArray(ovBody?.items)) throw new Error('Invalid apps response');
        apps = Array.isArray(ovBody) ? ovBody : ovBody.items;
      } catch {
        if (!disposed) showRefreshFailure(initial);
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

      const recovered = overviewStale;
      overviewStale = false;
      overviewSnapshot = { apps, metrics, history, updatedAt: new Date().toISOString() };
      const workspace = buildOverviewWorkspace(apps, metrics, history, selectedScope);
      scopeNotice = workspace.selectedKey !== selectedScope
        ? 'The selected fleet is no longer available. Showing all apps.' : '';
      selectedScope = workspace.selectedKey;
      const model = workspace.model;
      const announceResourceRetry = wasResourceRetry || resourceRetryPending;
      resourceRetryPending = false;
      // Async navigation must not take focus back after the operator has
      // started using another control or scrolling while readings load.
      const canRestoreContextFocus = window.scrollY === initialScrollY
        && (document.activeElement === document.body || document.activeElement === view.querySelector('h1'));
      replaceOverviewContent(body, render(workspace, activitySnapshot));
      if (returnContext) {
        restoreDisclosures(body, returnContext.disclosures);
        revealFocusedRow(body, returnContext.focusKey);
        if (returnContext.focusKey && canRestoreContextFocus) {
          if (!restoreFocus(body, returnContext.focusKey)) restoreOverviewStart(body);
          // The skeleton can shrink the document before Back restores its
          // scroll position. Keep the restored control visible once data lands.
          if (body.contains(document.activeElement)) document.activeElement.scrollIntoView?.({ block: 'nearest' });
        }
        returnContext = null;
      }
      if (scopeNotice) appendOverviewAnnouncement(live, scopeNotice);
      if (recovered) appendOverviewAnnouncement(live, 'Overview is current again.');
      const signature = resourceLiveSignature(model.resources);
      if (!initial && lastLiveSignature && signature !== lastLiveSignature) {
        appendOverviewAnnouncement(live, resourceLiveSummary(model.resources, lastLiveResources));
      }
      if (announceResourceRetry) appendOverviewAnnouncement(live, ['stale', 'unavailable'].includes(model.resources.state)
        ? 'Resource readings could not refresh. Retrying automatically every 10 seconds while this page is visible.'
        : 'Resource readings refreshed.');
      lastLiveSignature = signature;
      lastLiveResources = model.resources;
    } finally {
      loadInFlight = false;
      overviewRetryPending = false;
      resourceRetryPending = false;
      const resourceRetry = body.querySelector('[data-focus-key="usage:retry"]');
      if (resourceRetry) {
        resourceRetry.removeAttribute('aria-disabled');
        resourceRetry.textContent = 'Retry readings';
        body.querySelector('.ov-resources')?.removeAttribute('aria-busy');
      }
      const warning = body.querySelector('.ov-refresh-warning');
      if (warning) {
        warning.removeAttribute('aria-busy');
        const retry = warning.querySelector('button');
        retry.removeAttribute('aria-disabled');
        retry.textContent = 'Retry';
      }
    }
  }

  function retryResources() {
    if (disposed) return;
    resourceRetryPending = true;
    const retry = body.querySelector('[data-focus-key="usage:retry"]');
    // Keep the initiating control focusable while the guarded request runs.
    // Native disabled buttons lose focus in Chrome before snapshot replacement.
    if (retry) { retry.setAttribute('aria-disabled', 'true'); retry.textContent = 'Trying again…'; }
    body.querySelector('.ov-resources')?.setAttribute('aria-busy', 'true');
    if (!loadInFlight) void load(false);
  }

  function showRefreshFailure(initial) {
    if (initial || !overviewSnapshot) { body.replaceChildren(errorState()); return; }
    const changed = !overviewStale;
    const wasRetry = overviewRetryPending || resourceRetryPending;
    overviewRetryPending = false;
    overviewStale = true;
    const { apps, metrics, history } = overviewSnapshot;
    const workspace = buildOverviewWorkspace(apps, { ...metrics, state: 'stale' }, history, selectedScope);
    replaceOverviewContent(body, render(workspace, activitySnapshot));
    if (changed || wasRetry) appendOverviewAnnouncement(live, 'Overview could not refresh. Showing the last successful update.');
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
      availableApps: ctx.state.apps || [],
      retryPending: activityRetryPending,
    });
    current.replaceWith(next);
    restoreDisclosures(next, disclosures);
    if (!restoreFocus(next, focusKey) && (fallbackFocusKey || focusKey)) restoreFocus(next, fallbackFocusKey || 'activity:heading');
  }

  function selectScope(key, bringIntoView = false) {
    if (!overviewSnapshot || disposed) return;
    const { apps, metrics, history } = overviewSnapshot;
    const workspace = buildOverviewWorkspace(apps, overviewStale ? { ...metrics, state: 'stale' } : metrics, history, key);
    selectedScope = workspace.selectedKey;
    scopeNotice = '';
    lastLiveSignature = resourceLiveSignature(workspace.model.resources);
    lastLiveResources = workspace.model.resources;
    replaceOverviewContent(body, render(workspace, activitySnapshot));
    restoreOverviewStart(body);
    if (bringIntoView) body.querySelector('.ov-scope')?.scrollIntoView?.({ block: 'start' });
    live.textContent = '';
    appendOverviewAnnouncement(live, `${workspace.scopes.find((scope) => scope.key === selectedScope).label}. ${workspace.model.verdict.headline}. ${workspace.model.verdict.detail}`);
  }

  function render(workspace, activity) {
    const model = workspace.model;
    const root = el('div', 'ov-grid');
    if (overviewStale) {
      model.verdict = { tone: 'warning', headline: 'Overview data is stale', detail: 'App health, fleet membership, and resources reflect the last successful update.'
        + (model.attention.length ? ` Last reported: ${model.attention.length} ${model.attention.length === 1 ? 'app needs' : 'apps need'} attention.` : '') };
      const notice = el('section', 'ov-panel ov-refresh-warning');
      notice.setAttribute('aria-label', 'Overview refresh status');
      notice.appendChild(el('p', null, 'Overview could not refresh. Showing the last successful update.'));
      const stamp = el('time', 'ov-res-scope', `Last updated ${new Date(overviewSnapshot.updatedAt).toLocaleTimeString()}`);
      stamp.dateTime = overviewSnapshot.updatedAt;
      const retry = el('button', 'ov-btn', overviewRetryPending ? 'Trying again…' : 'Retry');
      if (overviewRetryPending) retry.setAttribute('aria-disabled', 'true');
      notice.setAttribute('aria-busy', String(overviewRetryPending));
      retry.type = 'button';
      retry.dataset.focusKey = 'overview:retry';
      retry.addEventListener('click', () => {
        if (retry.getAttribute('aria-disabled') === 'true') return;
        overviewRetryPending = true;
        retry.setAttribute('aria-disabled', 'true');
        retry.textContent = 'Trying again…';
        notice.setAttribute('aria-busy', 'true');
        if (!loadInFlight) void load(false);
      });
      notice.append(stamp, retry);
      root.appendChild(notice);
    }
    if (scopeNotice) root.appendChild(el('p', 'ov-scope-summary', scopeNotice));
    if (model.total === 0) {
      root.appendChild(renderFirstRun());
      return root;
    }
    // A scope picker earns its place only when it can narrow the app set.
    if (workspace.scopes.some((scope) => scope.total > 0 && scope.total < workspace.scopes[0].total)) {
      root.appendChild(renderScope(workspace));
    }
    root.appendChild(renderPulse(model));
    root.appendChild(renderFooter(model, activity));
    if (workspace.groups.length > 0) root.appendChild(renderFleets(workspace.groups));
    return root;
  }

  function renderScope(workspace) {
    const wrap = el('div', 'ov-scope');
    const field = el('div', 'ov-scope-field');
    const label = el('label', null, 'Show');
    label.htmlFor = 'ov-scope-select';
    const select = el('select', 'ov-scope-select');
    select.id = 'ov-scope-select';
    select.dataset.focusKey = 'overview:scope';
    for (const scope of workspace.scopes) {
      const option = el('option', null, `${scope.label} (${scope.total})`);
      option.value = scope.key;
      select.appendChild(option);
    }
    select.value = workspace.selectedKey;
    select.addEventListener('change', () => selectScope(select.value));
    field.append(label, select);
    wrap.appendChild(field);
    return wrap;
  }

  function renderFleets(groups) {
    const sec = el('section', 'ov-panel ov-fleets');
    sec.setAttribute('aria-labelledby', 'ov-fleets-title');
    const title = sectionTitle('Fleets');
    title.id = 'ov-fleets-title';
    sec.appendChild(title);
    sec.appendChild(el('p', 'ov-res-scope', 'App health and measured usage by fleet. Select a fleet to inspect its apps.'));
    const list = el('ul', 'ov-fleet-list');
    for (const group of groups) {
      const { model } = group;
      const row = el('li', 'ov-fleet-row');
      const main = el('div', 'ov-fleet-main');
      const button = el('button', 'ov-fleet-name', group.label);
      button.type = 'button';
      button.dataset.focusKey = `overview:${group.key}`;
      button.addEventListener('click', () => selectScope(group.key, true));
      main.appendChild(button);
      main.appendChild(el('span', 'ov-fleet-meta', `${model.total} ${model.total === 1 ? 'app' : 'apps'} · ${model.counts.healthy} running`));
      row.appendChild(main);
      row.appendChild(el('span', `ov-fleet-health ov-fleet-health--${overviewStale ? 'warning' : model.verdict.tone}`, overviewStale ? 'Last reported health · Stale' : model.verdict.headline));
      for (const metric of [model.resources.cpu, model.resources.memory]) {
        const value = el('span', 'ov-fleet-usage');
        const unavailable = metric.usage.state === 'unavailable';
        value.appendChild(el('b', null, model.resources.state === 'idle' ? '—' : unavailable ? 'Unavailable' : formatResource(metric.kind, metric.observedUsed)));
        value.appendChild(el('span', null, `${metric.kind === 'cpu' ? 'CPU' : 'Memory'}${metric.usage.state === 'partial' ? ' · Partial' : metric.usage.state === 'stale' ? ' · Stale' : ''}`));
        row.appendChild(value);
      }
      list.appendChild(row);
    }
    sec.appendChild(list);
    return sec;
  }

  // Health and recovery share one workspace; exceptional resource states still
  // offer direct jumps to their explanation below.
  function renderPulse(model) {
    const sec = el('section', 'ov-pulse ov-pulse--' + model.verdict.tone);
    sec.setAttribute('aria-label', `${model.resources.scope.label} status`);

    const verdict = el('div', 'ov-pulse-verdict');
    verdict.appendChild(el('span', 'ov-pulse-dot'));
    const vtext = el('div', 'ov-pulse-text');
    const headline = el('p', 'ov-pulse-headline', model.verdict.headline);
    headline.tabIndex = -1;
    headline.dataset.focusKey = 'overview:status';
    vtext.appendChild(headline);
    if (!model.attention.length || overviewStale) {
      const detail = ['nominal', 'neutral'].includes(model.verdict.tone)
        ? `${model.total} ${model.total === 1 ? 'app' : 'apps'}` : model.verdict.detail;
      vtext.appendChild(el('p', 'ov-pulse-detail', detail));
    }
    const actions = el('div', 'ov-pulse-actions');
    const addJump = (label, key, targetID) => {
      const inspect = el('button', 'ov-btn ov-coverage-jump', label);
      inspect.type = 'button';
      inspect.dataset.focusKey = key;
      inspect.addEventListener('click', () => {
        const target = document.getElementById(targetID);
        target?.focus();
        target?.scrollIntoView({ block: 'center' });
      });
      actions.appendChild(inspect);
    };
    const highUsage = [model.resources.cpu, model.resources.memory].some((metric) => ['critical', 'warning'].includes(metric.usage.severity));
    if (highUsage && !['stale', 'unavailable'].includes(model.resources.state)) addJump('Review app usage', 'usage:review', 'ov-resources-title');
    if (model.resources.hotspots.length > 0 && model.resources.state !== 'stale') {
      addJump('Review pressure alerts', 'pressure:review', 'ov-pressure-title');
    } else if (model.resources.state === 'stale') {
      addJump('Review last readings', 'usage:review', 'ov-resources-title');
    }
    if (model.resources.state === 'partial' || model.resources.state === 'unavailable') {
      addJump('Review resource coverage', 'coverage:review', 'ov-coverage-title');
    }
    if (actions.children.length) {
      actions.firstElementChild.classList.add('ov-primary-response');
      vtext.appendChild(actions);
    }
    verdict.appendChild(vtext);
    sec.appendChild(verdict);
    if (model.attention.length) sec.appendChild(renderAttention(model.attention));

    const active = model.segments.filter((s) => s.count > 0);
    if (model.total > 0) {
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
      const summary = el('div', 'ov-health-summary');
      summary.appendChild(legend);
      sec.appendChild(summary);
    }
    return sec;
  }

  // ── Attention: actionable rows for crashed / degraded apps. ──
  function renderAttention(items) {
    const sec = el('section', 'ov-attention');
    const title = sectionTitle('Needs attention', items.length);
    title.id = 'ov-attention-title';
    title.tabIndex = -1;
    title.dataset.focusKey = 'attention:heading';
    title.classList.add('sr-only');
    sec.setAttribute('aria-labelledby', title.id);
    sec.appendChild(title);
    const list = el('ul', 'ov-attn-list');
    for (const a of items) {
      const li = el('li', 'ov-attn-row');
      const main = el('div', 'ov-attn-main');
      const name = el('a', 'ov-attn-name', a.name);
      name.href = '/apps/' + encodeURIComponent(a.slug);
      name.setAttribute('data-nav', '');
      name.dataset.focusKey = `attention:${a.slug}:name`;
      main.appendChild(name);
      main.appendChild(el('span', 'ov-attn-reason', a.reason || formatStatus(a.status)));
      if (restartErrors.has(a.slug)) {
        const error = el('p', 'ov-attn-error', restartErrors.get(a.slug));
        error.setAttribute('role', 'alert');
        main.appendChild(error);
      }
      li.appendChild(main);

      const actions = el('div', 'ov-attn-actions');
      // Restart is a management action: only offer it to users who can manage
      // this app (the server would reject it for view-only members anyway), the
      // same gate the Apps grid uses.
      const lifecycle = appCardActions(a.app, ctx.canManageApp(ctx.state.user, a.app));
      if (lifecycle.showRestart || lifecycle.showStart) {
        const restart = el('button', 'ov-btn', pendingRestarts.has(a.slug) ? 'Restarting…' : 'Restart');
        restart.disabled = pendingRestarts.has(a.slug);
        restart.dataset.focusKey = `attention:${a.slug}:restart`;
        restart.type = 'button';
        restart.addEventListener('click', () => {
          if (pendingRestarts.has(a.slug)) return;
          restartErrors.delete(a.slug);
          main.querySelector('.ov-attn-error')?.remove();
          pendingRestarts.add(a.slug);
          restart.disabled = true;
          restart.textContent = 'Restarting…';
          Promise.resolve().then(() => ctx.restart(a.slug)).then((result) => {
            if (['error', 'network-error'].includes(result?.kind)) restartErrors.set(a.slug, result.message || 'Could not restart this app. Try again.');
          }).catch(() => {
            restartErrors.set(a.slug, 'Could not restart this app. Check your connection and try again.');
          }).finally(() => {
            pendingRestarts.delete(a.slug);
            if (!disposed) void load(false);
          });
        });
        actions.appendChild(restart);
      }
      const open = el('a', 'ov-btn ov-inspect', a.inspection.label);
      open.href = '/apps/' + encodeURIComponent(a.slug) + '/' + a.inspection.page;
      open.setAttribute('data-nav', '');
      open.dataset.focusKey = `attention:${a.slug}:open`;
      actions.prepend(open);
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
    const title = el('h2', 'ov-firstrun-title', 'Deploy your first Shiny app');
    title.tabIndex = -1;
    title.dataset.focusKey = 'overview:status';
    sec.appendChild(title);
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
      availableApps: ctx.state.apps || [],
      retryPending: activityRetryPending,
    }));
    return footer;
  }

  function renderResources(res) {
    return renderResourcePressure(res, {
      canManageApp: (app) => ctx.canManageApp(ctx.state.user, app),
      onRetry: overviewStale ? null : retryResources, retryPending: resourceRetryPending,
    });
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
  stopPoll = startVisiblePoll(document, POLL_MS, () => { if (!disposed) load(false); });

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
  heading.appendChild(el('p', 'ov-res-scope', 'Across the hub'));
  if (state === 'stale' && snapshot.updatedAt) {
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

  const appNames = new Map((options.availableApps || []).map((app) => [app.slug, app.name]));
  const availableAppSlugs = options.availableAppSlugs ?? (options.availableApps ? new Set(appNames.keys()) : null);
  const brief = buildActivityBrief(snapshot.events, undefined, availableAppSlugs, !!snapshot.hasMore, appNames);
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
    operation.target,
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
    meta.appendChild(activitySeparator());
  }
  const actorAlreadyNamed = operation.kind !== 'group' && operation.resourceType === 'user'
    && operation.actorLabel === operation.target;
  if (!actorAlreadyNamed) meta.appendChild(el('span', 'ov-activity-actor', operation.actorLabel));
  if (operation.kind !== 'group' && operation.resourceTypeLabel) {
    if (meta.childNodes.length) meta.appendChild(activitySeparator());
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
    'event',
    event.target,
  ));
  appendActivityTarget(main, event.target, event.targetHref, `${operationKey}:${event.id}:target`);
  if (event.tone) {
    main.appendChild(el('span', `ov-activity-status ov-activity-status--${event.tone.name}`, event.tone.label));
  }
  li.appendChild(main);
  li.appendChild(renderActivityTime(event.createdAt, 'ov-activity-child-time'));
  return li;
}

function activityActionLink(label, href, focusKey, extraClass = '', auditScope = 'event', target = '') {
  const link = el('a', `ov-activity-action ov-activity-action-link ${extraClass}`.trim(), label);
  link.href = href || '/audit-log';
  link.setAttribute('data-nav', '');
  link.dataset.focusKey = `activity:${focusKey}`;
  const destinationLabel = `Open audit ${auditScope} for ${label}${target ? ` ${target}` : ''}`;
  link.setAttribute('aria-label', destinationLabel);
  link.title = destinationLabel;
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
    parent.appendChild(link);
    return;
  }
  parent.appendChild(el('span', 'ov-activity-target ov-activity-target--static', target));
}

// The dot between two pieces of activity metadata is a visual divider and
// nothing more: the flex gap already separates the parts for a sighted reader,
// and a screen reader that announces it reads "middle dot" between every field.
// Built here rather than inline at each site so the two can never disagree
// about whether it is decoration.
function activitySeparator() {
  const dot = el('span', 'ov-activity-separator', '·');
  dot.setAttribute('aria-hidden', 'true');
  return dot;
}

function renderActivityTime(createdAt, cls = 'ov-activity-time') {
  const formatted = activityTime(createdAt);
  if (!formatted.valid) return el('span', `${cls} ov-activity-time--unknown`, formatted.exact);
  const time = el('time', cls, formatted.relative);
  time.dateTime = formatted.datetime;
  time.title = formatted.title;
  time.setAttribute('aria-label', `${formatted.title}, ${formatted.relative}`);
  return time;
}

function activityUnavailable(onRetry, retryPending = false) {
  const state = el('div', 'ov-activity-unavailable');
  state.appendChild(activityIcon('attention'));
  const copy = el('div', 'ov-activity-unavailable-copy');
  copy.appendChild(el('b', null, 'Activity unavailable'));
  copy.appendChild(el('p', null, 'Recent changes could not be loaded. App health is shown above.'));
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

function activityIconPath(kind) {
  if (kind === 'restart') return 'M15.2 7.1A5.8 5.8 0 1 0 15.6 13M15.2 3.8v3.7h-3.7';
  if (kind === 'deploy') return 'M10 3v9m0 0 3-3m-3 3L7 9M4 14.5h12';
  if (kind === 'fleet') return 'm10 3 6 3-6 3-6-3 6-3Zm6 7-6 3-6-3m12 4-6 3-6-3';
  if (kind === 'attention') return 'M10 6.2v4.4m0 3v.1M3.4 15.5l5.2-10a1.57 1.57 0 0 1 2.8 0l5.2 10A1 1 0 0 1 15.7 17H4.3a1 1 0 0 1-.9-1.5Z';
  if (kind === 'security') return 'M10 2.8 15 5v4.1c0 3.4-2 6.3-5 8.1-3-1.8-5-4.7-5-8.1V5l5-2.2Z';
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
export function renderResourcePressure(res, { canManageApp = () => false, onRetry = null, retryPending = false } = {}) {
  const sec = el('section', 'ov-panel ov-resources');
  sec.setAttribute('aria-labelledby', 'ov-resources-title');

  const head = el('div', 'ov-res-head');
  const titleWrap = el('div', 'ov-res-heading');
  const title = el('h2', 'ov-section-title', panelTitle(res.scale, res.scope));
  title.id = 'ov-resources-title';
  title.tabIndex = -1;
  title.dataset.focusKey = 'usage:heading';
  const titleLine = el('div', 'ov-res-titleline');
  titleLine.append(title, renderCapacityHelp(res));
  titleWrap.appendChild(titleLine);
  titleWrap.appendChild(el('p', 'ov-res-scope', panelScope(res)));
  head.appendChild(titleWrap);
  head.appendChild(el('p', 'ov-res-updated', updatedText(res)));
  sec.appendChild(head);
  if (res.state === 'stale' || res.state === 'unavailable') {
    const recovery = el('div', 'ov-resource-retry');
    recovery.appendChild(el('p', 'ov-capacity-basis', 'Retries every 10 seconds while this page is visible.'));
    if (onRetry) {
      const retry = el('button', 'ov-btn', retryPending ? 'Trying again…' : 'Retry readings');
      retry.type = 'button';
      if (retryPending) retry.setAttribute('aria-disabled', 'true');
      retry.dataset.focusKey = 'usage:retry';
      retry.addEventListener('click', () => { if (retry.getAttribute('aria-disabled') !== 'true') onRetry(); });
      recovery.appendChild(retry);
    }
    if (retryPending) sec.setAttribute('aria-busy', 'true');
    sec.appendChild(recovery);
  }

  if (res.state === 'unavailable') {
    const unavailable = el('div', 'ov-res-message ov-res-message--unavailable');
    unavailable.appendChild(el('b', null, 'Metrics unavailable'));
    unavailable.appendChild(el('span', null, 'CPU and memory pressure could not be verified. Health status remains available above.'));
    sec.appendChild(unavailable);
    const rows = el('div', 'ov-capacity-list');
    rows.append(renderCapacityRow('CPU', res.cpu, res.runningReplicas), renderCapacityRow('Memory', res.memory, res.runningReplicas));
    sec.appendChild(rows);
    sec.appendChild(renderCoverageGaps(res, canManageApp));
    return sec;
  }
  if (res.state === 'idle') {
    sec.appendChild(el('p', 'ov-res-message', 'No running replicas to measure.'));
    const rows = el('div', 'ov-capacity-list');
    rows.append(renderCapacityRow('CPU', res.cpu, 0), renderCapacityRow('Memory', res.memory, 0));
    sec.appendChild(rows);
    return sec;
  }

  const rows = el('div', 'ov-capacity-list');
  rows.appendChild(renderCapacityRow('CPU', res.cpu, res.runningReplicas));
  rows.appendChild(renderCapacityRow('Memory', res.memory, res.runningReplicas));
  sec.appendChild(rows);

  // With no limits anywhere there are no per-replica thresholds to alert on, so
  // the panel answers the next question instead: which apps account for this.
  if (res.scale !== 'limits') {
    if (res.state === 'stale' || res.cpu.gaps.length || res.memory.gaps.length) sec.appendChild(renderCoverageGaps(res, canManageApp));
    if (res.topConsumers) sec.appendChild(renderTopConsumers(res.topConsumers, res.scope));
    return sec;
  }

  if (res.hotspots.length > 0) {
    const hotspotHead = el('div', 'ov-hotspot-head');
    const title = el('h3', 'ov-res-subhead', 'Pressure alerts');
    title.id = 'ov-pressure-title';
    title.tabIndex = -1;
    title.dataset.focusKey = 'pressure:heading';
    hotspotHead.appendChild(title);
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
  }
  if (res.state === 'stale' || res.cpu.gaps.length || res.memory.gaps.length) sec.appendChild(renderCoverageGaps(res, canManageApp));
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
  revealFocusedRow(body, focusKey);
  if (!restoreFocus(body, focusKey) && focusKey && focusKey.startsWith('activity:')) {
    restoreFocus(body, 'activity:heading');
  } else if (focusKey === 'usage:retry' && !body.contains(document.activeElement)) {
    if (!restoreFocus(body, 'overview:retry')) restoreFocus(body, 'usage:heading');
  } else if (focusKey && !body.contains(document.activeElement)) {
    restoreOverviewStart(body);
  }
}

function restoreOverviewStart(body) {
  return restoreFocus(body, 'overview:scope') || restoreFocus(body, 'overview:status');
}

function revealFocusedRow(body, focusKey) {
  // A new higher-priority gap can push the focused row into the overflow.
  // Keep that still-present action visible before restoring its focus.
  if (focusKey?.startsWith('coverage') || focusKey?.startsWith('pressure:')) {
    const target = [...body.querySelectorAll('[data-focus-key]')].find((node) => node.dataset.focusKey === focusKey);
    if (target?.closest('.ov-coverage-row[hidden]')) {
      body.querySelector('[data-disclosure-key="coverage-gaps"]')?.click();
    }
    if (target?.closest('.ov-hotspot-row[hidden]')) {
      body.querySelector('[data-disclosure-key="pressure-alerts"]')?.click();
    }
  }
}

function renderCapacityRow(label, metric, runningReplicas) {
  const usage = metric.usage;
  const row = el('section', 'ov-capacity-row ov-capacity-row--' + usage.severity);
  row.setAttribute('aria-label', `${label} app usage and capacity`);
  const top = el('div', 'ov-capacity-top');
  top.appendChild(el('h3', 'ov-capacity-label', label === 'Memory' ? 'Memory (RSS)' : label));
  const status = usage.state === 'idle' ? 'Idle' : usage.state === 'unavailable' ? 'Unavailable' : usage.state === 'stale' ? 'Stale'
    : usage.state === 'partial' ? (usage.severity === 'critical' ? 'Near capacity · Partial' : usage.severity === 'warning' ? 'High usage · Partial' : 'Partial reporting')
      : usage.severity === 'critical' ? 'Near capacity' : usage.severity === 'warning' ? 'High app usage' : 'Live';
  if (status !== 'Live') top.appendChild(el('span', 'ov-pressure-state ov-pressure-state--' + stateTone(usage), status));
  row.appendChild(top);

  const values = el('dl', 'ov-resource-values');
  const measured = el('div');
  measured.appendChild(el('dt', null, usage.state === 'stale' ? 'Last reported app usage' : 'Measured app usage'));
  const measuredValue = el('dd', 'ov-capacity-value');
  setMetricValue(measuredValue, usage.state === 'idle' ? '—' : usage.state === 'unavailable' ? 'Unavailable' : formatResource(metric.kind, usage.used));
  measured.appendChild(measuredValue);
  values.appendChild(measured);
  const capacity = el('div');
  capacity.appendChild(el('dt', null, capacitySourceLabel(metric.hostCapacity?.source)));
  const capacityReading = el('dd', 'ov-capacity-value');
  setMetricValue(capacityReading, metric.hostCapacity ? formatResource(metric.kind, metric.hostCapacity.value) : 'Unknown');
  capacity.appendChild(capacityReading);
  values.appendChild(capacity);
  row.appendChild(values);

  if (usage.fraction != null) {
    const meter = document.createElement('meter');
    const meterText = `${label} reported app usage, ${percentage(usage.fraction)} of ${capacitySourceLabel(metric.hostCapacity?.source).toLowerCase()}; ${usage.coverage.observedReplicas} of ${runningReplicas} replicas reporting`;
    if (metric.kind === 'cpu') updateCPUMeter(meter, usage.fraction, meterText);
    else {
      meter.className = 'ov-capacity-meter ov-capacity-meter--' + usage.severity;
      meter.min = 0;
      meter.max = 1;
      meter.low = 0.85;
      meter.high = 0.95;
      meter.optimum = 0;
      meter.value = Math.min(1, Math.max(0, usage.fraction));
      meter.setAttribute('aria-label', meterText);
    }
    row.appendChild(meter);
  }
  const reporting = runningReplicas === 0 ? 'No running replicas' : `${metric.coverage.observedReplicas} of ${runningReplicas} replicas reporting`;
  row.appendChild(el('p', 'ov-capacity-coverage', `${reporting}${usage.fraction != null ? ` · ${percentage(usage.fraction)} of capacity` : ''}`));
  if (usage.state === 'ready') row.appendChild(el('p', 'ov-capacity-basis', trendText(usage.trend, metric.kind)));
  if (metric.scale === 'limits') {
    const limits = el('div', 'ov-app-limit-reading');
    limits.appendChild(el('h4', null, 'App-limit checks'));
    limits.appendChild(el('p', null, `${metric.coverage.coveredReplicas} of ${runningReplicas} replicas checked`));
    if (metric.capacity > 0) limits.appendChild(el('p', 'ov-limit-values', `${capacityValue(metric)} · ${percentage(metric.fraction)} of combined app limits`));
    row.appendChild(limits);
  }
  return row;
}

function percentage(fraction) {
  return fraction > 0 && fraction < 0.01 ? '<1%' : `${Math.round(fraction * 100)}%`;
}

function capacitySourceLabel(source) {
  if (source === 'cgroup-limit' || source === 'cgroup-quota' || source === 'cgroup-cpuset') return 'Container / service capacity';
  if (source === 'config') return 'Configured capacity';
  if (source === 'host-total') return 'Host capacity · OS total';
  if (source === 'affinity') return 'CPU capacity · Allowed cores';
  return 'Host capacity';
}

function disclosure(label, key, content) {
  const wrap = el('div', 'ov-resource-disclosure');
  const toggle = el('button', 'ov-res-toggle', label);
  toggle.classList.add('ov-disclosure-toggle');
  toggle.appendChild(activityIcon('chevron'));
  toggle.type = 'button';
  toggle.dataset.disclosureKey = key;
  toggle.dataset.focusKey = key;
  toggle.setAttribute('aria-expanded', 'false');
  content.id = `ov-details-${encodeURIComponent(key)}`;
  content.hidden = true;
  toggle.setAttribute('aria-controls', content.id);
  toggle.addEventListener('click', () => {
    const expanded = toggle.getAttribute('aria-expanded') === 'true';
    toggle.setAttribute('aria-expanded', String(!expanded));
    content.hidden = expanded;
  });
  wrap.append(toggle, content);
  return wrap;
}

function documentationLink(label, href, focusKey) {
  const link = el('a', 'ov-resource-help-link', label);
  link.href = href;
  link.target = '_blank';
  link.rel = 'noopener noreferrer';
  link.dataset.focusKey = focusKey;
  link.setAttribute('aria-label', `${label} (opens in a new tab)`);
  return link;
}

function renderCapacityHelp(res) {
  const content = el('div', 'ov-measurement-details');
  // Older browsers still have a useful help destination. Modern browsers own
  // top-layer placement, outside-click dismissal and Escape without global listeners.
  if (typeof content.showPopover !== 'function') {
    const link = documentationLink('About these metrics', 'https://shinyhub.dev/configuration/#host-capacity', 'capacity:guide');
    link.classList.add('ov-metrics-help-link');
    return link;
  }
  content.id = 'ov-metrics-help';
  content.setAttribute('popover', 'auto');
  content.setAttribute('role', 'region');
  content.setAttribute('aria-labelledby', 'ov-metrics-help-title');
  const toggle = el('button', 'ov-metrics-help');
  toggle.type = 'button';
  toggle.appendChild(activityIcon('info'));
  toggle.setAttribute('aria-label', 'About these metrics');
  toggle.setAttribute('title', 'About these metrics');
  toggle.setAttribute('aria-expanded', 'false');
  toggle.setAttribute('aria-controls', content.id);
  toggle.setAttribute('popovertarget', content.id);
  toggle.dataset.disclosureKey = 'capacity:help';
  toggle.dataset.focusKey = 'capacity:help';
  toggle.addEventListener('click', (event) => {
    event.preventDefault();
    if (toggle.getAttribute('aria-expanded') === 'true') content.hidePopover();
    else content.showPopover({ source: toggle });
  });
  content.addEventListener('beforetoggle', (event) => {
    toggle.setAttribute('aria-expanded', String(event.newState === 'open'));
  });
  const heading = el('div', 'ov-metrics-help-head');
  const title = el('h3', null, 'About these metrics');
  title.id = 'ov-metrics-help-title';
  const close = el('button', 'ov-metrics-help-close', 'Close');
  close.type = 'button';
  close.dataset.focusKey = 'capacity:close';
  close.setAttribute('aria-label', 'Close metrics help');
  close.addEventListener('click', () => {
    content.hidePopover();
    toggle.focus({ preventScroll: true });
  });
  heading.append(title, close);
  content.appendChild(heading);
  const definitions = el('dl', 'ov-measurement-definitions');
  const addDefinition = (label, ...paragraphs) => {
    const row = el('div', 'ov-measurement-definition');
    const description = el('dd');
    for (const paragraph of paragraphs) {
      description.appendChild(typeof paragraph === 'string' ? el('p', null, paragraph) : paragraph);
    }
    row.append(el('dt', null, label), description);
    definitions.appendChild(row);
  };
  addDefinition('Measured usage', 'Includes reporting apps in this view. Other host processes are excluded. Capacity is the total allowance, not free memory or idle CPU.');
  for (const metric of [res.cpu, res.memory]) {
    const source = metric.hostCapacity?.source;
    const resource = metric.kind === 'cpu' ? 'CPU' : 'Memory';
    const sourceText = el('p');
    if (!metric.hostCapacity) {
      sourceText.textContent = 'Could not be detected. Check the ShinyHub startup log.';
    } else if (source === 'config') {
      sourceText.append('Configured in ', el('code', null, `server.host_capacity_${metric.kind === 'cpu' ? 'cores' : 'memory_mb'}`), '.');
    } else {
      sourceText.textContent = source?.startsWith('cgroup-')
        ? 'Capacity is restricted by a container or service. If lower than expected, ask the host administrator to check its resource settings. Physical machine capacity is not reported separately.'
        : source === 'host-total' ? 'Total reported by the operating system.'
          : source === 'affinity' ? 'The CPU cores this process may run on.'
            : 'Reported by the server.';
    }
    addDefinition(`${resource} capacity`, sourceText);
  }
  addDefinition('Memory (RSS)', 'RAM held by reporting apps. Shared memory pages may be counted more than once when app usage is added together.');
  const help = el('div', 'ov-resource-help');
  help.appendChild(documentationLink('Host capacity guide', 'https://shinyhub.dev/configuration/#host-capacity', 'capacity:guide'));
  content.append(definitions, help);
  const wrapper = el('div', 'ov-metrics-help-wrap');
  wrapper.append(toggle, content);
  return wrapper;
}

function renderCoverageGaps(res, canManageApp) {
  const section = el('section', 'ov-coverage');
  const title = el('h3', 'ov-res-subhead', 'Resource coverage');
  title.id = 'ov-coverage-title';
  title.tabIndex = -1;
  title.dataset.focusKey = 'coverage:heading';
  section.appendChild(title);
  const groups = new Map();
  for (const metric of [res.cpu, res.memory]) {
    for (const gap of metric.gaps || []) {
      // Group by action identity, never by the changing count or replica index.
      const key = `${gap.slug}:${gap.reason}`;
      if (!groups.has(key)) groups.set(key, { ...gap, key, counts: [] });
      groups.get(key).counts.push(`${metric.kind === 'cpu' ? 'CPU' : 'Memory'}: ${gap.replicas} ${gap.replicas === 1 ? 'replica' : 'replicas'}`);
    }
  }
  if (!groups.size) {
    section.appendChild(el('p', 'ov-capacity-basis', 'Live metrics could not be loaded. Check the ShinyHub service logs and connection. This page retries automatically.'));
    return section;
  }
  section.appendChild(el('p', 'ov-capacity-basis', res.state === 'stale'
    ? 'Last reported gaps. Live metrics are being retried.'
    : [...groups.values()].every((gap) => gap.reason === 'unlimited')
      ? 'All replicas are reporting. Setting app limits is optional.'
      : 'Measurement gaps and app-limit checks.'));
  const reasons = {
    metrics: ['No measurement', 'Inspect the app’s replica health and recent worker logs. Check the ShinyHub service logs if several apps stop reporting. This page retries automatically.'],
    unlimited: ['No limit set · optional', 'Usage is still included above when this replica reports. Set a limit in app configuration only if you want to check this replica against an enforced allocation.'],
    unenforced: ['Limit not enforced · host action', 'Ask the host administrator to inspect runtime support and the ShinyHub startup warnings. Native Linux limits require cgroup v2 with the relevant controllers delegated to the service. Under systemd, check Delegate=cpu memory pids. Changing the limit alone cannot enable missing controllers.'],
    unknown: ['Limit status unknown', 'Inspect the app’s replica health and resource settings. Check worker connectivity and the ShinyHub service logs to confirm the effective limit and whether it is enforced.'],
  };
  const list = el('ul', 'ov-coverage-list');
  list.id = 'ov-coverage-gaps';
  // Prioritize missing measurements and ineffective protection before optional limits.
  const rank = { metrics: 0, unenforced: 1, unknown: 2, unlimited: 3 };
  const ordered = [...groups.values()].sort((a, b) => rank[a.reason] - rank[b.reason] || a.name.localeCompare(b.name));
  for (const [index, gap] of ordered.entries()) {
    const row = el('li', 'ov-coverage-row');
    if (index >= 4) row.hidden = true;
    const main = el('div', 'ov-coverage-main');
    main.appendChild(el('b', null, gap.name));
    const [reason, baseAdvice] = reasons[gap.reason];
    const advice = gap.reason === 'unlimited' && !canManageApp(gap.app)
      ? 'Usage is still included above when this replica reports. Ask an app manager to set a limit if you want to check this replica against an enforced allocation.' : baseAdvice;
    main.appendChild(el('p', 'ov-capacity-basis', reason));
    main.appendChild(el('p', 'ov-coverage-counts', gap.counts.join(' · ')));
    row.appendChild(main);
    const actions = el('div', 'ov-coverage-actions');
    const configure = ['unlimited', 'unknown'].includes(gap.reason) && canManageApp(gap.app);
    if (gap.reason !== 'unenforced') {
      const link = el('a', 'ov-btn', configure ? 'Review settings' : 'Inspect app');
      link.href = `/apps/${encodeURIComponent(gap.slug)}${configure ? '/configuration' : ''}`;
      link.setAttribute('data-nav', '');
      link.setAttribute('aria-label', `${link.textContent} for ${gap.name}`);
      link.dataset.focusKey = `coverage:${gap.key}`;
      actions.appendChild(link);
    }
    const help = el('div', 'ov-resource-help');
    help.appendChild(el('p', null, advice));
    if (gap.reason === 'unenforced') help.appendChild(documentationLink('Resource enforcement guide', 'https://shinyhub.dev/scaling/#native-mode-under-systemd-requires-delegate', `coverage-guide:${gap.key}`));
    const details = disclosure(gap.reason === 'unenforced' ? 'How to enable' : 'Details', `coverage-help:${gap.key}`, help);
    details.querySelector('button').setAttribute('aria-label', `${gap.reason === 'unenforced' ? 'How to enable limits' : 'Coverage details'} for ${gap.name}`);
    // Put the disclosure button with the actions, its content across the full row.
    actions.appendChild(details.firstElementChild);
    row.append(actions, help);
    list.appendChild(row);
  }
  section.appendChild(list);
  if (groups.size > 4) {
    const toggle = el('button', 'ov-res-toggle', `View all ${groups.size} coverage gaps`);
    toggle.type = 'button';
    toggle.setAttribute('aria-expanded', 'false');
    toggle.setAttribute('aria-controls', list.id);
    toggle.dataset.disclosureKey = 'coverage-gaps';
    toggle.dataset.focusKey = 'coverage:toggle';
    toggle.addEventListener('click', () => {
      const expanded = toggle.getAttribute('aria-expanded') === 'true';
      toggle.setAttribute('aria-expanded', String(!expanded));
      for (const [index, row] of [...list.children].entries()) if (index >= 4) row.hidden = expanded;
      toggle.textContent = expanded ? `View all ${groups.size} coverage gaps` : 'Show fewer';
    });
    section.appendChild(toggle);
  }
  return section;
}

// renderTopConsumers attributes fleet usage to the apps behind it. It replaces
// the pressure alerts on a fleet with no limits: nothing can be near a
// threshold that does not exist, so the useful question is who is using it.
function renderTopConsumers(top, scope = { key: 'all', label: 'All apps' }) {
  const wrap = document.createDocumentFragment();
  const head = el('div', 'ov-hotspot-head');
  head.appendChild(el('h3', 'ov-res-subhead', `Top ${top.kind === 'cpu' ? 'CPU' : 'memory'} consumers`));
  wrap.appendChild(head);
  wrap.appendChild(el('p', 'ov-res-scope', `Share of measured ${top.kind} usage · ${scope.label}${top.partial ? ' · Partial reporting' : ''}`));

  const list = el('ul', 'ov-consumer-list');
  for (const item of top.items) {
    const li = el('li', 'ov-consumer-row');
    const link = el('a', 'ov-hotspot-name', item.name);
    link.href = '/apps/' + encodeURIComponent(item.slug) + '/overview';
    link.setAttribute('aria-label', `${item.name} Inspect resources`);
    link.append(' ', el('span', 'ov-resource-link-hint', 'Inspect resources'));
    link.setAttribute('data-nav', '');
    link.dataset.focusKey = `consumer:${item.slug}:${top.kind}`;
    const main = el('div', 'ov-consumer-main');
    main.appendChild(link);
    if (scope.key === 'all' && item.fleetID) main.appendChild(el('span', 'ov-fleet-meta', `Fleet · ${item.fleetID}`));
    li.appendChild(main);

    const share = el('span', 'ov-consumer-share');
    share.setAttribute('role', 'img');
    share.setAttribute('aria-label', `${Math.round(item.fraction * 100)} percent of measured ${top.kind} usage in ${scope.label}`);
    const fill = el('span', 'ov-consumer-fill');
    fill.style.width = `${Math.max(2, Math.round(item.fraction * 100))}%`;
    share.appendChild(fill);
    li.appendChild(share);

    const values = el('span', 'ov-consumer-values');
    values.appendChild(el('b', null, formatResource(top.kind, item.used)));
    values.appendChild(el('span', null, `${Math.round(item.fraction * 100)}% of measured usage`));
    li.appendChild(values);
    list.appendChild(li);
  }
  wrap.appendChild(list);
  return wrap;
}

function panelTitle(scale, scope) {
  if (!scope || scope.key === 'all') return 'App resource usage';
  return scope.key.startsWith('fleet:') ? 'Fleet resource usage' : 'App resource usage';
}

// Keep the selected fleet explicit; each reading names its capacity source.
function panelScope(res) {
  const { scope } = res;
  const prefix = scope && scope.key !== 'all' ? scope.label : 'All apps';
  return `${scope && scope.key !== 'all' ? prefix + ' · ' : ''}Reporting apps only. Other host processes are excluded.`;
}

function renderHotspot(item) {
  const li = el('li', 'ov-hotspot-row');
  const main = el('div', 'ov-hotspot-main');
  const link = el('a', 'ov-hotspot-name', item.name);
  link.href = '/apps/' + encodeURIComponent(item.slug) + '/overview';
  link.setAttribute('aria-label', `${item.name} Inspect resources: ${item.metric === 'cpu' ? 'CPU' : 'memory'} pressure`);
  link.append(' ', el('span', 'ov-resource-link-hint', 'Inspect resources'));
  link.setAttribute('data-nav', '');
  link.dataset.focusKey = `pressure:${item.slug}:${item.metric}`;
  main.appendChild(link);
  const replica = item.replicaIndex == null ? '' : ` · Replica ${item.replicaIndex + 1}`;
  main.appendChild(el('span', 'ov-hotspot-meta', `${item.metric === 'cpu' ? 'CPU' : 'Memory'}${replica}`));
  li.appendChild(main);

  const meter = document.createElement('meter');
  if (item.metric === 'cpu') updateCPUMeter(meter, item.fraction, hotspotLabel(item));
  else {
    meter.className = 'ov-capacity-meter ov-capacity-meter--' + item.severity;
    meter.min = 0;
    meter.max = 1;
    meter.low = 0.85;
    meter.high = 0.95;
    meter.optimum = 0;
    meter.value = Math.min(1, item.fraction);
    meter.setAttribute('aria-label', hotspotLabel(item));
  }
  li.appendChild(meter);

  const values = el('span', 'ov-hotspot-values');
  values.appendChild(el('b', null, `${Math.round(item.fraction * 100)}%`));
  values.appendChild(el('span', null, `${formatResource(item.metric, item.used)} / ${formatResource(item.metric, item.limit)}`));
  li.appendChild(values);
  li.appendChild(el('span', 'ov-pressure-state ov-pressure-state--' + item.severity, titleCase(item.severity)));
  return li;
}

function capacityValue(metric) {
  if (metric.state === 'unavailable') return 'Unavailable';
  if (metric.capacity > 0) {
    if (metric.kind === 'cpu') return formatCoreCapacity(metric.used / 100, metric.capacity / 100);
    return `${formatResource(metric.kind, metric.used)} / ${formatResource(metric.kind, metric.capacity)}`;
  }
  if (metric.coverage.observedReplicas > 0) return `${formatResource(metric.kind, metric.observedUsed)} in use`;
  return 'Unavailable';
}

function formatResource(kind, value) {
  if (kind === 'cpu') {
    const cores = Number(value || 0) / 100;
    return cores > 0 && cores < 0.01 ? '<0.01 cores' : formatCoreCount(cores);
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
  const usage = [res.cpu, res.memory].map((metric) => `${metric.usage.state}:${metric.usage.severity}`).join(':');
  const gaps = [res.cpu, res.memory].flatMap((metric) => metric.gaps.map((gap) => `${metric.kind}:${gap.slug}:${gap.reason}:${gap.replicas}`)).sort().join(',');
  return [res.state, res.cpu.state, res.cpu.severity, res.memory.state, res.memory.severity, usage, gaps, hotspots].join(':');
}

export function resourceLiveSummary(res, previous = null) {
  if (res.state === 'unavailable') return 'Resource metrics are unavailable.';
  if (res.state === 'stale') return 'Resource metrics are stale; showing the last successful snapshot.';
  const changed = changedHotspot(res.hotspots, previous && previous.hotspots);
  if (changed) {
    if (changed.cleared) return `Resource pressure cleared: ${hotspotDescription(changed.item)}.`;
    return `Resource pressure alert: ${hotspotDescription(changed.item)}.`;
  }
  const highUsage = [res.memory, res.cpu].find((metric) => ['critical', 'warning'].includes(metric.usage.severity));
  if (highUsage) return `Reported app ${highUsage.kind} usage is ${percentage(highUsage.usage.fraction)} of available capacity.`;
  return `Resource coverage is ${res.state}. CPU: ${res.cpu.coverage.observedReplicas} of ${res.runningReplicas} replicas reporting. Memory: ${res.memory.coverage.observedReplicas} of ${res.runningReplicas} replicas reporting.`;
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
