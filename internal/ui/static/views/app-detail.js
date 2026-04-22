// App detail view. Mounts #app-detail-view, populates the header, and shows
// the requested tab. Tabs other than Overview are added in later tasks; for
// now Overview is the only one with a renderer and other tabs show "Coming
// soon" placeholders.
const TAB_ROUTES = ['overview', 'logs', 'deployments', 'configuration', 'data', 'access'];

export function mountAppDetail(ctx) {
  const view = document.getElementById('app-detail-view');
  const panels = {
    overview:      document.getElementById('detail-overview-panel'),
    logs:          document.getElementById('detail-logs-panel'),
    deployments:   document.getElementById('detail-deployments-panel'),
    configuration: document.getElementById('detail-configuration-panel'),
    data:          document.getElementById('detail-data-panel'),
    access:        document.getElementById('detail-access-panel'),
  };
  const tabEls = Object.fromEntries(
    TAB_ROUTES.map(t => [t, document.getElementById(`detail-tab-${t}`)]),
  );

  return async function mount(params) {
    const { slug } = params;
    const tab = TAB_ROUTES.includes(params.tab) ? params.tab : 'overview';

    // Canonicalize /apps/<slug>/overview → /apps/<slug>.
    if (params.tab === 'overview') {
      history.replaceState({}, '', `/apps/${slug}`);
    }

    // Populate tab hrefs so middle-click / cmd-click open real URLs.
    for (const t of TAB_ROUTES) {
      tabEls[t].setAttribute('href', t === 'overview' ? `/apps/${slug}` : `/apps/${slug}/${t}`);
      tabEls[t].classList.toggle('active', t === tab);
      tabEls[t].setAttribute('aria-selected', String(t === tab));
    }

    const resp = await ctx.api(`/api/apps/${slug}`);
    if (resp.status === 404) { ctx.navigate('/'); return {}; }
    if (resp.status === 401) { ctx.onUnauthorized(); return {}; }
    if (!resp.ok) { return {}; }
    const app = await resp.json();

    document.getElementById('app-detail-heading').textContent = app.name;
    document.getElementById('app-detail-slug').textContent = '/' + app.slug;
    document.getElementById('app-detail-deploy-count').textContent = `${app.deploy_count} deploys`;
    const statusEl = document.getElementById('app-detail-status');
    statusEl.textContent = app.status;
    statusEl.className = 'badge badge-' + app.status;
    const openLink = document.getElementById('app-detail-open');
    openLink.href = `/app/${app.slug}/`;
    openLink.hidden = app.status !== 'running';

    // Show the selected panel, hide the rest.
    for (const t of TAB_ROUTES) {
      panels[t].hidden = t !== tab;
    }

    // Render Overview (other tabs rendered in later tasks).
    if (tab === 'overview') {
      renderOverview(panels.overview, app, ctx);
    }

    view.hidden = false;
    ctx.updateActiveNav(location.pathname);
    ctx.metrics.setTargets([app.slug]);

    return {
      title: `${app.name} · ShinyHub`,
      unmount() {
        view.hidden = true;
        ctx.metrics.setTargets([]);
      },
    };
  };
}

function renderOverview(panel, app, ctx) {
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
  `;
}
