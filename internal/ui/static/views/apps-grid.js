// Apps grid view. Exports mountAppsGrid which renders the apps list into the
// existing DOM (no new DOM creation — uses #apps-view as mounted today).
//
// ctx: { state, metrics, api, navigate, onUnauthorized, canManageApp, renderGridVerbatim }
//   — passed from app.js so the view has no direct module-level state.
export function mountAppsGrid(ctx) {
  const appsView = document.getElementById('apps-view');
  const appGrid = document.getElementById('app-grid');
  const emptyState = document.getElementById('empty-state');

  appsView.hidden = false;

  async function load() {
    let resp;
    try {
      resp = await ctx.api('/api/apps');
    } catch {
      return;
    }
    if (resp.status === 401) { ctx.onUnauthorized(); return; }
    if (!resp.ok) { return; }
    const apps = (await resp.json()) || [];
    ctx.state.apps = apps;
    ctx.renderGridVerbatim(apps, appGrid, emptyState);
    // Grid polls every app so status/metrics line stays live.
    ctx.metrics.setTargets(apps.map(a => a.slug));
  }

  load();

  return {
    title: 'ShinyHub',
    unmount() {
      appsView.hidden = true;
      ctx.metrics.setTargets([]);
    },
  };
}
