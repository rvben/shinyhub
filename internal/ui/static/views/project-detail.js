// Project detail page. The project and app APIs are intentionally fetched
// together: project metadata is access-scoped by the server, while the app
// index supplies the same operational card data used on the Apps page.

function listItems(body) {
  if (Array.isArray(body)) return body;
  return body && Array.isArray(body.items) ? body.items : [];
}

function projectName(project) {
  const name = project && typeof project.name === 'string' ? project.name.trim() : '';
  return name || (project && project.slug) || 'Project';
}

export function projectDetailModel(project, apps) {
  if (!project || !project.slug) return null;
  const slug = String(project.slug);
  const members = (apps || []).filter((app) =>
    app && String(app.project_slug || '').trim() === slug);
  return {
    slug,
    name: projectName(project),
    description: String(project.description || '').trim(),
    iconEmoji: String(project.icon_emoji || '').trim(),
    apps: members,
    appCount: members.length,
    countLabel: `${members.length} ${members.length === 1 ? 'app' : 'apps'}`,
  };
}

function renderProjectIcon(doc, host, emoji) {
  host.replaceChildren();
  if (emoji) {
    host.classList.add('project-detail-avatar--emoji');
    host.textContent = emoji;
    return;
  }
  host.classList.remove('project-detail-avatar--emoji');
  const svg = doc.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '1.7');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('aria-hidden', 'true');
  const path = doc.createElementNS('http://www.w3.org/2000/svg', 'path');
  path.setAttribute('d', 'M3.5 7.5h6l2-2h9v13h-17z');
  svg.appendChild(path);
  host.appendChild(svg);
}

export function mountProjectDetail(ctx) {
  return async function mount(params) {
    const view = document.getElementById('project-detail-view');
    const heading = document.getElementById('project-detail-heading');
    const breadcrumb = document.getElementById('project-detail-breadcrumb');
    const slugEl = document.getElementById('project-detail-slug');
    const description = document.getElementById('project-detail-description');
    const icon = document.getElementById('project-detail-icon');
    const count = document.getElementById('project-detail-count');
    const grid = document.getElementById('project-detail-grid');
    const loading = document.getElementById('project-detail-loading');
    const error = document.getElementById('project-detail-error');
    const empty = document.getElementById('project-detail-empty');
    const edit = document.getElementById('project-detail-edit');
    const add = document.getElementById('project-detail-add');
    const emptyAdd = document.getElementById('project-detail-empty-add');
    const refresh = document.getElementById('project-detail-refresh');
    const requestedSlug = String(params && params.slug || '');
    let disposed = false;
    let currentModel = null;
    let loadVersion = 0;

    view.hidden = false;
    ctx.updateActiveNav(location.pathname);
    const role = ctx.state.user && ctx.state.user.role;
    const canEdit = role === 'admin' || role === 'operator';
    const canCreate = Boolean(ctx.state.canCreateApps);
    // Do not expose actions for a project until access-scoped project data has
    // resolved. This also keeps them unavailable after an initial load error.
    edit.hidden = true;
    add.hidden = true;
    emptyAdd.hidden = true;

    const openEditor = () => {
      if (!currentModel) return;
      ctx.openProjectEditModal({
        project: currentModel.slug,
        name: currentModel.name,
        iconEmoji: currentModel.iconEmoji,
      });
    };
    const openCreator = () => ctx.openNewAppModal(requestedSlug);
    edit.addEventListener('click', openEditor);
    add.addEventListener('click', openCreator);
    emptyAdd.addEventListener('click', openCreator);

    async function load({ announce = false } = {}) {
      const version = ++loadVersion;
      view.setAttribute('aria-busy', 'true');
      loading.hidden = false;
      loading.textContent = announce ? 'Refreshing project…' : 'Loading project…';
      error.hidden = true;
      error.textContent = '';
      empty.hidden = true;
      if (!currentModel) grid.replaceChildren();
      refresh.disabled = true;

      let projectResponse;
      let appsResponse;
      try {
        [projectResponse, appsResponse] = await Promise.all([
          ctx.api('/api/projects'),
          ctx.api('/api/apps'),
        ]);
      } catch {
        if (!disposed && version === loadVersion) showError('Could not load this project. Check your connection and try again.');
        return;
      } finally {
        if (!disposed && version === loadVersion) refresh.disabled = false;
      }
      if (disposed || version !== loadVersion) return;
      if (projectResponse.status === 401 || appsResponse.status === 401) {
        ctx.onUnauthorized();
        return;
      }
      if (!projectResponse.ok || !appsResponse.ok) {
        showError('Could not load this project. Check your connection and try again.');
        return;
      }

      let projectBody;
      let appsBody;
      try {
        [projectBody, appsBody] = await Promise.all([
          projectResponse.json(),
          appsResponse.json(),
        ]);
      } catch {
        showError('Could not read the project response. Refresh to try again.');
        return;
      }
      if (disposed || version !== loadVersion) return;
      const project = listItems(projectBody).find((item) => item && item.slug === requestedSlug);
      const model = projectDetailModel(project, listItems(appsBody));
      loading.hidden = true;
      view.setAttribute('aria-busy', 'false');
      if (!model) {
        currentModel = null;
        grid.replaceChildren();
        heading.textContent = 'Project not found';
        breadcrumb.textContent = 'Not found';
        slugEl.textContent = requestedSlug ? `/${requestedSlug}` : '';
        description.textContent = 'This project does not exist, or you no longer have access to it.';
        description.hidden = false;
        count.textContent = '';
        icon.hidden = true;
        edit.hidden = true;
        add.hidden = true;
        emptyAdd.hidden = true;
        ctx.metrics.setTargets([]);
        return;
      }

      currentModel = model;
      heading.textContent = model.name;
      breadcrumb.textContent = model.name;
      slugEl.textContent = `/${model.slug}`;
      description.textContent = model.description || 'No project description yet.';
      description.classList.toggle('is-placeholder', !model.description);
      description.hidden = false;
      count.textContent = model.countLabel;
      icon.hidden = false;
      edit.hidden = !canEdit;
      add.hidden = !canCreate;
      emptyAdd.hidden = !canCreate;
      renderProjectIcon(document, icon, model.iconEmoji);
      ctx.state.apps = listItems(appsBody);
      if (typeof ctx.syncSidebar === 'function') ctx.syncSidebar();
      ctx.metrics.setTargets(model.apps.map((app) => app.slug));

      if (model.apps.length === 0) {
        grid.replaceChildren();
        empty.hidden = false;
        return;
      }
      ctx.renderGridVerbatim(
        [{ project: '', name: '', iconEmoji: '', apps: model.apps }],
        grid,
        empty,
      );
    }

    function showError(message) {
      loading.hidden = true;
      view.setAttribute('aria-busy', 'false');
      error.textContent = message;
      error.hidden = false;
      if (currentModel) {
        empty.hidden = currentModel.apps.length !== 0;
        ctx.metrics.setTargets(currentModel.apps.map((app) => app.slug));
      } else {
        ctx.metrics.setTargets([]);
      }
    }

    const refreshClick = () => load({ announce: true });
    const mutation = (event) => {
      const path = event && event.detail && event.detail.path;
      if (typeof path !== 'string') return;
      if (path.startsWith('/api/projects/')) load({ announce: true });
    };
    const appsReloaded = () => load({ announce: true });
    refresh.addEventListener('click', refreshClick);
    document.addEventListener('shinyhub:mutated', mutation);
    document.addEventListener('shinyhub:apps-reloaded', appsReloaded);
    await load();

    return {
      title: heading.textContent || 'Project',
      unmount() {
        disposed = true;
        view.hidden = true;
        edit.removeEventListener('click', openEditor);
        add.removeEventListener('click', openCreator);
        emptyAdd.removeEventListener('click', openCreator);
        refresh.removeEventListener('click', refreshClick);
        document.removeEventListener('shinyhub:mutated', mutation);
        document.removeEventListener('shinyhub:apps-reloaded', appsReloaded);
        ctx.metrics.setTargets([]);
      },
    };
  };
}
