// Apps grid view. Exports mountAppsGrid which renders the apps list into the
// existing DOM (no new DOM creation — uses #apps-view as mounted today).
//
// ctx: { state, metrics, api, navigate, onUnauthorized, canManageApp,
//        renderGridVerbatim, applyGridFilters } — passed from app.js so the
//        view has no direct module-level state.
//
// IMPORTANT: mount is async and only resolves after the initial /api/apps
// load has populated state.apps. Callers that depend on state.apps being
// ready (e.g. handleDeployHash) await router.start() / router.navigate()
// to get that guarantee. See contract_test.go and Codex review #1
// (deploy-hash race) for the regression this prevents.
export async function mountAppsGrid(ctx) {
  const appsView = document.getElementById('apps-view');
  const appGrid = document.getElementById('app-grid');
  const emptyState = document.getElementById('empty-state');

  appsView.hidden = false;

  let resp;
  try {
    resp = await ctx.api('/api/apps');
  } catch {
    return viewObject();
  }
  if (resp.status === 401) { ctx.onUnauthorized(); return viewObject(); }
  if (!resp.ok) { return viewObject(); }
  const apps = (await resp.json()) || [];
  ctx.state.apps = apps;
  // Use applyGridFilters when available so persisted search/sort apply on
  // first mount; otherwise fall back to the verbatim renderer.
  if (typeof ctx.applyGridFilters === 'function') {
    ctx.applyGridFilters();
  } else {
    ctx.renderGridVerbatim(apps, appGrid, emptyState);
  }
  // Grid polls every app so status/metrics line stays live.
  ctx.metrics.setTargets(apps.map(a => a.slug));

  return viewObject();

  function viewObject() {
    return {
      title: 'ShinyHub',
      unmount() {
        appsView.hidden = true;
        ctx.metrics.setTargets([]);
      },
    };
  }
}
