// Shared project-group disclosure for the sidebar, operator app grid, and
// viewer Launchpad. Each surface gets its own persisted state: compacting the
// sidebar must not unexpectedly hide sections in the page a user opens next.

const STORAGE_PREFIX = 'shinyhub.collapsedProjectGroups.v1.';

function storageFor(explicit) {
  if (explicit !== undefined) return explicit;
  try { return globalThis.localStorage || null; } catch { return null; }
}

function storageKey(view) {
  return `${STORAGE_PREFIX}${view}`;
}

export function collapsedGroups(view, storage) {
  const s = storageFor(storage);
  if (!s) return new Set();
  try {
    const value = JSON.parse(s.getItem(storageKey(view)) || '[]');
    return new Set(Array.isArray(value) ? value.filter((key) => typeof key === 'string') : []);
  } catch {
    return new Set();
  }
}

export function isGroupCollapsed(view, groupKey, storage) {
  return collapsedGroups(view, storage).has(String(groupKey));
}

export function persistGroupCollapsed(view, groupKey, collapsed, storage) {
  const s = storageFor(storage);
  if (!s) return;
  try {
    const groups = collapsedGroups(view, s);
    if (collapsed) groups.add(String(groupKey));
    else groups.delete(String(groupKey));
    s.setItem(storageKey(view), JSON.stringify([...groups].sort()));
  } catch {
    // Storage can be unavailable in private browsing. Disclosure still works
    // for the current render; only persistence is skipped.
  }
}

function domToken(value) {
  return encodeURIComponent(String(value) || 'ungrouped').replaceAll('%', '_');
}

function countLabel(count) {
  return `${count} ${count === 1 ? 'app' : 'apps'}`;
}

/**
 * Build an accessible project section with a real heading and button. Callers
 * append their app rows/cards to `body`, and may append an Edit button to
 * `header` beside (never inside) the disclosure button.
 */
export function createGroupDisclosure(doc, options) {
  const {
    view,
    groupKey,
    label,
    count,
    iconEmoji = '',
    classPrefix,
    forceExpanded = false,
    storage,
  } = options;

  const root = doc.createElement('section');
  root.className = `${classPrefix}-group group-disclosure`;
  root.dataset.groupKey = String(groupKey);

  const header = doc.createElement('div');
  header.className = `${classPrefix}-group-header`;

  const heading = doc.createElement('h2');
  heading.className = `${classPrefix}-group-heading`;

  const toggle = doc.createElement('button');
  toggle.type = 'button';
  toggle.className = `${classPrefix}-group-toggle group-disclosure-toggle`;
  const bodyId = `${classPrefix}-group-${domToken(view)}-${domToken(groupKey)}`;
  toggle.setAttribute('aria-controls', bodyId);
  toggle.setAttribute('aria-label', `${label}, ${countLabel(count)}`);

  const chevron = doc.createElementNS('http://www.w3.org/2000/svg', 'svg');
  chevron.classList.add('group-disclosure-chevron');
  chevron.setAttribute('viewBox', '0 0 16 16');
  chevron.setAttribute('fill', 'none');
  chevron.setAttribute('stroke', 'currentColor');
  chevron.setAttribute('stroke-width', '1.8');
  chevron.setAttribute('stroke-linecap', 'round');
  chevron.setAttribute('stroke-linejoin', 'round');
  chevron.setAttribute('aria-hidden', 'true');
  const path = doc.createElementNS('http://www.w3.org/2000/svg', 'polyline');
  path.setAttribute('points', '5 3 10 8 5 13');
  chevron.appendChild(path);
  toggle.appendChild(chevron);

  if (iconEmoji) {
    const icon = doc.createElement('span');
    icon.className = `${classPrefix}-group-icon`;
    icon.textContent = iconEmoji;
    icon.setAttribute('aria-hidden', 'true');
    toggle.appendChild(icon);
  }

  const name = doc.createElement('span');
  name.className = `${classPrefix}-group-name`;
  name.textContent = label;
  name.title = label;
  toggle.appendChild(name);

  const badge = doc.createElement('span');
  badge.className = `${classPrefix}-group-count`;
  badge.textContent = String(count);
  badge.setAttribute('aria-hidden', 'true');
  toggle.appendChild(badge);

  heading.appendChild(toggle);
  header.appendChild(heading);
  root.appendChild(header);

  const body = doc.createElement('div');
  body.id = bodyId;
  body.className = `${classPrefix}-group-body group-disclosure-body`;
  root.appendChild(body);

  function setCollapsed(collapsed, persist = true) {
    body.hidden = collapsed;
    root.classList.toggle('is-collapsed', collapsed);
    toggle.setAttribute('aria-expanded', String(!collapsed));
    if (persist) persistGroupCollapsed(view, groupKey, collapsed, storage);
  }

  // The active app must be findable on a deep link even when the user had
  // previously collapsed its project. Do not overwrite their preference: when
  // they navigate away, that project may return to its compact state.
  setCollapsed(forceExpanded ? false : isGroupCollapsed(view, groupKey, storage), false);
  toggle.addEventListener('click', () => {
    setCollapsed(toggle.getAttribute('aria-expanded') === 'true');
  });

  return { root, header, heading, toggle, body, setCollapsed };
}
