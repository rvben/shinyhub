import { supportSessionCaps } from './user-row.js';

export function createSupportSessionAction({ document, user, selfId, enabled, onStart }) {
  if (enabled !== true) return null;
  const caps = supportSessionCaps(user, selfId, true);
  const group = document.createElement('div');
  group.className = 'users-support-action';
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'btn-row btn-row-support';
  button.textContent = 'Support session';
  group.append(button);
  const reason = caps.hint;
  if (reason) {
    const hint = document.createElement('span');
    hint.id = `support-hint-${user.id}`;
    hint.className = 'users-support-hint';
    hint.textContent = reason;
    button.setAttribute('aria-describedby', hint.id);
    group.append(hint);
  }
  if (!caps.canStart) {
    button.disabled = true;
  } else {
    button.setAttribute('aria-label', `Start support session as ${user.username}`);
    button.addEventListener('click', onStart);
  }
  return group;
}
