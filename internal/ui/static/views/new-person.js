import {requestJSON} from './json-request.js';

export function createNewPersonController({ document, api, origin, activate, release, onCreated, onUnauthorized, copy, requestOptions = {} }) {
  const get = id => document.getElementById(id);
  const modal = get('new-user-modal');
  const form = get('new-user-form');
  const username = get('new-user-username');
  const error = get('new-user-error');
  const success = get('new-user-success');
  const submit = get('new-user-submit');
  const check = get('new-user-check'), view = get('new-user-view-invitations');
  let busy = false, generation = 0, uncertain = null, inFlight = null;
  function showError(message) {
    error.textContent = message;
    error.hidden = !message;
  }
  function setBusy(value) {
    busy = value;
    form.setAttribute('aria-busy', String(value));
    for (const input of form.elements) input.disabled = value;
    username.disabled = value || Boolean(uncertain);
    get('new-user-role').disabled = value || Boolean(uncertain);
    get('new-user-close').disabled = value;
    submit.disabled = value || Boolean(uncertain);
    submit.hidden = Boolean(uncertain);
    submit.textContent = value && !uncertain ? 'Creating invitation…' : 'Create invitation';
    check.textContent = value && uncertain ? 'Checking…' : 'Check invitation status';
  }
  function open() {
    if (busy) return;
    generation++;
    form.reset();
    form.hidden = false;
    success.hidden = true;
    get('new-user-heading').textContent = 'Invite person';
    get('new-user-snippet').textContent = '';
    get('new-user-snippet-status').textContent = '';
    showError('');
    check.hidden = view.hidden = true;
    if (uncertain) {
      username.value = uncertain.username;
      form.elements.namedItem('role').value = uncertain.role;
      showUncertain();
    }
    setBusy(false);
    modal.hidden = false;
    activate();
    username.focus();
  }
  function close() {
    if (busy) return;
    modal.hidden = true;
    form.reset();
    get('new-user-snippet').textContent = '';
    release();
  }
  function showUncertain() {
    showError('The request ended before ShinyHub could confirm the invitation. It may have been created. Check its status before trying again.');
    check.hidden = false; view.hidden = true;
  }
  async function request(path, options = {}) {
    const controller = new AbortController(); inFlight = controller;
    try { return await requestJSON(api, path, {...options, signal: controller.signal}, requestOptions); }
    finally { if (inFlight === controller) inFlight = null; }
  }
  function showInvitation(result, replaced = false) {
    if (!result.token || !result.invitation) throw new Error('Invalid invitation response');
    uncertain = null; check.hidden = view.hidden = true;
    const wasHidden = modal.hidden;
    modal.hidden = false;
    form.hidden = true;
    success.hidden = false;
    get('new-user-heading').textContent = replaced ? 'Invitation replaced' : 'Invitation created';
    get('new-user-success-heading').textContent = `Invite ${result.invitation.username || username.value.trim()}`;
    const role = result.invitation.role || form.elements.namedItem('role').value;
    get('new-user-result-detail').textContent = `${role.charAt(0).toUpperCase() + role.slice(1)} · Expires ${new Date(result.invitation.expires_at).toLocaleString()}${replaced ? '. The previous link no longer works.' : '.'}`;
    get('new-user-snippet').textContent = `${origin}/invite#${result.token}`;
    get('new-user-snippet-status').textContent = '';
    if (wasHidden) activate();
    get('new-user-success-heading').focus();
  }
  async function create(event) {
    event.preventDefault();
    if (busy || uncertain || form.hidden) return;
    username.value = username.value.trim();
    if (!form.reportValidity()) return;
    const current = generation;
    const name = username.value;
    const role = form.elements.namedItem('role').value;
    const payload = { username: name, role };
    showError('');
    setBusy(true);
    try {
      const response = await request('/api/user-invitations', { method: 'POST', body: JSON.stringify(payload) });
      if (current !== generation) return;
      if (response.status === 401) { await onUnauthorized(); return; }
      if (response.status >= 500 || response.status === 408) throw new Error('Creation could not be confirmed');
      if (!response.ok) {
        let message = 'Could not create the invitation. Try again.';
        try { const body = await response.json(); if (body?.error) message = body.error; } catch {}
        showError(message);
        return;
      }
      const result = await response.json();
      if (current !== generation) return;
      showInvitation(result);
      onCreated();
    } catch {
      if (current !== generation) return;
      uncertain = payload; showUncertain();
    } finally {
      if (current === generation) setBusy(false);
    }
  }
  async function checkStatus() {
    if (busy || !uncertain) return;
    const current = generation, pending = uncertain;
    showError(''); setBusy(true);
    try {
      const response = await request('/api/user-invitations', {cache: 'no-store'});
      if (current !== generation) return;
      if (response.status === 401) { await onUnauthorized(); return; }
      if (!response.ok) throw new Error();
      const body = await response.json();
      if (current !== generation) return;
      if (!Array.isArray(body?.items) || body.items.some(item => typeof item?.username !== 'string')) throw new Error();
      const existing = body.items.find(item => item.username === pending.username);
      if (existing) {
        showError(`An invitation for ${pending.username} already exists. Open Pending invitations and replace its link to get one you can share.`);
        check.hidden = true; view.hidden = false;
      } else {
        uncertain = null; check.hidden = true;
        showError(`No pending invitation was found for ${pending.username}. You can try creating it again.`);
      }
      onCreated();
    } catch {
      if (current !== generation) return;
      showError('Could not check invitation status. Check your connection and try again, or close this dialog and refresh People.');
    } finally { if (current === generation) setBusy(false); }
  }
  check.addEventListener('click', checkStatus);
  view.addEventListener('click', async () => {
    if (busy) return;
    const current = generation;
    uncertain = null; close();
    await onCreated();
    if (current === generation && !get('people-invitations')?.hidden) get('people-invitations-heading')?.focus();
  });
  get('new-user-snippet-copy').addEventListener('click', async () => {
    try {
      await copy(get('new-user-snippet').textContent);
      get('new-user-snippet-status').textContent = 'Invitation link copied. Send it privately to the intended person.';
    } catch {
      get('new-user-snippet-status').textContent = 'Could not copy. Select and copy the invitation link above.';
    }
  });
  get('new-user-done').addEventListener('click', close);
  function reset() {
    generation++; inFlight?.abort(); inFlight = null; uncertain = null; setBusy(false);
    const wasOpen = !modal.hidden; modal.hidden = true;
    form.reset(); get('new-user-snippet').textContent = ''; showError('');
    if (wasOpen) release();
  }
  return { open, close, submit: create, showInvitation, reset, checkStatus };
}
