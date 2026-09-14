import { initTheme } from './theme.js';

export function mountInvitation({ document, location, history, fetch }) {
  const get = id => document.getElementById(id);
  let token = location.hash.slice(1);
  const form = get('invitation-form');
  const password = get('invitation-password');
  const confirm = get('invitation-confirm');
  const error = get('invitation-error');
  const status = get('invitation-status');
  let busy = false;
  function showError(message) { error.textContent = message; error.hidden = !message; }
  async function request(action, body) {
    const response = await fetch(`/api/auth/invitations/${action}`, {
      method: 'POST', credentials: 'omit', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token, ...body }),
    });
    const result = await response.json();
    if (!response.ok) {
      const failure = new Error(result.error || 'Could not check the invitation. Try again.');
      failure.retryable = response.status >= 500 || response.status === 429;
      throw failure;
    }
    return result;
  }
  async function preview() {
    if (busy) return;
    showError('');
    get('invitation-retry').hidden = true;
    get('invitation-signin').hidden = true;
    form.hidden = true;
    status.textContent = 'Checking your invitation…';
    if (!/^[a-f0-9]{64}$/.test(token)) {
      status.textContent = '';
      showError('This invitation link is incomplete. Ask your administrator for a new link.');
      return;
    }
    busy = true;
    try {
      const inv = await request('preview', {});
      get('invitation-username').textContent = inv.username;
      get('invitation-role').textContent = inv.role.charAt(0).toUpperCase() + inv.role.slice(1);
      get('invitation-role-description').textContent = {
        viewer: 'You can use apps you have access to.',
        developer: 'You can create apps and manage your own apps.',
        operator: 'You can access and manage all apps.',
        admin: 'You’ll have full access, including people and server settings.',
      }[inv.role];
      status.textContent = `Set your password to finish joining. This invitation expires ${new Date(inv.expires_at).toLocaleString()}.`;
      form.hidden = false;
      password.focus();
    } catch (failure) {
      status.textContent = '';
      showError(failure.message || 'Could not connect. Check your connection and try again.');
      get('invitation-retry').hidden = failure.retryable === false;
      get('invitation-signin').hidden = false;
      get('invitation-signin').textContent = 'Already joined? Sign in';
    } finally { busy = false; }
  }
  async function submit(event) {
    event.preventDefault();
    if (busy || form.hidden || !form.reportValidity()) return;
    showError('');
    if ([...password.value].length < 15 || new TextEncoder().encode(password.value).length > 72) {
      showError([...password.value].length < 15 ? 'Use at least 15 characters. Try a few memorable words.' : 'This password is too long. Shorten your passphrase or use fewer emoji.'); password.focus(); return;
    }
    if (password.value !== confirm.value) { showError('The passwords don’t match. Enter the same password in both fields.'); confirm.focus(); return; }
    busy = true;
    form.setAttribute('aria-busy', 'true');
    for (const input of form.elements) input.disabled = true;
    get('invitation-submit').textContent = 'Creating your account…';
    try {
      await request('accept', { password: password.value });
      // Account creation is already complete. A sign-in failure must never send
      // the person back to an invitation that has just been consumed.
      let signedIn = false;
      try {
        const login = await fetch('/api/auth/session', {
          method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({username: get('invitation-username').textContent, password: password.value}),
        });
        signedIn = login.ok;
      } catch { /* The account exists; offer the normal sign-in path. */ }
      password.value = ''; confirm.value = ''; token = '';
      history.replaceState(null, '', location.pathname);
      form.hidden = true;
      get('invitation-heading').textContent = 'Welcome to ShinyHub';
      status.textContent = signedIn
        ? `You’re signed in as ${get('invitation-username').textContent}. Open Apps to see what you can use. If an app is missing, ask the person who invited you to grant access.`
        : 'Your account is ready. Sign in to open Apps. If an app is missing, ask the person who invited you to grant access.';
      get('invitation-signin').href = signedIn ? '/apps' : '/login?next=%2Fapps';
      get('invitation-signin').textContent = signedIn ? 'Open Apps' : 'Sign in to open Apps';
      get('invitation-signin').hidden = false;
      get('invitation-signin').focus();
    } catch (failure) {
      showError(failure.message || 'Could not connect. Check your connection and try again.');
    } finally {
      busy = false; form.removeAttribute('aria-busy');
      for (const input of form.elements) input.disabled = false;
      get('invitation-submit').textContent = 'Create account and sign in';
    }
  }
  get('invitation-show-password').addEventListener('change', event => {
    password.type = confirm.type = event.target.checked ? 'text' : 'password';
  });
  get('invitation-retry').addEventListener('click', preview);
  form.addEventListener('submit', submit);
  return { preview, submit };
}

if (typeof window !== 'undefined' && document.getElementById('invitation-form')) {
  initTheme(window);
  mountInvitation({ document, location: window.location, history: window.history, fetch: window.fetch.bind(window) }).preview();
}
