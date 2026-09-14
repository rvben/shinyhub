import { wireKebab } from './kebab-menu.js';

// Reuse the application's menu keyboard and focus behavior. Call close before
// removing the row so an open menu cannot retain document-level listeners.
export function createPersonActions(document, username, actions) {
  const container = document.createElement('div');
  container.className = 'kebab-menu person-actions';
  const toggle = document.createElement('button');
  toggle.type = 'button';
  toggle.setAttribute('aria-label', `More actions for ${username}`);
  toggle.setAttribute('aria-haspopup', 'menu');
  toggle.setAttribute('aria-expanded', 'false');
  toggle.innerHTML = '<svg aria-hidden="true" width="20" height="20" viewBox="0 0 20 20" fill="currentColor"><circle cx="4" cy="10" r="1.5"/><circle cx="10" cy="10" r="1.5"/><circle cx="16" cy="10" r="1.5"/></svg>';
  const menu = document.createElement('div');
  menu.className = 'kebab-list person-actions-list';
  menu.setAttribute('role', 'menu');
  menu.setAttribute('aria-label', `Actions for ${username}`);
  menu.hidden = true;
  for (const action of actions) {
    const button = action.matches('button') ? action : action.querySelector('button');
    if (!button || button.disabled || button.hidden) continue;
    const destructive = button.classList.contains('btn-row-danger');
    button.className = destructive ? 'person-action-danger' : '';
    button.setAttribute('role', 'menuitem');
    button.tabIndex = -1;
    menu.append(button);
  }
  container.append(toggle, menu);
  container.hidden = menu.childElementCount === 0;
  const controller = wireKebab(toggle, menu, container);
  return { element: container, close: controller.close };
}
