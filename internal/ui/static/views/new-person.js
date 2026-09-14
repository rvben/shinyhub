export function createNewPersonController({ document, api, origin, activate, release, onCreated, onUnauthorized, copy }) {
  const get = id => document.getElementById(id);
  const modal = get('new-user-modal');
  const form = get('new-user-form');
  const username = get('new-user-username');
  const error = get('new-user-error');
  const success = get('new-user-success');
  const submit = get('new-user-submit');
  let busy = false;
  function showError(message) {
    error.textContent = message;
    error.hidden = !message;
  }
  function setBusy(value) {
    busy = value;
    form.setAttribute('aria-busy', String(value));
    for (const input of form.elements) input.disabled = value;
    get('new-user-close').disabled = value;
    submit.textContent = value ? 'Creating invitation…' : 'Create invitation';
  }
  function open() {
    form.reset();
    form.hidden = false;
    success.hidden = true;
    get('new-user-heading').textContent = 'Invite person';
    get('new-user-snippet').textContent = '';
    get('new-user-snippet-status').textContent = '';
    showError('');
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
  function showInvitation(result, replaced = false) {
    if (!result.token || !result.invitation) throw new Error('Invalid invitation response');
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
    if (busy || form.hidden) return;
    username.value = username.value.trim();
    if (!form.reportValidity()) return;
    const name = username.value;
    const role = form.elements.namedItem('role').value;
    const payload = { username: name, role };
    showError('');
    setBusy(true);
    try {
      const response = await api('/api/user-invitations', { method: 'POST', body: JSON.stringify(payload) });
      if (response.status === 401) { await onUnauthorized(); return; }
      if (!response.ok) {
        let message = 'Could not create the invitation. Try again.';
        try { const body = await response.json(); if (body?.error) message = body.error; } catch {}
        showError(message);
        return;
      }
      const result = await response.json();
      showInvitation(result);
      onCreated();
    } catch {
      showError('Could not reach ShinyHub. Check your connection and try again.');
    } finally {
      setBusy(false);
    }
  }
  get('new-user-snippet-copy').addEventListener('click', async () => {
    try {
      await copy(get('new-user-snippet').textContent);
      get('new-user-snippet-status').textContent = 'Invitation link copied. Send it privately to the intended person.';
    } catch {
      get('new-user-snippet-status').textContent = 'Could not copy. Select and copy the invitation link above.';
    }
  });
  get('new-user-done').addEventListener('click', close);
  return { open, close, submit: create, showInvitation };
}
