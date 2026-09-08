import { supportSessionCaps } from './user-row.js';

export function renderSupportSessionSettings(document, enabled, loading = false) {
  const known = typeof enabled === 'boolean';
  document.getElementById('support-settings-status').textContent = loading ? 'Checking status…' : !known
    ? 'Status unavailable' : enabled ? 'Enabled' : 'Not enabled';
  document.getElementById('support-settings-message').textContent = loading ? 'Checking whether this server allows support sessions.' : !known
    ? 'Support-session status could not be confirmed. Refresh to try again.'
    : enabled
      ? 'Choose Support session beside an eligible person below. Administrators, operators, service accounts, and your own account cannot be represented.'
      : 'Enable support sessions in your server configuration to get started. Expand the setup guide below for instructions.';
}

export function createSupportSessionAction({ document, user, selfId, enabled, onStart }) {
  const caps = supportSessionCaps(user, selfId, true);
  const group = document.createElement('div');
  group.className = 'users-support-action';
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'btn-row btn-row-support';
  button.textContent = 'Support session';
  group.append(button);
  const reason = !caps.canStart ? caps.hint : enabled === false
    ? 'Setup required' : enabled !== true ? 'Refresh to check availability' : '';
  if (reason) {
    const hint = document.createElement('span');
    hint.id = `support-hint-${user.id}`;
    hint.className = 'users-support-hint';
    hint.textContent = reason;
    button.setAttribute('aria-describedby', hint.id);
    group.append(hint);
  }
  if (!caps.canStart || typeof enabled !== 'boolean') {
    button.disabled = true;
  } else if (enabled) {
    button.setAttribute('aria-label', `Start support session as ${user.username}`);
    button.addEventListener('click', onStart);
  } else {
    button.setAttribute('aria-label', `Support session for ${user.username}: view setup`);
    button.setAttribute('aria-controls', 'support-settings-setup');
    button.addEventListener('click', () => {
      const guide = document.getElementById('support-settings-setup');
      guide.open = true;
      guide.querySelector('summary').focus();
    });
  }
  return group;
}
