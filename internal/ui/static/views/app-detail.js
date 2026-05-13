// App detail view. Mounts #app-detail-view, populates the header, and shows
// the requested tab. Tabs other than Overview are added in later tasks; for
// now Overview is the only one with a renderer and other tabs show "Coming
// soon" placeholders.
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
    const replicasStatus = Array.isArray(body.replicas_status) ? body.replicas_status : [];

    const canManage = ctx.canManageApp(ctx.state.user, app);

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

    // Show the selected panel, hide the rest.
    for (const t of TAB_ROUTES) {
      panels[t].hidden = t !== tab;
    }

    if (tabCleanup) { tabCleanup(); tabCleanup = null; }

    // Render the active tab.
    if (tab === 'overview') {
      renderOverview(panels.overview, app, replicasStatus, ctx);
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
      title: `${app.name} · ShinyHub`,
      unmount() {
        if (tabCleanup) { tabCleanup(); tabCleanup = null; }
        view.hidden = true;
        ctx.metrics.setTargets([]);
      },
    };
  };
}

function renderLogs(panel, app) {
  panel.innerHTML = `
    <div class="logs-toolbar">
      <label><input id="logs-follow" type="checkbox" checked> Follow</label>
      <button id="logs-copy" type="button">Copy all</button>
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
  es.onerror = () => { es.close(); };

  copyBtn.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(body.textContent); } catch {}
  });

  return () => { es.close(); };
}

async function renderDeployments(panel, app, ctx) {
  panel.innerHTML = `
    <ul id="detail-deployments-list" class="deployments-list"></ul>
    <p id="detail-deployments-empty" class="env-empty" hidden>No deployments yet.</p>
    <div id="detail-deployments-error" class="deployments-error" hidden>
      <p class="error"></p>
      <button type="button" class="btn-row" id="detail-deployments-retry">Retry</button>
    </div>`;
  const list = document.getElementById('detail-deployments-list');
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
    list.textContent = '';
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

    for (const d of rows) {
      const li = document.createElement('li');
      li.className = 'deployment-row';
      li.innerHTML = `
        <span class="deployment-version">v${d.version}</span>
        <span class="deployment-when">${new Date(d.created_at).toLocaleString()}</span>
        <span class="deployment-user">${d.deployed_by ?? '—'}</span>
        <button type="button" class="rollback-btn" data-id="${d.id}">Roll back</button>
      `;
      list.appendChild(li);
    }
  }

  document.getElementById('detail-deployments-retry').addEventListener('click', load);
  await load();
}

function renderOverview(panel, app, replicasStatus, ctx) {
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
        <dt>Version</dt><dd>${app.current_version ?? '—'}</dd>
        <dt>Deployed</dt><dd>${app.last_deployed_at ? new Date(app.last_deployed_at).toLocaleString() : '—'}</dd>
        <dt>Deploys</dt><dd>${app.deploy_count}</dd>
      </dl>
      <div class="overview-links">
        <a href="/apps/${app.slug}/logs" data-nav>View logs →</a>
        <a href="/apps/${app.slug}/deployments" data-nav>Deployment history →</a>
      </div>
    </section>
    <section class="overview-card overview-replicas">
      <h3>Replicas <span id="overview-replicas-cap" class="overview-replicas-cap"></span></h3>
      <ul id="overview-replicas-list" class="replicas-list" aria-live="polite">
        <li class="replicas-empty">Waiting for metrics…</li>
      </ul>
    </section>
  `;

  // Seed the Replicas list from /api/apps/:slug's replicas_status so the
  // panel shows index + status immediately. Sessions / CPU / RAM stay as
  // placeholders until the metrics poll fills them in.
  seedReplicasFromStatus(app, replicasStatus);
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
    li.innerHTML = `
      <span class="replica-index">#${r.index}</span>
      <span class="badge badge-${status}">${formatStatus(status)}</span>
      <span class="replica-sessions">— sessions</span>
      <span class="replica-cpu">CPU —</span>
      <span class="replica-ram">RAM —</span>
    `;
    listEl.appendChild(li);
  }
}

function renderConfiguration(panel, app, ctx) {
  ctx.setSettingsSlug(app.slug);
  ctx.populateGeneralTab(app);
  ctx.refreshEnvList(app.slug);
  ctx.loadSchedules(app.slug);
}

function renderData(panel, app, ctx) {
  ctx.setSettingsSlug(app.slug);
  ctx.refreshDataTab(app.slug);
  ctx.loadSharedData(app.slug);
}

function renderAccess(panel, app, ctx) {
  ctx.setSettingsSlug(app.slug);
  ctx.populateAccessPanel(app);
  ctx.refreshMemberList();
}

// renderTraces polls /api/apps/<slug>/traces every 5 s and renders recent
// slow/error proxy spans. When tracing is disabled server-side the panel shows
// a one-line empty state pointing operators at the config block; when enabled
// but the ring buffer is empty (no slow/error requests yet) it explains how
// admission to the buffer works so the absence of rows is not surprising.
function renderTraces(panel, app, ctx) {
  panel.innerHTML = `
    <div class="traces-toolbar">
      <button id="traces-refresh" type="button">Refresh</button>
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
      const tr = document.createElement('tr');
      tr.className = (s.status >= 500 || s.error) ? 'replica-row replica-row-error' : 'replica-row';
      const when = s.started_at ? new Date(s.started_at).toLocaleTimeString() : '—';
      const traceCell = linkTpl
        ? `<a href="${escapeAttr(linkTpl.replace('{trace_id}', s.trace_id))}" target="_blank" rel="noopener">${shortHex(s.trace_id)}</a>`
        : `<code>${shortHex(s.trace_id)}</code>`;
      tr.innerHTML = `
        <td>${when}</td>
        <td>${escapeText(s.method || '')}</td>
        <td><code>${escapeText(s.path || '')}</code></td>
        <td>${s.status || '—'}</td>
        <td>${s.duration_ms} ms</td>
        <td>${s.replica >= 0 ? '#' + s.replica : '—'}</td>
        <td>${traceCell}</td>
      `;
      tbodyEl.appendChild(tr);
    }
  }

  refreshEl.addEventListener('click', load);
  load();
  const interval = setInterval(load, 5000);
  return () => { clearInterval(interval); };
}

function escapeText(s) {
  return String(s).replace(/[&<>]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c]));
}
function escapeAttr(s) {
  return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function shortHex(s) {
  if (!s || s.length <= 12) return s || '';
  return s.slice(0, 8) + '…';
}
