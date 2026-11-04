import { userRowCaps, userRolePresentation, RESERVED_USER_HINT } from './user-row.js';
import { createPersonActions } from './person-actions.js';
import { createSupportSessionAction } from './support-session-settings.js';
import { applyPeopleOnboarding, createInvitationList } from './people-onboarding.js';
import { focusedKey, restoreFocus } from './focus-restore.js';
import { requestJSON } from './json-request.js';
import { bindFilterBar, createListState, setFeedback } from './page-components.js';

const roles = ['viewer', 'developer', 'operator', 'admin'];
const roleLabel = role => role.charAt(0).toUpperCase() + role.slice(1);
const roleHelp = {
  viewer: 'Can use apps they have access to.',
  developer: 'Can create apps and manage their own apps.',
  operator: 'Can access and manage all apps across ShinyHub.',
  admin: 'Has full access across ShinyHub, including people and server settings.',
};

// The directory owns its requests, inventory, filters and row actions only
// while Identity is mounted. No response may repopulate a departed session.
export function createPeopleDirectory({document, state, api, onUnauthorized: handleUnauthorized,
  navigate, flashToast, confirm, makeCopyButton, openResetPasswordModal,
  openSupportSessionModal, onReplaced, onAccessLost = () => {}}) {
  const get = id => document.getElementById(id);
  const usersBody = get('users-body'), usersTable = get('users-table'), usersError = get('users-error');
  const search = get('people-search'), filter = get('people-role-filter');
  const noMatches = get('people-no-matches'), empty = get('people-empty'), clearFilters = get('people-clear');
  const status = get('people-status'), loading = get('people-loading');
  const list = createListState({content:usersTable,loading,error:usersError,empty,noMatches});
  let active = false, session, userID, peopleInventory = null, peopleOnboarding = null;
  let personActionMenus = [], loadPromise = null, generation = 0;
  const requests = new Set(), busy = new Set(), rowErrors = new Map();
  const roleDrafts = new Map();
  const invitationList = createInvitationList({document, api: request, onUnauthorized: handleUnauthorized,
    confirm, announce: message => flashToast(message, 'success'), onReplaced,
    canInvite: () => peopleOnboarding?.local === true});
  const current = () => active && (state.sessionIdentity ?? state.user) === session
    && state.user?.id === userID && state.user?.role === 'admin';
  const setError = (node,message) => setFeedback(node,message,{error:true});
  function clear() {
    personActionMenus.forEach(menu => menu.close()); personActionMenus = [];
    peopleInventory = null; peopleOnboarding = null; usersBody.replaceChildren();
    get('people-count').textContent = ''; status.textContent = ''; rowErrors.clear(); busy.clear(); roleDrafts.clear();
    list.clear();
    get('people-invitations').hidden = true; get('people-invitations-list').replaceChildren();
    applyPeopleOnboarding(document, null); setError(usersError, '');
  }
  function stop() {
    active = false; generation++; invitationList.clear();
    for (const controller of requests) controller.abort(); requests.clear(); loadPromise = null;
    clear(); document.removeEventListener('session-capabilities-change', checkAccess);
  }
  function checkAccess() {
    if (current()) return true;
    if (active) { stop(); onAccessLost(); navigate('/apps', {replace: true}); }
    return false;
  }
  async function request(path, options = {}) {
    if (!checkAccess()) throw new Error('Access changed');
    const controller = new AbortController(); requests.add(controller);
    try {
      const result = await requestJSON(api, path, {...options, signal: controller.signal});
      if (!checkAccess()) throw new Error('Access changed');
      if (result.status === 401) { stop(); handleUnauthorized(); throw new Error('Session ended'); }
      if (result.status === 403) { stop(); onAccessLost(); navigate('/apps', {replace: true}); throw new Error('Access denied'); }
      return result;
    } finally { requests.delete(controller); }
  }
  async function load() {
    if (!checkAccess() || busy.size) return;
    if (loadPromise) return loadPromise;
    const version = generation;
    list.pending({retain:peopleInventory !== null});
    status.textContent = peopleInventory === null ? 'Loading people…' : 'Refreshing people…';
    const task = (async () => {
      try {
        const response = await request('/api/users');
        if (!response.ok) throw new Error();
        const payload = await response.json();
        if (!checkAccess() || version !== generation) return;
        const users = Array.isArray(payload) ? payload : payload?.items;
        if (!Array.isArray(users) || users.some(u => !u || !Number.isInteger(u.id) || typeof u.username !== 'string' || !roles.includes(u.role))) throw new Error();
        peopleOnboarding = payload.onboarding || null;
        state.supportSessions = payload.support_sessions || {enabled: null};
        applyPeopleOnboarding(document, peopleOnboarding, get('identity-people-panel').hidden);
        setError(usersError, ''); renderUsers(users.filter(u => u.principal_type !== 'service_account'));
        invitationList.load(); status.textContent = 'People refreshed.';
      } catch {
        if (!current() || version !== generation) return;
        list.fail(peopleInventory === null
          ? 'Could not load people. Check your connection and select Refresh to try again.'
          : 'Could not refresh people. Showing the last loaded list. Select Refresh to try again.',{retain:peopleInventory !== null});
        status.textContent = '';
      } finally {
        if (current() && version === generation) list.finish();
        if (loadPromise === task) loadPromise = null;
      }
    })();
    loadPromise = task;
    return task;
  }
  function renderUsers(users) {
    if (!current() || !users) return;
    personActionMenus.forEach(menu => menu.close());
    personActionMenus = [];
    const focus = focusedKey(usersBody);
    peopleInventory = users;
    const query = document.getElementById('people-search').value.trim().toLocaleLowerCase();
    const roleFilter = document.getElementById('people-role-filter').value;
    const filtered = users.filter(user => (!roleFilter || user.role === roleFilter)
      && `${user.username} ${user.display_name || ''}`.toLocaleLowerCase().includes(query));
    document.getElementById('people-count').textContent = query || roleFilter
      ? `${filtered.length} of ${users.length} people` : `${users.length} ${users.length === 1 ? 'person' : 'people'}`;
    usersBody.textContent = '';
    list.results({total:users.length,visible:filtered.length});
    clearFilters.hidden = !filtered.length || (!query && !roleFilter);
    const selfId = state.user ? state.user.id : null;

    for (const u of filtered) {
      const tr = document.createElement('tr');
      const caps = userRowCaps(u, selfId);

      // Username
      const nameCell = document.createElement('td');
      nameCell.className = 'users-username';
      const usernameText = document.createElement('span');
      usernameText.className = 'person-name';
      usernameText.textContent = u.display_name || u.username;
      nameCell.appendChild(usernameText);
      if (caps.isSelf) {
        const tag = document.createElement('span');
        tag.className = 'users-self-tag';
        tag.textContent = 'you';
        nameCell.appendChild(tag);
      }
      if (caps.reserved) {
        const tag = document.createElement('span');
        tag.className = 'users-reserved-tag';
        tag.textContent = 'token';
        tag.title = RESERVED_USER_HINT;
        nameCell.appendChild(tag);
      }
      const copy = makeCopyButton(u.username, `Copy username ${u.username}`);
      copy.dataset.focusKey = `person:${u.id}:copy`;
      nameCell.appendChild(copy);
      if (typeof u.has_local_password === 'boolean') {
        const signIn = document.createElement('span');
        signIn.className = 'users-display-name';
        signIn.textContent = `${u.display_name ? `@${u.username} · ` : ''}${u.has_local_password ? (peopleOnboarding?.local === false ? 'Password sign-in disabled' : 'Password sign-in') : 'Single sign-on'}`;
        nameCell.appendChild(signIn);
      }
      // Older responses may omit the sign-in metadata.
      if (u.display_name && typeof u.has_local_password !== 'boolean') {
        const dn = document.createElement('span');
        dn.className = 'users-display-name';
        dn.textContent = `@${u.username}`;
        nameCell.appendChild(dn);
      }
      tr.appendChild(nameCell);

      // Role (editable select; disabled for self)
      const roleCell = document.createElement('td');
      const select = document.createElement('select');
      select.className = 'users-row-role';
      select.dataset.focusKey = `person:${u.id}:role`;
      const rolePresentation = userRolePresentation(u);
      select.setAttribute('aria-label', `Role for ${u.username}`);
      const automatic = document.createElement('option');
      automatic.value = '';
      automatic.textContent = rolePresentation.automaticLabel;
      select.appendChild(automatic);
      for (const r of ['viewer', 'developer', 'operator', 'admin']) {
        const opt = document.createElement('option');
        opt.value = r;
        opt.textContent = r.charAt(0).toUpperCase() + r.slice(1);
        select.appendChild(opt);
      }
      const draft = roleDrafts.get(u.id);
      select.value = draft ? draft.value : rolePresentation.selected;
      if (draft) select.setAttribute('aria-describedby', `person-role-review-${u.id}`);
      select.dataset.previous = rolePresentation.selected;
      if (!caps.canChangeRole) {
        select.disabled = true;
        if (caps.roleHint) select.title = caps.roleHint;
      } else {
        select.addEventListener('change', () => proposeRole(u, select.value));
        select.addEventListener('keydown', event => {
          if (event.key === 'Escape' && roleDrafts.has(u.id)) { event.preventDefault(); cancelRole(u.id); }
        });
      }
      if (caps.isSelf) {
        const ownRole = document.createElement('strong');
        ownRole.className = 'person-own-role'; ownRole.textContent = roleLabel(u.role);
        roleCell.appendChild(ownRole);
      } else {
        select.disabled = busy.has(u.id) || !caps.canChangeRole;
        roleCell.appendChild(select);
      }
      const roleSource = document.createElement('span');
      roleSource.className = 'users-display-name';
      roleSource.textContent = draft ? 'Unsaved change' : rolePresentation.sourceLabel;
      if (caps.isSelf) roleSource.title = 'Your role can be changed by another admin.';
      roleCell.appendChild(roleSource);
      if (draft) roleCell.appendChild(renderRoleReview(u, draft));
      tr.appendChild(roleCell);

      // Created
      const createdCell = document.createElement('td');
      createdCell.className = 'users-created';
      const ts = new Date(u.created_at);
      createdCell.textContent = Number.isFinite(ts.getTime()) ? ts.toLocaleDateString(undefined, {year: 'numeric', month: 'short', day: 'numeric'}) : 'Not reported';
      if (Number.isFinite(ts.getTime())) createdCell.title = ts.toLocaleString(undefined, {dateStyle: 'medium', timeStyle: 'long'});
      tr.appendChild(createdCell);

      // Actions
      const actionsCell = document.createElement('td');
      const actions = document.createElement('div');
      actions.className = 'users-row-actions';

      const supportAction = createSupportSessionAction({
        document, user: u, selfId, enabled: state.supportSessions.enabled,
        onStart: () => openSupportSessionModal(u),
      });
      if (supportAction) actions.appendChild(supportAction);

      const resetBtn = document.createElement('button');
      resetBtn.type = 'button';
      resetBtn.className = 'btn-row';
      resetBtn.textContent = 'Reset password';
      resetBtn.setAttribute('aria-label', `Reset password for ${u.username}`);
      if (!caps.canResetPassword || u.has_local_password === false || peopleOnboarding?.local === false) {
        resetBtn.disabled = true;
        resetBtn.title = caps.reserved ? RESERVED_USER_HINT : 'Password is managed by the sign-in provider';
        resetBtn.hidden = !caps.reserved;
      } else {
        resetBtn.addEventListener('click', () => openResetPasswordModal(u));
      }
      actions.appendChild(resetBtn);

      const revokeBtn = document.createElement('button');
      revokeBtn.type = 'button';
      revokeBtn.className = 'btn-row btn-row-warn';
      revokeBtn.textContent = 'Sign out everywhere';
      revokeBtn.setAttribute('aria-label', `Sign out ${u.username} everywhere`);
      if (!caps.canRevokeSessions) {
        revokeBtn.disabled = true;
        if (caps.revokeSessionsHint) revokeBtn.title = caps.revokeSessionsHint;
      } else {
        revokeBtn.addEventListener('click', () => revokeUserSessions(u.id, u.username, revokeBtn));
      }
      actions.appendChild(revokeBtn);

      const delBtn = document.createElement('button');
      delBtn.type = 'button';
      delBtn.className = 'btn-row btn-row-danger';
      delBtn.textContent = 'Delete person';
      delBtn.setAttribute('aria-label', `Delete user ${u.username}`);
      if (!caps.canDelete) {
        delBtn.disabled = true;
        if (caps.deleteHint) delBtn.title = caps.deleteHint;
      } else {
        delBtn.addEventListener('click', () => deleteUser(u.id, u.username, delBtn));
      }
      actions.appendChild(delBtn);

      const personMenu = createPersonActions(document, u.username, [...actions.children]);
      personMenu.element.querySelector('button').dataset.focusKey = `person:${u.id}:actions`;
      for (const button of personMenu.element.querySelectorAll('button')) button.disabled = busy.has(u.id);
      personActionMenus.push(personMenu);
      actionsCell.appendChild(personMenu.element);
      tr.appendChild(actionsCell);

      if (rowErrors.has(u.id)) {
        const error = document.createElement('p'); error.className = 'person-row-error';
        error.setAttribute('role', 'alert'); error.textContent = rowErrors.get(u.id);
        roleCell.appendChild(error);
      }
      usersBody.appendChild(tr);
    }
    if (focus && !restoreFocus(usersBody, focus)) search.focus({preventScroll: true});
  }

  function cancelRole(id) {
    roleDrafts.delete(id); rowErrors.delete(id); renderUsers(peopleInventory);
    usersBody.querySelector(`[data-focus-key="person:${id}:role"]`)?.focus({preventScroll: true});
  }
  function renderRoleReview(user, draft) {
    const review = document.createElement('div'); review.className = 'person-role-review';
    review.id = `person-role-review-${user.id}`;
    review.setAttribute('aria-busy', String(busy.has(user.id)));
    const summary = document.createElement('p'); summary.className = 'person-role-plan';
    summary.setAttribute('role', 'status');
    const description = document.createElement('p');
    const effective = draft.value || draft.preview?.role;
    summary.textContent = effective
      ? `${roleLabel(user.role)} to ${roleLabel(effective)}${draft.value ? '' : ' · Automatic'}`
      : 'Checking automatic role…';
    description.textContent = draft.error || (effective ? roleHelp[effective] : 'Loading the role from this person’s saved groups and the server default.');
    if (!draft.value && draft.preview) description.textContent += draft.preview.source === 'sso'
      ? ' This follows their sign-in group rules.' : ' This follows the server default.';
    if (effective === 'operator' || effective === 'admin') description.className = 'person-role-consequence';
    const actions = document.createElement('div'); actions.className = 'person-role-actions';
    const save = document.createElement('button'); save.type = 'button'; save.className = 'btn-primary';
    save.textContent = busy.has(user.id) ? 'Saving…' : effective && roles.indexOf(effective) > roles.indexOf(user.role)
      ? `Grant ${roleLabel(effective)} access` : 'Save role';
    save.setAttribute('aria-label', `Save role for ${user.username}`);
    save.dataset.focusKey = `person:${user.id}:save-role`;
    save.disabled = !effective || Boolean(draft.error);
    save.setAttribute('aria-disabled', String(busy.has(user.id)));
    save.addEventListener('click', () => updateUserRole(user.id, user.username, draft));
    const cancel = document.createElement('button'); cancel.type = 'button'; cancel.className = 'ov-btn'; cancel.textContent = 'Cancel';
    cancel.disabled = busy.has(user.id); cancel.addEventListener('click', () => cancelRole(user.id));
    if (draft.error) {
      const retry = document.createElement('button'); retry.type = 'button'; retry.className = 'ov-btn'; retry.textContent = 'Retry preview';
      retry.addEventListener('click', () => proposeRole(user, '')); actions.appendChild(retry);
    }
    actions.append(save, cancel); review.append(summary, description, actions);
    review.addEventListener('keydown', event => {
      if (event.key === 'Escape' && !busy.has(user.id)) { event.preventDefault(); cancelRole(user.id); }
    });
    return review;
  }
  async function proposeRole(user, value) {
    if (!checkAccess() || busy.has(user.id)) return;
    if (value === userRolePresentation(user).selected) { cancelRole(user.id); return; }
    const draft = {value}; roleDrafts.set(user.id, draft); rowErrors.delete(user.id); renderUsers(peopleInventory);
    if (value) return;
    try {
      const response = await request(`/api/users/${user.id}/automatic-role`);
      if (!response.ok) throw new Error();
      const preview = await response.json();
      if (!roles.includes(preview?.role) || !['sso', 'default'].includes(preview.source)) throw new Error();
      if (!current() || roleDrafts.get(user.id) !== draft) return;
      draft.preview = preview;
    } catch {
      if (!current() || roleDrafts.get(user.id) !== draft) return;
      draft.error = 'Could not check the automatic role. Retry the preview or cancel.';
    }
    renderUsers(peopleInventory);
  }


  async function act(user, operation, button) {
    if (!checkAccess() || busy.has(user.id)) return;
    const signingOut = operation === 'signout';
    if (!confirm(signingOut
      ? `Sign out "${user.username}" everywhere? Every active session ends immediately; they will need to log in again.`
      : `Delete person "${user.username}"? This cannot be undone.`)) return;
    busy.add(user.id); rowErrors.delete(user.id); button.disabled = true;
    // A read started before a mutation must never overwrite its result.
    generation++; loading.hidden = true; usersTable.removeAttribute('aria-busy'); loadPromise = null;
    try {
      const response = await request(signingOut ? `/api/users/${user.id}/revoke-sessions` : `/api/users/${user.id}`,
        {method: signingOut ? 'POST' : 'DELETE'});
      if (!response.ok) throw Object.assign(new Error(await failureMessage(response, signingOut ? 'Could not sign out this person.' : 'Could not delete this person.')), {safe: true});
      if (!checkAccess()) return;
      if (signingOut && user.id === state.user.id) { stop(); handleUnauthorized(); return; }
      if (!signingOut) peopleInventory = peopleInventory.filter(u => u.id !== user.id);
      flashToast(`${user.username} ${signingOut ? 'signed out everywhere' : 'deleted'}.`, 'success');
      status.textContent = `${user.username} ${signingOut ? 'signed out everywhere' : 'deleted'}.`;
    } catch (failure) {
      if (current()) rowErrors.set(user.id, `${failure.safe ? failure.message : 'Could not confirm the action. Check your connection.'} Check the current state with Refresh before retrying.`);
    } finally {
      busy.delete(user.id);
      if (current()) { renderUsers(peopleInventory); get('users-refresh').focus(); }
    }
  }
  async function failureMessage(response, fallback) {
    try { const body = await response.json(); if (typeof body?.error === 'string' && body.error) return body.error.replace(/^./, c => c.toUpperCase()); } catch {}
    return fallback;
  }
  async function updateUserRole(id, username, draft) {
    if (!checkAccess() || busy.has(id)) return;
    if (roleDrafts.get(id) !== draft || (!draft.value && !draft.preview) || draft.error) return;
    const newRole = draft.value;
    busy.add(id); rowErrors.delete(id); generation++; loading.hidden = true; usersTable.removeAttribute('aria-busy'); loadPromise = null;
    renderUsers(peopleInventory);
    try {
      const response = await request(`/api/users/${id}`, {method: 'PATCH', body: JSON.stringify({role: newRole,
        ...(!newRole ? {expected_role: draft.preview.role} : {})})});
      if (!response.ok) throw Object.assign(new Error(await failureMessage(response, 'Could not update this role.')), {safe: true});
      const changed = await response.json();
      if (!checkAccess()) return;
      if (changed?.id !== id || !roles.includes(changed.role)) throw new Error('The role could not be confirmed. Select Refresh to check it.');
      peopleInventory = peopleInventory.map(u => u.id === id ? {...u, ...changed} : u);
      roleDrafts.delete(id);
      flashToast(`${username} now has the ${roleLabel(changed.role)} role.`, 'success');
      status.textContent = `Role updated for ${username}: ${roleLabel(changed.role)}.`;
    } catch (failure) {
      roleDrafts.delete(id);
      if (current()) rowErrors.set(id, `${failure.safe ? failure.message : 'Could not confirm the role change. Check your connection.'} Select Refresh to check the role, then try again.`);
    } finally {
      busy.delete(id);
      if (current()) {
        renderUsers(peopleInventory);
        usersBody.querySelector(`[data-focus-key="person:${id}:role"]`)?.focus({preventScroll: true});
      }
    }
  }
  function revokeUserSessions(id, username, button) { return act({id, username}, 'signout', button); }
  function deleteUser(id, username, button) { return act({id, username}, 'delete', button); }
  function filterRows() { if (current() && peopleInventory) renderUsers(peopleInventory); }
  bindFilterBar({root:search.closest('.people-filters'),fields:{search,role:filter},
    clearButtons:[clearFilters,get('people-no-matches-clear')],onChange:filterRows});
  return {
    load, current, request, refreshInvitations: () => invitationList.load(),
    applyOnboarding: services => applyPeopleOnboarding(document, peopleOnboarding, services),
    start() {
      stop(); active = true; session = state.sessionIdentity ?? state.user; userID = state.user?.id;
      search.value = ''; filter.value = ''; search.disabled = filter.disabled = false;
      document.addEventListener('session-capabilities-change', checkAccess); return load();
    }, stop,
  };
}
