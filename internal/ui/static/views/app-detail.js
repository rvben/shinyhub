import { revealConfigurationSection, applyScheduleTimezone } from '/static/views/configuration-context.js';
import { deployCommands } from '/static/views/deploy-commands.js';
// App detail view. Mounts #app-detail-view, populates the header, and shows
// the requested tab while keeping app identity and health consistent.
import { makeFleetStateCard, renderFleetBadges } from '/static/views/fleet-ui.js';
import { backendLabel, metricsText, reasonLabel } from '/static/views/replica-display.js';
import { createOverviewActivity } from '/static/views/app-overview-activity.js';
import { createDataViewer } from '/static/views/data-ui.js';
import { createTracesViewer } from '/static/views/traces-ui.js';
import { startVisiblePoll } from '/static/views/visible-poll.js';
import {
  summariseAutoscale,
  formatRejectsByReason,
  rejectionGuidance,
  renderAutoscaleSummary,
  renderRejectsByReason,
} from '/static/views/autoscale.js';
import {
  deploymentTimelineModels,
  relativeTime,
} from '/static/views/deployment-row.js';
import { releaseStripModel, renderReleaseStrip } from '/static/views/release-strip.js';
import { statusPillClass } from '/static/views/stat-format.js';
import { seedDetailCapacity } from '/static/views/detail-capacity.js';
import { formatStatus } from '/static/views/status-label.js';
import { appAttention } from '/static/views/app-attention.js';
import { appStatusView } from '/static/views/app-card-badge.js';
import { awaitingFirstDeploy, firstDeployFailed, hasSucceededDeploy } from '/static/views/app-deploy-state.js';
import { appCardActions } from '/static/views/app-card-actions.js';
import { capacityBanner } from '/static/views/capacity-banner.js';
import { connectivityBanner } from '/static/views/connectivity-banner.js';
import { renderTrendsCard } from '/static/views/trends-card.js';
import {
  TAB_ROUTES,
  resolveDetailAccess,
} from '/static/views/app-detail-nav.js';
import { normalizeAppEnvelope } from '/static/views/app-detail-envelope.js';
import { createFreshness } from '/static/views/freshness.js';
import { createLogsViewer } from '/static/views/logs-ui.js';
import { createUsageViewer } from '/static/views/usage-ui.js';

function pluralize(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
}

function capacityProcessUnit(app, metrics = null) {
  const isolation = metrics?.worker_isolation || app.effective_worker_isolation || app.worker_isolation;
  return ['grouped', 'per_session'].includes(isolation) ? 'worker' : 'replica';
}

// Write only when the value actually differs. Assigning textContent replaces
// the text node even when the string is identical, so an unconditional write in
// a re-applied render is a real DOM change: it discards a selection inside the
// element and shows up as churn to anything watching.
function setText(el, value) {
  if (el && el.textContent !== value) el.textContent = value;
}

export function mountAppDetail(ctx) {
  const view = document.getElementById('app-detail-view');
  const health = view.querySelector('#overview-health');
  const overviewHeading = view.querySelector('#app-overview-heading');
  const overviewRefresh = view.querySelector('#overview-refresh');
  const overviewRefreshStatus = view.querySelector('#overview-refresh-status');
  const metricsDisclosure = view.querySelector('.app-technical-summary');
  const recover = view.querySelector('#overview-health-recover');
  const editIcon = view.querySelector('#app-detail-icon-edit');
  editIcon?.addEventListener('click', async () => {
    if (!showing || !ctx.canManageApp(ctx.state.user, showing.app)) return;
    const slug = showing.slug;
    await ctx.navigate(`/apps/${encodeURIComponent(slug)}/settings/general`);
    // Navigation can be vetoed by unsaved edits or superseded by another app.
    // Only move focus once this app's editor is actually on screen.
    if (!showing || showing.slug !== slug || panels.configuration.hidden
        || panels.configuration.dataset.settingsPage !== 'general'
        || !ctx.canManageApp(ctx.state.user, showing.app)) return;
    revealConfigurationSection(view, 'configuration-general');
    const upload = view.querySelector('#general-icon-upload');
    upload?.focus({ preventScroll: true });
    upload?.closest('.scaling-row')?.scrollIntoView({ block: 'center' });
  });
  recover?.addEventListener('click', async () => {
    if (!showing || showing.app.deployment_repair_required || !appCardActions(showing.app, showing.canManage).showStart) return;
    const { slug, app } = showing;
    if (app.status === 'crashed') await ctx.restart(slug, recover);
    else await ctx.start(slug, recover);
    await revalidate(slug, true);
  });
  const panels = {
    overview:      document.getElementById('detail-overview-panel'),
    usage:         document.getElementById('detail-usage-panel'),
    logs:          document.getElementById('detail-logs-panel'),
    traces:        document.getElementById('detail-traces-panel'),
    deployments:   document.getElementById('detail-deployments-panel'),
    schedules:     document.getElementById('detail-schedules-panel'),
    configuration: document.getElementById('detail-configuration-panel'),
    data:          document.getElementById('detail-data-panel'),
    access:        document.getElementById('detail-access-panel'),
  };
  let tabCleanup = null;
  let overviewManualRevision = 0;
  let configurationInitialized = false;
  let accessInitialized = false;
  let activeTab = 'overview';
  // The app currently on screen, with the envelope it was rendered from. A tab
  // switch reuses it (see update below) instead of refetching and repainting
  // what the visitor is already looking at.
  let showing = null;
  // How long the cached envelope may go unchecked. Matched to the metrics poll
  // interval: nothing else on this page is fresher than that anyway, so clicking
  // through several tabs costs one background refresh rather than one per tab.
  const ENVELOPE_MAX_AGE_MS = 10000;
  const freshness = createFreshness({ maxAgeMs: ENVELOPE_MAX_AGE_MS });
  let revalidation = 0;
  let overviewRefreshAt = 0;
  let overviewCheckedAt = 0;
  let overviewEnvelopeError = false;
  let overviewRefreshPending = false;
  let liveMetrics = null;
  let metricsStale = false;

  // api() in app.js announces every successful mutating request, so a rollback,
  // a lifecycle action, a config save, or anything added later invalidates this
  // page's cached copy without the call site having to remember.
  document.addEventListener('shinyhub:mutated', () => freshness.mutated());

  function enforceCurrentAccess() {
    if (!showing) return false;
    const { redirect } = resolveDetailAccess({
      user: ctx.state.user, canManage: showing.canManage, requestedTab: activeTab, slug: showing.slug,
    });
    if (!redirect) return true;
    panels.configuration.hidden = true;
    panels.access.hidden = true;
    configurationInitialized = false; accessInitialized = false;
    ctx.clearSettingsDrafts?.();
    ctx.navigate(redirect.path, { replace: redirect.replace });
    return false;
  }

  // Show the selected panel, hide the rest, and render the active tab. The
  // panel that is about to fill is the only region allowed to be empty while
  // its data loads; the app header and contextual navigation stay put.
  async function renderTab(tab, params) {
    const { app, body, replicasStatus } = showing;
    activeTab = tab;
    view.dataset.appPage = tab;
    syncHeaderDestinations();
    if (tab !== 'configuration' && tab !== 'access') { configurationInitialized = false; accessInitialized = false; }
    for (const t of TAB_ROUTES) {
      panels[t].hidden = t !== tab;
    }

    if (tabCleanup) { tabCleanup(); tabCleanup = null; }
    overviewManualRevision++;
    overviewRefresh.disabled = false;
    overviewRefreshStatus.textContent = '';
    overviewHeading.hidden = tab !== 'overview';
    overviewRefreshStatus.hidden = tab !== 'overview';
    metricsDisclosure.hidden = tab !== 'overview' || !hasSucceededDeploy(app);
    renderAppHealth(app, body, liveMetrics, metricsStale,
      overviewEnvelopeError || Date.now() - overviewCheckedAt > 60000);

    if (tab === 'overview') {
      tabCleanup = renderOverview(panels.overview, app, replicasStatus, body, ctx);
      renderAppHealth(app, body, liveMetrics, metricsStale,
        overviewEnvelopeError || Date.now() - overviewCheckedAt > 60000);
    }
    if (tab === 'usage') {
      tabCleanup = renderUsage(panels.usage, app, ctx);
    }
    if (tab === 'logs') {
      tabCleanup = renderLogs(panels.logs, app, replicasStatus, ctx);
    }
    if (tab === 'traces') {
      tabCleanup = renderTraces(panels.traces, app, ctx);
    }
    if (tab === 'deployments') {
      const cleanup = renderDeployments(panels.deployments, app, ctx, () => {
        freshness.mutated();
        return revalidate(app.slug, true);
      });
      tabCleanup = cleanup;
    }
    if (tab === 'schedules') {
      tabCleanup = renderSchedules(panels.schedules, app, ctx, params.scheduleId, body);
    }
    if (tab === 'configuration') {
      if (!configurationInitialized) {
        renderConfiguration(panels.configuration, app, ctx, body);
        configurationInitialized = true;
      }
      revealConfigurationSection(view, params.section || 'general');
    }
    if (tab === 'data') {
      tabCleanup = renderData(panels.data, app, ctx, body);
    }
    if (tab === 'access') {
      if (!accessInitialized) {
        renderAccess(panels.access, app, ctx);
        accessInitialized = true;
      }
    }
  }

  // A tab switch inside the app already on screen. The view is never hidden and
  // nothing outside the panel is rebuilt, so the header, the metric tiles, the
  // scroll position and the visitor's focus all survive untouched. This exists
  // because remounting the view made a tab switch read as a page reload.
  async function update(params) {
    if (!showing || showing.slug !== params.slug) {
      // The router only routes an update here when the key (the slug) matches,
      // so this is unreachable; fail loudly rather than render another app's
      // data under this app's header.
      throw new Error(`app detail: update for ${params.slug} while showing ${showing && showing.slug}`);
    }
    const { tab, redirect } = resolveDetailAccess({
      user: ctx.state.user, canManage: showing.canManage, requestedTab: params.tab, slug: params.slug,
    });
    if (redirect) { ctx.navigate(redirect.path, { replace: redirect.replace }); return; }

    if (freshness.isStale()) await refetch(params.slug);
    // A refetch that found the app gone has already navigated away.
    if (!showing || showing.slug !== params.slug) return;
    activeTab = tab;
    if (!enforceCurrentAccess()) return;
    await renderTab(tab, params);
    ctx.updateActiveNav(location.pathname);
    ctx.metrics.setTargets([showing.app.slug]);
    revalidate(params.slug);
  }

  // Pull the envelope again and adopt it, for the case where the page is known
  // to be out of date (an action was taken) and the next render must not come
  // from before it. A failure leaves the cache in place and still stale: showing
  // the last known-good data beats blanking the page over one lost request, and
  // the next switch tries again.
  async function refetch(slug) {
    const stamp = freshness.begin();
    let resp;
    try {
      resp = await ctx.api(`/api/apps/${slug}`);
    } catch {
      return;
    }
    if (resp.status === 404) {
      showing = null;
      freshness.forget();
      ctx.navigate('/apps', { replace: true });
      return;
    }
    if (resp.status === 401) { ctx.onUnauthorized(); return; }
    if (!resp.ok) return;
    const body = await resp.json();
    if (!showing || showing.slug !== slug) return;
    const { app, replicasStatus } = normalizeAppEnvelope(body);
    revalidation++; // any background revalidation in flight is now the older answer
    showing = {
      slug, app, body, replicasStatus,
      canManage: ctx.canManageApp(ctx.state.user, app),
    };
    freshness.adopt(stamp);
    if (ctx.setDetailApp) ctx.setDetailApp(app);
    applyHeader(app, body);
    if (!panels.overview.hidden) syncOverviewSignals(panels.overview, app, body, ctx);
    overviewCheckedAt = Date.now();
    overviewEnvelopeError = false;
    renderAppHealth(app, body, liveMetrics, metricsStale, false);
  }

  // Refresh the envelope in the background. Changed health signals are patched
  // into Overview without rebuilding the charts or replica list.
  function revalidate(slug, force = false) {
    if (!showing || showing.slug !== slug) return;
    if (!force && !freshness.isOld()) return;
    const token = ++revalidation;
    const stamp = freshness.begin();
    return Promise.resolve()
      .then(async () => {
        const resp = await ctx.api(`/api/apps/${slug}`);
        if (token !== revalidation || !showing || showing.slug !== slug) return;
        if (resp.status === 401) { ctx.onUnauthorized(); return; }
        if (!resp.ok) {
          overviewEnvelopeError = true;
          renderAppHealth(showing.app, showing.body, liveMetrics, metricsStale, true);
          return;
        }
        const body = await resp.json();
        // A newer revalidation, or a move to another app, has superseded us.
        if (token !== revalidation || !showing || showing.slug !== slug) return;
        const unchanged = JSON.stringify(body) === JSON.stringify(showing.body);
        freshness.adopt(stamp);
        overviewCheckedAt = Date.now();
        overviewEnvelopeError = false;
        // Nothing changed, so there is nothing to repaint. Writing the same
        // values back would still replace DOM nodes for no reason.
        if (unchanged) {
          renderAppHealth(showing.app, showing.body, liveMetrics, metricsStale, false);
          return;
        }
        const { app, replicasStatus } = normalizeAppEnvelope(body);
        showing = {
          slug, app, body, replicasStatus,
          canManage: ctx.canManageApp(ctx.state.user, app),
        };
        if (ctx.setDetailApp) ctx.setDetailApp(app);
        if (!enforceCurrentAccess()) return;
        if (ctx.setDetailEnvelope) ctx.setDetailEnvelope(body);
        applyHeader(app, body);
        if (!panels.overview.hidden) {
          const normal = !awaitingFirstDeploy(app) && !firstDeployFailed(app);
          const hasNormalPanel = !!panels.overview.querySelector('.overview-grid');
          if (normal !== hasNormalPanel || (!normal && !!firstDeployFailed(app) !== !!panels.overview.querySelector('#overview-failed-error'))) {
            tabCleanup?.();
            tabCleanup = renderOverview(panels.overview, app, replicasStatus, body, ctx);
          } else {
            syncOverviewSignals(panels.overview, app, body, ctx);
          }
          const autoscale = panels.overview.querySelector('#autoscale-summary');
          if (autoscale) renderAutoscaleSummary(autoscale, summariseAutoscale(app, body));
          if (ctx.setDetailEnvelope) ctx.setDetailEnvelope(body);
        }
        renderAppHealth(app, body, liveMetrics, metricsStale, false);
      })
      .catch(() => {
        if (token !== revalidation || !showing || showing.slug !== slug) return;
        overviewEnvelopeError = true;
        renderAppHealth(showing.app, showing.body, liveMetrics, metricsStale, true);
      });
  }

  overviewRefresh.addEventListener('click', async () => {
    if (!showing || activeTab !== 'overview' || overviewRefresh.disabled) return;
    const slug = showing.slug;
    const revision = ++overviewManualRevision;
    overviewRefresh.disabled = true;
    overviewRefreshStatus.textContent = 'Refreshing overview…';
    try {
      const results = await Promise.allSettled([revalidate(slug, true), ctx.metrics.refresh?.(), refreshTrends(slug), tabCleanup?.refresh?.(true)]);
      if (revision !== overviewManualRevision || !showing || showing.slug !== slug || activeTab !== 'overview') return;
      overviewRefreshStatus.textContent = overviewEnvelopeError || metricsStale || results.some(result => result.status === 'rejected' || result.value === false)
        ? 'Some overview information could not be refreshed. Check the messages in each section.' : 'Overview updated.';
    } finally { if (revision === overviewManualRevision) overviewRefresh.disabled = false; }
  });

  // A metrics poll is also the cadence for checking the slower envelope-only
  // health signals. Keep this separate from the 10s live resource measurements.
  mount.onLiveMetrics = (slug, metrics) => {
    if (!showing || showing.slug !== slug) return;
    const statusChanged = !!metrics.status && metrics.status !== showing.app.status;
    if (statusChanged || typeof metrics.deploying === 'boolean') {
      showing.app = { ...showing.app, ...(metrics.status ? { status: metrics.status } : {}),
        ...(typeof metrics.deploying === 'boolean' ? { deploying: metrics.deploying } : {}) };
      if (ctx.setDetailApp) ctx.setDetailApp(showing.app);
      applyHeader(showing.app, showing.body);
    }
    liveMetrics = metrics;
    const trends = document.getElementById('overview-trends');
    if (trends) trends.dataset.cpuCapacityCores = String(metrics.cpu_capacity_cores || '');
    metricsStale = false;
    renderAppHealth(showing.app, showing.body, liveMetrics, metricsStale, overviewEnvelopeError || Date.now() - overviewCheckedAt > 60000);
    if (overviewRefresh.disabled) return;
    if (overviewRefreshPending || (!statusChanged && Date.now() - overviewRefreshAt < 30000)) return;
    overviewRefreshAt = Date.now();
    overviewRefreshPending = true;
    Promise.resolve(revalidate(slug, true)).finally(() => { overviewRefreshPending = false; });
    refreshTrends(slug);
    if (activeTab === 'overview') void tabCleanup?.refresh?.();
  };
  mount.onMetricsError = (slug) => {
    if (!showing || showing.slug !== slug) return;
    metricsStale = true;
    renderAppHealth(showing.app, showing.body, liveMetrics, metricsStale, overviewEnvelopeError || Date.now() - overviewCheckedAt > 60000);
  };

  // A fresh arrival on this page: fetch the app, build the whole view, reveal it.
  async function mount(params) {
    const sessionIdentity = ctx.state.sessionIdentity;
    const { slug } = params;
    const stamp = freshness.begin();

    // Preserve the user's URL: /apps/<slug>/overview is a legitimate route
    // (every other tab keeps its segment, so /overview should too). The
    // previous version replaced it with /apps/<slug>, which surprised users
    // who pasted/bookmarked the explicit overview URL.

    const resp = await ctx.api(`/api/apps/${slug}`);
    if (sessionIdentity !== ctx.state.sessionIdentity) return {};
    if (resp.status === 404) { ctx.navigate('/apps', { replace: true }); return {}; }
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
    if (sessionIdentity !== ctx.state.sessionIdentity) return {};
    const { app, replicasStatus } = normalizeAppEnvelope(body);

    const canManage = ctx.canManageApp(ctx.state.user, app);

    // Resolve which tab to show and whether the visitor must be redirected:
    // a pure viewer who doesn't manage this app is returned to Apps (`/apps`),
    // and a non-manager on a manager-only tab (configuration/data/access) is sent
    // to the app root. An unknown tab falls back to overview. See
    // views/app-detail-nav.js. The gate runs after the app loads, so it covers
    // every path in (a sidebar app link, a typed URL, a bookmark).
    const { tab, redirect } = resolveDetailAccess({
      user: ctx.state.user, canManage, requestedTab: params.tab, slug,
    });
    if (redirect) { ctx.navigate(redirect.path, { replace: redirect.replace }); return {}; }

    configurationInitialized = false; accessInitialized = false;
    showing = { slug, app, body, replicasStatus, canManage };
    liveMetrics = null;
    metricsStale = false;
    overviewRefreshAt = Date.now();
    overviewCheckedAt = Date.now();
    overviewEnvelopeError = false;
    freshness.adopt(stamp);

    // Record the app so the static header kebab (wired once in app.js) acts on
    // the right app.
    if (ctx.setDetailApp) ctx.setDetailApp(app);
    if (ctx.setDetailEnvelope) ctx.setDetailEnvelope(body);

    applyHeader(app, body);
    // Seed the metric tiles until the first metrics poll arrives. Only on a
    // fresh mount: on a tab switch the poller is still running and the tiles
    // already hold real values, and blanking them to "—" is precisely what made
    // a tab click look like a page load.
    overviewRefreshStatus.textContent = '';
    seedStats(app);

    await renderTab(tab, params);
    if (sessionIdentity !== ctx.state.sessionIdentity) return {};

    view.hidden = false;
    ctx.updateActiveNav(location.pathname);
    ctx.metrics.setTargets([app.slug]);

    return {
      title: app.name,
      update,
      unmount() {
        overviewManualRevision++;
        if (tabCleanup) { tabCleanup(); tabCleanup = null; }
        // Abandon any in-flight revalidation: its result belongs to a page the
        // visitor has left.
        revalidation++;
        showing = null;
        if (ctx.setDetailApp) ctx.setDetailApp(null);
        liveMetrics = null;
        freshness.forget();
        view.hidden = true;
        ctx.metrics.setTargets([]);
      },
    };
  }

  // Header fields, all derived from the envelope. Safe to re-apply at any time:
  // it writes only values it has, and touches no metric tile. Text goes through
  // setText so a background refresh that finds nothing changed also changes
  // nothing on screen: assigning the same string still replaces the text node,
  // which would drop a selection the visitor had made inside the header.
  function applyHeader(app, body) {
    setText(document.getElementById('app-detail-heading'), app.name);
    if (editIcon) editIcon.hidden = !ctx.canManageApp(ctx.state.user, app);
    const descriptionEl = document.getElementById('app-detail-description');
    if (descriptionEl) {
      const description = typeof app.description === 'string' ? app.description.trim() : '';
      setText(descriptionEl, description);
      descriptionEl.hidden = !description;
    }
    setText(document.getElementById('app-detail-slug'), '/' + app.slug);
    const statusEl = document.getElementById('app-detail-status');
    // Derive the pill from the same appStatusView the apps-grid card uses, so
    // the two surfaces always agree: a zero-deploy app reads "Awaiting deploy"
    // (not "degraded"), and a zero-deploy crash-looped one reads "Failed", not
    // the benign "Awaiting deploy".
    const statusView = appStatusView(app, formatStatus);
    setText(statusEl, statusView.text);
    const pillClass = statusPillClass(statusView.state);
    if (statusEl.className !== pillClass) statusEl.className = pillClass;
    const openLink = document.getElementById('app-detail-open');
    const openHref = `/app/${app.slug}/`;
    if (openLink.getAttribute('href') !== openHref) openLink.href = openHref;
    const hasRelease = hasSucceededDeploy(app) || app.last_deployment_status === 'succeeded';
    openLink.hidden = !hasRelease || !['running', 'idle', 'hibernated', 'degraded'].includes(app.status);
    statusEl.hidden = !awaitingFirstDeploy(app) && !firstDeployFailed(app);
    if (health) health.hidden = awaitingFirstDeploy(app) || firstDeployFailed(app);
    if (recover) {
      recover.hidden = !!app.deployment_repair_required || !appCardActions(app, ctx.canManageApp(ctx.state.user, app)).showStart;
      recover.textContent = app.status === 'crashed' ? 'Restart' : 'Start app';
    }
    document.querySelector('.app-detail-capacity').hidden = !hasRelease;
    metricsDisclosure.hidden = panels.overview.hidden || !hasRelease;

    // The header kebab holds only manager actions, so a viewer must not see it
    // at all. That decision, and which individual items apply to this app, is
    // made once in app.js (syncDetailHeaderActions, driven by the same
    // appCardActions helper the dashboard cards use) and applied through
    // ctx.setDetailApp above. Setting it here as well would give one flag two
    // writers, and whichever ran last would silently win.

    const fleetSlot = document.getElementById('app-detail-fleet-badge');
    if (fleetSlot) {
      renderFleetBadges(fleetSlot, app, body.fleet_state);
    }
    renderReleaseStrip(document, document.getElementById('app-detail-release'), releaseStripModel(app), {
      markFor: provenanceMark,
    });
    syncHeaderDestinations();
  }

  function syncHeaderDestinations() {
    const history = view.querySelector('.release-history');
    if (history) history.hidden = activeTab === 'deployments';
  }

  // A fresh mount has no live measurements; preserve values on tab switches.
  function seedStats(app) {
    seedDetailCapacity(document, app);
  }

  return mount;
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

// provenanceMark draws the icon that leads the release strip's source: the
// official GitLab/GitHub mark, or ShinyHub's own manual, rollback or CI glyph.
// Channels named in words only (CLI, API, live development) get no mark, since
// the sentence beside it already says the same thing.
function provenanceMark(model) {
  const icon = model.providerIcon || model.markIcon;
  if (!icon) return null;
  const mark = document.createElement('span');
  mark.className = 'provenance-provider';
  if (icon === 'gitlab' || icon === 'github') {
    mark.classList.add('is-brand', `is-${icon}`);
    mark.append(providerBrandIcon(icon));
  } else {
    mark.classList.add(`is-${icon}`);
    mark.append(provenanceIcon(icon));
  }
  return mark;
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
    // The press-kit SVG ships on a 380px canvas around a ~152px mark. Use an
    // evenly padded viewport around the unchanged official paths so the visible
    // fox—not its transparent canvas—is centered inside ShinyHub's icon tile.
    svg.setAttribute('viewBox', '100 100 180 180');
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
  // An app nobody has ever tried to deploy has no log sources. Keep the
  // intentional first-deploy guidance instead of mounting a viewer that can
  // only report an unavailable source.
  //
  // The gate is "has a deploy ever been ATTEMPTED", not "has one ever
  // succeeded": a first deploy that crashed on startup writes its traceback to
  // the app log, and gating on deploy_count (successes only) hid exactly that
  // traceback behind "awaiting its first deploy".
  if (awaitingFirstDeploy(app)) {
    panel.innerHTML = `
      <div class="logs-empty">
        <h2>No logs yet</h2>
        <p>This app is awaiting its first deploy. Output appears here once it's
           deployed and running.</p>
        <p><a href="/apps/${app.slug}/overview" data-nav>Deploy from the Overview tab <span aria-hidden="true">→</span></a></p>
      </div>
    `;
    return () => {};
  }
  return createLogsViewer({ panel, app, initialSources: replicasStatus, api: ctx.api });
}

function renderUsage(panel, app, ctx) {
  return createUsageViewer({ panel, app, api: ctx.api, onUnauthorized: ctx.onUnauthorized });
}

function makeStatusBadge(cls, text) {
  const span = document.createElement('span');
  span.className = cls;
  span.textContent = text;
  return span;
}

function makeDeploymentRow(m, nested = false, app) {
  const li = document.createElement('li');
  li.className = 'deployment-row' + (m.isCurrent ? ' deployment-row-current' : '') + (nested ? ' deployment-row-attempt' : '');
  const verCell = document.createElement('span');
  verCell.className = 'deployment-version';
  const num = document.createElement('strong');
  num.className = 'deployment-number';
  num.textContent = m.releaseLabel;
  if (m.releaseLabel) num.title = 'bundle ' + m.version;
  verCell.appendChild(num);
  if (m.isCurrent) {
    verCell.appendChild(makeStatusBadge('deployment-current-badge', 'Current'));
  } else if (m.status === 'failed') {
    const b = makeStatusBadge('deployment-failed-badge', 'Failed');
    if (m.failureReason) b.title = m.failureReason;
    verCell.appendChild(b);
  } else if (m.status === 'blocked') {
    const b = makeStatusBadge('deployment-pending-badge', 'Blocked');
    if (m.failureReason) b.title = m.failureReason;
    verCell.appendChild(b);
  } else if (m.status !== 'succeeded') {
    verCell.appendChild(makeStatusBadge('deployment-pending-badge', 'Deploying'));
  }
  const whenCell = document.createElement('span');
  whenCell.className = 'deployment-when';
  if (m.time) {
    const time = document.createElement('time');
    time.dateTime = m.time.datetime;
    time.textContent = m.time.absolute;
    const age = document.createElement('span');
    age.textContent = m.time.relative;
    whenCell.append(time, age);
  } else whenCell.textContent = 'Time unavailable';

  const sourceCell = document.createElement('span');
  sourceCell.className = 'deployment-source';
  const sourcePrimary = document.createElement('span');
  sourcePrimary.className = 'deployment-source-primary';
  if (nested && m.source.developmentSession) sourcePrimary.textContent = 'Development save';
  else if (m.source.url) sourcePrimary.appendChild(externalLink(m.source.label, m.source.url));
  else sourcePrimary.textContent = m.source.available ? m.source.label : 'Source not recorded';
  const sourceDetail = document.createElement('span');
  sourceDetail.className = 'deployment-source-detail';
  if (m.source.available) sourceDetail.append(m.source.detail);
  if (m.source.change) {
    sourceDetail.append(' · ');
    sourceDetail.append(m.source.change.url ? externalLink(m.source.change.label, m.source.change.url) : m.source.change.label);
  }
  if (m.restoredFromReleaseNumber != null) sourceDetail.append(` · restored bundle from v${m.restoredFromReleaseNumber}`);
  sourceCell.appendChild(sourcePrimary);
  if (sourceDetail.textContent) sourceCell.appendChild(sourceDetail);
  if (m.status === 'failed' || m.status === 'blocked') {
    const reason = document.createElement('span');
    reason.className = 'deployment-failure-reason';
    reason.textContent = m.failureReason || (m.status === 'blocked' ? 'Deployment is blocked.' : 'No failure details were recorded.');
    sourceCell.appendChild(reason);
  }

  const actionCell = document.createElement('span');
  actionCell.className = 'deployment-action';
  if (m.canRollback) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'rollback-btn';
    btn.dataset.id = m.id;
    btn.dataset.release = m.releaseLabel || `deployment #${m.id}`;
    btn.setAttribute('aria-label', `Roll back to ${btn.dataset.release}`);
    btn.textContent = 'Roll back';
    actionCell.appendChild(btn);
  } else if ((m.status === 'failed' || m.status === 'blocked') && app) {
    const logs = document.createElement('a');
    logs.href = `/apps/${encodeURIComponent(app.slug)}/logs`;
    logs.dataset.nav = '';
    logs.className = 'deployment-logs-link';
    logs.textContent = 'App logs';
    actionCell.appendChild(logs);
  }
  // Definition pairs carry the same relationships on desktop, mobile and in
  // a screen reader, including attempts nested under development sessions.
  const fields = document.createElement('dl');
  fields.className = 'deployment-fields';
  for (const [label, cell] of [['Deployment', verCell], ['Source & outcome', sourceCell], ['Started', whenCell], ['Actions', actionCell]]) {
    if (label === 'Actions' && !cell.hasChildNodes()) continue;
    const group = document.createElement('div');
    group.className = cell.className;
    const term = document.createElement('dt');
    term.className = 'sr-only';
    term.textContent = label;
    const value = document.createElement('dd');
    value.append(...cell.childNodes);
    group.append(term, value);
    fields.appendChild(group);
  }
  li.appendChild(fields);
  return li;
}

function makeDevelopmentSessionRow(model, app) {
  const li = document.createElement('li');
  li.className = 'development-session-row' + (model.isCurrent ? ' development-session-current' : '');
  const details = document.createElement('details');
  details.className = 'development-session-details';
  details.dataset.sessionId = model.id;
  if (model.pendingCount > 0) details.open = true;
  const summary = document.createElement('summary');
  summary.className = 'development-session-summary';

  const identity = document.createElement('span');
  identity.className = 'development-session-identity';
  const badge = makeStatusBadge('development-session-badge', 'DEV');
  const title = document.createElement('strong');
  title.textContent = 'Remote development';
  identity.append(badge, title);

  const context = document.createElement('span');
  context.className = 'development-session-context';
  context.textContent = [model.actor, model.targetLabel].filter(Boolean).join(' · ');

  const outcome = document.createElement('span');
  outcome.className = 'development-session-outcome';
  const parts = [];
  if (model.latestReleaseLabel) parts.push(model.latestReleaseLabel + (model.isCurrent ? ' current' : ''));
  parts.push(pluralize(model.attemptCount, 'save', 'saves'));
  if (model.failedCount) parts.push(pluralize(model.failedCount, 'failed', 'failed'));
  outcome.textContent = parts.join(' · ');

  const when = document.createElement('span');
  when.className = 'development-session-when';
  when.textContent = model.status === 'active'
    ? `Last save ${model.relWhen || 'just now'}`
    : `Ended ${model.relWhen || ''}`.trim();
  if (model.absWhen) when.title = `Started ${model.absWhen}`;
  // Match the parent timeline columns: identity, source context, deployed time,
  // then the compact outcome in the action column.
  summary.append(identity, context, when, outcome);

  const tools = document.createElement('nav');
  tools.className = 'development-session-tools';
  tools.setAttribute('aria-label', 'Development session observability');
  const query = `development_session_id=${encodeURIComponent(model.id)}`;
  for (const [label, tab] of [['View logs', 'logs'], ['View traces', 'traces']]) {
    const link = document.createElement('a');
    link.href = `/apps/${encodeURIComponent(app.slug)}/${tab}?${query}`;
    link.dataset.nav = '';
    link.textContent = label;
    tools.appendChild(link);
  }
  const attempts = document.createElement('ul');
  attempts.className = 'development-session-attempts';
  attempts.setAttribute('aria-label', `${model.attemptCount} deployment attempts`);
  for (const attempt of model.attempts) attempts.appendChild(makeDeploymentRow(attempt, true, app));
  details.append(summary, tools, attempts);
  li.appendChild(details);
  return li;
}

function renderDeployments(panel, app, ctx, onChanged) {
  const canManage = ctx.canManageApp(ctx.state.user, app);
  const identity = ctx.state.sessionIdentity;
  let disposed = false, loading = false, rollingBack = false, pending = false;
  let rows = [], total = 0, signature = '', stopPoll = null;
  const active = () => !disposed && identity === ctx.state.sessionIdentity;
  panel.innerHTML = `
    <header class="app-tab-heading">
      <div><h2>Deployments</h2><p>Release history and rollbacks.</p></div>
      <div class="deployment-tools">
        <button type="button" class="btn-row" id="detail-deployments-refresh">Refresh</button>
        ${canManage ? '<button type="button" class="btn-primary" id="detail-deploy-new">Deploy new version</button>' : ''}
      </div>
    </header>
    <p id="detail-deployments-status" class="deployment-status" role="status"></p>
    <p id="detail-deployments-announcement" class="sr-only" role="status" aria-live="polite" aria-atomic="true"></p>
    <ul id="detail-deployments-list" class="deployments-list" aria-label="Deployment history" hidden>
      <li class="deployments-head" aria-hidden="true">
        <span>Deployment</span><span>Source &amp; outcome</span><span>Started</span>
        <span class="deployment-action">Actions</span>
      </li>
    </ul>
    <p id="detail-deployments-empty" class="env-empty" hidden>${canManage ? 'No deployments yet. Deploy your first version to get started.' : 'No deployments yet. An app manager can deploy the first version.'}</p>
    <div id="detail-deployments-error" class="deployments-error" hidden>
      <p class="error" role="alert"></p>
      <button type="button" class="btn-row" id="detail-deployments-retry">Retry</button>
    </div>
    <button type="button" class="btn-row deployment-earlier" id="detail-deployments-earlier" hidden>Load earlier deployments</button>`;
  const list = panel.querySelector('#detail-deployments-list');
  const head = list.querySelector('.deployments-head');
  const empty = panel.querySelector('#detail-deployments-empty');
  const errWrap = panel.querySelector('#detail-deployments-error');
  const status = panel.querySelector('#detail-deployments-status');
  const announcement = panel.querySelector('#detail-deployments-announcement');
  const refresh = panel.querySelector('#detail-deployments-refresh');
  const retry = panel.querySelector('#detail-deployments-retry');
  const earlier = panel.querySelector('#detail-deployments-earlier');
  const deploy = panel.querySelector('#detail-deploy-new');
  deploy?.addEventListener('click', () => ctx.openDeployModal(app));

  function syncControls() {
    refresh.disabled = loading || rollingBack;
    retry.disabled = loading || rollingBack;
    earlier.disabled = loading || rollingBack;
    if (deploy) deploy.disabled = rollingBack || pending;
    list.querySelectorAll('.rollback-btn').forEach(btn => { btn.disabled = loading || rollingBack || pending; });
    list.setAttribute('aria-busy', String(loading));
    setText(status, rollingBack ? 'Rolling back… Keep this page open to see the result.'
      : pending ? 'Deployment in progress. History updates automatically.'
      : loading ? (rows.length ? 'Refreshing deployment history…' : 'Loading deployments…') : '');
    status.hidden = !status.textContent;
  }

  function showError(message) {
    setText(errWrap.querySelector('.error'), message + (rows.length ? ' Showing the last loaded history.' : ''));
    errWrap.hidden = false;
  }

  function paint() {
    const next = JSON.stringify(rows);
    if (next !== signature) {
      const focusID = list.contains(document.activeElement) ? document.activeElement.dataset.id : null;
      const openSessions = new Map([...list.querySelectorAll('details')].map(el => [el.dataset.sessionId, el.open]));
      list.replaceChildren(head);
      for (const item of deploymentTimelineModels(rows)) {
        list.appendChild(item.kind === 'development-session'
          ? makeDevelopmentSessionRow(item, app)
          : makeDeploymentRow(item.deployment, false, app));
      }
      list.querySelectorAll('details').forEach(el => {
        if (openSessions.has(el.dataset.sessionId)) el.open = openSessions.get(el.dataset.sessionId);
      });
      if (!canManage) list.querySelectorAll('.rollback-btn').forEach(button => button.closest('.deployment-action').remove());
      if (focusID) [...list.querySelectorAll('.rollback-btn')].find(btn => btn.dataset.id === focusID)?.focus();
      signature = next;
    }
    list.hidden = rows.length === 0;
    empty.hidden = rows.length !== 0;
    earlier.hidden = rows.length >= total;
  }

  async function load(append = false, manual = false) {
    if (!active() || loading || rollingBack) return;
    const initiator = panel.contains(document.activeElement) ? document.activeElement : null;
    loading = true;
    if (manual) setText(announcement, '');
    empty.hidden = true;
    errWrap.hidden = true;
    syncControls();
    try {
      // A refresh reloads every visible page so prepended releases cannot shift
      // the offset under an already expanded history.
      const fresh = [];
      const target = append ? rows.length + 50 : Math.max(rows.length, 50);
      let nextTotal = 0;
      do {
        const offset = fresh.length;
        const query = `?limit=50&offset=${offset}`;
        const resp = await ctx.api(`/api/apps/${app.slug}/deployments${query}`);
        if (!active()) return;
        if (resp.status === 401) { ctx.onUnauthorized(); return; }
        if (!resp.ok) {
          let message = `Could not load deployments (HTTP ${resp.status}). Try again.`;
          try { const body = await resp.json(); if (body?.error) message = body.error; } catch { /* non-JSON */ }
          throw new Error(message);
        }
        const body = await resp.json();
        if (!active()) return;
        const page = Array.isArray(body) ? body : (body && body.items);
        if (!Array.isArray(page) || page.some(row => !row || typeof row !== 'object' || !Number.isSafeInteger(row.id))) {
          throw new Error('Could not read deployment history. Try again.');
        }
        nextTotal = Number.isSafeInteger(body?.total) ? body.total : fresh.length + page.length;
        fresh.push(...page);
        if (!page.length) break;
      } while (fresh.length < target && fresh.length < nextTotal);
      rows = fresh;
      total = nextTotal;
      const wasPending = pending;
      pending = rows.some(row => row.status === 'pending');
      paint();
      if (pending && !stopPoll) stopPoll = startVisiblePoll(document, 5000, () => { void load(); });
      if (!pending && stopPoll) { stopPoll(); stopPoll = null; }
      if (wasPending && !pending) await onChanged?.();
      if (manual && active()) setText(announcement, append ? 'Earlier deployments loaded.' : 'Deployment history updated.');
    } catch (error) {
      if (active()) showError(error instanceof SyntaxError ? 'Could not read deployment history. Try again.'
        : error instanceof TypeError ? 'Network error: could not load deployments. Try again.' : error.message || 'Could not load deployments. Try again.');
    } finally {
      if (active()) {
        loading = false;
        syncControls();
        if (initiator && (document.activeElement === document.body || document.activeElement === initiator)) {
          (initiator.isConnected && !initiator.hidden && !initiator.closest('[hidden]') ? initiator : refresh).focus();
        }
      }
    }
  }

  // One delegate survives retries and polling; only one rollback can run.
  list.onclick = async (event) => {
    const btn = event.target.closest('.rollback-btn');
    if (!btn || !canManage || loading || rollingBack || pending || !active()) return;
    const release = btn.dataset.release;
    if (!window.confirm(`Roll back ${app.name} to ${release}?\n\nThis creates a new release from that bundle and restarts the app. Active sessions may be interrupted.`)) return;
    rollingBack = true;
    errWrap.hidden = true;
    syncControls();
    try {
      const resp = await ctx.api(`/api/apps/${app.slug}/rollback`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Shinyhub-Deploy-Channel': 'dashboard' },
        body: JSON.stringify({ deployment_id: Number(btn.dataset.id) }),
      });
      if (!active()) return;
      if (resp.status === 401) { ctx.onUnauthorized(); return; }
      let body;
      try { body = await resp.json(); } catch { /* response metadata is optional */ }
      if (!active()) return;
      if (!resp.ok) {
        const msg = body?.error ? `Rollback failed: ${body.error}` : 'Rollback failed. Refresh history before trying again.';
        showError(msg);
        ctx.flashToast(msg, 'error');
      } else {
        ctx.flashToast(body?.warning || `Rolled back to ${release}.`, body?.warning ? 'warning' : 'success');
      }
      // Even an unsuccessful response can follow a runtime change. Recheck the
      // header and history, but never repeat a mutation automatically.
      await onChanged?.();
      if (!active()) return;
      rollingBack = false;
      const rollbackError = !errWrap.hidden ? errWrap.querySelector('.error').textContent : '';
      await load();
      if (active() && rollbackError) showError(rollbackError.replace(' Showing the last loaded history.', ''));
    } catch {
      if (!active()) return;
      showError('Could not confirm rollback. Refresh history before trying again.');
      ctx.flashToast('Could not confirm rollback. Refresh history before trying again.', 'error');
    } finally {
      if (active()) {
        rollingBack = false;
        syncControls();
        if (document.activeElement === document.body) refresh.focus();
      }
    }
  };
  refresh.addEventListener('click', () => { void load(false, true); });
  retry.addEventListener('click', () => { void load(false, true); });
  earlier.addEventListener('click', () => { void load(true, true); });
  const cleanup = () => { disposed = true; stopPoll?.(); list.onclick = null; };
  void load();
  return cleanup;
}

function renderOverview(panel, app, replicasStatus, envelope, ctx) {
  const processUnit = capacityProcessUnit(app);
  // Onboarding is for an app nobody has deployed yet. An app whose deploy was
  // attempted and failed gets the failure summary below instead: it has a
  // cause to explain, and showing it the brand-new-app snippet claimed nothing
  // had been deployed while the crash sat in its log.
  if (awaitingFirstDeploy(app)) {
    const canDeploy = ctx.canManageApp(ctx.state.user, app);
    panel.innerHTML = `
      <section class="first-deploy" aria-labelledby="first-deploy-heading">
        <div class="first-deploy-intro">
          <h2 id="first-deploy-heading">Deploy your first release</h2>
          <p>${canDeploy
            ? 'Run this command from your app’s project directory to deploy its first release.'
            : 'This app has not been deployed yet. An app manager can deploy its first release.'}</p>
          ${canDeploy ? `<p class="first-deploy-note">The CLI is recommended for production deployments.</p>
          <button type="button" class="btn-row" id="overview-deploy-btn">Deployment options</button>` : ''}
        </div>
        ${canDeploy ? `<div class="first-deploy-terminal">
          <div class="first-deploy-terminal-header">
            <h3>Run in your terminal</h3>
            <button type="button" id="overview-cli-copy" class="btn-row">Copy command</button>
          </div>
          <pre tabindex="0" role="region" aria-label="CLI deployment commands"><code id="overview-cli-snippet"></code></pre>
          <p id="overview-cli-copy-status" class="first-deploy-copy-status" role="status" aria-live="polite"></p>
        </div>
        <div class="first-deploy-setup">
          <div><a href="/apps/${app.slug}/settings/general" data-nav>Configure app</a><p>Set environment variables and runtime options.</p></div>
          <div><a href="/apps/${app.slug}/settings/access" data-nav>Manage access</a><p>Choose who can open or manage this app.</p></div>
        </div>` : ''}
      </section>
    `;
    if (canDeploy) {
      const commands = deployCommands(location.origin, app.slug);
      document.getElementById('overview-cli-snippet').textContent = commands;
      const copy = document.getElementById('overview-cli-copy');
      const copyStatus = document.getElementById('overview-cli-copy-status');
      copy.addEventListener('click', async () => {
        try {
          await navigator.clipboard.writeText(commands);
          copy.textContent = 'Copied';
          copyStatus.textContent = 'Deployment command copied to clipboard.';
        } catch {
          copyStatus.textContent = 'Could not copy. Select the commands above and copy them manually.';
        }
      });
      document.getElementById('overview-deploy-btn').addEventListener('click', () => {
        ctx.openDeployModal(app);
      });
    }
    return;
  }
  // Deployed at least once, never successfully, newest attempt failed: there is
  // no running bundle for the normal overview cards to describe, but there IS a
  // failure to explain. Name it and point at the two tabs that carry the
  // detail, instead of the current-deployment grid full of dashes.
  if (firstDeployFailed(app)) {
    panel.innerHTML = `
      <section class="emptystate-card">
        <p class="emptystate-eyebrow">Deploy failed</p>
        <h2>This app has never started</h2>
        <p class="lead">A deploy was attempted and failed, so there is no
           running version yet. The startup output, including any traceback,
           is on the Logs tab.</p>
        <div class="snippet" id="overview-failed-error" hidden>
          <pre role="region" tabindex="0" aria-label="Deployment failure details"><code id="overview-failed-error-text"></code></pre>
        </div>
        <div class="emptystate-actions">
          <a class="emptystate-btn emptystate-btn-primary" href="/apps/${app.slug}/logs" data-nav>View logs</a>
          <a class="emptystate-btn" href="/apps/${app.slug}/deployments" data-nav>Deployment history</a>
          ${ctx.canManageApp(ctx.state.user, app) ? '<button type="button" class="emptystate-btn" id="overview-deploy-btn">Deploy again</button>' : ''}
        </div>
      </section>
    `;
    // last_error is server-supplied text (a boot error, often a traceback tail).
    // textContent, never interpolation, so it cannot inject markup.
    const failedError = (app.last_error || '').trim();
    if (failedError) {
      document.getElementById('overview-failed-error-text').textContent = failedError;
      document.getElementById('overview-failed-error').hidden = false;
    }
    document.getElementById('overview-deploy-btn')?.addEventListener('click', () => {
      ctx.openDeployModal(app);
    });
    return;
  }
  panel.innerHTML = `
    <div class="overview-alerts" id="overview-alerts"></div>
    <section id="overview-rejects-by-reason" class="overview-card overview-rejects" hidden>
      <div class="overview-card-heading">
        <div>
          <h2>Recent admission signals</h2>
          <p class="overview-card-description">Capacity and readiness events in the last 10 minutes</p>
        </div>
      </div>
      <ul id="overview-rejects-by-reason-list" class="rejects-list" aria-live="polite"></ul>
      <div class="overview-rejects-next" id="overview-rejects-next" hidden>
        <p id="overview-rejects-guidance"></p>
        <a id="overview-rejects-action" href=""></a>
      </div>
    </section>
    <div class="overview-grid">
      <div id="overview-trends" class="overview-card overview-trends">
        <h2>Resource history</h2><p class="trends-empty">Loading recent metrics…</p>
      </div>
      <section id="overview-activity" class="overview-card overview-recent-activity" aria-label="Recent activity"></section>
      <section class="overview-card overview-replicas">
        <div class="overview-card-heading overview-replicas-heading">
          <div>
            <h2 id="overview-replicas-heading">${processUnit === 'worker' ? 'Workers' : 'Replicas'}</h2>
            <p class="overview-card-description">Live process state and resource use</p>
          </div>
          <span id="overview-replicas-cap" class="overview-replicas-cap"></span>
        </div>
        <details class="overview-memory-help"><summary>About memory measurements</summary><p class="overview-replica-note">PSS estimates each process's share of physical memory, including shared pages. It can be unavailable on remote workers.</p></details>
        <div class="overview-replica-columns" aria-hidden="true">
          <span>Process</span>
          <div><span>Sessions</span><span>CPU</span><span>Memory</span><span>PSS</span></div>
        </div>
        <ul id="overview-replicas-list" class="replicas-list">
          <li class="replicas-empty">Waiting for metrics…</li>
        </ul>
      </section>
      <details class="overview-card overview-autoscale overview-scaling-details">
        <summary>Scaling policy</summary>
        <div class="overview-scaling-content">
          <p class="overview-card-description">Current policy and controller state.</p>
          ${ctx.canManageApp(ctx.state.user, app) ? `<a class="overview-card-link" href="/apps/${app.slug}/settings/scaling" data-nav>Edit scaling settings <span aria-hidden="true">→</span></a>` : ''}
          <dl id="autoscale-summary" class="overview-dl"></dl>
        </div>
      </details>
    </div>
  `;

  syncOverviewSignals(panel, app, envelope, ctx);

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

  refreshTrends(app.slug);
  return createOverviewActivity({panel: panel.querySelector('#overview-activity'), app, api: ctx.api,
    canReadSchedules: app.schedule_capabilities?.can_read ?? ctx.canManageApp(ctx.state.user, app),
    onUnauthorized: ctx.onUnauthorized});
}

function syncOverviewSignals(panel, app, envelope, ctx) {
  const health = document.getElementById('overview-health');
  if (!health || !panel.querySelector('#overview-alerts')) return;
  const state = envelope || {};
  const key = JSON.stringify([app.status, app.last_error, state.fleet_state,
    state.connectivity, state.rejects_by_reason, state.update_status]);
  if (panel.dataset.signalsKey === key) return;
  panel.dataset.signalsKey = key;

  const alerts = panel.querySelector('#overview-alerts');
  alerts.replaceChildren();
  const connectivity = connectivityBanner(document, state);
  const capacity = capacityBanner(document, state);
  if (connectivity) alerts.append(connectivity);
  if (capacity) alerts.append(capacity);

  const grid = panel.querySelector('.overview-grid');
  const previousFleet = grid.querySelector('.overview-fleet-state');
  const nextFleet = makeFleetStateCard(document, state.fleet_state, {
    configurationHref: `/apps/${app.slug}/settings/general`, relativeTime,
  });
  if (previousFleet) previousFleet.remove();
  grid.classList.toggle('has-fleet-state', !!nextFleet);
  if (nextFleet) grid.prepend(nextFleet);

  const section = panel.querySelector('#overview-rejects-by-reason');
  const list = panel.querySelector('#overview-rejects-by-reason-list');
  const nextEl = panel.querySelector('#overview-rejects-next');
  const rejects = formatRejectsByReason(state.rejects_by_reason).filter(row => row.count > 0);
  renderRejectsByReason(section, list, rejects);
  const next = rejectionGuidance(rejects, ctx.canManageApp(ctx.state.user, app));
  nextEl.hidden = !next;
  if (next) {
    panel.querySelector('#overview-rejects-guidance').textContent = next.message;
    const action = panel.querySelector('#overview-rejects-action');
    action.textContent = next.action;
    const settingsSection = rejects.some(row => row.reason === 'pool-saturated') ? 'scaling' : 'resources';
    action.href = next.route === 'fleet' ? '/' : next.route === 'replicas'
      ? '#overview-replicas-list' : next.route === 'configuration' ? `/apps/${app.slug}/settings/${settingsSection}` : `/apps/${app.slug}/${next.route}`;
    if (next.route === 'replicas') action.removeAttribute('data-nav');
    else action.dataset.nav = '';
  }
  renderAppHealth(app, state, null, false, false);
}

function renderAppHealth(app, envelope, metrics, metricsStale, envelopeStale) {
  const box = document.getElementById('overview-health');
  if (!box) return;
  const processUnit = capacityProcessUnit(app, metrics);
  const processes = `${processUnit}s`;
  const status = app.status;
  const signals = formatRejectsByReason(envelope?.rejects_by_reason);
  const rejects = signals.filter(row => row.count > 0 && row.reason !== 'render-deferred');
  const count = rejects.reduce((sum, row) => sum + row.count, 0);
  const deferred = signals.find(row => row.reason === 'render-deferred')?.count || 0;
  const replicas = Array.isArray(metrics?.replicas) ? metrics.replicas : null;
  const ready = replicas?.filter(row => row.status === 'running').length;
  let title = 'App status needs review';
  let detail = 'Check the process state and recent logs.';
  let level = 'watch';
  let action = null;
  if (metricsStale || envelopeStale) {
    title = 'Health data may be out of date';
    detail = 'The latest health check did not complete. The page will check again; use the logs before acting on these values.';
    action = ['View logs', `/apps/${app.slug}/logs`];
  } else if (app.deploying || status === 'deploying') {
    title = 'Deployment in progress';
    detail = status === 'running'
      ? 'The previous release is still serving while the new version is prepared.'
      : 'The new version is being prepared. Follow its progress in deployment history.';
    level = 'progress';
    action = ['View deployment', `/apps/${app.slug}/deployments`];
  } else if (app.deployment_repair_required) {
    const repair = appAttention(app);
    title = repair.label;
    detail = repair.detail;
    level = repair.severity === 'danger' ? 'danger' : 'watch';
    action = ['Review deployments', `/apps/${app.slug}/deployments`];
  } else if (status === 'failed') {
    title = 'App is unavailable';
    detail = 'A data producer or deployment failed. Review the failure before starting the app again.';
    level = 'danger';
    action = ['Review deployments', `/apps/${app.slug}/deployments`];
  } else if (status === 'crashed') {
    title = 'App is unavailable';
    detail = 'Review the startup error and logs before restarting.';
    level = 'danger';
    action = ['View logs', `/apps/${app.slug}/logs`];
  } else if (status === 'degraded') {
    title = 'App is degraded';
    detail = `Some capacity may be unavailable. Inspect the ${processes} and recent logs.`;
    level = 'danger';
    action = [`Inspect ${processes}`, '#overview-replicas-list'];
  } else if (envelope?.connectivity?.serving_without_ws) {
    title = 'App is serving, but interactions may fail';
    detail = 'Realtime connections have not succeeded. Check the reverse proxy warning below.';
    level = 'danger';
    action = ['Review warning', '#overview-alerts'];
  } else if (count > 0) {
    title = 'App has admission issues';
    detail = `${count} ${count === 1 ? 'event' : 'events'} in the last 10 minutes. Review the cause and next step below.`;
    action = ['Review signals', '#overview-rejects-by-reason'];
  } else if (deferred > 0) {
    title = 'App is serving with waits';
    detail = `${deferred} render-capacity wait ${deferred === 1 ? 'signal' : 'signals'} in the last 10 minutes; these do not mean failed sessions.`;
    action = ['Review signals', '#overview-rejects-by-reason'];
  } else if (status === 'running' && replicas && replicas.length > 0 && ready < replicas.length) {
    title = `Some ${processes} are not serving`;
    detail = `${ready} of ${replicas.length} ${processes} running. Inspect the process list below.`;
    action = [`Inspect ${processes}`, '#overview-replicas-list'];
  } else if (status === 'running' && app.last_deployment_status === 'failed') {
    title = 'Latest deployment failed';
    detail = 'The previous release is still serving.';
    action = ['Review deployment', `/apps/${app.slug}/deployments`];
  } else if (status === 'running') {
    title = 'App is serving normally';
    detail = replicas?.length ? `${ready} ${ready === 1 ? processUnit : processes} running; no recent admission issue reported.`
      : `No recent admission issue reported. Waiting for live ${processUnit} metrics.`;
    level = 'healthy';
  } else if (status === 'idle' || status === 'hibernated') {
    title = 'Ready to wake';
    detail = 'Open the app to start it. The first visit may take a moment.';
    level = 'quiet';
  } else if (status === 'deploying' || status === 'starting' || status === 'waking') {
    title = 'App is starting';
    level = 'progress';
    detail = `Watch ${processUnit} readiness and startup logs.`;
    action = ['View logs', `/apps/${app.slug}/logs`];
  } else if (status === 'stopped' || status === 'suspended') {
    title = 'App is stopped';
    level = 'quiet';
    detail = 'Start the app when it should be available to visitors.';
  }
  box.dataset.level = level;
  box.hidden = awaitingFirstDeploy(app) || firstDeployFailed(app);
  const error = box.querySelector('#overview-health-error');
  if (error) {
    const message = ['crashed', 'failed'].includes(status) && !app.deploying ? (app.last_error || '').trim() : '';
    error.hidden = !message;
    setText(error, message);
  }
  // Logs is always one tap away in the header. Keep this slot for a specific
  // next step, and route to Overview when the target panel is not visible.
  if (action?.[1] === `/apps/${app.slug}/logs`) action = null;
  if (action?.[1] === `/apps/${app.slug}/deployments`
      && (document.getElementById('app-detail-view')?.dataset.appPage === 'deployments'
        || document.getElementById('app-detail-release')?.hidden === false)) action = null;
  if (action?.[1].startsWith('#') && document.getElementById('detail-overview-panel')?.hidden) {
    action = [action[0], `/apps/${app.slug}`];
  }
  setText(box.querySelector('#overview-health-title'), title);
  setText(box.querySelector('#overview-health-detail'), detail);
  setText(box.querySelector('#overview-health-freshness'), metricsStale
    ? 'Live metrics unavailable' : envelopeStale ? 'Health check unavailable'
      : metrics ? 'Live metrics checked recently' : 'Waiting for live metrics');
  const actionEl = box.querySelector('#overview-health-action');
  actionEl.hidden = !action;
  if (action) {
    setText(actionEl, action[0]);
    actionEl.href = action[1];
    if (action[1].startsWith('#')) actionEl.removeAttribute('data-nav');
    else actionEl.dataset.nav = '';
  }
}

// History is a separate request from live metrics. Preserve the last chart on
// refresh failure, but say plainly that it is no longer current.
function refreshTrends(slug, initiator = null) {
  const container = document.getElementById('overview-trends');
  if (!container || container.closest('.settings-tab-panel')?.hidden) return;
  // Let an explicit retry finish before a background check replaces its state.
  if (container.dataset.historyRetryPending === 'true') return;
  const restoreFocus = initiator === document.activeElement;
  let announcement = container.querySelector('.trends-refresh-status');
  if (initiator) {
    container.dataset.historyRetryPending = 'true';
    container.setAttribute('aria-busy', 'true');
    initiator.disabled = true;
    initiator.textContent = 'Checking…';
    if (!announcement) {
      announcement = document.createElement('p');
      announcement.className = 'trends-refresh-status sr-only';
      announcement.setAttribute('role', 'status');
      container.appendChild(announcement);
    }
    announcement.textContent = 'Checking metrics history…';
  }
  const request = Number(container.dataset.historyRequest || 0) + 1;
  container.dataset.historyRequest = String(request);
  const current = () => Number(container.dataset.historyRequest) === request
    && container.isConnected && document.getElementById('overview-trends') === container
    && !container.closest('.settings-tab-panel')?.hidden;
  const focus = target => {
    // Moving to another control while loading revokes focus restoration.
    if (restoreFocus && (document.activeElement === initiator || document.activeElement === document.body)) {
      target?.focus({ preventScroll: true });
    }
  };
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 8000);
  return Promise.resolve().then(() => fetch(`/api/apps/${encodeURIComponent(slug)}/metrics/history`, { credentials: 'include', signal: controller.signal }))
    .then((r) => {
      if (!r.ok) throw new Error(`History request failed (${r.status})`);
      return r.json();
    })
    .then((body) => {
      if (!current()) return;
      const series = body.series || {};
      const timestamps = Array.isArray(series.ts) ? series.ts : [];
      const serverNow = Date.parse(body.generated_at);
      const cutoff = Math.floor((Number.isFinite(serverNow) ? serverNow : Date.now()) / 1000) - 15 * 60;
      const firstRecent = timestamps.findIndex(ts => ts >= cutoff);
      const first = firstRecent < 0 ? timestamps.length : firstRecent;
      const recent = { ...body, window_seconds: Math.min(Number(body.window_seconds) || 0, 900),
        series: Object.fromEntries(Object.entries(series).map(([key, values]) =>
          [key, Array.isArray(values) ? values.slice(first) : values])) };
      const card = renderTrendsCard(document, recent, Number(container.dataset.cpuCapacityCores));
      if (!card) {
        container.hidden = false;
        const heading = document.createElement('h2'); heading.textContent = 'Resource history';
        const note = document.createElement('p'); note.className = 'trends-empty'; note.textContent = 'Metrics history is disabled on this server.'; note.tabIndex = -1;
        container.replaceChildren(heading, note);
        if (announcement) { container.appendChild(announcement); announcement.textContent = 'Metrics history is disabled.'; focus(note); }
        return true;
      }
      const previous = container.querySelector('.trends-card');
      const focusedLatest = document.activeElement === previous?.querySelector('.trends-latest');
      const focusedChart = focusedLatest ? document.activeElement.dataset.returnChart
        : previous?.contains(document.activeElement)
          ? document.activeElement.closest('[data-history-chart]')?.dataset.historyChart : null;
      const selectedTime = previous?.dataset.inspectTimestamp;
      const selectedIndex = previous?.dataset.inspectIndex;
      const inspecting = previous?.dataset.inspectActive === 'true';
      container.hidden = false;
      container.replaceChildren(card);
      if (focusedChart || focusedLatest) {
        const chart = [...card.querySelectorAll('[data-history-chart]')].find(el => el.dataset.historyChart === focusedChart);
        chart?.focus({preventScroll: true});
        // Restore the selected instant before focusing a contextual control.
        if (inspecting) {
          if (selectedTime && Number.isFinite(Number(selectedTime))) card.inspectTime?.(Number(selectedTime));
          else if (selectedIndex != null) card.inspectIndex?.(Number(selectedIndex));
        }
        if (focusedLatest) card.querySelector('.trends-latest')?.focus({preventScroll: true});
        if (!inspecting) card.finishInspection?.();
      }
      if (initiator) {
        container.appendChild(announcement);
        announcement.textContent = 'Metrics history updated.';
        const heading = card.querySelector('h2');
        heading.tabIndex = -1;
        focus(heading);
      }
      return true;
    })
    .catch(() => {
      if (!current()) return;
      container.querySelector(':scope > .trends-empty')?.remove();
      let state = container.querySelector('.trends-load-state');
      if (!state) {
        state = document.createElement('div');
        state.className = 'trends-load-state';
        state.setAttribute('role', 'status');
        container.append(state);
      }
      state.replaceChildren();
      const message = document.createElement('p');
      message.textContent = container.querySelector('.trends-card')
        ? 'Chart may be out of date.' : 'Metrics history is unavailable.';
      const retry = document.createElement('button');
      retry.type = 'button';
      retry.textContent = 'Retry';
      retry.addEventListener('click', () => refreshTrends(slug, retry));
      state.append(message, retry);
      if (announcement) announcement.textContent = '';
      if (initiator) focus(retry);
      return false;
    })
    .finally(() => {
      clearTimeout(timeout);
      if (Number(container.dataset.historyRequest) !== request) return;
      delete container.dataset.historyRetryPending;
      container.removeAttribute('aria-busy');
      // Navigation can leave the cached chart in place. Keep its retry usable
      // when the operator returns, without moving focus into a hidden panel.
      if (initiator?.isConnected) {
        initiator.disabled = false;
        initiator.textContent = 'Retry';
      }
    });
}

function seedReplicasFromStatus(app, replicasStatus) {
  const listEl = document.getElementById('overview-replicas-list');
  const capEl = document.getElementById('overview-replicas-cap');
  if (!listEl || !capEl) return;
  const cap = Number(app.max_sessions_per_replica || 0);
  if (cap > 0) capEl.textContent = `(cap ${cap} sessions/${capacityProcessUnit(app)})`;
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
    const { cpuText: cpuInit, ramText: ramInit, physicalText: physicalInit, note } = metricsText({
      metrics_available: r.metrics_available,
    });
    const cpuDisplay = status === 'running' ? cpuInit : '—';
    const ramDisplay = status === 'running' ? ramInit : '—';
    const physicalDisplay = status === 'running' ? physicalInit : '—';
    // reason explains a degraded state (e.g. "worker unavailable" for a lost
    // replica); empty for healthy replicas.
    const reason = reasonLabel(r);
    // Build the li via innerHTML for fixed strings, but set backend + reason via
    // textContent to avoid XSS from operator-controlled values.
    li.innerHTML = `
      <div class="replica-identity">
        <span class="replica-index">#${r.index}</span>
        <span class="badge badge-${status}"></span>
        <span class="replica-backend" title="Backend/tier"></span>
      </div>
      <span class="replica-reason"></span>
      <div class="replica-measures">
        <span class="replica-measure replica-sessions"><span class="replica-measure-label">Sessions</span><span class="replica-measure-value">—</span></span>
        <span class="replica-measure replica-cpu"><span class="replica-measure-label">CPU</span><span class="replica-measure-value">${cpuDisplay}</span></span>
        <span class="replica-measure replica-ram"${note ? ` title="${note}"` : ''}><span class="replica-measure-label">Memory</span><span class="replica-measure-value">${ramDisplay}</span></span>
        <span class="replica-measure replica-physical"><span class="replica-measure-label">PSS</span><span class="replica-measure-value">${physicalDisplay}</span></span>
      </div>
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
}

function renderSchedules(panel, app, ctx, selectedScheduleID, envelope) {
  applyScheduleTimezone(document, envelope);
  ctx.setSettingsSlug(app.slug);
  const declared = app.schedule_capabilities || {};
  const canManage = typeof declared.can_manage === 'boolean'
    ? declared.can_manage
    : ctx.canManageApp(ctx.state.user, app);
  return ctx.loadSchedules(app.slug, {
    canManage,
    canReadLogs: declared.can_read_logs === true || canManage,
    canCancel: declared.can_cancel === true || canManage,
    selectedScheduleID: selectedScheduleID || null,
		latestActivations: app.latest_schedule_activations || [],
  });
}

function renderData(panel, app, ctx, envelope) {
  return createDataViewer({panel, app, api: ctx.api,
    canManage: ctx.canManageApp(ctx.state.user, app),
    onUnauthorized: ctx.onUnauthorized, runtimeMode: envelope.runtime_mode});
}

function renderAccess(panel, app, ctx) {
  ctx.setSettingsSlug(app.slug);
  ctx.populateAccessPanel(app);
  ctx.refreshMemberList();
  ctx.refreshGroupAccessList();
}

// Refreshes only fleet-derived presentation after a successful dashboard
// mutation. This preserves the active form, focus, and unrelated unsaved edits
// while replacing the mount-time convergence envelope with a freshly proven
// server result.
export function refreshFleetSurfaces(doc, app, state) {
  const fleetSlot = doc.getElementById('app-detail-fleet-badge');
  if (fleetSlot) renderFleetBadges(fleetSlot, app, state);

  const overviewGrid = doc.querySelector('#detail-overview-panel .overview-grid');
  if (overviewGrid) {
    overviewGrid.querySelector(':scope > .overview-fleet-state')?.remove();
    const card = makeFleetStateCard(doc, state, {
      configurationHref: `/apps/${app.slug}/settings/general`,
      relativeTime,
    });
    overviewGrid.classList.toggle('has-fleet-state', !!card);
    if (card) overviewGrid.prepend(card);
  }

}

// Keep the diagnostic viewer lifecycle scoped to the current app panel.
function renderTraces(panel, app, ctx) {
  return createTracesViewer({ panel, app, api: ctx.api, onUnauthorized: ctx.onUnauthorized });
}
