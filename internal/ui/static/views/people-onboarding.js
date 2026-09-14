export function applyPeopleOnboarding(document, mode, services = false) {
  const known = typeof mode?.local === 'boolean' && typeof mode?.sso === 'boolean';
  const intro = document.getElementById('people-onboarding');
  document.getElementById('new-user-button').hidden = services || !known || !mode.local;
  document.getElementById('people-signin-copy').hidden = services || !known || !mode.sso;
  document.getElementById('people-joining').hidden = services || !known;
  intro.hidden = !known;
  const providers = Array.isArray(mode?.providers) && mode.providers.length ? new Intl.ListFormat('en', {style: 'long', type: 'disjunction'}).format(mode.providers) : 'your sign-in provider';
  intro.textContent = !known ? '' : mode.sso
    ? `People appear here after their first sign-in with ${providers}. Manage who can sign in through that provider. Roles follow group rules and server defaults unless assigned directly.${mode.local ? ' Use an invitation for a local account.' : ''}`
    : 'Invite people to set their own password. Choose their role before sharing the invitation.';

}

export function createInvitationList({ document, api, onUnauthorized, confirm, announce, onReplaced, canInvite = () => true }) {
  const section = document.getElementById('people-invitations');
  const list = document.getElementById('people-invitations-list');
  const error = document.getElementById('people-invitations-error');
  let generation = 0;
  async function load() {
    const current = ++generation;
    error.hidden = true;
    let response;
    try {
      response = await api('/api/user-invitations');
      if (current !== generation) return;
      if (response.status === 401) { await onUnauthorized(); return; }
      if (!response.ok) throw new Error();
      const body = await response.json();
      if (current !== generation) return;
      if (!Array.isArray(body.items)) throw new Error();
      section.hidden = body.items.length === 0;
      list.replaceChildren();
      for (const invitation of body.items) {
        const row = document.createElement('div'); row.className = 'person-invitation';
        const description = document.createElement('div');
        const name = document.createElement('strong'); name.textContent = invitation.username;
        const detail = document.createElement('p');
        const expired = new Date(invitation.expires_at) <= new Date();
        detail.textContent = `${invitation.role.charAt(0).toUpperCase() + invitation.role.slice(1)} · ${expired ? 'Expired' : 'Expires'} ${new Date(invitation.expires_at).toLocaleString()}`;
        description.append(name, detail);
        const revoke = document.createElement('button'); revoke.type = 'button';
        revoke.textContent = expired ? 'Remove' : 'Revoke invitation';
        revoke.setAttribute('aria-label', `${expired ? 'Remove expired' : 'Revoke'} invitation for ${invitation.username}`);
        revoke.addEventListener('click', async () => {
          if (!confirm(`Revoke the invitation for ${invitation.username}? The link will stop working.`)) return;
          revoke.disabled = true; error.hidden = true;
          try {
            const result = await api(`/api/user-invitations/${encodeURIComponent(invitation.id)}`, { method: 'DELETE' });
            if (result.status === 401) { await onUnauthorized(); return; }
            if (!result.ok) throw new Error();
            announce(`Invitation for ${invitation.username} revoked.`);
            await load();
            document.getElementById('users-refresh').focus();
          } catch {
            error.textContent = 'Could not revoke the invitation. Refresh to check its status, then try again.'; error.hidden = false;
          } finally { revoke.disabled = false; }
        });
        const actions = document.createElement('div'); actions.className = 'invitation-actions';
        const replace = document.createElement('button'); replace.type = 'button';
        replace.textContent = 'Replace link'; replace.hidden = !canInvite();
        replace.setAttribute('aria-label', `Replace invitation link for ${invitation.username}`);
        replace.addEventListener('click', async () => {
          if (!confirm(`Create a new invitation link for ${invitation.username}? Their role stays the same. The old link will stop working.`)) return;
          replace.disabled = revoke.disabled = true; replace.textContent = 'Replacing…'; error.hidden = true;
          try {
            const result = await api(`/api/user-invitations/${encodeURIComponent(invitation.id)}/replace`, {method: 'POST'});
            if (result.status === 401) { await onUnauthorized(); return; }
            if (!result.ok) { let message = 'Could not replace the invitation. Refresh to check its status.'; try { message = (await result.json()).error || message; } catch {} throw new Error(message); }
            const replacement = await result.json();
            onReplaced(replacement);
            await load();
          } catch (failure) {
            error.textContent = failure.message || 'Could not replace the invitation. Refresh to check its status.'; error.hidden = false;
          } finally { replace.disabled = revoke.disabled = false; replace.textContent = 'Replace link'; }
        });
        actions.append(replace, revoke);
        row.append(description, actions); list.append(row);
      }
    } catch {
      if (current !== generation) return;
      section.hidden = false; list.replaceChildren();
      error.textContent = 'Could not load invitations. Use Refresh to try again.'; error.hidden = false;
    }
  }
  return { load };
}
