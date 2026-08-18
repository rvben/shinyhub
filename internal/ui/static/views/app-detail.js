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
import { deploymentListModels, provenanceModel, relativeTime } from '/static/views/deployment-row.js';
import { statusPillClass } from '/static/views/stat-format.js';
import { formatStatus } from '/static/views/status-label.js';
import { appStatusView } from '/static/views/app-card-badge.js';
import { crashBanner } from '/static/views/crash-banner.js';
import { connectivityBanner } from '/static/views/connectivity-banner.js';
import { renderTrendsCard } from '/static/views/trends-card.js';
import { createTablistNav } from '/static/views/tablist-keys.js';
import {
  TAB_ROUTES,
  resolveDetailAccess,
  tabStripScrollTarget,
  tabViewModels,
} from '/static/views/app-detail-nav.js';
import { normalizeAppEnvelope } from '/static/views/app-detail-envelope.js';
import { createLogsViewer } from '/static/views/logs-ui.js';

function pluralize(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
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
    if (activeEl && tabsNav) {
      const navRect = tabsNav.getBoundingClientRect();
      const activeRect = activeEl.getBoundingClientRect();
      tabsNav.scrollLeft = tabStripScrollTarget({
        clientWidth: tabsNav.clientWidth,
        scrollWidth: tabsNav.scrollWidth,
        tabLeft: activeRect.left - navRect.left + tabsNav.scrollLeft,
        tabWidth: activeRect.width,
      });
    }
    updateTabOverflow();
  }
  if (tabsNav) {
    tabsNav.addEventListener('scroll', updateTabOverflow, { passive: true });
    window.addEventListener('resize', updateTabOverflow, { passive: true });
    // WAI-ARIA tablist keyboard nav (manual activation): arrow/Home/End move
    // focus between the visible tabs; Enter/Space commit the focused tab by
    // reusing the delegated data-nav click handler to navigate. Manual (not
    // focus-follows) activation is required here because navigating moves page
    // focus to the section heading, which would otherwise break the next arrow.
    // Wired once; roving tabindex is refreshed per render in the loop below.
    createTablistNav(tabsNav, document, { onActivate: (el) => el.click() });
  }

  let tabCleanup = null;

  return async function mount(params) {
    const { slug } = params;

    // Preserve the user's URL: /apps/<slug>/overview is a legitimate route
    // (every other tab keeps its segment, so /overview should too). The
    // previous version replaced it with /apps/<slug>, which surprised users
    // who pasted/bookmarked the explicit overview URL.

    const resp = await ctx.api(`/api/apps/${slug}`);
    if (resp.status === 404) { ctx.navigate('/'); return {}; }
    if (resp.status === 401) { ctx.onUnauthorized(); return {}; }
    // A silent `return {}` here used to leave the main panel totally blank with
    // no error or retry (a 500/502 read as a successful, empty mount). Throw so
    // the router's error boundary (showRouteError in app.js) catches it and
    // reveals #route-error-view with a Reload button instead.
    if (!resp.ok) { throw new Error(`Failed to load app (HTTP ${resp.status}).`); }
    // GET /api/apps/:slug returns a wrapped envelope; normalizeAppEnvelope folds
    // the envelope-level fields (can_manage, runtime_mode, resource_enforcement,
    // release_number/released_at/released_version) onto app and returns
    // replicas_status. See views/app-detail-envelope.js.
    const body = await resp.json();
    const { app, replicasStatus } = normalizeAppEnvelope(body);

    const canManage = ctx.canManageApp(ctx.state.user, app);

    // Resolve which tab to show and whether the visitor must be redirected:
    // a pure viewer who doesn't manage this app is bounced to the Launchpad ('/'),
    // and a non-manager on a manager-only tab (configuration/data/access) is sent
    // to the app root. An unknown tab falls back to overview. See
    // views/app-detail-nav.js. The gate runs after the app loads, so it covers
    // every path in (a sidebar app link, a typed URL, a bookmark).
    const { tab, redirect } = resolveDetailAccess({
      user: ctx.state.user, canManage, requestedTab: params.tab, slug,
    });
    if (redirect) { ctx.navigate(redirect.path, { replace: redirect.replace }); return {}; }

    // Record the app so the static header kebab (wired once in app.js) acts on
    // the right app.
    if (ctx.setDetailApp) ctx.setDetailApp(app);

    // Apply the per-tab visibility/href/ARIA/roving-tabindex model. Hrefs are
    // populated so middle-click / cmd-click open real URLs; only the active tab
    // carries tabindex 0 (arrow keys move between the rest, see createTablistNav).
    for (const vm of tabViewModels(slug, tab, canManage)) {
      const el = tabEls[vm.route];
      el.hidden = vm.hidden;
      el.setAttribute('href', vm.href);
      el.classList.toggle('active', vm.active);
      el.setAttribute('aria-selected', String(vm.ariaSelected));
      el.setAttribute('tabindex', vm.tabindex);
      if (vm.ariaCurrent) el.setAttribute('aria-current', vm.ariaCurrent);
      else el.removeAttribute('aria-current');
    }
    // On narrow screens the tab bar scrolls horizontally. Keep the active tab
    // for the post-render centering pass below; measuring while the view is
    // still hidden would produce zero-width geometry on a deep link.
    const activeTabEl = tabEls[tab];

    document.getElementById('app-detail-heading').textContent = app.name;
    document.getElementById('app-detail-slug').textContent = '/' + app.slug;
    const deployCountEl = document.getElementById('app-detail-deploy-count');
    deployCountEl.textContent = pluralize(app.deploy_count, 'deploy', 'deploys');
    // Release chip (human-friendly vN) + deployed-ago meta. The epoch version is
    // kept on the chip's title for support.
    const versionEl = document.getElementById('app-detail-version');
    if (versionEl) {
      if (app.release_number != null) {
        versionEl.textContent = 'v' + app.release_number;
        if (app.released_version) versionEl.title = 'bundle ' + app.released_version;
        versionEl.hidden = false;
      } else {
        versionEl.hidden = true;
      }
    }
    const deployedEl = document.getElementById('app-detail-deployed');
    if (deployedEl) {
      const deployedAt = app.released_at || app.last_deployed_at;
      if (deployedAt) {
        const d = new Date(deployedAt);
        deployedEl.textContent = 'deployed ' + relativeTime(d);
        deployedEl.title = d.toLocaleString();
      } else {
        deployedEl.textContent = '';
        deployedEl.removeAttribute('title');
      }
    }
    const statusEl = document.getElementById('app-detail-status');
    // Derive the pill from the same appStatusView the apps-grid card uses, so
    // the two surfaces always agree: a zero-deploy app reads "Awaiting deploy"
    // (not "degraded"), and a zero-deploy crash-looped one reads "Failed", not
    // the benign "Awaiting deploy".
    const statusView = appStatusView(app, formatStatus);
    statusEl.textContent = statusView.text;
    statusEl.className = statusPillClass(statusView.state);
    // Seed the metric tiles until the first metrics poll arrives. Replicas shows
    // the configured count immediately (CPU/Memory/Sessions fill in on poll).
    const seedStat = (id, val) => {
      const el = document.getElementById(id);
      if (el) {
        el.textContent = val;
        el.classList.toggle('is-empty', val === '—');
        el.removeAttribute('title'); // clear any stale tooltip from a prior app
      }
    };
    seedStat('app-detail-cpu', '—');
    seedStat('app-detail-ram', '—');
    seedStat('app-detail-sessions', '—');
    seedStat('app-detail-replicas', '0 / ' + (app.replicas || 1));
    const openLink = document.getElementById('app-detail-open');
    openLink.href = `/app/${app.slug}/`;
    openLink.hidden = !['running', 'idle'].includes(app.status);

    // The header kebab holds only manager actions, so a viewer must not see it
    // at all. That decision, and which individual items apply to this app, is
    // made once in app.js (syncDetailHeaderActions, driven by the same
    // appCardActions helper the dashboard cards use) and applied through
    // ctx.setDetailApp above. Setting it here as well would give one flag two
    // writers, and whichever ran last would silently win.

    const fleetSlot = document.getElementById('app-detail-fleet-badge');
    if (fleetSlot) {
      fleetSlot.textContent = '';
      const fb = makeFleetBadge(document, app);
      if (fb) fleetSlot.appendChild(fb);
    }
    const fleetDigest = document.getElementById('app-detail-fleet');
    if (fleetDigest) renderFleetDigest(fleetDigest, app);
    renderHeaderProvenance(document.getElementById('app-detail-provenance'), app.deployment_provenance);

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
      tabCleanup = renderLogs(panels.logs, app, replicasStatus, ctx);
    }
    if (tab === 'traces') {
      tabCleanup = renderTraces(panels.traces, app, ctx);
    }
    if (tab === 'deployments') {
      await renderDeployments(panels.deployments, app, ctx);
    }
    if (tab === 'configuration') {
      renderConfiguration(panels.configuration, app, ctx, body);
    }
    if (tab === 'data') {
      renderData(panels.data, app, ctx);
    }
    if (tab === 'access') {
      renderAccess(panels.access, app, ctx);
    }

    view.hidden = false;
    requestAnimationFrame(() => syncTabStrip(activeTabEl));
    if (document.fonts && document.fonts.ready) {
      document.fonts.ready.then(() => syncTabStrip(activeTabEl)).catch(() => {});
    }
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

function externalLink(label, href, className = '') {
  const a = document.createElement('a');
  a.textContent = label;
  a.href = href;
  a.target = '_blank';
  a.rel = 'noopener noreferrer';
  if (className) a.className = className;
  return a;
}

function renderHeaderProvenance(host, raw) {
  if (!host) return;
  host.replaceChildren();
  const model = provenanceModel(raw);
  host.hidden = !model.available;
  if (!model.available) return;

  const mark = document.createElement('span');
  mark.className = 'provenance-provider';
  if (model.providerIcon === 'gitlab' || model.providerIcon === 'github') {
    mark.classList.add('is-brand', `is-${model.providerIcon}`);
    mark.append(providerBrandIcon(model.providerIcon));
  } else if (model.providerIcon) {
    mark.classList.add(`is-${model.providerIcon}`);
    mark.append(provenanceIcon(model.providerIcon));
  } else if (model.markIcon) {
    mark.classList.add(`is-${model.markIcon}`);
    mark.append(provenanceIcon(model.markIcon));
  } else {
    mark.textContent = model.mark;
  }
  mark.setAttribute('aria-hidden', 'true');
  const copy = document.createElement('span');
  copy.className = 'provenance-copy';
  const primary = document.createElement('span');
  primary.className = 'provenance-primary';
  if (model.headerText) {
    primary.textContent = model.headerText;
  } else {
    primary.append('Deployed by ');
    primary.append(model.url ? externalLink(model.label, model.url) : model.label);
  }
  const detail = document.createElement('span');
  detail.className = 'provenance-detail';
  detail.append(model.headerDetail || model.detail);
  if (model.change) {
    detail.append(' · ');
    detail.append(model.change.url ? externalLink(model.change.label, model.change.url) : model.change.label);
  }
  copy.append(primary, detail);
  host.append(mark, copy);
  if (model.url) host.append(externalLink('Open pipeline ↗', model.url, 'provenance-open'));
}

// Exact paths from the providers' official brand kits. They are inlined so the
// provenance indicator never depends on an external request. GitHub's permitted
// black/white mark is selected by CSS for theme contrast; GitLab remains in its
// official full-colour treatment.
// GitLab: https://about.gitlab.com/images/press/gitlab-logo-500-rgb.svg
// GitHub: https://brand.github.com/GitHub_Logos.zip
function providerBrandIcon(name) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('aria-hidden', 'true');
  if (name === 'gitlab') {
    svg.setAttribute('viewBox', '0 0 380 380');
    const paths = [
      ['#e24329', 'M265.26416,174.37243l-.2134-.55822-21.19899-55.30908c-.4236-1.08359-1.18542-1.99642-2.17699-2.62689-.98837-.63373-2.14749-.93253-3.32305-.87014-1.1689.06239-2.29195.48925-3.20809,1.21821-.90957.73554-1.56629,1.73047-1.87493,2.85346l-14.31327,43.80662h-57.90965l-14.31327-43.80662c-.30864-1.12299-.96536-2.11791-1.87493-2.85346-.91614-.72895-2.03911-1.15582-3.20809-1.21821-1.17548-.06239-2.33468.23641-3.32297.87014-.99166.63047-1.75348,1.5433-2.17707,2.62689l-21.19891,55.31237-.21348.55493c-6.28158,16.38521-.92929,34.90803,13.05891,45.48782.02621.01641.04922.03611.07552.05582l.18719.14119,32.29094,24.17392,15.97151,12.09024,9.71951,7.34871c2.34117,1.77316,5.57877,1.77316,7.92002,0l9.71943-7.34871,15.96822-12.09024,32.48142-24.31511c.02958-.02299.05588-.04269.08538-.06568,13.97834-10.57977,19.32735-29.09604,13.04905-45.47796Z'],
      ['#fc6d26', 'M265.26416,174.37243l-.2134-.55822c-10.5174,2.16062-20.20405,6.6099-28.49844,12.81593-.1346.0985-25.20497,19.05805-46.55171,35.19699,15.84998,11.98517,29.6477,22.40405,29.6477,22.40405l32.48142-24.31511c.02958-.02299.05588-.04269.08538-.06568,13.97834-10.57977,19.32735-29.09604,13.04905-45.47796Z'],
      ['#fca326', 'M160.34962,244.23117l15.97151,12.09024,9.71951,7.34871c2.34117,1.77316,5.57877,1.77316,7.92002,0l9.71943-7.34871,15.96822-12.09024s-13.79772-10.41888-29.6477-22.40405c-15.85327,11.98517-29.65099,22.40405-29.65099,22.40405Z'],
      ['#fc6d26', 'M143.44561,186.63014c-8.29111-6.20274-17.97446-10.65531-28.49507-12.81264l-.21348.55493c-6.28158,16.38521-.92929,34.90803,13.05891,45.48782.02621.01641.04922.03611.07552.05582l.18719.14119,32.29094,24.17392s13.79772-10.41888,29.65099-22.40405c-21.34673-16.13894-46.42031-35.09848-46.55499-35.19699Z'],
    ];
    for (const [fill, d] of paths) {
      const path = document.createElementNS(ns, 'path');
      path.setAttribute('fill', fill);
      path.setAttribute('d', d);
      svg.append(path);
    }
    return svg;
  }

  svg.setAttribute('viewBox', '0 0 98 96');
  const path = document.createElementNS(ns, 'path');
  path.setAttribute('d', 'M41.4395 69.3848C28.8066 67.8535 19.9062 58.7617 19.9062 46.9902C19.9062 42.2051 21.6289 37.0371 24.5 33.5918C23.2559 30.4336 23.4473 23.7344 24.8828 20.959C28.7109 20.4805 33.8789 22.4902 36.9414 25.2656C40.5781 24.1172 44.4062 23.543 49.0957 23.543C53.7852 23.543 57.6133 24.1172 61.0586 25.1699C64.0254 22.4902 69.2891 20.4805 73.1172 20.959C74.457 23.543 74.6484 30.2422 73.4043 33.4961C76.4668 37.1328 78.0937 42.0137 78.0937 46.9902C78.0937 58.7617 69.1934 67.6621 56.3691 69.2891C59.623 71.3945 61.8242 75.9883 61.8242 81.252L61.8242 91.2051C61.8242 94.0762 64.2168 95.7031 67.0879 94.5547C84.4102 87.9512 98 70.6289 98 49.1914C98 22.1074 75.9883 6.69539e-07 48.9043 4.309e-07C21.8203 1.92261e-07 -1.9479e-07 22.1074 -4.3343e-07 49.1914C-6.20631e-07 70.4375 13.4941 88.0469 31.6777 94.6504C34.2617 95.6074 36.75 93.8848 36.75 91.3008L36.75 83.6445C35.4102 84.2188 33.6875 84.6016 32.1562 84.6016C25.8398 84.6016 22.1074 81.1563 19.4277 74.7441C18.375 72.1602 17.2266 70.6289 15.0254 70.3418C13.877 70.2461 13.4941 69.7676 13.4941 69.1934C13.4941 68.0449 15.4082 67.1836 17.3223 67.1836C20.0977 67.1836 22.4902 68.9063 24.9785 72.4473C26.8926 75.2227 28.9023 76.4668 31.2949 76.4668C33.6875 76.4668 35.2187 75.6055 37.4199 73.4043C39.0469 71.7773 40.291 70.3418 41.4395 69.3848Z');
  svg.append(path);
  return svg;
}

function provenanceIcon(name) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  const paths = name === 'manual'
    ? [
        ['circle', { cx: '12', cy: '8', r: '3.25' }],
        ['path', { d: 'M5.5 19.5c.9-3.6 3.1-5.4 6.5-5.4s5.6 1.8 6.5 5.4' }],
      ]
    : name === 'rollback' ? [
        ['path', { d: 'M4.5 7.5v5h5' }],
        ['path', { d: 'M5.2 12.2A7.25 7.25 0 1 0 7.4 7' }],
      ] : [
        ['path', { d: 'M5 5.5h5v5H5zM14 13.5h5v5h-5z' }],
        ['path', { d: 'M10 8h2.5a4 4 0 0 1 4 4v1.5M7.5 10.5v5a3 3 0 0 0 3 3H14' }],
      ];
  for (const [tag, attrs] of paths) {
    const node = document.createElementNS(ns, tag);
    for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, value);
    svg.append(node);
  }
  return svg;
}

function renderLogs(panel, app, replicasStatus, ctx) {
  // An app awaiting its first deploy has no log sources. Keep the intentional
  // first-deploy guidance instead of mounting a viewer that can only report an
  // unavailable source.
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
  return createLogsViewer({ panel, app, initialSources: replicasStatus, api: ctx.api });
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
        <span>Source</span>
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
        headers: {
          'Content-Type': 'application/json',
          'X-Shinyhub-Deploy-Channel': 'dashboard',
        },
        body: JSON.stringify({ deployment_id: Number(btn.dataset.id) }),
      });
    } catch {
      btn.disabled = false;
      ctx.flashToast('Rollback failed: network error.', 'error');
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
    ctx.flashToast(msg, 'error');
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

    let body;
    try { body = await resp.json(); } catch {
      errWrap.querySelector('.error').textContent = 'Invalid response from server.';
      errWrap.hidden = false;
      list.hidden = true;
      return;
    }

    // The server returns the standard {items,total,limit,offset} list envelope;
    // tolerate a bare array for resilience across versions.
    const rows = Array.isArray(body) ? body : (body && Array.isArray(body.items) ? body.items : []);
    if (rows.length === 0) { empty.hidden = false; list.hidden = true; return; }

    const models = deploymentListModels(rows);
    for (const m of models) {
      const li = document.createElement('li');
      li.className = 'deployment-row' + (m.isCurrent ? ' deployment-row-current' : '');
      const verCell = document.createElement('span');
      verCell.className = 'deployment-version';
      const num = document.createElement('strong');
      num.className = 'deployment-number';
      num.textContent = m.releaseLabel; // "v3"; empty for failed/pending (badge carries status)
      if (m.releaseLabel) num.title = 'bundle ' + m.version; // epoch id on hover (only where there's a label)
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
      const whenCell = document.createElement('span');
      whenCell.className = 'deployment-when';
      whenCell.textContent = m.relWhen || '—';
      if (m.absWhen) whenCell.title = m.absWhen;

      const sourceCell = document.createElement('span');
      sourceCell.className = 'deployment-source';
      const sourcePrimary = document.createElement('span');
      sourcePrimary.className = 'deployment-source-primary';
      if (m.source.url) sourcePrimary.appendChild(externalLink(m.source.label, m.source.url));
      else sourcePrimary.textContent = m.source.label;
      const sourceDetail = document.createElement('span');
      sourceDetail.className = 'deployment-source-detail';
      sourceDetail.append(m.source.detail);
      if (m.source.change) {
        sourceDetail.append(' · ');
        sourceDetail.append(m.source.change.url ? externalLink(m.source.change.label, m.source.change.url) : m.source.change.label);
      }
      if (m.restoredFromReleaseNumber != null) sourceDetail.append(` · restored bundle from v${m.restoredFromReleaseNumber}`);
      sourceCell.append(sourcePrimary, sourceDetail);

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

      li.append(verCell, sourceCell, whenCell, actionCell);
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
        <dt>Version</dt><dd class="overview-version"${app.released_version ? ` title="bundle ${app.released_version}"` : ''}>${app.release_number != null ? 'v' + app.release_number : '—'}</dd>
        <dt>Deployed</dt><dd${(app.released_at || app.last_deployed_at) ? ` title="${new Date(app.released_at || app.last_deployed_at).toLocaleString()}"` : ''}>${(app.released_at || app.last_deployed_at) ? relativeTime(new Date(app.released_at || app.last_deployed_at)) : '—'}</dd>
        <dt>Deploys</dt><dd>${app.deploy_count}</dd>
      </dl>
      <div class="overview-links">
        <a href="/apps/${app.slug}/logs" data-nav>View logs →</a>
        <a href="/apps/${app.slug}/deployments" data-nav>Deployment history →</a>
      </div>
    </section>
    <div id="overview-trends" class="overview-card overview-trends">
      <h3>Trends</h3>
      <p class="trends-empty">Collecting...</p>
    </div>
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

  // A crashed app shows a prominent failure banner (the reason + a Restart) above
  // the overview cards so the operator immediately sees why it is down and can
  // recover it, instead of a silent status pill.
  const crashEl = crashBanner(document, app, {
    canManage: ctx.canManageApp(ctx.state.user, app),
    onRestart: () => ctx.restart(app.slug),
  });
  if (crashEl) panel.insertBefore(crashEl, panel.firstChild);

  // A running app that is serving pages but whose WebSocket never connects shows
  // an amber connectivity warning above the overview cards, so an operator sees
  // "your reverse proxy is likely blocking WebSockets" instead of users silently
  // hitting "Shiny disconnected". Reads envelope.connectivity.serving_without_ws.
  const connEl = connectivityBanner(document, envelope || {});
  if (connEl) panel.insertBefore(connEl, panel.firstChild);

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

  // Load the in-memory metrics history into the Trends card. Best-effort: a
  // failed fetch or disabled history just leaves the Collecting placeholder.
  loadTrends(app.slug);
}

// loadTrends fetches the app's metrics history and renders the Trends card. The
// response is the columnar { window_seconds, interval_seconds, series } payload
// from GET /api/apps/:slug/metrics/history.
function loadTrends(slug) {
  const container = document.getElementById('overview-trends');
  if (!container) return;
  fetch(`/api/apps/${slug}/metrics/history`, { credentials: 'include' })
    .then((r) => (r.ok ? r.json() : null))
    .then((body) => {
      if (!body) return;
      const card = renderTrendsCard(document, body);
      if (!card) {
        // History disabled server-side: hide the card entirely.
        container.hidden = true;
        return;
      }
      container.replaceChildren(card);
    })
    .catch(() => {});
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

// envelope is the raw GET /api/apps/:slug body, passed through because the
// render-pacing advisory (render_pacing) is an envelope-level block, not an app
// column: normalizeAppEnvelope deliberately folds only the fields that belong on
// the app itself.
function renderConfiguration(panel, app, ctx, envelope) {
  ctx.setSettingsSlug(app.slug);
  ctx.populateGeneralTab(app, envelope);
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
