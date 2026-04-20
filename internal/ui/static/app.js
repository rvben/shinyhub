function setHidden(element, hidden) {
  element.hidden = hidden;
}

function setError(element, message) {
  element.textContent = message || '';
  element.hidden = !message;
}

function canManageApp(user, app) {
  if (!user || !app) {
    return false;
  }
  return user.role === 'admin' || user.role === 'operator' || user.id === app.owner_id;
}

function relativeTime(date) {
  const diff = Math.floor((Date.now() - date.getTime()) / 1000);
  if (diff < 60)    return `${diff}s ago`;
  if (diff < 3600)  return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

document.addEventListener('DOMContentLoaded', () => {
  const state = {
    user: null,
    apps: [],
    metricsInterval: null,
    auditPage: 0,
    auditHasMore: false,
    canCreateApps: false,
  };

  const loginView = document.getElementById('login-view');
  const appsView = document.getElementById('apps-view');
  const loginForm = document.getElementById('login-form');
  const usernameInput = document.getElementById('login-username');
  const passwordInput = document.getElementById('login-password');
  const loginError = document.getElementById('login-error');
  const appError = document.getElementById('app-error');
  const refreshButton = document.getElementById('refresh-button');
  const logoutButton = document.getElementById('logout-button');
  const sessionUser = document.getElementById('session-user');
  const appGrid = document.getElementById('app-grid');
  const emptyState = document.getElementById('empty-state');
  const logPane = document.getElementById('log-pane');
  const logPaneTitle = document.getElementById('log-pane-title');
  const logPaneBody = document.getElementById('log-pane-body');
  const logPaneClose = document.getElementById('log-pane-close');
  const auditView   = document.getElementById('audit-view');
  const auditError  = document.getElementById('audit-error');
  const auditBody   = document.getElementById('audit-body');
  const auditPrev   = document.getElementById('audit-prev');
  const auditNext   = document.getElementById('audit-next');
  const auditRange  = document.getElementById('audit-range');
  const tabBar      = document.getElementById('tab-bar');
  const tabApps     = document.getElementById('tab-apps');
  const tabAudit    = document.getElementById('tab-audit');
  const historyModal    = document.getElementById('history-modal');
  const historyAppName  = document.getElementById('history-app-name');
  const historyList     = document.getElementById('history-list');

  const newAppButton       = document.getElementById('new-app-button');
  const newAppModal        = document.getElementById('new-app-modal');
  const newAppClose        = document.getElementById('new-app-close');
  const newAppCancel       = document.getElementById('new-app-cancel');
  const newAppForm         = document.getElementById('new-app-form');
  const newAppSlug         = document.getElementById('new-app-slug');
  const newAppName         = document.getElementById('new-app-name');
  const newAppProject      = document.getElementById('new-app-project');
  const newAppError        = document.getElementById('new-app-error');
  const newAppHandoff      = document.getElementById('new-app-handoff');
  const newAppSnippet      = document.getElementById('new-app-snippet');
  const newAppSnippetLabel = document.getElementById('new-app-snippet-label');
  const newAppSnippetCopy  = document.getElementById('new-app-snippet-copy');
  const newAppDone         = document.getElementById('new-app-done');
  const newAppSubmit       = document.getElementById('new-app-submit');

  const deployModal        = document.getElementById('deploy-modal');
  const deployModalClose   = document.getElementById('deploy-modal-close');
  const deployAppName      = document.getElementById('deploy-app-name');
  const deployDropzone     = document.getElementById('deploy-dropzone');
  const deployPick         = document.getElementById('deploy-pick');
  const deployFileInput    = document.getElementById('deploy-file');
  const deploySummary      = document.getElementById('deploy-summary');
  const deploySourceName   = document.getElementById('deploy-source-name');
  const deployFileCount    = document.getElementById('deploy-file-count');
  const deployBundleSize   = document.getElementById('deploy-bundle-size');
  const deployIgnoredRow   = document.getElementById('deploy-ignored-row');
  const deployIgnoredList  = document.getElementById('deploy-ignored-list');
  const deployProgressWrap = document.getElementById('deploy-progress-wrap');
  const deployProgressBar  = document.getElementById('deploy-progress-bar');
  const deployProgressText = document.getElementById('deploy-progress-text');
  const deployError        = document.getElementById('deploy-error');
  const deployCancel       = document.getElementById('deploy-cancel');
  const deploySubmit       = document.getElementById('deploy-submit');

  const SLUG_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;
  const DEPLOY_MAX_BYTES = 128 * 1024 * 1024;
  const DEPLOY_IGNORE_DIRS = new Set(['.git', '.venv', '__pycache__', 'node_modules', '.renv', '.Rproj.user']);

  let activeEventSource = null;
  let deployState = null; // { slug, appName, blob, fileCount, ignored: Set<string>, xhr }
  let slugEdited = false;

  // Derive a slug from a display name: strip diacritics, lowercase, replace
  // runs of non-alphanumerics with dashes, trim dashes, cap at 63 chars.
  function slugify(name) {
    return name
      .normalize('NFKD')
      .replace(/[\u0300-\u036f]/g, '')
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 63);
  }

  function readCookie(name) {
    const prefix = name + '=';
    for (const raw of document.cookie.split(';')) {
      const c = raw.trim();
      if (c.startsWith(prefix)) return decodeURIComponent(c.slice(prefix.length));
    }
    return '';
  }

  async function api(path, options = {}) {
    const init = {
      credentials: 'same-origin',
      headers: {},
      ...options,
    };
    init.headers = {...init.headers};
    if (init.body && !init.headers['Content-Type']) {
      init.headers['Content-Type'] = 'application/json';
    }
    const method = (init.method || 'GET').toUpperCase();
    if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
      const token = readCookie('csrf_token');
      if (token) init.headers['X-CSRF-Token'] = token;
    }
    return fetch(path, init);
  }

  function renderApps() {
    appGrid.textContent = '';
    emptyState.hidden = state.apps.length !== 0;

    for (const app of state.apps) {
      const card = document.createElement('div');
      card.className = 'app-card';

      const header = document.createElement('div');
      header.className = 'app-header';

      const name = document.createElement('strong');
      name.textContent = app.name;
      header.appendChild(name);

      const neverDeployed = (app.deploy_count || 0) === 0;

      const badge = document.createElement('span');
      if (neverDeployed) {
        badge.className = 'badge badge-new';
        badge.textContent = 'Awaiting deploy';
      } else {
        badge.className = `badge badge-${app.status}`;
        badge.textContent = app.status;
      }
      header.appendChild(badge);

      const meta = document.createElement('div');
      meta.className = 'app-meta';

      const slug = document.createElement('span');
      slug.textContent = `/${app.slug}`;
      meta.appendChild(slug);

      const deployCount = document.createElement('span');
      deployCount.textContent = `${app.deploy_count} deploys`;
      meta.appendChild(deployCount);

      const actions = document.createElement('div');
      actions.className = 'app-actions';

      if (!neverDeployed) {
        const openLink = document.createElement('a');
        openLink.href = `/app/${app.slug}/`;
        openLink.target = '_blank';
        openLink.rel = 'noopener noreferrer';
        openLink.textContent = 'Open';
        openLink.setAttribute('aria-label', `Open ${app.name}`);
        actions.appendChild(openLink);
      }

      if (canManageApp(state.user, app)) {
        const deployButton = document.createElement('button');
        deployButton.type = 'button';
        deployButton.textContent = neverDeployed ? 'Deploy first bundle' : 'Deploy';
        if (neverDeployed) deployButton.className = 'btn-primary';
        deployButton.setAttribute('aria-label', `Deploy new bundle to ${app.name}`);
        deployButton.addEventListener('click', () => openDeployModal(app));
        actions.appendChild(deployButton);

        if (!neverDeployed) {
          const restartButton = document.createElement('button');
          restartButton.type = 'button';
          restartButton.textContent = 'Restart';
          restartButton.setAttribute('aria-label', `Restart ${app.name}`);
          restartButton.addEventListener('click', () => restart(app.slug));
          actions.appendChild(restartButton);

          const historyButton = document.createElement('button');
          historyButton.type = 'button';
          historyButton.textContent = 'History';
          historyButton.setAttribute('aria-label', `Deployment history for ${app.name}`);
          historyButton.addEventListener('click', () => openHistoryModal(app.slug));
          actions.appendChild(historyButton);

          const logsButton = document.createElement('button');
          logsButton.type = 'button';
          logsButton.textContent = 'Logs';
          logsButton.setAttribute('aria-label', `View logs for ${app.name}`);
          logsButton.addEventListener('click', () => openLogs(app.slug));
          actions.appendChild(logsButton);
        }

        const accessButton = document.createElement('button');
        accessButton.className = 'btn btn-access';
        accessButton.textContent = 'Access';
        accessButton.setAttribute('aria-label', `Manage access for ${app.name}`);
        accessButton.addEventListener('click', () => openAccessModal(app));
        actions.appendChild(accessButton);
      }

      const metricsLine = document.createElement('div');
      metricsLine.className = 'app-metrics';
      metricsLine.dataset.slug = app.slug;

      card.appendChild(header);
      card.appendChild(meta);
      card.appendChild(metricsLine);
      card.appendChild(actions);
      appGrid.appendChild(card);

      if (app.status === 'running') {
        fetchMetrics(app.slug);
      }
    }
  }

  function showView(name) {
    appsView.hidden  = name !== 'apps';
    auditView.hidden = name !== 'audit';
    tabApps.classList.toggle('tab-active',  name === 'apps');
    tabAudit.classList.toggle('tab-active', name === 'audit');
    if (name === 'audit') loadAuditEvents(0);
  }

  function showLoggedOut() {
    closeLogs();
    clearInterval(state.metricsInterval);
    state.metricsInterval = null;
    state.user = null;
    state.apps = [];
    state.auditPage = 0;
    state.auditHasMore = false;
    state.canCreateApps = false;
    newAppButton.hidden = true;
    sessionUser.textContent = '';
    setHidden(logoutButton, true);
    setHidden(loginView, false);
    setHidden(appsView, true);
    setHidden(auditView, true);
    tabBar.hidden = true;
    setError(loginError, '');
    setError(appError, '');
    renderApps();
  }

  function showLoggedIn(payload) {
    state.user = payload.user;
    state.canCreateApps = !!payload.can_create_apps;
    sessionUser.textContent = payload.user.username;
    setHidden(logoutButton, false);
    setHidden(loginView, true);
    tabBar.hidden = false;
    tabAudit.hidden = payload.user.role !== 'admin';
    newAppButton.hidden = !state.canCreateApps;
    showView('apps');
  }

  async function handleUnauthorized() {
    showLoggedOut();
    setError(loginError, '');
  }

  async function loadApps() {
    setError(appError, '');

    let response;
    try {
      response = await api('/api/apps');
    } catch {
      setError(appError, 'Network error');
      return;
    }

    if (response.status === 401) {
      await handleUnauthorized();
      return;
    }
    if (!response.ok) {
      setError(appError, 'Failed to load apps');
      return;
    }

    state.apps = (await response.json()) || [];
    renderApps();
  }

  async function loadAuditEvents(page) {
    setError(auditError, '');
    const offset = page * 100;
    let resp;
    try {
      resp = await api(`/api/audit?limit=101&offset=${offset}`);
    } catch {
      setError(auditError, 'Network error');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) { setError(auditError, 'Failed to load audit log'); return; }

    let events;
    try {
      events = (await resp.json()) || [];
    } catch {
      setError(auditError, 'Invalid response from server');
      return;
    }
    state.auditHasMore = events.length > 100;
    state.auditPage = page;
    renderAuditEvents(events.slice(0, 100));
  }

  function renderAuditEvents(events) {
    auditBody.textContent = '';

    const knownActions = ['deploy', 'restart', 'rollback', 'login', 'login_failed'];

    for (const e of events) {
      const tr = document.createElement('tr');

      // Time
      const timeCell = document.createElement('td');
      const ts = new Date(e.created_at);
      timeCell.textContent = relativeTime(ts);
      timeCell.title = ts.toISOString();
      tr.appendChild(timeCell);

      // User
      const userCell = document.createElement('td');
      userCell.textContent = e.username || '—';
      tr.appendChild(userCell);

      // Action badge
      const actionCell = document.createElement('td');
      const badge = document.createElement('span');
      badge.className = 'badge ' + (knownActions.includes(e.action)
        ? `badge-action-${e.action}`
        : 'badge-action-default');
      badge.textContent = e.action;
      actionCell.appendChild(badge);
      tr.appendChild(actionCell);

      // Resource
      const resourceCell = document.createElement('td');
      const parts = [e.resource_type, e.resource_id].filter(Boolean);
      resourceCell.textContent = parts.length ? parts.join(' ') : '—';
      tr.appendChild(resourceCell);

      // IP
      const ipCell = document.createElement('td');
      ipCell.textContent = e.ip_address || '—';
      tr.appendChild(ipCell);

      auditBody.appendChild(tr);
    }

    const start = state.auditPage * 100 + 1;
    const end   = state.auditPage * 100 + events.length;
    auditRange.textContent = events.length === 0
      ? 'No events'
      : `Showing ${start}–${end}`;
    auditPrev.disabled = state.auditPage === 0;
    auditNext.disabled = !state.auditHasMore;
  }

  async function restart(slug) {
    setError(appError, '');

    let response;
    try {
      response = await api(`/api/apps/${slug}/restart`, {method: 'POST'});
    } catch {
      setError(appError, 'Network error');
      return;
    }

    if (response.status === 401) {
      await handleUnauthorized();
      return;
    }
    if (!response.ok) {
      setError(appError, 'Restart failed');
      return;
    }

    window.setTimeout(loadApps, 1000);
  }

  function openLogs(slug) {
    closeLogs();
    logPaneTitle.textContent = `Logs — ${slug}`;
    logPaneBody.textContent = '';
    setHidden(logPane, false);

    const es = new EventSource(`/api/apps/${slug}/logs`, {withCredentials: true});
    activeEventSource = es;

    es.onmessage = (event) => {
      const atBottom =
        logPaneBody.scrollHeight - Math.ceil(logPaneBody.scrollTop) <= logPaneBody.clientHeight + 1;
      logPaneBody.appendChild(document.createTextNode(event.data + '\n'));
      if (atBottom) {
        logPaneBody.scrollTop = logPaneBody.scrollHeight;
      }
    };

    es.onerror = () => {
      es.close();
      activeEventSource = null;
    };
  }

  function closeLogs() {
    if (activeEventSource) {
      activeEventSource.close();
      activeEventSource = null;
    }
    setHidden(logPane, true);
  }

  // --- Access modal ---

  let accessSlug = null;
  let historySlug = null;

  async function openAccessModal(app) {
    accessSlug = app.slug;
    document.getElementById('access-app-name').textContent = app.name;

    // Set visibility radio to current access level.
    const radios = document.querySelectorAll('input[name="access-level"]');
    radios.forEach(r => { r.checked = r.value === app.access; });

    // Clear previous state.
    document.getElementById('members-list').innerHTML = '';
    document.getElementById('grant-username').value = '';
    document.getElementById('grant-error').hidden = true;

    document.getElementById('access-modal').hidden = false;
    // Move focus to the close button for keyboard/screen-reader users.
    document.getElementById('access-modal-close').focus();

    await refreshMemberList();
  }

  function closeAccessModal() {
    document.getElementById('access-modal').hidden = true;
    accessSlug = null;
  }

  async function openHistoryModal(slug) {
    historySlug = slug;
    const app = state.apps.find(a => a.slug === slug);
    historyAppName.textContent = app ? app.name : slug;
    historyList.textContent = '';
    historyModal.hidden = false;
    document.getElementById('history-modal-close').focus();
    historyList.innerHTML = '<li style="color:var(--text-muted);padding:0.5rem 0">Loading…</li>';

    let resp;
    try {
      resp = await api(`/api/apps/${slug}/deployments`);
    } catch {
      historyList.innerHTML = '<li style="color:var(--text-muted)">Failed to load deployments</li>';
      return;
    }
    if (!resp.ok) {
      historyList.innerHTML = '<li style="color:var(--text-muted)">Failed to load deployments</li>';
      return;
    }

    let deployments;
    try {
      deployments = (await resp.json()) || [];
    } catch {
      historyList.innerHTML = '<li style="color:var(--text-muted)">Failed to load deployments</li>';
      return;
    }
    if (historySlug !== slug) return;  // modal was superseded by a later open
    historyList.textContent = '';

    for (const d of deployments) {
      const li = document.createElement('li');
      if (d.status === 'active') li.className = 'deployment-active';

      const meta = document.createElement('div');
      meta.className = 'deployment-meta';

      const versionRow = document.createElement('div');

      const versionEl = document.createElement('code');
      versionEl.className = 'deployment-version';
      const isHash = /^[0-9a-f]{8,}$/i.test(d.version);
      versionEl.textContent = isHash ? d.version.slice(0, 7) : d.version;
      versionRow.appendChild(versionEl);

      const statusBadge = document.createElement('span');
      statusBadge.className = `badge badge-${d.status}`;
      statusBadge.textContent = d.status;
      versionRow.appendChild(statusBadge);

      meta.appendChild(versionRow);

      const dateEl = document.createElement('div');
      dateEl.className = 'deployment-date';
      const ts = new Date(d.created_at);
      dateEl.textContent = relativeTime(ts);
      dateEl.title = ts.toISOString();
      meta.appendChild(dateEl);

      li.appendChild(meta);

      if (d.status === 'success') {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.textContent = 'Rollback';
        btn.addEventListener('click', () => rollbackTo(slug, d.id));
        li.appendChild(btn);
      } else if (d.status === 'active') {
        const label = document.createElement('span');
        label.style.cssText = 'color:var(--text-muted);font-size:0.72rem';
        label.textContent = 'current';
        li.appendChild(label);
      }

      historyList.appendChild(li);
    }
  }

  function closeHistoryModal() {
    historyModal.hidden = true;
    historySlug = null;
  }

  async function rollbackTo(slug, deploymentId) {
    let resp;
    try {
      resp = await api(`/api/apps/${slug}/rollback`, {
        method: 'PUT',
        body: JSON.stringify({ deployment_id: deploymentId }),
      });
    } catch {
      historyList.insertAdjacentHTML('beforeend',
        '<li style="color:var(--red);padding:0.5rem 0">Network error — rollback not sent</li>');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      historyList.insertAdjacentHTML('beforeend',
        '<li style="color:var(--red);padding:0.5rem 0">Rollback failed</li>');
      return;
    }
    closeHistoryModal();
    window.setTimeout(loadApps, 1000);
  }

  async function refreshMemberList() {
    if (!accessSlug) return;
    const list = document.getElementById('members-list');
    list.innerHTML = '<li class="loading-placeholder">Loading…</li>';
    let resp;
    try {
      resp = await api(`/api/apps/${accessSlug}/members`);
    } catch { list.innerHTML = ''; return; }
    if (!resp.ok) { list.innerHTML = ''; return; }
    const members = await resp.json();
    list.innerHTML = '';
    for (const m of members) {
      const li = document.createElement('li');
      const nameSpan = document.createElement('span');
      nameSpan.className = 'member-name';
      nameSpan.textContent = m.username;
      const roleSpan = document.createElement('span');
      roleSpan.className = 'member-role';
      roleSpan.textContent = m.role;
      const revokeBtn = document.createElement('button');
      revokeBtn.textContent = 'Revoke';
      revokeBtn.addEventListener('click', async () => {
        const slug = accessSlug;
        if (!slug) return;
        try {
          const r = await api(`/api/apps/${slug}/members`, {
            method: 'DELETE',
            body: JSON.stringify({ user_id: m.user_id }),
          });
          if (r.ok) li.remove();
        } catch { /* network error — leave row in place */ }
      });
      li.appendChild(nameSpan);
      li.appendChild(roleSpan);
      li.appendChild(revokeBtn);
      list.appendChild(li);
    }
  }

  logPaneClose.addEventListener('click', closeLogs);

  tabApps.addEventListener('click',  () => showView('apps'));
  tabAudit.addEventListener('click', () => showView('audit'));
  auditPrev.addEventListener('click', () => loadAuditEvents(state.auditPage - 1));
  auditNext.addEventListener('click', () => loadAuditEvents(state.auditPage + 1));

  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
      if (!deployModal.hidden) {
        closeDeployModal();
      } else if (!newAppModal.hidden) {
        closeNewAppModal();
      } else if (!document.getElementById('access-modal').hidden) {
        closeAccessModal();
      } else if (!historyModal.hidden) {
        closeHistoryModal();
      } else if (!document.getElementById('log-pane').hidden) {
        closeLogs();
      }
    }
  });

  // Close modal on × or overlay click.
  document.getElementById('access-modal-close').addEventListener('click', closeAccessModal);
  document.getElementById('access-modal').addEventListener('click', e => {
    if (e.target === e.currentTarget) closeAccessModal();
  });

  document.getElementById('history-modal-close').addEventListener('click', closeHistoryModal);
  historyModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeHistoryModal();
  });

  // Visibility radio change → PATCH access level.
  document.querySelectorAll('input[name="access-level"]').forEach(radio => {
    radio.addEventListener('change', async () => {
      if (!accessSlug) return;
      const slug = accessSlug;
      let resp;
      try {
        resp = await api(`/api/apps/${slug}/access`, {
          method: 'PATCH',
          body: JSON.stringify({ access: radio.value }),
        });
      } catch {
        return;
      }
      if (!resp.ok) return;
      // Update local state so card reflects the new level.
      const app = state.apps.find(a => a.slug === slug);
      if (app) app.access = radio.value;
    });
  });

  // Grant button.
  document.getElementById('grant-btn').addEventListener('click', async () => {
    const grantBtn = document.getElementById('grant-btn');
    const username = document.getElementById('grant-username').value.trim();
    const errEl = document.getElementById('grant-error');
    errEl.hidden = true;
    if (!username) return;

    grantBtn.disabled = true;
    grantBtn.textContent = 'Granting…';
    try {
      // Resolve username → user_id.
      const lookupResp = await api(`/api/users/${encodeURIComponent(username)}`);
      if (!lookupResp.ok) {
        errEl.textContent = lookupResp.status === 404 ? 'User not found' : 'Lookup failed';
        errEl.hidden = false;
        return;
      }
      const user = await lookupResp.json();

      // Grant access.
      const grantResp = await api(`/api/apps/${accessSlug}/members`, {
        method: 'POST',
        body: JSON.stringify({ user_id: user.id }),
      });
      if (!grantResp.ok) {
        errEl.textContent = 'Grant failed';
        errEl.hidden = false;
        return;
      }
      document.getElementById('grant-username').value = '';
      await refreshMemberList();
    } finally {
      grantBtn.disabled = false;
      grantBtn.textContent = 'Grant';
    }
  });

  function renderNewAppSnippet(slug) {
    const origin = window.location.origin;
    const username = (state.user && state.user.username) || '<your-name>';
    const effectiveSlug = slug && slug.length > 0 ? slug : '<slug>';
    newAppSnippet.textContent =
      `shiny login --host ${origin} --username ${username}\n` +
      `shiny deploy --slug ${effectiveSlug} <path-to-your-app>`;
  }

  const newAppDivider = document.querySelector('#new-app-modal .handoff-card-divider');

  function resetNewAppModal() {
    newAppForm.hidden = false;
    if (newAppDivider) newAppDivider.hidden = false;
    newAppHandoff.hidden = true;
    newAppSnippetLabel.textContent = 'Deploy from your machine after creating';
    newAppSlug.value = '';
    newAppName.value = '';
    newAppProject.value = '';
    setError(newAppError, '');
    newAppSubmit.disabled = false;
    newAppSubmit.textContent = 'Create';
    slugEdited = false;
    renderNewAppSnippet('');
  }

  function openNewAppModal() {
    resetNewAppModal();
    newAppModal.hidden = false;
    newAppName.focus();
  }

  function closeNewAppModal() {
    newAppModal.hidden = true;
    resetNewAppModal();
  }

  function showNewAppHandoff(slug) {
    newAppForm.hidden = true;
    if (newAppDivider) newAppDivider.hidden = true;
    newAppHandoff.hidden = false;
    newAppSnippetLabel.textContent = 'Deploy from your machine';
    renderNewAppSnippet(slug);
    newAppDone.focus();
  }

  async function submitNewApp(event) {
    event.preventDefault();
    setError(newAppError, '');

    const slug = newAppSlug.value.trim();
    const name = newAppName.value.trim();
    const projectSlug = newAppProject.value.trim();

    if (!SLUG_RE.test(slug)) {
      setError(newAppError, 'Slug must be 1–63 lowercase letters, digits, or dashes (cannot start with a dash).');
      newAppSlug.focus();
      return;
    }
    if (name.length < 1 || name.length > 128) {
      setError(newAppError, 'Display name must be 1–128 characters.');
      newAppName.focus();
      return;
    }

    newAppSubmit.disabled = true;
    newAppSubmit.textContent = 'Creating…';

    let resp;
    try {
      resp = await api('/api/apps', {
        method: 'POST',
        body: JSON.stringify({ slug, name, project_slug: projectSlug }),
      });
    } catch {
      setError(newAppError, 'Network error');
      newAppSubmit.disabled = false;
      newAppSubmit.textContent = 'Create';
      return;
    }

    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (resp.status === 409) {
      setError(newAppError, 'Slug already taken. Pick another.');
      newAppSubmit.disabled = false;
      newAppSubmit.textContent = 'Create';
      return;
    }
    if (resp.status === 403) {
      setError(newAppError, 'You do not have permission to create apps.');
      newAppSubmit.disabled = false;
      newAppSubmit.textContent = 'Create';
      return;
    }
    if (!resp.ok) {
      let message = 'Failed to create app.';
      try {
        const body = await resp.json();
        if (body && body.error) message = body.error;
      } catch { /* non-JSON response */ }
      setError(newAppError, message);
      newAppSubmit.disabled = false;
      newAppSubmit.textContent = 'Create';
      return;
    }

    showNewAppHandoff(slug);
    await loadApps();
  }

  function formatBytes(n) {
    if (n < 1024) return `${n} B`;
    if (n < 1048576) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / 1048576).toFixed(1)} MB`;
  }

  function resetDeployModal() {
    deploySummary.hidden = true;
    deployIgnoredRow.hidden = true;
    deployProgressWrap.hidden = true;
    deployProgressBar.value = 0;
    deployProgressText.textContent = '0%';
    setError(deployError, '');
    deploySubmit.disabled = true;
    deploySubmit.textContent = 'Deploy';
    deployFileInput.value = '';
    deployDropzone.classList.remove('dragover');
    if (deployState && deployState.xhr) {
      deployState.xhr.abort();
    }
    deployState = null;
  }

  function openDeployModal(app) {
    resetDeployModal();
    deployState = { slug: app.slug, appName: app.name, blob: null, fileCount: 0, ignored: new Set(), xhr: null };
    deployAppName.textContent = app.name;
    deployModal.hidden = false;
    deployDropzone.focus();
  }

  function closeDeployModal() {
    resetDeployModal();
    deployModal.hidden = true;
  }

  function renderDeploySummary(sourceName, blobSize, fileCount, ignored) {
    deploySourceName.textContent = sourceName;
    deployFileCount.textContent = fileCount == null ? '—' : String(fileCount);
    deployBundleSize.textContent = formatBytes(blobSize);
    if (ignored && ignored.size > 0) {
      deployIgnoredRow.hidden = false;
      deployIgnoredList.textContent = [...ignored].sort().join(', ');
    } else {
      deployIgnoredRow.hidden = true;
    }
    deploySummary.hidden = false;

    if (blobSize > DEPLOY_MAX_BYTES) {
      setError(deployError, `Bundle is ${formatBytes(blobSize)} — exceeds the 128 MiB upload limit. Use the CLI for larger bundles.`);
      deploySubmit.disabled = true;
      return;
    }
    setError(deployError, '');
    deploySubmit.disabled = false;
  }

  async function acceptZipFile(file) {
    // Pre-built .zip — counting files would require parsing the central
    // directory, so leave the file count unknown and render it as "—".
    deployState.blob = file;
    deployState.fileCount = null;
    deployState.ignored = new Set();
    renderDeploySummary(file.name, file.size, null, null);
  }

  function bindDropzoneEvents() {
    deployDropzone.addEventListener('click', (e) => {
      if (e.target.tagName === 'BUTTON') return; // pick button handles itself
      deployFileInput.click();
    });
    deployDropzone.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        deployFileInput.click();
      }
    });
    deployPick.addEventListener('click', (e) => {
      e.stopPropagation();
      deployFileInput.click();
    });
    deployFileInput.addEventListener('change', async () => {
      const file = deployFileInput.files && deployFileInput.files[0];
      if (!file) return;
      if (!file.name.toLowerCase().endsWith('.zip')) {
        setError(deployError, 'File picker only accepts .zip. Drop a folder to zip it in the browser.');
        return;
      }
      await acceptZipFile(file);
    });

    deployDropzone.addEventListener('dragover', (e) => {
      e.preventDefault();
      deployDropzone.classList.add('dragover');
    });
    deployDropzone.addEventListener('dragleave', () => {
      deployDropzone.classList.remove('dragover');
    });
    deployDropzone.addEventListener('drop', async (e) => {
      e.preventDefault();
      deployDropzone.classList.remove('dragover');
      await handleDrop(e);
    });
  }

  async function handleDrop(e) {
    const items = [...(e.dataTransfer?.items || [])];
    if (items.length === 0) {
      setError(deployError, 'Nothing to upload.');
      return;
    }
    // Single .zip file → treat as pre-built.
    if (items.length === 1 && items[0].kind === 'file') {
      const entry = items[0].webkitGetAsEntry ? items[0].webkitGetAsEntry() : null;
      const file = items[0].getAsFile();
      if (file && !entry?.isDirectory && file.name.toLowerCase().endsWith('.zip')) {
        await acceptZipFile(file);
        return;
      }
      if (entry && entry.isDirectory) {
        await zipFolderEntry(entry);
        return;
      }
    }
    // Folder drop may come in as multiple file entries OR a single directory
    // entry depending on the browser.  If any item is a directory entry, zip it.
    const directories = items
      .map(i => i.webkitGetAsEntry ? i.webkitGetAsEntry() : null)
      .filter(en => en && en.isDirectory);
    if (directories.length === 1) {
      await zipFolderEntry(directories[0]);
      return;
    }
    setError(deployError, 'Drop a single folder or a single .zip. Multiple files are not supported yet.');
  }

  let jsZipPromise = null;

  function loadJSZip() {
    if (!jsZipPromise) {
      jsZipPromise = new Promise((resolve, reject) => {
        const s = document.createElement('script');
        s.src = '/static/vendor/jszip.min.js';
        s.onload = () => {
          if (window.JSZip) resolve(window.JSZip);
          else reject(new Error('JSZip loaded but global is missing'));
        };
        s.onerror = () => reject(new Error('Failed to load JSZip'));
        document.head.appendChild(s);
      });
    }
    return jsZipPromise;
  }

  function readEntriesAll(dirReader) {
    // webkit's readEntries returns results in chunks; loop until empty.
    return new Promise((resolve, reject) => {
      const all = [];
      function readBatch() {
        dirReader.readEntries((entries) => {
          if (entries.length === 0) { resolve(all); return; }
          all.push(...entries);
          readBatch();
        }, reject);
      }
      readBatch();
    });
  }

  function fileFromEntry(entry) {
    return new Promise((resolve, reject) => entry.file(resolve, reject));
  }

  // Walks a DirectoryEntry tree, yielding { relativePath, file } for every file
  // not under an ignored directory.  rootEntry itself contributes no prefix —
  // mirrors the CLI's `filepath.Rel(dir, path)` behavior so the archive contains
  // the folder's contents, not the folder itself.
  async function* walkFolder(rootEntry, ignored) {
    const queue = [{ entry: rootEntry, path: '' }];
    while (queue.length > 0) {
      const { entry, path } = queue.shift();
      if (entry.isDirectory) {
        const reader = entry.createReader();
        const children = await readEntriesAll(reader);
        for (const child of children) {
          const childPath = path ? `${path}/${child.name}` : child.name;
          if (child.isDirectory && DEPLOY_IGNORE_DIRS.has(child.name)) {
            ignored.add(child.name);
            continue;
          }
          queue.push({ entry: child, path: childPath });
        }
      } else if (entry.isFile) {
        const file = await fileFromEntry(entry);
        yield { relativePath: path, file };
      }
    }
  }

  // buildZipNative and supportsCompressionStream are defined in zip-writer.js,
  // loaded before this script in index.html.

  async function buildZipJSZip(fileList) {
    const JSZip = await loadJSZip();
    const zip = new JSZip();
    for (const { relativePath, file } of fileList) zip.file(relativePath, file);
    return zip.generateAsync({
      type: 'blob',
      compression: 'DEFLATE',
      compressionOptions: { level: 6 },
    });
  }

  async function zipFolderEntry(rootEntry) {
    setError(deployError, '');
    deploySubmit.disabled = true;
    deploySourceName.textContent = rootEntry.name + '/';
    deployFileCount.textContent = 'Reading…';
    deployBundleSize.textContent = '—';
    deploySummary.hidden = false;

    const ignored = new Set();
    const fileList = [];
    try {
      for await (const item of walkFolder(rootEntry, ignored)) fileList.push(item);
    } catch (err) {
      console.error('walkFolder failed:', err);
      setError(deployError, 'Failed to read folder contents.');
      return;
    }

    if (fileList.length === 0) {
      setError(deployError, 'Folder is empty after filtering ignored directories.');
      return;
    }

    const hasAppPy = fileList.some(f => f.relativePath === 'app.py' || f.relativePath.endsWith('/app.py'));
    const hasAppR  = fileList.some(f => f.relativePath === 'app.R'  || f.relativePath.endsWith('/app.R'));
    if (!hasAppPy && !hasAppR) {
      setError(deployError, 'Bundle contains no app.py or app.R at any level. Did you drop the wrong folder?');
      return;
    }

    let blob;
    try {
      blob = supportsCompressionStream()
        ? await buildZipNative(fileList)
        : await buildZipJSZip(fileList);
    } catch (err) {
      console.error('zip build failed:', err);
      setError(deployError, 'Failed to build zip archive.');
      return;
    }

    deployState.blob = blob;
    deployState.fileCount = fileList.length;
    deployState.ignored = ignored;
    renderDeploySummary(rootEntry.name + '/', blob.size, fileList.length, ignored);
  }

  function uploadBundle(slug, blob) {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      const form = new FormData();
      form.append('bundle', blob, 'bundle.zip');

      xhr.open('POST', `/api/apps/${encodeURIComponent(slug)}/deploy`, true);
      xhr.withCredentials = true;
      const csrf = readCookie('csrf_token');
      if (csrf) xhr.setRequestHeader('X-CSRF-Token', csrf);

      xhr.upload.addEventListener('progress', (e) => {
        if (!e.lengthComputable) return;
        const pct = Math.floor((e.loaded / e.total) * 100);
        deployProgressBar.value = pct;
        deployProgressText.textContent = `${pct}%`;
      });
      xhr.addEventListener('load', () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve(xhr.responseText);
        } else {
          let msg = 'Deploy failed';
          try {
            const body = JSON.parse(xhr.responseText);
            if (body && body.error) msg = body.error;
          } catch { /* non-JSON; keep default */ }
          const err = new Error(msg);
          err.status = xhr.status;
          reject(err);
        }
      });
      xhr.addEventListener('error',  () => reject(new Error('Network error during upload')));
      xhr.addEventListener('abort',  () => {
        const err = new Error('Upload cancelled');
        err.code = 'UPLOAD_CANCELLED';
        reject(err);
      });
      xhr.addEventListener('timeout',() => reject(new Error('Upload timed out')));

      deployState.xhr = xhr;
      xhr.send(form);
    });
  }

  deploySubmit.addEventListener('click', async () => {
    if (!deployState || !deployState.blob) return;
    setError(deployError, '');
    deploySubmit.disabled = true;
    deployCancel.textContent = 'Close';
    deployProgressWrap.hidden = false;
    deployProgressBar.value = 0;
    deployProgressText.textContent = '0%';

    const { slug, blob } = deployState;

    try {
      await uploadBundle(slug, blob);
    } catch (err) {
      if (err.code === 'UPLOAD_CANCELLED') {
        return; // closeDeployModal resets state
      }
      if (err.status === 401) { await handleUnauthorized(); return; }
      setError(deployError, err.message || 'Deploy failed');
      deploySubmit.disabled = false;
      deployCancel.textContent = 'Cancel';
      return;
    }

    deployProgressText.textContent = 'Deployed';
    closeDeployModal();
    await loadApps();
    openLogs(slug);
  });

  deployModalClose.addEventListener('click', closeDeployModal);
  deployCancel.addEventListener('click', closeDeployModal);
  deployModal.addEventListener('click', (e) => {
    if (e.target === e.currentTarget) closeDeployModal();
  });
  bindDropzoneEvents();

  newAppButton.addEventListener('click', openNewAppModal);
  newAppClose.addEventListener('click', closeNewAppModal);
  newAppCancel.addEventListener('click', closeNewAppModal);
  newAppDone.addEventListener('click', closeNewAppModal);
  newAppModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeNewAppModal();
  });
  newAppForm.addEventListener('submit', submitNewApp);
  newAppName.addEventListener('input', () => {
    if (slugEdited) return;
    newAppSlug.value = slugify(newAppName.value);
    renderNewAppSnippet(newAppSlug.value);
  });
  newAppSlug.addEventListener('input', () => {
    slugEdited = newAppSlug.value.length > 0;
    renderNewAppSnippet(newAppSlug.value);
  });
  const newAppSnippetCopyLabel  = newAppSnippetCopy.querySelector('.copy-label');
  const newAppSnippetCopyStatus = document.getElementById('new-app-snippet-status');

  newAppSnippetCopy.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(newAppSnippet.textContent);
      newAppSnippetCopy.classList.add('is-copied');
      if (newAppSnippetCopyLabel)  newAppSnippetCopyLabel.textContent  = 'Copied';
      if (newAppSnippetCopyStatus) newAppSnippetCopyStatus.textContent = 'Copied to clipboard';
      setTimeout(() => {
        newAppSnippetCopy.classList.remove('is-copied');
        if (newAppSnippetCopyLabel)  newAppSnippetCopyLabel.textContent  = 'Copy';
        if (newAppSnippetCopyStatus) newAppSnippetCopyStatus.textContent = '';
      }, 1800);
    } catch { /* clipboard blocked; user can select text manually */ }
  });

  loginForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    setError(loginError, '');

    let response;
    try {
      response = await api('/api/auth/session', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({
          username: usernameInput.value,
          password: passwordInput.value,
        }),
      });
    } catch {
      setError(loginError, 'Network error');
      return;
    }

    if (response.status === 401) {
      setError(loginError, 'Invalid credentials');
      return;
    }
    if (!response.ok) {
      setError(loginError, 'Login failed');
      return;
    }

    const payload = await response.json();
    showLoggedIn(payload);
    passwordInput.value = '';
    await loadApps();
    startMetricsPolling();
  });

  refreshButton.addEventListener('click', () => {
    loadApps();
  });

  logoutButton.addEventListener('click', async () => {
    try {
      await api('/api/auth/logout', {method: 'POST'});
    } catch {
      // Logging out should still reset local UI state even if the request fails.
    }
    showLoggedOut();
  });

  async function fetchMetrics(slug) {
    let resp;
    try {
      resp = await api(`/api/apps/${slug}/metrics`);
    } catch {
      return;
    }
    if (!resp.ok) return;
    const m = await resp.json();
    const el = appGrid.querySelector(`.app-metrics[data-slug="${slug}"]`);
    if (!el) return;
    if (m.status !== 'running') {
      el.textContent = '';
      return;
    }
    const cpu = m.cpu_percent.toFixed(1);
    const ram = m.rss_bytes >= 1 << 20
      ? (m.rss_bytes / (1 << 20)).toFixed(0) + ' MB'
      : (m.rss_bytes / 1024).toFixed(0) + ' KB';
    el.textContent = `CPU ${cpu}% · ${ram} RAM`;
  }

  // Poll metrics every 10 seconds for all running apps.
  function startMetricsPolling() {
    clearInterval(state.metricsInterval);
    state.metricsInterval = null;
    if (!state.apps.some(a => a.status === 'running')) return;
    state.metricsInterval = setInterval(() => {
      for (const app of state.apps) {
        if (app.status === 'running') {
          fetchMetrics(app.slug);
        }
      }
    }, 10_000);
  }

  async function loadProviders() {
    try {
      const resp = await api('/api/auth/providers');
      if (!resp.ok) return;
      const data = await resp.json();
      if (data && data.oidc && data.oidc.enabled) {
        const btn = document.createElement('a');
        btn.className = 'oidc-login';
        btn.href = '/api/auth/oidc/login';
        btn.textContent = data.oidc.display_name || 'Sign in with SSO';
        const googleLink = document.querySelector('.google-login');
        if (googleLink) {
          googleLink.insertAdjacentElement('afterend', btn);
        } else {
          const ghLink = document.querySelector('.github-login');
          if (ghLink) {
            ghLink.insertAdjacentElement('afterend', btn);
          } else {
            document.querySelector('.login-box').appendChild(btn);
          }
        }
      }
    } catch (e) { /* non-critical */ }
  }

  async function initialize() {
    loadProviders();
    setError(loginError, '');

    let response;
    try {
      response = await api('/api/auth/me');
    } catch {
      showLoggedOut();
      setError(loginError, 'Network error');
      return;
    }

    if (response.status === 401) {
      showLoggedOut();
      return;
    }
    if (!response.ok) {
      showLoggedOut();
      setError(loginError, 'Failed to load session');
      return;
    }

    const payload = await response.json();
    showLoggedIn(payload);
    await loadApps();
    startMetricsPolling();
    handleDeployHash();
  }

  // Honour /#deploy=<slug> from the server-rendered empty-state page:
  // after the apps list has loaded, scroll to the matching card and open the
  // deploy modal. Clears the hash afterwards so refreshing doesn't re-trigger.
  function handleDeployHash() {
    const match = /^#deploy=([a-z0-9][a-z0-9-]{0,62})$/i.exec(window.location.hash);
    if (!match) return;
    const slug = match[1];
    const app = state.apps.find(a => a.slug === slug);
    // Clear hash without adding a history entry.
    history.replaceState(null, '', window.location.pathname + window.location.search);
    if (!app) return;
    if (!canManageApp(state.user, app)) return;
    const card = [...appGrid.querySelectorAll('.app-card')].find(
      c => c.querySelector('.app-meta span')?.textContent === `/${slug}`
    );
    if (card) card.scrollIntoView({behavior: 'smooth', block: 'center'});
    openDeployModal(app);
  }

  initialize();
});
