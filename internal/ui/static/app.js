import { createRouter } from '/static/router.js';
import { createMetricsController } from '/static/metrics-controller.js';
import { mountAppsGrid } from '/static/views/apps-grid.js';
import { mountUsers } from '/static/views/users.js';
import { mountAuditLog } from '/static/views/audit-log.js';
import { mountAppDetail } from '/static/views/app-detail.js';

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
    auditPage: 0,
    auditHasMore: false,
    canCreateApps: false,
    resetPwTargetId: null,
    resetPwTargetUsername: '',
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
  const emptyStateHeading = document.getElementById('empty-state-heading');
  const emptyStateEyebrow = document.getElementById('empty-state-eyebrow');
  const emptyStateLead    = document.getElementById('empty-state-lead');
  const emptyStateActions = document.getElementById('empty-state-actions');
  const emptyStateCTA     = document.getElementById('empty-state-cta');
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
  const tabUsers    = document.getElementById('tab-users');
  const usersView   = document.getElementById('users-view');
  const usersError  = document.getElementById('users-error');
  const usersBody   = document.getElementById('users-body');
  const usersRefresh   = document.getElementById('users-refresh');
  const newUserButton  = document.getElementById('new-user-button');
  const newUserModal   = document.getElementById('new-user-modal');
  const newUserClose   = document.getElementById('new-user-close');
  const newUserCancel  = document.getElementById('new-user-cancel');
  const newUserForm    = document.getElementById('new-user-form');
  const newUserUsername = document.getElementById('new-user-username');
  const newUserPassword = document.getElementById('new-user-password');
  const newUserRole     = document.getElementById('new-user-role');
  const newUserError    = document.getElementById('new-user-error');
  const resetPwModal    = document.getElementById('reset-password-modal');
  const resetPwClose    = document.getElementById('reset-password-close');
  const resetPwCancel   = document.getElementById('reset-password-cancel');
  const resetPwForm     = document.getElementById('reset-password-form');
  const resetPwInput    = document.getElementById('reset-password-input');
  const resetPwUsername = document.getElementById('reset-password-username');
  const resetPwError    = document.getElementById('reset-password-error');
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
  const deployCliSnippet   = document.getElementById('deploy-cli-snippet');
  const deployCliCopy      = document.getElementById('deploy-cli-snippet-copy');
  const deployCliCopyLabel = deployCliCopy.querySelector('.copy-label');
  const deployCliCopyStatus = document.getElementById('deploy-cli-snippet-status');

  const SLUG_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;

  let BUNDLE_RULES = null;
  async function loadBundleRules() {
    if (BUNDLE_RULES) return BUNDLE_RULES;
    const resp = await fetch('/static/bundle-rules.json');
    if (!resp.ok) throw new Error('failed to load bundle rules');
    BUNDLE_RULES = await resp.json();
    return BUNDLE_RULES;
  }

  // Classify a single entry (file or directory) relative to the bundle root.
  // Returns one of: 'accept', 'skipCacheDir', 'rejectDataDir',
  // 'rejectDatasetDir', 'rejectExtension', 'rejectFileSize'.
  // Directory decisions should pass size=0. The leading-slash strip keeps
  // first-segment classification in lockstep with server-side bundle.Inspect,
  // which operates on paths cleaned via path.Clean.
  function inspectBundleEntry(rules, relPath, size) {
    const clean = relPath.replace(/^\/+/, '');
    const first = clean.split('/')[0];
    if (first === 'data') return 'rejectDataDir';
    if (first === 'datasets' || first === '.shinyhub-data') return 'rejectDatasetDir';
    if (rules.cacheDirs.includes(first)) return 'skipCacheDir';
    const lower = clean.toLowerCase();
    for (const ext of rules.dataExtensions) {
      if (lower.endsWith(ext.toLowerCase())) return 'rejectExtension';
    }
    if (rules.maxFileBytes > 0 && size > rules.maxFileBytes) return 'rejectFileSize';
    return 'accept';
  }

  let activeEventSource = null;
  let deployState = null; // { slug, appName, blob, fileCount, rejections: Map<string, string[]>, xhr }
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

  function renderEmptyStateCopy() {
    if (state.canCreateApps) {
      emptyStateEyebrow.textContent = 'Ready when you are';
      emptyStateHeading.innerHTML = 'Deploy your <strong>first Shiny app</strong>.';
      emptyStateLead.textContent =
        'Apps you create will appear here as cards. Start by naming your app — you can hand off to the CLI or drop a bundle straight into the browser.';
      emptyStateActions.hidden = false;
    } else {
      emptyStateEyebrow.textContent = 'Nothing here yet';
      emptyStateHeading.innerHTML = 'No apps are visible to you.';
      emptyStateLead.textContent =
        'Apps shared with you will appear here once the owner deploys them. Check back soon.';
      emptyStateActions.hidden = true;
    }
  }

  // Renders apps into the provided grid and empty-state elements. Takes explicit
  // DOM references so mountAppsGrid can call it from the view module without
  // closing over the closure-level appGrid/emptyState constants.
  function renderGridVerbatim(apps, gridEl, emptyEl) {
    gridEl.textContent = '';
    const empty = apps.length === 0;
    emptyEl.hidden = !empty;
    if (empty) renderEmptyStateCopy();

    for (const app of apps) {
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
        deployButton.textContent = 'Deploy';
        if (neverDeployed) deployButton.className = 'btn-primary';
        deployButton.setAttribute('aria-label', `Deploy new bundle to ${app.name}`);
        deployButton.addEventListener('click', () => openDeployModal(app));
        actions.appendChild(deployButton);

        if (!neverDeployed) {
          const kebab = document.createElement('div');
          kebab.className = 'kebab-menu';
          kebab.innerHTML = `
            <button type="button" aria-haspopup="menu" aria-expanded="false">⋯</button>
            <ul class="kebab-list" role="menu" hidden>
              <li role="menuitem"><button type="button" data-kebab="restart">Restart</button></li>
            </ul>
          `;
          const kebabBtn = kebab.querySelector('button');
          kebabBtn.setAttribute('aria-label', `More actions for ${app.name}`);
          const kebabList = kebab.querySelector('.kebab-list');
          kebabBtn.addEventListener('click', (e) => {
            e.stopPropagation();
            const open = !kebabList.hidden;
            kebabList.hidden = open;
            kebabBtn.setAttribute('aria-expanded', String(!open));
          });
          kebabList.querySelector('[data-kebab="restart"]').addEventListener('click', () => restart(app.slug));
          actions.appendChild(kebab);
        }
      }

      const metricsLine = document.createElement('div');
      metricsLine.className = 'app-metrics';
      metricsLine.dataset.slug = app.slug;

      const link = document.createElement('a');
      link.href = `/apps/${app.slug}`;
      link.setAttribute('data-nav', '');
      link.className = 'app-card-body-link';
      link.appendChild(header);
      link.appendChild(meta);
      link.appendChild(metricsLine);
      card.appendChild(link);
      card.appendChild(actions);
      gridEl.appendChild(card);
    }
  }

  function renderApps() {
    renderGridVerbatim(state.apps, appGrid, emptyState);
  }

  function showView(name) {
    appsView.hidden  = name !== 'apps';
    usersView.hidden = name !== 'users';
    auditView.hidden = name !== 'audit';
    tabApps.classList.toggle('tab-active',  name === 'apps');
    tabUsers.classList.toggle('tab-active', name === 'users');
    tabAudit.classList.toggle('tab-active', name === 'audit');
    if (name === 'audit') loadAuditEvents(0);
    if (name === 'users') loadUsers();
  }

  function showLoggedOut() {
    closeLogs();
    metrics.setTargets([]);
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
    setHidden(usersView, true);
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
    tabUsers.hidden = payload.user.role !== 'admin';
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
    metrics.setTargets(state.apps.map(a => a.slug));
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

  async function loadUsers() {
    setError(usersError, '');
    let resp;
    try {
      resp = await api('/api/users');
    } catch {
      setError(usersError, 'Network error');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (resp.status === 403) { setError(usersError, 'Admin only'); return; }
    if (!resp.ok) { setError(usersError, 'Failed to load users'); return; }
    let users = [];
    try { users = (await resp.json()) || []; }
    catch { setError(usersError, 'Invalid response'); return; }
    renderUsers(users);
  }

  function renderUsers(users) {
    usersBody.textContent = '';
    const selfId = state.user ? state.user.id : null;

    for (const u of users) {
      const tr = document.createElement('tr');

      // Username
      const nameCell = document.createElement('td');
      nameCell.className = 'users-username';
      nameCell.textContent = u.username;
      if (u.id === selfId) {
        const tag = document.createElement('span');
        tag.className = 'users-self-tag';
        tag.textContent = 'you';
        nameCell.appendChild(tag);
      }
      tr.appendChild(nameCell);

      // Role (editable select; disabled for self)
      const roleCell = document.createElement('td');
      const select = document.createElement('select');
      select.className = 'users-row-role';
      for (const r of ['developer', 'operator', 'admin']) {
        const opt = document.createElement('option');
        opt.value = r;
        opt.textContent = r.charAt(0).toUpperCase() + r.slice(1);
        if (u.role === r) opt.selected = true;
        select.appendChild(opt);
      }
      if (u.id === selfId) {
        select.disabled = true;
        select.title = 'You cannot change your own role';
      } else {
        select.addEventListener('change', () => updateUserRole(u.id, u.username, select));
      }
      roleCell.appendChild(select);
      tr.appendChild(roleCell);

      // Created
      const createdCell = document.createElement('td');
      createdCell.className = 'users-created';
      const ts = new Date(u.created_at);
      createdCell.textContent = relativeTime(ts);
      createdCell.title = ts.toISOString();
      tr.appendChild(createdCell);

      // Actions
      const actionsCell = document.createElement('td');
      const actions = document.createElement('div');
      actions.className = 'users-row-actions';

      const resetBtn = document.createElement('button');
      resetBtn.type = 'button';
      resetBtn.className = 'btn-row';
      resetBtn.textContent = 'Reset password';
      resetBtn.addEventListener('click', () => openResetPasswordModal(u));
      actions.appendChild(resetBtn);

      const delBtn = document.createElement('button');
      delBtn.type = 'button';
      delBtn.className = 'btn-row btn-row-danger';
      delBtn.textContent = 'Delete';
      if (u.id === selfId) {
        delBtn.disabled = true;
        delBtn.title = 'You cannot delete yourself';
      } else {
        delBtn.addEventListener('click', () => deleteUser(u.id, u.username));
      }
      actions.appendChild(delBtn);

      actionsCell.appendChild(actions);
      tr.appendChild(actionsCell);

      usersBody.appendChild(tr);
    }
  }

  async function updateUserRole(id, username, selectEl) {
    const newRole = selectEl.value;
    const previous = selectEl.dataset.previous || '';
    selectEl.disabled = true;
    let resp;
    try {
      resp = await api(`/api/users/${id}`, {
        method: 'PATCH',
        body: JSON.stringify({role: newRole}),
      });
    } catch {
      setError(usersError, 'Network error');
      selectEl.disabled = false;
      return;
    }
    selectEl.disabled = false;
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      setError(usersError, `Failed to update role for ${username}`);
      if (previous) selectEl.value = previous;
      return;
    }
    setError(usersError, '');
    loadUsers();
  }

  async function deleteUser(id, username) {
    if (!confirm(`Delete user "${username}"? This cannot be undone.`)) return;
    let resp;
    try {
      resp = await api(`/api/users/${id}`, {method: 'DELETE'});
    } catch {
      setError(usersError, 'Network error');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) { setError(usersError, `Failed to delete ${username}`); return; }
    setError(usersError, '');
    loadUsers();
  }

  function openResetPasswordModal(user) {
    state.resetPwTargetId = user.id;
    state.resetPwTargetUsername = user.username;
    resetPwUsername.textContent = user.username;
    resetPwInput.value = '';
    setError(resetPwError, '');
    resetPwModal.hidden = false;
    resetPwInput.focus();
  }

  function closeResetPasswordModal() {
    resetPwModal.hidden = true;
    state.resetPwTargetId = null;
    state.resetPwTargetUsername = '';
    resetPwInput.value = '';
    setError(resetPwError, '');
  }

  async function submitResetPassword(event) {
    event.preventDefault();
    const id = state.resetPwTargetId;
    if (!id) return;
    const password = resetPwInput.value;
    if (password.length < 8) {
      setError(resetPwError, 'Password must be at least 8 characters');
      return;
    }
    let resp;
    try {
      resp = await api(`/api/users/${id}/password`, {
        method: 'PATCH',
        body: JSON.stringify({password}),
      });
    } catch {
      setError(resetPwError, 'Network error');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let msg = 'Failed to reset password';
      try { const j = await resp.json(); if (j && j.error) msg = j.error; } catch {}
      setError(resetPwError, msg);
      return;
    }
    closeResetPasswordModal();
  }

  function openNewUserModal() {
    newUserForm.reset();
    setError(newUserError, '');
    newUserModal.hidden = false;
    newUserUsername.focus();
  }

  function closeNewUserModal() {
    newUserModal.hidden = true;
    newUserForm.reset();
    setError(newUserError, '');
  }

  async function submitNewUser(event) {
    event.preventDefault();
    const username = newUserUsername.value.trim();
    const password = newUserPassword.value;
    const role     = newUserRole.value;
    if (!username || password.length < 8) {
      setError(newUserError, 'Username and 8+ char password are required');
      return;
    }
    let resp;
    try {
      resp = await api('/api/users', {
        method: 'POST',
        body: JSON.stringify({username, password, role}),
      });
    } catch {
      setError(newUserError, 'Network error');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let msg = 'Failed to create user';
      try { const j = await resp.json(); if (j && j.error) msg = j.error; } catch {}
      setError(newUserError, msg);
      return;
    }
    closeNewUserModal();
    loadUsers();
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

  let settingsSlug = null;

  function populateAccessPanel(app) {
    // Set visibility radio to current access level.
    const radios = document.querySelectorAll('input[name="access-level"]');
    radios.forEach(r => { r.checked = r.value === app.access; });

    // Clear previous state.
    document.getElementById('members-list').innerHTML = '';
    document.getElementById('grant-username').value = '';
    document.getElementById('grant-error').hidden = true;

    // Danger zone: visible only to managers (owner/admin/operator), wired per-app.
    resetDangerZone();
    const dangerZone = document.getElementById('danger-zone');
    dangerZone.hidden = !canManageApp(state.user, app);
    document.getElementById('delete-confirm-slug').textContent = app.slug;
  }

  // --- General tab ---

  function hibernateModeFromValue(minutes) {
    if (minutes === null || minutes === undefined) return 'default';
    if (minutes === 0) return 'never';
    return 'custom';
  }

  function populateGeneralTab(app) {
    const mode = hibernateModeFromValue(app.hibernate_timeout_minutes);
    document.querySelectorAll('input[name="hibernate-mode"]').forEach(r => {
      r.checked = r.value === mode;
    });

    const customInput = document.getElementById('hibernate-custom-minutes');
    customInput.value = mode === 'custom' ? String(app.hibernate_timeout_minutes) : '';
    customInput.disabled = mode !== 'custom';

    const canEdit = canManageApp(state.user, app);
    document.querySelectorAll('input[name="hibernate-mode"]').forEach(r => {
      r.disabled = !canEdit;
    });
    if (!canEdit) customInput.disabled = true;
    document.getElementById('hibernate-save-btn').hidden = !canEdit;

    setError(document.getElementById('hibernate-error'), '');
    setHidden(document.getElementById('hibernate-status'), true);

    // Scaling fieldset: replicas + per-replica session cap.
    const replicasInput = document.getElementById('scaling-replicas');
    const capInput = document.getElementById('scaling-cap');
    const currentReplicas = app.replicas ?? 1;
    replicasInput.value = String(currentReplicas);
    capInput.value = String(app.max_sessions_per_replica ?? 0);
    replicasInput.dataset.original = String(currentReplicas);
    replicasInput.dataset.appStatus = String(app.status ?? '');
    replicasInput.disabled = !canEdit;
    capInput.disabled = !canEdit;
    document.getElementById('scaling-save-btn').hidden = !canEdit;
    setError(document.getElementById('scaling-error'), '');
    setHidden(document.getElementById('scaling-status'), true);
    updateScalingCeiling();
  }

  function updateScalingCeiling() {
    const el = document.getElementById('scaling-ceiling');
    if (!el) return;
    const r = parseInt(document.getElementById('scaling-replicas').value, 10);
    const c = parseInt(document.getElementById('scaling-cap').value, 10);
    if (!Number.isFinite(r) || r < 1) { el.textContent = ''; return; }
    if (!Number.isFinite(c) || c < 0) { el.textContent = ''; return; }
    if (c === 0) {
      el.innerHTML = `Admission ceiling: <strong>${r}</strong> replica${r === 1 ? '' : 's'} × runtime default cap.`;
    } else {
      el.innerHTML = `Admission ceiling: <strong>${r} × ${c} = ${r * c}</strong> concurrent new sessions before 503.`;
    }
  }

  function onHibernateModeChange() {
    const selected = document.querySelector('input[name="hibernate-mode"]:checked');
    const customInput = document.getElementById('hibernate-custom-minutes');
    const isCustom = selected && selected.value === 'custom';
    customInput.disabled = !isCustom;
    if (isCustom && !customInput.value) customInput.value = '60';
    if (isCustom) customInput.focus();
    setError(document.getElementById('hibernate-error'), '');
    setHidden(document.getElementById('hibernate-status'), true);
  }

  async function saveHibernateSettings() {
    if (!settingsSlug) return;
    const errEl = document.getElementById('hibernate-error');
    const statusEl = document.getElementById('hibernate-status');
    setError(errEl, '');
    setHidden(statusEl, true);

    const selected = document.querySelector('input[name="hibernate-mode"]:checked');
    if (!selected) {
      setError(errEl, 'Pick an option.');
      return;
    }

    let payload;
    if (selected.value === 'default') {
      payload = { hibernate_timeout_minutes: null };
    } else if (selected.value === 'never') {
      payload = { hibernate_timeout_minutes: 0 };
    } else {
      const raw = document.getElementById('hibernate-custom-minutes').value.trim();
      const n = parseInt(raw, 10);
      if (!Number.isFinite(n) || n < 1) {
        setError(errEl, 'Enter a whole number of minutes (1 or more).');
        return;
      }
      payload = { hibernate_timeout_minutes: n };
    }

    const btn = document.getElementById('hibernate-save-btn');
    btn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(settingsSlug)}`, {
        method: 'PATCH',
        body: JSON.stringify(payload),
      });
    } catch {
      btn.disabled = false;
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }
    btn.disabled = false;

    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    statusEl.textContent = 'Saved.';
    setHidden(statusEl, false);
    await loadApps();
  }

  async function saveScalingSettings() {
    if (!settingsSlug) return;
    const errEl = document.getElementById('scaling-error');
    const statusEl = document.getElementById('scaling-status');
    setError(errEl, '');
    setHidden(statusEl, true);

    const replicasRaw = document.getElementById('scaling-replicas').value.trim();
    const capRaw = document.getElementById('scaling-cap').value.trim();
    const replicas = parseInt(replicasRaw, 10);
    const cap = parseInt(capRaw, 10);
    if (!Number.isFinite(replicas) || replicas < 1) {
      setError(errEl, 'Replicas must be a whole number ≥ 1.');
      return;
    }
    if (!Number.isFinite(cap) || cap < 0 || cap > 1000) {
      setError(errEl, 'Max sessions per replica must be between 0 and 1000.');
      return;
    }

    // Replica-count changes restart the app (apps.go redeployApp), which
    // drops every active session. Cap changes are hot. Confirm before the
    // disruptive case.
    const replicasInput = document.getElementById('scaling-replicas');
    const originalReplicas = parseInt(replicasInput.dataset.original ?? '', 10);
    const wasRunning = replicasInput.dataset.appStatus === 'running';
    if (wasRunning && Number.isFinite(originalReplicas) && replicas !== originalReplicas) {
      const ok = window.confirm(
        `Changing replicas from ${originalReplicas} to ${replicas} will restart the app and drop all active sessions. Continue?`,
      );
      if (!ok) return;
    }

    const payload = { replicas, max_sessions_per_replica: cap };
    const btn = document.getElementById('scaling-save-btn');
    btn.disabled = true;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(settingsSlug)}`, {
        method: 'PATCH',
        body: JSON.stringify(payload),
      });
    } catch {
      btn.disabled = false;
      setError(errEl, 'Failed to save. Check your connection.');
      return;
    }
    btn.disabled = false;

    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to save.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    statusEl.textContent = 'Saved.';
    setHidden(statusEl, false);
    await loadApps();
  }

  function resetDangerZone() {
    const input = document.getElementById('delete-confirm');
    const btn = document.getElementById('delete-app-btn');
    input.value = '';
    btn.disabled = true;
    btn.textContent = 'Delete app';
    setError(document.getElementById('delete-error'), '');
  }

  // --- Environment tab ---

  let envEditingKey = null;

  async function refreshEnvList(slug) {
    const tbody = document.querySelector('#env-list tbody');
    tbody.innerHTML = '';
    const errEl = document.getElementById('env-form-error');

    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/env`);
    } catch {
      setError(errEl, 'Failed to load environment variables.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to load environment variables.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    let data;
    try { data = await resp.json(); } catch { setError(errEl, 'Invalid response from server.'); return; }

    const vars = (data && data.env) || [];
    const empty = document.getElementById('env-empty');
    const table = document.getElementById('env-list');
    empty.hidden = vars.length > 0;
    table.hidden = vars.length === 0;

    const app = state.apps.find(a => a.slug === slug);
    const canWrite = canManageApp(state.user, app);
    document.getElementById('env-add-btn').hidden = !canWrite;

    for (const v of vars) {
      const tr = document.createElement('tr');
      const keyTd = document.createElement('td');
      keyTd.textContent = v.key;
      const valTd = document.createElement('td');
      valTd.className = v.secret ? 'env-secret' : '';
      valTd.textContent = v.secret ? '••••••' : v.value;
      const actTd = document.createElement('td');
      actTd.className = 'env-actions';
      if (canWrite) {
        const editBtn = document.createElement('button');
        editBtn.type = 'button';
        editBtn.textContent = 'Edit';
        editBtn.addEventListener('click', () => openEnvForm(v));
        const delBtn = document.createElement('button');
        delBtn.type = 'button';
        delBtn.className = 'env-btn-danger';
        delBtn.textContent = 'Delete';
        delBtn.addEventListener('click', () => deleteEnvVar(slug, v.key));
        actTd.append(editBtn, delBtn);
      }
      tr.append(keyTd, valTd, actTd);
      tbody.appendChild(tr);
    }
  }

  function openEnvForm(existing) {
    envEditingKey = existing ? existing.key : null;
    document.getElementById('env-form-heading').textContent = existing ? `Edit ${existing.key}` : 'Add variable';
    const keyInput = document.getElementById('env-form-key');
    const valueInput = document.getElementById('env-form-value');
    const secretInput = document.getElementById('env-form-secret');
    keyInput.value = existing ? existing.key : '';
    keyInput.readOnly = !!existing;
    valueInput.value = '';
    valueInput.placeholder = (existing && existing.secret) ? 'Enter new value (current value is write-only)' : '';
    secretInput.checked = existing ? existing.secret : false;
    secretInput.disabled = !!existing;
    document.getElementById('env-form-error').hidden = true;
    document.getElementById('env-form').hidden = false;
    keyInput.focus();
  }

  function closeEnvForm() {
    const form = document.getElementById('env-form');
    if (form) form.hidden = true;
    envEditingKey = null;
  }

  async function submitEnvForm(e) {
    e.preventDefault();
    const key = document.getElementById('env-form-key').value.trim();
    const value = document.getElementById('env-form-value').value;
    const secret = document.getElementById('env-form-secret').checked;
    const restart = document.getElementById('env-form-restart').checked;
    const errEl = document.getElementById('env-form-error');
    errEl.hidden = true;

    if (!/^[A-Z_][A-Z0-9_]*$/.test(key)) {
      setError(errEl, 'Key must match [A-Z_][A-Z0-9_]*');
      return;
    }

    const url = `/api/apps/${encodeURIComponent(settingsSlug)}/env/${encodeURIComponent(key)}` + (restart ? '?restart=true' : '');
    let resp;
    try {
      resp = await api(url, { method: 'PUT', body: JSON.stringify({ value, secret }) });
    } catch {
      setError(errEl, 'Network error.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Save failed.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    closeEnvForm();
    await refreshEnvList(settingsSlug);
  }

  async function deleteEnvVar(slug, key) {
    if (!window.confirm(`Delete environment variable ${key}?`)) return;
    const errEl = document.getElementById('env-form-error');
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/env/${encodeURIComponent(key)}?restart=true`, { method: 'DELETE' });
    } catch {
      setError(errEl, 'Network error.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok && resp.status !== 204) {
      let message = 'Delete failed.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }
    await refreshEnvList(slug);
  }

  // --- Data tab ---

  function encodeDataPath(p) {
    return p.split('/').map(encodeURIComponent).join('/');
  }

  async function refreshDataTab(slug) {
    const tbody = document.getElementById('data-list');
    tbody.innerHTML = '';
    const errEl = document.getElementById('data-error');
    setError(errEl, '');

    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/data`);
    } catch {
      setError(errEl, 'Failed to load data files.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok) {
      let message = 'Failed to load data files.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }

    let env;
    try { env = await resp.json(); } catch { setError(errEl, 'Invalid response from server.'); return; }

    const files = (env && env.files) || [];
    const empty = document.getElementById('data-empty');
    const table = document.getElementById('data-list-table');
    empty.hidden = files.length > 0;
    table.hidden = files.length === 0;

    const app = state.apps.find(a => a.slug === slug);
    const canWrite = canManageApp(state.user, app);
    document.getElementById('data-upload-form').hidden = !canWrite;

    const quotaEl = document.getElementById('data-quota');
    if (env) {
      const used = formatBytes(env.used_bytes || 0);
      quotaEl.textContent = env.quota_mb
        ? `Using ${used} of ${env.quota_mb} MiB`
        : `Using ${used} (no quota set)`;
    } else {
      quotaEl.textContent = '';
    }

    for (const f of files) {
      const tr = document.createElement('tr');

      const pathTd = document.createElement('td');
      pathTd.textContent = f.path;

      const sizeTd = document.createElement('td');
      sizeTd.textContent = formatBytes(f.size);

      const modTd = document.createElement('td');
      modTd.textContent = new Date(f.modified_at * 1000).toLocaleString();

      const actTd = document.createElement('td');
      actTd.className = 'env-actions';
      if (canWrite) {
        const delBtn = document.createElement('button');
        delBtn.type = 'button';
        delBtn.className = 'env-btn-danger';
        delBtn.textContent = 'Delete';
        delBtn.addEventListener('click', () => deleteDataFile(slug, f.path));
        actTd.appendChild(delBtn);
      }

      tr.append(pathTd, sizeTd, modTd, actTd);
      tbody.appendChild(tr);
    }
  }

  async function deleteDataFile(slug, path) {
    if (!window.confirm(`Delete ${path}?`)) return;
    const errEl = document.getElementById('data-error');
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/data/${encodeDataPath(path)}`, { method: 'DELETE' });
    } catch {
      setError(errEl, 'Network error.');
      return;
    }
    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok && resp.status !== 204) {
      let message = 'Delete failed.';
      try { const b = await resp.json(); if (b && b.error) message = b.error; } catch { /* non-JSON */ }
      setError(errEl, message);
      return;
    }
    await refreshDataTab(slug);
  }

  function uploadDataFile(event) {
    event.preventDefault();
    const slug = settingsSlug;
    if (!slug) return;

    const fileInput = document.getElementById('data-file-input');
    const destInput = document.getElementById('data-dest-input');
    const restartInput = document.getElementById('data-restart-input');
    const uploadBtn = document.getElementById('data-upload-btn');
    const progressEl = document.getElementById('data-progress');
    const errEl = document.getElementById('data-error');

    const file = fileInput.files[0];
    if (!file) return;

    const dest = destInput.value.trim() || file.name;
    const restart = restartInput.checked;
    const url = `/api/apps/${encodeURIComponent(slug)}/data/${encodeDataPath(dest)}${restart ? '?restart=true' : ''}`;

    setError(errEl, '');
    uploadBtn.disabled = true;
    progressEl.value = 0;
    progressEl.hidden = false;

    const xhr = new XMLHttpRequest();
    xhr.withCredentials = true;
    xhr.open('PUT', url);
    xhr.setRequestHeader('Content-Type', 'application/octet-stream');
    xhr.setRequestHeader('X-CSRF-Token', readCookie('csrf_token'));

    xhr.upload.addEventListener('progress', e => {
      if (e.lengthComputable) {
        progressEl.value = Math.round((e.loaded / e.total) * 100);
      }
    });

    xhr.onload = () => {
      progressEl.hidden = true;
      if (xhr.status >= 400) {
        let message = `Upload failed (${xhr.status} ${xhr.statusText})`;
        try {
          const b = JSON.parse(xhr.responseText);
          if (b && b.error) {
            message = b.error;
            if (b.used_bytes !== undefined && b.quota_bytes !== undefined) {
              message += ` — quota: ${formatBytes(b.used_bytes + b.requested_bytes)} requested, ${formatBytes(b.quota_bytes)} limit`;
            }
          }
        } catch { /* non-JSON */ }
        setError(errEl, message);
      } else {
        document.getElementById('data-upload-form').reset();
        setError(errEl, '');
        refreshDataTab(slug);
      }
    };

    xhr.onloadend = () => {
      uploadBtn.disabled = false;
      progressEl.hidden = true;
    };

    xhr.send(file);
  }

  async function refreshMemberList() {
    if (!settingsSlug) return;
    const list = document.getElementById('members-list');
    list.innerHTML = '<li class="loading-placeholder">Loading…</li>';
    let resp;
    try {
      resp = await api(`/api/apps/${settingsSlug}/members`);
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
        const slug = settingsSlug;
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

  usersRefresh.addEventListener('click', () => loadUsers());
  newUserButton.addEventListener('click', openNewUserModal);
  newUserClose.addEventListener('click', closeNewUserModal);
  newUserCancel.addEventListener('click', closeNewUserModal);
  newUserForm.addEventListener('submit', submitNewUser);
  resetPwClose.addEventListener('click', closeResetPasswordModal);
  resetPwCancel.addEventListener('click', closeResetPasswordModal);
  resetPwForm.addEventListener('submit', submitResetPassword);
  auditPrev.addEventListener('click', () => loadAuditEvents(state.auditPage - 1));
  auditNext.addEventListener('click', () => loadAuditEvents(state.auditPage + 1));

  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
      if (!deployModal.hidden) {
        closeDeployModal();
      } else if (!newAppModal.hidden) {
        closeNewAppModal();
      } else if (!newUserModal.hidden) {
        closeNewUserModal();
      } else if (!resetPwModal.hidden) {
        closeResetPasswordModal();
      } else if (!document.getElementById('log-pane').hidden) {
        closeLogs();
      }
    }
  });

  document.addEventListener('click', () => {
    for (const el of document.querySelectorAll('.kebab-list')) el.hidden = true;
    for (const el of document.querySelectorAll('.kebab-menu [aria-expanded]')) el.setAttribute('aria-expanded', 'false');
  });

  newUserModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeNewUserModal();
  });
  resetPwModal.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeResetPasswordModal();
  });

  // General tab: hibernate radio + save button.
  document.querySelectorAll('input[name="hibernate-mode"]').forEach(r => {
    r.addEventListener('change', onHibernateModeChange);
  });
  document.getElementById('hibernate-save-btn').addEventListener('click', saveHibernateSettings);
  document.getElementById('scaling-save-btn').addEventListener('click', saveScalingSettings);
  document.getElementById('scaling-replicas').addEventListener('input', updateScalingCeiling);
  document.getElementById('scaling-cap').addEventListener('input', updateScalingCeiling);

  // Environment tab: add button, form submit/cancel.
  document.getElementById('env-add-btn').addEventListener('click', () => openEnvForm(null));
  document.getElementById('env-form').addEventListener('submit', submitEnvForm);
  document.getElementById('env-form-cancel').addEventListener('click', closeEnvForm);

  // Data tab: upload form submit.
  document.getElementById('data-upload-form').addEventListener('submit', uploadDataFile);

  // Visibility radio change → PATCH access level.
  document.querySelectorAll('input[name="access-level"]').forEach(radio => {
    radio.addEventListener('change', async () => {
      if (!settingsSlug) return;
      const slug = settingsSlug;
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
      const grantResp = await api(`/api/apps/${settingsSlug}/members`, {
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

  // Danger zone: typed-confirmation unlocks the Delete button.
  document.getElementById('delete-confirm').addEventListener('input', (e) => {
    const btn = document.getElementById('delete-app-btn');
    btn.disabled = e.target.value !== settingsSlug;
  });

  document.getElementById('delete-app-btn').addEventListener('click', async () => {
    if (!settingsSlug) return;
    const slug = settingsSlug;
    const btn = document.getElementById('delete-app-btn');
    const errEl = document.getElementById('delete-error');
    setError(errEl, '');
    btn.disabled = true;
    btn.textContent = 'Deleting…';

    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}`, { method: 'DELETE' });
    } catch {
      setError(errEl, 'Network error');
      btn.disabled = false;
      btn.textContent = 'Delete app';
      return;
    }

    if (resp.status === 401) { await handleUnauthorized(); return; }
    if (!resp.ok && resp.status !== 204) {
      let message = 'Delete failed.';
      try {
        const body = await resp.json();
        if (body && body.error) message = body.error;
      } catch { /* non-JSON */ }
      setError(errEl, message);
      btn.disabled = false;
      btn.textContent = 'Delete app';
      return;
    }

    router.navigate('/');
  });

  function renderNewAppSnippet(slug) {
    const origin = window.location.origin;
    const username = (state.user && state.user.username) || '<your-name>';
    const effectiveSlug = slug && slug.length > 0 ? slug : '<slug>';
    newAppSnippet.textContent =
      `shinyhub login --host ${origin} --username ${username}\n` +
      `shinyhub deploy --slug ${effectiveSlug} .`;
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
    deployState = { slug: app.slug, appName: app.name, blob: null, fileCount: 0, rejections: new Map(), xhr: null };
    deployAppName.textContent = app.name;
    renderDeployCliSnippet(app.slug);
    deployModal.hidden = false;
    deployDropzone.focus();
  }

  function renderDeployCliSnippet(slug) {
    const origin = window.location.origin;
    const username = (state.user && state.user.username) || '<your-name>';
    deployCliSnippet.textContent =
      `shinyhub login --host ${origin} --username ${username}\n` +
      `shinyhub deploy --slug ${slug} .`;
  }

  function closeDeployModal() {
    resetDeployModal();
    deployModal.hidden = true;
  }

  function renderDeploySummary(sourceName, blobSize, fileCount, rejections, rules) {
    deploySourceName.textContent = sourceName;
    deployFileCount.textContent = fileCount == null ? '—' : String(fileCount);
    deployBundleSize.textContent = formatBytes(blobSize);
    if (rejections && rejections.size > 0) {
      deployIgnoredRow.hidden = false;
      const labels = {
        rejectDataDir:     'data dir',
        rejectDatasetDir:  'dataset dir',
        rejectExtension:   'data extension',
        rejectFileSize:    'oversize file',
      };
      const parts = [];
      for (const [decision, paths] of rejections) {
        const label = labels[decision] || decision;
        parts.push(`${label}: ${paths.sort().join(', ')}`);
      }
      parts.sort();
      deployIgnoredList.textContent = parts.join('; ');
    } else {
      deployIgnoredRow.hidden = true;
    }
    deploySummary.hidden = false;

    const cap = rules && rules.maxBundleBytes > 0 ? rules.maxBundleBytes : 0;
    if (cap > 0 && blobSize > cap) {
      const mib = Math.round(cap / (1024 * 1024));
      setError(deployError, `Bundle is ${formatBytes(blobSize)} — exceeds the ${mib} MiB upload limit. Use the CLI for larger bundles.`);
      deploySubmit.disabled = true;
      return;
    }
    setError(deployError, '');
    deploySubmit.disabled = false;
  }

  async function acceptZipFile(file) {
    // Pre-built .zip — counting files would require parsing the central
    // directory, so leave the file count unknown and render it as "—".
    let rules;
    try {
      rules = await loadBundleRules();
    } catch (err) {
      console.error('loadBundleRules failed:', err);
      setError(deployError, 'Failed to load bundle rules from server.');
      return;
    }
    deployState.blob = file;
    deployState.fileCount = null;
    deployState.rejections = new Map();
    renderDeploySummary(file.name, file.size, null, null, rules);
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

  // Walks a DirectoryEntry tree, yielding { relativePath, file } for every
  // accepted file. rootEntry itself contributes no prefix — mirrors the CLI's
  // filepath.Rel(dir, path) behavior so the archive contains the folder's
  // contents, not the folder itself.
  //
  // Cache dirs (e.g. .venv, __pycache__) are silently skipped.
  // Data dirs, dataset dirs, oversize files, and data-extension files are
  // recorded in the rejections Map under the corresponding decision key.
  async function* walkFolder(rootEntry, rules, rejections) {
    const queue = [{ entry: rootEntry, path: '' }];
    while (queue.length > 0) {
      const { entry, path } = queue.shift();
      if (entry.isDirectory) {
        const reader = entry.createReader();
        const children = await readEntriesAll(reader);
        for (const child of children) {
          const childPath = path ? `${path}/${child.name}` : child.name;
          if (child.isDirectory) {
            // Inspect with size=0 — directory decisions don't depend on size.
            const decision = inspectBundleEntry(rules, childPath, 0);
            if (decision === 'skipCacheDir') {
              continue; // silent skip
            }
            if (decision !== 'accept') {
              const arr = rejections.get(decision) || [];
              arr.push(childPath);
              rejections.set(decision, arr);
              continue;
            }
          }
          queue.push({ entry: child, path: childPath });
        }
      } else if (entry.isFile) {
        const file = await fileFromEntry(entry);
        const decision = inspectBundleEntry(rules, path, file.size);
        if (decision === 'skipCacheDir') {
          continue;
        }
        if (decision !== 'accept') {
          const arr = rejections.get(decision) || [];
          arr.push(path);
          rejections.set(decision, arr);
          continue;
        }
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

    let rules;
    try {
      rules = await loadBundleRules();
    } catch (err) {
      console.error('loadBundleRules failed:', err);
      setError(deployError, 'Failed to load bundle rules from server.');
      return;
    }

    const rejections = new Map();
    const fileList = [];
    try {
      for await (const item of walkFolder(rootEntry, rules, rejections)) fileList.push(item);
    } catch (err) {
      console.error('walkFolder failed:', err);
      setError(deployError, 'Failed to read folder contents.');
      return;
    }

    if (fileList.length === 0) {
      setError(deployError, 'Folder is empty after filtering excluded paths.');
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
    deployState.rejections = rejections;
    renderDeploySummary(rootEntry.name + '/', blob.size, fileList.length, rejections, rules);
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
  emptyStateCTA.addEventListener('click', openNewAppModal);
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

  deployCliCopy.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(deployCliSnippet.textContent);
      deployCliCopy.classList.add('is-copied');
      if (deployCliCopyLabel)  deployCliCopyLabel.textContent  = 'Copied';
      if (deployCliCopyStatus) deployCliCopyStatus.textContent = 'Copied to clipboard';
      setTimeout(() => {
        deployCliCopy.classList.remove('is-copied');
        if (deployCliCopyLabel)  deployCliCopyLabel.textContent  = 'Copy';
        if (deployCliCopyStatus) deployCliCopyStatus.textContent = '';
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
    router.start();
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
    if (resp.status === 401) { await handleUnauthorized(); return; }
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

  const metrics = createMetricsController({
    intervalMs: 10000,
    onMetrics: (slug, m) => {
      // Grid card.
      const gridEl = appGrid.querySelector(`.app-metrics[data-slug="${slug}"]`);
      if (gridEl) {
        if (m.status !== 'running') {
          gridEl.textContent = '';
        } else {
          const cpu = m.cpu_percent.toFixed(1);
          const ram = m.rss_bytes >= 1 << 20
            ? (m.rss_bytes / (1 << 20)).toFixed(0) + ' MB'
            : (m.rss_bytes / 1024).toFixed(0) + ' KB';
          gridEl.textContent = `CPU ${cpu}% · ${ram} RAM`;
        }
      }
      // Detail header (only when the detail view for this slug is visible).
      const detailView = document.getElementById('app-detail-view');
      if (!detailView.hidden && location.pathname.startsWith(`/apps/${slug}`)) {
        const cpuEl = document.getElementById('app-detail-cpu');
        const ramEl = document.getElementById('app-detail-ram');
        if (m.status !== 'running') {
          cpuEl.textContent = 'CPU —';
          ramEl.textContent = 'RAM —';
        } else {
          cpuEl.textContent = `CPU ${m.cpu_percent.toFixed(1)}%`;
          const ramMb = m.rss_bytes >= 1 << 20
            ? (m.rss_bytes / (1 << 20)).toFixed(0) + ' MB'
            : (m.rss_bytes / 1024).toFixed(0) + ' KB';
          ramEl.textContent = `RAM ${ramMb}`;
        }
        renderReplicasPanel(m);
      }
    },
  });

  function renderReplicasPanel(m) {
    const listEl = document.getElementById('overview-replicas-list');
    const capEl = document.getElementById('overview-replicas-cap');
    if (!listEl || !capEl) return;
    const cap = Number(m.sessions_cap || 0);
    capEl.textContent = cap > 0 ? `(cap ${cap} sessions/replica)` : '(uncapped)';
    const replicas = Array.isArray(m.replicas) ? m.replicas : [];
    if (replicas.length === 0) {
      listEl.innerHTML = '<li class="replicas-empty">No replicas tracked yet.</li>';
      return;
    }
    listEl.innerHTML = '';
    for (const r of replicas) {
      const li = document.createElement('li');
      li.className = 'replica-row';
      const status = r.status || 'stopped';
      const sessions = Number(r.sessions ?? -1);
      const sessionsText = sessions < 0
        ? '—'
        : (cap > 0 ? `${sessions}/${cap}` : String(sessions));
      const saturated = cap > 0 && sessions >= cap;
      const cpu = (status === 'running' && typeof r.cpu_percent === 'number')
        ? `${r.cpu_percent.toFixed(1)}%`
        : '—';
      const rssBytes = Number(r.rss_bytes || 0);
      const ram = (status === 'running' && rssBytes > 0)
        ? (rssBytes >= 1 << 20
            ? (rssBytes / (1 << 20)).toFixed(0) + ' MB'
            : (rssBytes / 1024).toFixed(0) + ' KB')
        : '—';
      li.innerHTML = `
        <span class="replica-index">#${r.index}</span>
        <span class="badge badge-${status}">${status}</span>
        <span class="replica-sessions${saturated ? ' replica-sessions-saturated' : ''}" title="Active sessions${cap > 0 ? ` / cap` : ''}">${sessionsText} sessions</span>
        <span class="replica-cpu">CPU ${cpu}</span>
        <span class="replica-ram">RAM ${ram}</span>
      `;
      listEl.appendChild(li);
    }
  }

  const router = createRouter();

  function updateActiveNav(pathname) {
    for (const el of document.querySelectorAll('[data-nav]')) {
      const url = new URL(el.href);
      const active = url.pathname === pathname
        || (pathname === '/' && url.pathname === '/')
        || (pathname.startsWith('/apps/') && url.pathname === '/');
      el.classList.toggle('tab-active', active);
      if (active) el.setAttribute('aria-current', 'page'); else el.removeAttribute('aria-current');
    }
  }

  const ctx = {
    state,
    metrics,
    api,
    navigate: (p, o) => router.navigate(p, o),
    onUnauthorized: handleUnauthorized,
    canManageApp,
    renderGridVerbatim,
    updateActiveNav,
    setSettingsSlug: (slug) => { settingsSlug = slug; },
    populateGeneralTab,
    populateAccessPanel,
    refreshEnvList,
    refreshDataTab,
    loadSchedules,
    loadSharedData,
    refreshMemberList,
  };

  const appDetailMount = mountAppDetail({
    ...ctx,
    openDeployModal,
  });

  router.register('/', () => {
    const view = mountAppsGrid(ctx);
    updateActiveNav(location.pathname);
    return view;
  });
  router.register('/users', () => mountUsers({ ...ctx, loadUsers }));
  router.register('/audit-log', () => mountAuditLog({ ...ctx, loadAuditEvents }));
  router.register('/apps/:slug',      (p) => appDetailMount({ ...p, tab: 'overview' }));
  router.register('/apps/:slug/:tab', (p) => appDetailMount(p));

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
    await router.start();
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

  // --- Schedules + Shared Data ---

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
  }

  function flashToast(msg) {
    const t = document.createElement('div');
    t.className = 'toast';
    t.textContent = msg;
    document.body.appendChild(t);
    requestAnimationFrame(() => t.classList.add('show'));
    setTimeout(() => {
      t.classList.remove('show');
      setTimeout(() => t.remove(), 200);
    }, 2000);
  }

  // Cron preview helpers.
  function parseCronField(s, max) {
    const [base, stepStr] = s.split('/');
    const step = stepStr ? parseInt(stepStr, 10) : 1;
    if (!Number.isFinite(step) || step <= 0) throw new Error('invalid step');
    if (base === '*') {
      const out = [];
      for (let i = 0; i <= max; i += step) out.push(i);
      return out;
    }
    if (base.includes(',')) return base.split(',').flatMap(part => parseCronField(part, max));
    if (base.includes('-')) {
      const [a, b] = base.split('-').map(Number);
      const out = [];
      for (let i = a; i <= b; i += step) out.push(i);
      return out;
    }
    return [parseInt(base, 10)];
  }

  function nextCronFires(expr, count) {
    const fields = expr.trim().split(/\s+/);
    if (fields.length !== 5) throw new Error('expected 5 fields');
    const min = parseCronField(fields[0], 59);
    const hr  = parseCronField(fields[1], 23);
    const dom = parseCronField(fields[2], 31);
    const mon = parseCronField(fields[3], 12);
    const dow = parseCronField(fields[4], 6);
    const out = [];
    const t = new Date();
    t.setSeconds(0); t.setMilliseconds(0);
    t.setMinutes(t.getMinutes() + 1);
    for (let i = 0; i < 60 * 24 * 366 && out.length < count; i++) {
      if (min.includes(t.getMinutes()) && hr.includes(t.getHours())
          && dom.includes(t.getDate()) && mon.includes(t.getMonth() + 1)
          && dow.includes(t.getDay())) {
        out.push(new Date(t));
      }
      t.setMinutes(t.getMinutes() + 1);
    }
    return out;
  }

  function updateCronPreview(expr) {
    const el = document.getElementById('cron-preview');
    if (!el) return;
    if (!expr.trim()) { el.textContent = ''; return; }
    try {
      const fires = nextCronFires(expr, 5);
      if (fires.length === 0) {
        el.textContent = 'No fires found in next year';
      } else {
        el.textContent = 'Next: ' + fires.map(d => d.toLocaleString()).join(' · ');
      }
    } catch {
      el.textContent = 'Invalid cron expression';
    }
  }

  // Load and render the schedules list for a given app slug.
  async function loadSchedules(slug) {
    const container = document.getElementById('schedules-list');
    if (!container) return;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/schedules`);
    } catch {
      container.innerHTML = '<p class="error">Failed to load schedules.</p>';
      return;
    }
    if (!resp.ok) {
      container.innerHTML = '<p class="error">Failed to load schedules.</p>';
      return;
    }
    const schedules = await resp.json();
    if (schedules.length === 0) {
      container.innerHTML = '<p class="env-empty">No schedules configured for this app.</p>';
      return;
    }
    const rows = schedules.map(s => `
      <tr>
        <td>${escapeHtml(s.name)}</td>
        <td><code>${escapeHtml(s.cron_expr)}</code></td>
        <td>${escapeHtml((s.command || []).join(' '))}</td>
        <td><span class="status-pill ${s.enabled ? 'status-on' : 'status-off'}">${s.enabled ? 'on' : 'off'}</span></td>
        <td>${s.next_fire ? new Date(s.next_fire).toLocaleString() : '—'}</td>
        <td class="table-actions">
          <button type="button" class="env-btn-secondary" data-action="history" data-id="${s.id}">History</button>
          <button type="button" class="env-btn-secondary" data-action="run" data-id="${s.id}">Run now</button>
          <button type="button" class="env-btn-secondary" data-action="edit" data-schedule='${escapeHtml(JSON.stringify(s))}'>Edit</button>
          <button type="button" class="btn-danger-sm" data-action="delete" data-id="${s.id}" data-name="${escapeHtml(s.name)}">Delete</button>
        </td>
      </tr>`).join('');
    container.innerHTML = `
      <table>
        <thead><tr>
          <th>Name</th><th>Cron</th><th>Command</th><th>Status</th><th>Next fire</th><th></th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>`;

    container.querySelectorAll('[data-action]').forEach(btn => {
      btn.addEventListener('click', () => {
        const action = btn.dataset.action;
        const id = parseInt(btn.dataset.id, 10);
        if (action === 'run') runScheduleNow(slug, id);
        else if (action === 'delete') deleteSchedule(slug, id, btn.dataset.name);
        else if (action === 'history') openScheduleHistory(slug, id);
        else if (action === 'edit') {
          const s = JSON.parse(btn.dataset.schedule);
          openScheduleForm(slug, s);
        }
      });
    });
  }

  // Load and render the shared-data mounts list.
  async function loadSharedData(slug) {
    const container = document.getElementById('shared-data-list');
    if (!container) return;
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/shared-data`);
    } catch {
      container.innerHTML = '<p class="error">Failed to load shared data mounts.</p>';
      return;
    }
    if (!resp.ok) {
      container.innerHTML = '<p class="error">Failed to load shared data mounts.</p>';
      return;
    }
    const mounts = await resp.json();
    if (mounts.length === 0) {
      container.innerHTML = '<p class="env-empty">No shared data mounts configured.</p>';
      return;
    }
    const items = mounts.map(m => `
      <li>
        <span>data/shared/<strong>${escapeHtml(m.source_slug)}</strong>/</span>
        <button type="button" class="btn-danger-sm" data-action="revoke" data-slug="${escapeHtml(m.source_slug)}">Unmount</button>
      </li>`).join('');
    container.innerHTML = `<ul class="shared-data-list">${items}</ul>`;

    container.querySelectorAll('[data-action="revoke"]').forEach(btn => {
      btn.addEventListener('click', async () => {
        const sourceSlug = btn.dataset.slug;
        if (!confirm(`Unmount data from "${sourceSlug}"?`)) return;
        const r = await api(`/api/apps/${encodeURIComponent(slug)}/shared-data/${encodeURIComponent(sourceSlug)}`, {
          method: 'DELETE',
        });
        if (!r.ok) { flashToast('Unmount failed: ' + await r.text()); return; }
        await loadSharedData(slug);
      });
    });
  }

  // Open the schedule add/edit form modal.
  function openScheduleForm(slug, existing) {
    const modal = document.getElementById('schedule-form-modal');
    const title = document.getElementById('schedule-form-title');
    const form = document.getElementById('schedule-form');
    const errEl = document.getElementById('schedule-form-error');
    if (!modal || !form) return;

    title.textContent = existing ? 'Edit schedule' : 'Add schedule';
    form.reset();

    if (existing) {
      document.getElementById('sched-name').value = existing.name || '';
      document.getElementById('sched-cron').value = existing.cron_expr || '';
      document.getElementById('sched-command').value = (existing.command || []).join('\n');
      document.getElementById('sched-timeout').value = existing.timeout_seconds || 3600;
      document.getElementById('sched-overlap').value = existing.overlap_policy || 'skip';
      document.getElementById('sched-missed').value = existing.missed_policy || 'skip';
      document.getElementById('sched-enabled').checked = existing.enabled !== false;
    }
    updateCronPreview(document.getElementById('sched-cron').value);

    // Replace the submit handler to capture the current slug/existing binding.
    const newForm = form.cloneNode(true);
    form.parentNode.replaceChild(newForm, form);
    setError(document.getElementById('schedule-form-error'), '');

    // Re-attach cron preview input listener and cancel button.
    newForm.querySelector('#sched-cron').addEventListener('input', e => updateCronPreview(e.target.value));
    newForm.querySelector('#schedule-form-cancel')?.addEventListener('click', closeScheduleForm);

    newForm.addEventListener('submit', async e => {
      e.preventDefault();
      const name = newForm.querySelector('#sched-name').value.trim();
      const cronExpr = newForm.querySelector('#sched-cron').value.trim();
      const command = newForm.querySelector('#sched-command').value.split('\n').map(l => l.trim()).filter(Boolean);
      const timeoutSeconds = parseInt(newForm.querySelector('#sched-timeout').value, 10);
      const overlapPolicy = newForm.querySelector('#sched-overlap').value;
      const missedPolicy = newForm.querySelector('#sched-missed').value;
      const enabled = newForm.querySelector('#sched-enabled').checked;

      const newErrEl = document.getElementById('schedule-form-error');
      setError(newErrEl, '');

      const body = JSON.stringify({name, cron_expr: cronExpr, command, timeout_seconds: timeoutSeconds, overlap_policy: overlapPolicy, missed_policy: missedPolicy, enabled});
      let r;
      if (existing) {
        r = await api(`/api/apps/${encodeURIComponent(slug)}/schedules/${existing.id}`, {
          method: 'PATCH',
          headers: {'Content-Type': 'application/json'},
          body,
        });
      } else {
        r = await api(`/api/apps/${encodeURIComponent(slug)}/schedules`, {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body,
        });
      }
      if (!r.ok) {
        const msg = await r.text().catch(() => 'Request failed');
        setError(newErrEl, msg);
        return;
      }
      closeScheduleForm();
      await loadSchedules(slug);
    });

    modal.hidden = false;
  }

  function closeScheduleForm() {
    const modal = document.getElementById('schedule-form-modal');
    if (modal) modal.hidden = true;
  }

  async function runScheduleNow(slug, id) {
    const r = await api(`/api/apps/${encodeURIComponent(slug)}/schedules/${id}/run`, {method: 'POST'});
    if (!r.ok) {
      flashToast('Run failed: ' + await r.text().catch(() => 'error'));
      return;
    }
    flashToast('Schedule started.');
    await loadSchedules(slug);
  }

  async function deleteSchedule(slug, id, name) {
    if (!confirm(`Delete schedule "${name}"?`)) return;
    const r = await api(`/api/apps/${encodeURIComponent(slug)}/schedules/${id}`, {method: 'DELETE'});
    if (!r.ok) {
      flashToast('Delete failed: ' + await r.text().catch(() => 'error'));
      return;
    }
    await loadSchedules(slug);
  }

  // Open the log pane showing the run history for a schedule.
  async function openScheduleHistory(slug, schedID) {
    let resp;
    try {
      resp = await api(`/api/apps/${encodeURIComponent(slug)}/schedules/${schedID}/runs`);
    } catch {
      flashToast('Failed to load run history.');
      return;
    }
    if (!resp.ok) {
      flashToast('Failed to load run history: ' + await resp.text().catch(() => 'error'));
      return;
    }
    const runs = await resp.json();

    // Reuse existing log pane infrastructure.
    if (activeEventSource) { activeEventSource.close(); activeEventSource = null; }
    logPaneTitle.textContent = 'Run history';
    logPaneBody.innerHTML = '';
    setHidden(logPane, false);

    if (!runs || runs.length === 0) {
      logPaneBody.textContent = 'No runs yet.';
      return;
    }

    const ul = document.createElement('ul');
    ul.className = 'run-history-list';
    runs.forEach(run => {
      const li = document.createElement('li');
      const started = run.StartedAt ? new Date(run.StartedAt).toLocaleString() : '—';
      const status = run.Status || '—';
      const exit = run.ExitCode != null ? ` · exit ${run.ExitCode}` : '';
      li.innerHTML = `<button type="button" class="run-history-btn">${escapeHtml(started)} · <strong>${escapeHtml(status)}</strong>${escapeHtml(exit)}</button>`;
      li.querySelector('button').addEventListener('click', () => {
        openScheduleRunLogs(slug, schedID, run.ID);
      });
      ul.appendChild(li);
    });
    logPaneBody.appendChild(ul);
  }

  // Stream logs for a specific schedule run into the log pane.
  function openScheduleRunLogs(slug, schedID, runID) {
    if (activeEventSource) { activeEventSource.close(); activeEventSource = null; }
    logPaneTitle.textContent = `Run #${runID} logs`;
    logPaneBody.textContent = '';
    setHidden(logPane, false);

    const url = `/api/apps/${encodeURIComponent(slug)}/schedules/${schedID}/runs/${runID}/logs`;
    const es = new EventSource(url);
    activeEventSource = es;
    es.onmessage = e => {
      const line = document.createElement('div');
      line.textContent = e.data;
      logPaneBody.appendChild(line);
      logPaneBody.scrollTop = logPaneBody.scrollHeight;
    };
    es.onerror = () => { es.close(); activeEventSource = null; };
  }

  // Wire the Schedules and Shared Data buttons.
  document.getElementById('schedules-add-btn')?.addEventListener('click', () => {
    if (settingsSlug) openScheduleForm(settingsSlug, null);
  });

  document.getElementById('shared-data-add-btn')?.addEventListener('click', async () => {
    if (!settingsSlug) return;
    const sourceSlug = (prompt('Source app slug to mount read-only:') || '').trim();
    if (!sourceSlug) return;
    const r = await api(`/api/apps/${encodeURIComponent(settingsSlug)}/shared-data`, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({source_slug: sourceSlug}),
    });
    if (!r.ok) { flashToast('Mount failed: ' + await r.text().catch(() => 'error')); return; }
    await loadSharedData(settingsSlug);
  });

  // Wire the schedule form close button and overlay-click dismiss.
  document.getElementById('schedule-form-close')?.addEventListener('click', closeScheduleForm);
  document.getElementById('schedule-form-modal')?.addEventListener('click', e => {
    if (e.target === e.currentTarget) closeScheduleForm();
  });

  initialize();
});
