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

document.addEventListener('DOMContentLoaded', () => {
  const state = {
    user: null,
    apps: [],
    metricsInterval: null,
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

  let activeEventSource = null;

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

      const badge = document.createElement('span');
      badge.className = `badge badge-${app.status}`;
      badge.textContent = app.status;
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

      const openLink = document.createElement('a');
      openLink.href = `/app/${app.slug}/`;
      openLink.target = '_blank';
      openLink.rel = 'noopener noreferrer';
      openLink.textContent = 'Open';
      actions.appendChild(openLink);

      if (canManageApp(state.user, app)) {
        const restartButton = document.createElement('button');
        restartButton.type = 'button';
        restartButton.textContent = 'Restart';
        restartButton.addEventListener('click', () => restart(app.slug));
        actions.appendChild(restartButton);

        const rollbackButton = document.createElement('button');
        rollbackButton.type = 'button';
        rollbackButton.textContent = 'Rollback';
        rollbackButton.addEventListener('click', () => rollback(app.slug));
        actions.appendChild(rollbackButton);

        const logsButton = document.createElement('button');
        logsButton.type = 'button';
        logsButton.textContent = 'Logs';
        logsButton.addEventListener('click', () => openLogs(app.slug));
        actions.appendChild(logsButton);

        const accessButton = document.createElement('button');
        accessButton.className = 'btn btn-access';
        accessButton.textContent = 'Access';
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

  function showLoggedOut() {
    closeLogs();
    clearInterval(state.metricsInterval);
    state.metricsInterval = null;
    state.user = null;
    state.apps = [];
    sessionUser.textContent = '';
    setHidden(logoutButton, true);
    setHidden(loginView, false);
    setHidden(appsView, true);
    setError(loginError, '');
    setError(appError, '');
    renderApps();
  }

  function showLoggedIn(user) {
    state.user = user;
    sessionUser.textContent = user.username;
    setHidden(logoutButton, false);
    setHidden(loginView, true);
    setHidden(appsView, false);
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

  async function rollback(slug) {
    setError(appError, '');

    let response;
    try {
      response = await api(`/api/apps/${slug}/rollback`, {method: 'PUT'});
    } catch {
      setError(appError, 'Network error');
      return;
    }

    if (response.status === 401) {
      await handleUnauthorized();
      return;
    }
    if (!response.ok) {
      setError(appError, 'Rollback failed');
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

  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
      if (!document.getElementById('access-modal').hidden) {
        closeAccessModal();
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
      const lookupResp = await api(`/api/users?username=${encodeURIComponent(username)}`);
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
    showLoggedIn(payload.user);
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

  async function initialize() {
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
    showLoggedIn(payload.user);
    await loadApps();
    startMetricsPolling();
  }

  initialize();
});
