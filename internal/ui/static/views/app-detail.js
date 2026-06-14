// App detail view. Mounts #app-detail-view, populates the header, and shows
// the requested tab. Tabs other than Overview are added in later tasks; for
// now Overview is the only one with a renderer and other tabs show "Coming
// soon" placeholders.
import { makeFleetBadge, renderFleetDigest } from '/static/views/fleet-ui.js';
import { backendLabel, metricsText, reasonLabel } from '/static/views/replica-display.js';
import { makeTraceRow, formatPollStatus } from '/static/views/traces-ui.js';
import {
  summariseAutoscale,
  formatRejectsByReason,
  renderAutoscaleSummary,
  renderRejectsByReason,
} from '/static/views/autoscale.js';
import { deploymentListModels, relativeTime } from '/static/views/deployment-row.js';

const TAB_ROUTES = ['overview', 'logs', 'traces', 'deployments', 'configuration', 'data', 'access'];
const MANAGER_ONLY_TABS = new Set(['configuration', 'data', 'access']);

function pluralize(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
}

function formatStatus(status) {
  if (!status) return '';
  return status.charAt(0).toUpperCase() + status.slice(1);
}

export function mountAppDetail(ctx) {
  const view = document.getElementById('app-detail-view');
  const panels = {
    overview:      document.getElementById('detail-overview-panel'),
    logs:          document.getElementById('detail-logs-panel'),
    traces:        document.getElementById('detail-traces-panel'),
    deployments:   document.getElementById('detail-deployments-panel'),
    configuration: document.getElementById('detail-configuration-panel'),
    data:          document.getElementById('detail-data-panel'),
    access:        document.getElementById('detail-access-panel'),
  };
  const tabEls = Object.fromEntries(
    TAB_ROUTES.map(t => [t, document.getElementById(`detail-tab-${t}`)]),
  );

  // Maintain a data-overflow hint on the tab strip so CSS can fade only the
  // edge(s) with clipped tabs when it scrolls (mobile). Wired once; the strip is
  // static markup, so the listeners outlive individual renders.
  const tabsNav = document.querySelector('.settings-tabs');
  function updateTabOverflow() {
    if (!tabsNav) return;
    const slack = tabsNav.scrollWidth - tabsNav.clientWidth;
    if (slack <= 1) { tabsNav.removeAttribute('data-overflow'); return; }
    const atStart = tabsNav.scrollLeft <= 1;
    const atEnd = tabsNav.scrollLeft >= slack - 1;
    tabsNav.setAttribute('data-overflow', atStart ? 'start' : atEnd ? 'end' : 'mid');
  }
  // Center the active tab and recompute the edge fade. Run on the next frame and
  // again after web fonts load (on a fresh/deep-link load the tabs widen when
  // the font swaps in, which is what makes the strip overflow in the first place).
  function syncTabStrip(activeEl) {
    if (activeEl) {
      try { activeEl.scrollIntoView({ inline: 'center', block: 'nearest' }); } catch { /* older browsers */ }
    }
    updateTabOverflow();
  }
  if (tabsNav) {
    tabsNav.addEventListener('scroll', updateTabOverflow, { passive: true });
    window.addEventListener('resize', updateTabOverflow, { passive: true });
  }

  let tabCleanup = null;

  return async function mount(params) {
    const { slug } = params;
    const tab = TAB_ROUTES.includes(params.tab) ? params.tab : 'overview';

    // Preserve the user's URL: /apps/<slug>/overview is a legitimate route
    // (every other tab keeps its segment, so /overview should too). The
    // previous version replaced it with /apps/<slug>, which surprised users
    // who pasted/bookmarked the explicit overview URL.

    const resp = await ctx.api(`/api/apps/${slug}`);
    if (resp.status === 404) { ctx.navigate('/'); return {}; }
    if (resp.status === 401) { ctx.onUnauthorized(); return {}; }
    if (!resp.ok) { return {}; }
    // GET /api/apps/:slug returns {app, replicas_status}.
    const body = await resp.json();
    const app = body.app || body;
    if (typeof body.can_manage === 'boolean') app.can_manage = body.can_manage;
    // runtime_mode gates the Resources controls (limits are docker-only).
    if (typeof body.runtime_mode === 'string') app.runtime_mode = body.runtime_mode;
    const replicasStatus = Array.isArray(body.replicas_status) ? body.replicas_status : [];

    const canManage = ctx.canManageApp(ctx.state.user, app);

    // Record the app so the static header kebab (wired once in app.js) acts on
    // the right app.
    if (ctx.setDetailApp) ctx.setDetailApp(app);

    if (!canManage && MANAGER_ONLY_TABS.has(tab)) {
      ctx.navigate(`/apps/${slug}`, { replace: true });
      return {};
    }

    // Populate tab hrefs so middle-click / cmd-click open real URLs.
    for (const t of TAB_ROUTES) {
      tabEls[t].hidden = !canManage && MANAGER_ONLY_TABS.has(t);
      tabEls[t].setAttribute('href', t === 'overview' ? `/apps/${slug}` : `/apps/${slug}/${t}`);
      tabEls[t].classList.toggle('active', t === tab);
      tabEls[t].setAttribute('aria-selected', String(t === tab));
      if (t === tab) tabEls[t].setAttribute('aria-current', 'page');
      else tabEls[t].removeAttribute('aria-current');
    }
    // On narrow screens the tab bar scrolls horizontally; bring the active tab
    // into view so the user can see which section they're on. block:'nearest'
    // avoids any vertical page jump (no-op on desktop where it's already shown).
    const activeTabEl = tabEls[tab];
    requestAnimationFrame(() => syncTabStrip(activeTabEl));
    if (document.fonts && document.fonts.ready) {
      document.fonts.ready.then(() => syncTabStrip(activeTabEl)).catch(() => {});
    }

    document.getElementById('app-detail-heading').textContent = app.name;
    document.getElementById('app-detail-slug').textContent = '/' + app.slug;
    const deployCountEl = document.getElementById('app-detail-deploy-count');
    deployCountEl.textContent = pluralize(app.deploy_count, 'deploy', 'deploys');
    const statusEl = document.getElementById('app-detail-status');
    // Mirror the apps-grid behaviour: an app with zero deploys is not
    // "degraded" — there's nothing wrong with it, it just hasn't run yet.
    if ((app.deploy_count || 0) === 0) {
      statusEl.textContent = 'Awaiting deploy';
      statusEl.className = 'badge badge-new';
    } else {
      statusEl.textContent = formatStatus(app.status);
      statusEl.className = 'badge badge-' + app.status;
    }
    const openLink = document.getElementById('app-detail-open');
    openLink.href = `/app/${app.slug}/`;
    openLink.hidden = app.status !== 'running';

    // The header kebab's only action is Restart, a manager action. Hide the
    // whole kebab for non-managers (mirrors the dashboard card, which only shows
    // Restart when canManage) so a viewer can't trigger a forbidden POST.
    const headerKebab = document.querySelector('.app-detail-actions .kebab-menu');
    if (headerKebab) headerKebab.hidden = !canManage;

    const fleetSlot = document.getElementById('app-detail-fleet-badge');
    if (fleetSlot) {
      fleetSlot.textContent = '';
      const fb = makeFleetBadge(document, app);
      if (fb) fleetSlot.appendChild(fb);
    }
    const fleetDigest = document.getElementById('app-detail-fleet');
    if (fleetDigest) renderFleetDigest(fleetDigest, app);

    // Show the selected panel, hide the rest.
    for (const t of TAB_ROUTES) {
      panels[t].hidden = t !== tab;
    }

    if (tabCleanup) { tabCleanup(); tabCleanup = null; }

    // Render the active tab.
    if (tab === 'overview') {
      renderOverview(panels.overview, app, replicasStatus, body, ctx);
    }
    if (tab === 'logs') {
      tabCleanup = renderLogs(panels.logs, app);
    }
    if (tab === 'traces') {
      tabCleanup = renderTraces(panels.traces, app, ctx);
    }
    if (tab === 'deployments') {
      await renderDeployments(panels.deployments, app, ctx);
    }
    if (tab === 'configuration') {
      renderConfiguration(panels.configuration, app, ctx);
    }
    if (tab === 'data') {
      renderData(panels.data, app, ctx);
    }
    if (tab === 'access') {
      renderAccess(panels.access, app, ctx);
    }

    view.hidden = false;
    ctx.updateActiveNav(location.pathname);
    ctx.metrics.setTargets([app.slug]);

    return {
      title: app.name,
      unmount() {
        if (tabCleanup) { tabCleanup(); tabCleanup = null; }
        view.hidden = true;
        ctx.metrics.setTargets([]);
      },
    };
  };
}

function renderLogs(panel, app) {
  // An app awaiting its first deploy has no log file, so opening the stream
  // just errors into "(log stream disconnected)". Show an empty state instead.
  if ((app.deploy_count || 0) === 0) {
    panel.innerHTML = `
      <div class="logs-empty">
        <h3>No logs yet</h3>
        <p>This app is awaiting its first deploy. Output appears here once it's
           deployed and running.</p>
        <p><a href="/apps/${app.slug}/overview" data-nav>Deploy from the Overview tab →</a></p>
      </div>
    `;
    return () => {};
  }
  panel.innerHTML = `
    <div class="logs-toolbar">
      <label><input id="logs-follow" type="checkbox" checked> Follow</label>
      <button id="logs-copy" type="button" class="btn-row">Copy all</button>
    </div>
    <pre id="detail-logs-body" class="detail-logs-body" aria-live="polite"></pre>
  `;
  const body = document.getElementById('detail-logs-body');
  const followCb = document.getElementById('logs-follow');
  const copyBtn = document.getElementById('logs-copy');

  const es = new EventSource(`/api/apps/${app.slug}/logs`, { withCredentials: true });
  es.onmessage = (e) => {
    const atBottom = body.scrollHeight - Math.ceil(body.scrollTop) <= body.clientHeight + 1;
    body.appendChild(document.createTextNode(e.data + '\n'));
    if (followCb.checked && atBottom) body.scrollTop = body.scrollHeight;
  };
  es.onerror = () => {
    es.close();
    body.appendChild(document.createTextNode('(log stream disconnected)\n'));
    if (followCb.checked) body.scrollTop = body.scrollHeight;
  };

  copyBtn.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(body.textContent); } catch {}
  });

  return () => { es.close(); };
}

function makeStatusBadge(cls, text) {
  const span = document.createElement('span');
  span.className = cls;
  span.textContent = text;
  return span;
}

async function renderDeployments(panel, app, ctx) {
  panel.innerHTML = `
    <ul id="detail-deployments-list" class="deployments-list" hidden>
      <li class="deployments-head" aria-hidden="true">
        <span>Deployment</span>
        <span>Deployed</span>
        <span></span>
      </li>
    </ul>
    <p id="detail-deployments-empty" class="env-empty" hidden>No deployments yet.</p>
    <div id="detail-deployments-error" class="deployments-error" hidden>
      <p class="error"></p>
      <button type="button" class="btn-row" id="detail-deployments-retry">Retry</button>
    </div>`;
  const list = document.getElementById('detail-deployments-list');
  const head = list.querySelector('.deployments-head');
  const empty = document.getElementById('detail-deployments-empty');
  const errWrap = document.getElementById('detail-deployments-error');

  // Bind the rollback delegate exactly once per render. The earlier code
  // registered it inside load(), so every Retry attached another listener
  // and a single Roll back click fanned out into N concurrent POSTs.
  // Using onclick (not addEventListener) makes the single-handler invariant
  // structurally enforced — re-assignment replaces the previous delegate.
  list.onclick = async (e) => {
    const btn = e.target.closest('.rollback-btn');
    if (!btn) return;
    if (!window.confirm(`Roll back ${app.name} to deployment ${btn.dataset.id}?`)) return;
    // Disable the button immediately so a double-click can't fire two POSTs
    // before navigation completes. Every code path below MUST re-enable it
    // unless we've already navigated away — otherwise a transport failure
    // leaves the user staring at a permanently-disabled button.
    btn.disabled = true;
    let r;
    try {
      r = await ctx.api(`/api/apps/${app.slug}/rollback`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ deployment_id: Number(btn.dataset.id) }),
      });
    } catch {
      btn.disabled = false;
      alert('Rollback failed: network error.');
      return;
    }
    if (r.status === 401) {
      btn.disabled = false;
      ctx.onUnauthorized();
      return;
    }
    if (r.ok) {
      // Navigating away unmounts this view; no need to re-enable the button.
      ctx.navigate(`/apps/${app.slug}`);
      return;
    }
    btn.disabled = false;
    let msg = 'Rollback failed.';
    try { const j = await r.json(); if (j && j.error) msg = `Rollback failed: ${j.error}`; } catch { /* non-JSON */ }
    alert(msg);
  };

  async function load() {
    list.hidden = false;
    // Keep the header row; drop only previously-rendered deployment rows.
    list.querySelectorAll('.deployment-row').forEach(r => r.remove());
    empty.hidden = true;
    errWrap.hidden = true;

    let resp;
    try {
      resp = await ctx.api(`/api/apps/${app.slug}/deployments`);
    } catch {
      errWrap.querySelector('.error').textContent = 'Network error — could not load deployments.';
      errWrap.hidden = false;
      list.hidden = true;
      return;
    }

    // Session expired: route through the same logged-out flow the rest of
    // the SPA uses. Falling into the generic !resp.ok branch would show
    // "Failed to load deployments (HTTP 401)" while the rest of the page
    // still looks signed in — client and server session state diverge
    // until the user refreshes.
    if (resp.status === 401) { ctx.onUnauthorized(); return; }
    if (!resp.ok) {
      let msg = `Failed to load deployments (HTTP ${resp.status}).`;
      try { const j = await resp.json(); if (j && j.error) msg = j.error; } catch { /* non-JSON */ }
      errWrap.querySelector('.error').textContent = msg;
      errWrap.hidden = false;
      list.hidden = true;
      return;
    }

    let rows;
    try { rows = await resp.json(); } catch {
      errWrap.querySelector('.error').textContent = 'Invalid response from server.';
      errWrap.hidden = false;
      list.hidden = true;
      return;
    }

    if (!rows || rows.length === 0) { empty.hidden = false; list.hidden = true; return; }

    const models = deploymentListModels(rows);
    for (const m of models) {
      const li = document.createElement('li');
      li.className = 'deployment-row' + (m.isCurrent ? ' deployment-row-current' : '');
      const verCell = document.createElement('span');
      verCell.className = 'deployment-version';
      const num = document.createElement('strong');
      num.className = 'deployment-number';
      num.textContent = m.deployNumber;
      verCell.appendChild(num);
      // Status badge: Current (live), Failed, or Deploying. A plain succeeded
      // (non-live) row gets no badge — it's just a rollback target.
      if (m.isCurrent) {
        verCell.appendChild(makeStatusBadge('deployment-current-badge', 'Current'));
      } else if (m.status === 'failed') {
        const b = makeStatusBadge('deployment-failed-badge', 'Failed');
        if (m.failureReason) b.title = m.failureReason;
        verCell.appendChild(b);
      } else if (m.status !== 'succeeded') {
        verCell.appendChild(makeStatusBadge('deployment-pending-badge', 'Deploying'));
      }
      const verId = document.createElement('span');
      verId.className = 'deployment-version-id';
      verId.textContent = `v${m.version}`;
      verCell.appendChild(verId);

      const whenCell = document.createElement('span');
      whenCell.className = 'deployment-when';
      whenCell.textContent = m.relWhen || '—';
      if (m.absWhen) whenCell.title = m.absWhen;

      const actionCell = document.createElement('span');
      actionCell.className = 'deployment-action';
      if (m.canRollback) {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'rollback-btn';
        btn.dataset.id = m.id;
        btn.textContent = 'Roll back';
        actionCell.appendChild(btn);
      } else if (m.isCurrent) {
        const span = document.createElement('span');
        span.className = 'deployment-live-note';
        span.textContent = 'Live';
        actionCell.appendChild(span);
      }

      li.append(verCell, whenCell, actionCell);
      list.appendChild(li);
    }
  }

  document.getElementById('detail-deployments-retry').addEventListener('click', load);
  await load();
}

function renderOverview(panel, app, replicasStatus, envelope, ctx) {
  if (app.deploy_count === 0) {
    panel.innerHTML = `
      <section class="emptystate-card">
        <p class="emptystate-eyebrow"><span class="sparkle" aria-hidden="true"></span>Awaiting deploy</p>
        <h2>Deploy your first bundle</h2>
        <p class="lead">Your app isn't running yet. Upload a <code>.zip</code>
           or use the CLI snippet below.</p>
        <div class="snippet">
          <pre><code id="overview-cli-snippet"></code></pre>
        </div>
        <div class="emptystate-actions">
          <button type="button" class="btn-primary" id="overview-deploy-btn">Deploy</button>
        </div>
      </section>
    `;
    document.getElementById('overview-cli-snippet').textContent =
      `shinyhub login --host ${location.origin} --username ${ctx.state.user.username}\n` +
      `shinyhub deploy --slug ${app.slug} .`;
    document.getElementById('overview-deploy-btn').addEventListener('click', () => {
      ctx.openDeployModal(app);
    });
    return;
  }
  panel.innerHTML = `
    <section class="overview-card">
      <h3>Current deployment</h3>
      <dl class="overview-dl">
        <dt>Version</dt><dd class="overview-version">${app.current_version ? 'v' + app.current_version : '—'}</dd>
        <dt>Deployed</dt><dd${app.last_deployed_at ? ` title="${new Date(app.last_deployed_at).toLocaleString()}"` : ''}>${app.last_deployed_at ? relativeTime(new Date(app.last_deployed_at)) : '—'}</dd>
        <dt>Deploys</dt><dd>${app.deploy_count}</dd>
      </dl>
      <div class="overview-links">
        <a href="/apps/${app.slug}/logs" data-nav>View logs →</a>
        <a href="/apps/${app.slug}/deployments" data-nav>Deployment history →</a>
      </div>
    </section>
    <section class="overview-card overview-autoscale">
      <h3>Autoscale</h3>
      <dl id="autoscale-summary" class="overview-dl"></dl>
    </section>
    <section class="overview-card overview-replicas">
      <h3>Replicas <span id="overview-replicas-cap" class="overview-replicas-cap"></span></h3>
      <ul id="overview-replicas-list" class="replicas-list" aria-live="polite">
        <li class="replicas-empty">Waiting for metrics…</li>
      </ul>
    </section>
    <section id="overview-rejects-by-reason" class="overview-card overview-rejects" hidden>
      <h3>Recent rejections (10 min)</h3>
      <ul id="overview-rejects-by-reason-list" class="rejects-list" aria-live="polite"></ul>
    </section>
  `;

  // Seed the Replicas list from /api/apps/:slug's replicas_status so the
  // panel shows index + status immediately. Sessions / CPU / RAM stay as
  // placeholders until the metrics poll fills them in.
  seedReplicasFromStatus(app, replicasStatus);

  // Autoscale summary reads app.autoscale_* plus envelope fields:
  //   autoscale_status (last_action_at, last_action, in_cooldown, cooldown_until)
  //   global_autoscale_enabled (kill-switch: false means scaling is paused globally)
  // Both are emitted by handleGetApp and are consumed by summariseAutoscale.
  const autoscaleDl = document.getElementById('autoscale-summary');
  if (autoscaleDl) {
    renderAutoscaleSummary(autoscaleDl, summariseAutoscale(app, envelope || {}));
  }

  // Store the envelope so the 10s metrics poll (onMetrics in app.js) can keep
  // autoscale_status fresh without a full re-fetch of GET /api/apps/:slug.
  if (ctx.setDetailEnvelope) ctx.setDetailEnvelope(envelope || {});

  // Rejects-by-reason is optional in the envelope; the helpers tolerate a
  // missing/empty rollup and hide the card so a healthy app shows nothing.
  const rejectsSection = document.getElementById('overview-rejects-by-reason');
  const rejectsList = document.getElementById('overview-rejects-by-reason-list');
  if (rejectsSection && rejectsList) {
    renderRejectsByReason(rejectsSection, rejectsList, formatRejectsByReason(envelope && envelope.rejects_by_reason));
  }
}

function seedReplicasFromStatus(app, replicasStatus) {
  const listEl = document.getElementById('overview-replicas-list');
  const capEl = document.getElementById('overview-replicas-cap');
  if (!listEl || !capEl) return;
  const cap = Number(app.max_sessions_per_replica || 0);
  if (cap > 0) capEl.textContent = `(cap ${cap} sessions/replica)`;
  if (replicasStatus.length === 0) return;
  listEl.innerHTML = '';
  for (const r of replicasStatus) {
    const li = document.createElement('li');
    li.className = 'replica-row';
    const status = r.status || 'stopped';
    // Read r.tier and r.provider that handleGetApp already includes in
    // replicas_status (db.Replica carries Tier + Provider; plan-01 Contract 5).
    const backend = backendLabel({ tier: r.tier, provider: r.provider });
    // Show n/a immediately for known-PID-less replicas so the initial panel state
    // is honest before the first metrics poll fills in real values.
    const { cpuText: cpuInit, ramText: ramInit, note } = metricsText({
      metrics_available: r.metrics_available,
    });
    const cpuDisplay = status === 'running' ? cpuInit : '—';
    const ramDisplay = status === 'running' ? ramInit : '—';
    // reason explains a degraded state (e.g. "worker unavailable" for a lost
    // replica); empty for healthy replicas.
    const reason = reasonLabel(r);
    // Build the li via innerHTML for fixed strings, but set backend + reason via
    // textContent to avoid XSS from operator-controlled values.
    li.innerHTML = `
      <span class="replica-index">#${r.index}</span>
      <span class="badge badge-${status}"></span>
      <span class="replica-backend" title="Backend/tier"></span>
      <span class="replica-reason"></span>
      <span class="replica-sessions">— sessions</span>
      <span class="replica-cpu">CPU ${cpuDisplay}</span>
      <span class="replica-ram"${note ? ` title="${note}"` : ''}>RAM ${ramDisplay}</span>
    `;
    const badgeEl = li.querySelector('.badge');
    badgeEl.textContent = formatStatus(status);
    if (reason) badgeEl.title = reason;
    li.querySelector('.replica-backend').textContent = backend;
    li.querySelector('.replica-reason').textContent = reason;
    listEl.appendChild(li);
  }
}

function renderConfiguration(panel, app, ctx) {
  ctx.setSettingsSlug(app.slug);
  ctx.populateGeneralTab(app);
  ctx.populateAutoscaleTab(app);
  ctx.refreshEnvList(app.slug);
  ctx.loadSchedules(app.slug);
}

function renderData(panel, app, ctx) {
  ctx.setSettingsSlug(app.slug);
  // Pass the fetched app (which carries can_manage from the GET envelope) so the
  // upload form's write-permission check works on a direct deep-link, where the
  // cached apps LIST (state.apps) is not yet populated.
  ctx.refreshDataTab(app.slug, app);
  ctx.loadSharedData(app.slug);
}

function renderAccess(panel, app, ctx) {
  ctx.setSettingsSlug(app.slug);
  ctx.populateAccessPanel(app);
  ctx.refreshMemberList();
  ctx.refreshGroupAccessList();
}

// renderTraces polls /api/apps/<slug>/traces every 5 s and renders recent
// slow/error proxy spans. When tracing is disabled server-side the panel shows
// a one-line empty state pointing operators at the config block; when enabled
// but the ring buffer is empty (no slow/error requests yet) it explains how
// admission to the buffer works so the absence of rows is not surprising.
function renderTraces(panel, app, ctx) {
  panel.innerHTML = `
    <div class="traces-toolbar">
      <button id="traces-refresh" type="button" class="btn-row">Refresh</button>
      <span id="traces-status" class="hibernate-status"></span>
    </div>
    <p id="traces-empty" class="env-empty" hidden></p>
    <table id="traces-table" class="env-list" hidden>
      <thead><tr>
        <th>When</th><th>Method</th><th>Path</th><th>Status</th>
        <th>Duration</th><th>Replica</th><th>Trace</th>
      </tr></thead>
      <tbody id="traces-tbody"></tbody>
    </table>
    <p id="traces-error" class="error" hidden></p>
  `;
  const tableEl   = document.getElementById('traces-table');
  const tbodyEl   = document.getElementById('traces-tbody');
  const emptyEl   = document.getElementById('traces-empty');
  const errEl     = document.getElementById('traces-error');
  const refreshEl = document.getElementById('traces-refresh');
  const statusEl  = document.getElementById('traces-status');

  // Track the last successful poll so the status line can report freshness
  // ("updated Xs ago"), ticked once a second between the 5 s reloads.
  let lastLoaded = null;
  function paintStatus() {
    if (statusEl) statusEl.textContent = formatPollStatus(lastLoaded);
  }

  async function load() {
    errEl.hidden = true;
    let r;
    try {
      r = await ctx.api(`/api/apps/${app.slug}/traces`);
    } catch {
      errEl.textContent = 'Network error — could not load traces.';
      errEl.hidden = false;
      return;
    }
    if (r.status === 401) { ctx.onUnauthorized(); return; }
    if (!r.ok) {
      errEl.textContent = `Failed to load traces (HTTP ${r.status}).`;
      errEl.hidden = false;
      return;
    }
    let body;
    try { body = await r.json(); } catch {
      errEl.textContent = 'Invalid response from server.';
      errEl.hidden = false;
      return;
    }
    // A successful poll refreshes the status line even when tracing is
    // disabled or the buffer is empty - those are the common steady states, and
    // the operator still wants to see polling is alive.
    lastLoaded = new Date();
    paintStatus();
    const spans = Array.isArray(body.spans) ? body.spans : [];
    if (!body.enabled) {
      tableEl.hidden = true;
      emptyEl.hidden = false;
      emptyEl.innerHTML =
        'Tracing is disabled. Set <code>tracing.enabled: true</code> and ' +
        '<code>tracing.otlp_endpoint</code> in <code>shinyhub.yaml</code> to ' +
        'forward Shiny’s OpenTelemetry spans to your backend.';
      return;
    }
    if (spans.length === 0) {
      tableEl.hidden = true;
      emptyEl.hidden = false;
      emptyEl.textContent =
        'No slow or error requests captured yet. Traces are retained when ' +
        'a request exceeds the slow_request_ms threshold or returns 5xx.';
      return;
    }
    emptyEl.hidden = true;
    tableEl.hidden = false;
    tbodyEl.innerHTML = '';
    const linkTpl = typeof body.trace_link_template === 'string' ? body.trace_link_template : '';
    for (const s of spans) {
      tbodyEl.appendChild(makeTraceRow(document, s, linkTpl, lastLoaded));
    }
  }

  refreshEl.addEventListener('click', load);
  load();
  const interval = setInterval(load, 5000);
  const statusTick = setInterval(paintStatus, 1000);
  return () => { clearInterval(interval); clearInterval(statusTick); };
}
