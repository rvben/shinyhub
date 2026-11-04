import { focusedKey, restoreFocus } from './focus-restore.js';
import { runInFlight } from './in-flight.js';

// Responses and row actions belong to the app that created them. A route
// change (including A -> B -> A) invalidates both, even if a fetch finishes late.
export function createAccessLists({ document: doc, request, onUnauthorized, notify = () => {} }) {
  let app = null;
  let generation = 0;
  const mutations = { members: 0, 'group-access': 0 };
  const reads = { members: 0, 'group-access': 0 };
  const listFor = kind => doc.getElementById(kind === 'members' ? 'members-list' : 'group-access-list');
  const errorFor = kind => doc.getElementById(kind === 'members' ? 'grant-error' : 'group-access-error');
  const owner = () => ({ slug: app, generation });
  const current = stamp => !!stamp.slug && stamp.slug === app && stamp.generation === generation;
  const path = (stamp, kind) => `/api/apps/${encodeURIComponent(stamp.slug)}/${kind}`;
  function error(kind, message) {
    const node = errorFor(kind);
    node.textContent = message;
    node.hidden = !message;
  }
  function status(list, message, retry) {
    list.querySelector('[data-access-status]')?.remove();
    const row = doc.createElement('li');
    row.dataset.accessStatus = 'true';
    const text = doc.createElement('span');
    text.setAttribute('role', retry ? 'alert' : 'status');
    text.textContent = message;
    row.append(text);
    if (retry) {
      const button = doc.createElement('button');
      button.type = 'button';
      button.textContent = 'Try again';
      button.addEventListener('click', retry);
      row.append(button);
    }
    list.prepend(row);
  }
  async function mutate(stamp, kind, control, options, after, failure) {
    if (!current(stamp) || !control.isConnected) return;
    error(kind, '');
    await runInFlight(control, async () => {
      mutations[kind]++;
      reads[kind]++;
      listFor(kind).removeAttribute('aria-busy');
      listFor(kind).querySelector('[data-access-status]')?.remove();
      try {
        const { suffix = '', ...init } = options;
        const response = await request(path(stamp, kind) + suffix, { ...init, signal: AbortSignal.timeout(20000) });
        if (!current(stamp)) return;
        if (response.status === 401) { await onUnauthorized(); return; }
        if (!response.ok) throw new Error('request failed');
        reads[kind]++; // Invalidate reads started before this mutation completed.
        listFor(kind).removeAttribute('aria-busy');
        listFor(kind).querySelector('[data-access-status]')?.remove();
        after();
      } catch {
        if (current(stamp)) error(kind, failure);
      } finally { if (current(stamp)) mutations[kind]--; }
    });
  }
  function memberRow(member, stamp) {
    const row = doc.createElement('li');
    const name = doc.createElement('span');
    name.className = 'member-name'; name.textContent = member.username;
    const role = doc.createElement('select');
    role.className = 'member-role member-role-select';
    role.dataset.focusKey = `role:${member.user_id}`;
    role.setAttribute('aria-label', `Role for ${member.username}`);
    for (const value of ['viewer', 'manager']) {
      const option = doc.createElement('option');
      option.value = value; option.textContent = value === 'viewer' ? 'Viewer' : 'Manager';
      role.append(option);
    }
    role.value = member.role;
    let savedRole = member.role;
    role.addEventListener('change', async () => {
      const next = role.value;
      await mutate(stamp, 'members', role, {
        method: 'PATCH', suffix: `/${member.user_id}`, body: JSON.stringify({ role: next }),
      }, () => { savedRole = next; notify(`Updated ${member.username} to ${next}`, 'success'); },
      `Could not update ${member.username}. Check their current role before trying again.`);
      role.value = savedRole;
    });
    const revoke = doc.createElement('button');
    revoke.dataset.focusKey = `revoke:${member.user_id}`;
    revoke.type = 'button'; revoke.textContent = 'Revoke';
    revoke.setAttribute('aria-label', `Revoke access for ${member.username}`);
    revoke.addEventListener('click', () => mutate(stamp, 'members', revoke, {
      method: 'DELETE', body: JSON.stringify({ user_id: member.user_id }),
    }, () => {
      row.remove(); notify(`Revoked access for ${member.username}`, 'success');
      doc.getElementById('grant-username').focus();
    }, `Could not confirm revocation for ${member.username}. Refresh the access list to check, then try again.`));
    row.append(name, role, revoke);
    return row;
  }
  function groupRow(rule, stamp) {
    const row = doc.createElement('li');
    const name = doc.createElement('span');
    name.className = 'member-name'; name.textContent = rule.group;
    const role = doc.createElement('span');
    role.className = 'member-role'; role.textContent = rule.role;
    row.append(name, role);
    if (rule.source === 'manifest') {
      const label = doc.createElement('span'); label.className = 'member-role'; label.textContent = '(manifest)'; row.append(label);
    } else {
      const remove = doc.createElement('button');
      remove.dataset.focusKey = `group:${rule.group}`;
      remove.type = 'button'; remove.textContent = 'Remove';
      remove.setAttribute('aria-label', `Remove group rule ${rule.group}`);
      remove.addEventListener('click', () => mutate(stamp, 'group-access', remove, {
        method: 'DELETE', suffix: `/${encodeURIComponent(rule.group)}`,
      }, () => {
        row.remove(); notify(`Removed access rule for ${rule.group}`, 'success');
        doc.getElementById('group-access-name').focus();
      }, `Could not confirm removal of ${rule.group}. Refresh the access list to check, then try again.`));
      row.append(remove);
    }
    return row;
  }
  async function refresh(kind) {
    const stamp = owner();
    if (!stamp.slug || mutations[kind]) return;
    const id = ++reads[kind];
    const list = listFor(kind);
    const fresh = () => current(stamp) && reads[kind] === id;
    const label = kind === 'members' ? 'individual access' : 'group access';
    const retryFocused = list.querySelector('[data-access-status]')?.contains(doc.activeElement);
    status(list, `Refreshing ${label}…`);
    list.setAttribute('aria-busy', 'true');
    if (retryFocused) { list.tabIndex = -1; list.focus(); }
    try {
      const response = await request(path(stamp, kind), { signal: AbortSignal.timeout(20000) });
      if (!fresh()) return;
      if (response.status === 401) { await onUnauthorized(); return; }
      if (!response.ok) throw new Error('request failed');
      const body = await response.json();
      if (!fresh()) return;
      const entries = Array.isArray(body) ? body : body?.items;
      if (!Array.isArray(entries)) throw new Error('invalid response');
      const hadFocus = doc.activeElement === list || list.contains(doc.activeElement);
      const key = focusedKey(list);
      list.replaceChildren(...entries.map(item => kind === 'members' ? memberRow(item, stamp) : groupRow(item, stamp)));
      if (!entries.length) status(list, kind === 'members' ? 'No individual access grants.' : 'No group access rules.');
      if (hadFocus && !restoreFocus(list, key)) {
        (list.querySelector('select, button') || doc.getElementById(kind === 'members' ? 'grant-username' : 'group-access-name')).focus();
      }
    } catch {
      if (!fresh()) return;
      const stale = list.querySelector('select, .member-name') ? ' Showing the last known entries.' : '';
      const hadFocus = doc.activeElement === list || list.querySelector('[data-access-status]')?.contains(doc.activeElement);
      status(list, `Could not check ${label}.${stale} Check your connection and try again.`, () => refresh(kind));
      if (hadFocus) list.querySelector('[data-access-status] button')?.focus();
    } finally {
      if (fresh()) list.removeAttribute('aria-busy');
    }
  }
  return {
    capture() { const stamp = owner(); return () => current(stamp); },
    setApp(slug) {
      if (slug === app) return;
      app = slug; generation++;
      for (const kind of Object.keys(reads)) { mutations[kind] = 0; listFor(kind).replaceChildren(); listFor(kind).removeAttribute('aria-busy'); error(kind, ''); }
    },
    refreshMembers: () => refresh('members'),
    refreshGroups: () => refresh('group-access'),
  };
}
