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

  localStorage.removeItem('shinyhub_token');

  async function api(path, options = {}) {
    const init = {
      credentials: 'same-origin',
      headers: {},
      ...options,
    };
    init.headers = {...init.headers};
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
      }

      card.appendChild(header);
      card.appendChild(meta);
      card.appendChild(actions);
      appGrid.appendChild(card);
    }
  }

  function showLoggedOut() {
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
        logPaneBody.scrollHeight - logPaneBody.scrollTop <= logPaneBody.clientHeight + 4;
      logPaneBody.textContent += event.data + '\n';
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

  logPaneClose.addEventListener('click', closeLogs);

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
  }

  initialize();
});
