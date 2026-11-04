import { startVisiblePoll } from './visible-poll.js';
import { focusedKey, restoreFocus } from './focus-restore.js';
import { bindFilterBar, createListState, setFeedback } from './page-components.js';

const STATUS = {
  down: { label: 'Offline', tone: 'lost', order: 0, detail: 'The hub is not receiving heartbeats from this worker.' },
  unknown: { label: 'Unknown', tone: 'warning', order: 1, detail: 'The hub has not reported a recognized worker status.' },
  joining: { label: 'Joining', tone: 'stopped', order: 2, detail: 'Registered and waiting for its first heartbeat.' },
  up: { label: 'Online', tone: 'running', order: 3, detail: 'This worker is sending heartbeats to the hub.' },
  revoked: { label: 'Revoked', tone: 'stopped', order: 4, detail: 'Worker access has been revoked. This worker cannot connect to the hub.' },
};
const text = (value) => typeof value === 'string' ? value.trim() : '';

export function workerDisplay(input) {
  const w = input && typeof input === 'object' ? input : {};
  const raw = text(w.status);
  const key = w.revoked ? 'revoked' : Object.hasOwn(STATUS, raw) ? raw : 'unknown';
  const status = STATUS[key];
  const id = text(w.node_id);
  const name = text(w.name) || id || 'Unknown worker';
  return {
    node: text(w.name) && id ? `${name} (${id})` : id || 'unknown',
    name, id, key, order: status.order,
    tier: text(w.tier) || 'Not reported',
    statusText: status.label, statusClass: status.tone,
    detail: key === 'unknown' && raw ? `${status.detail} Reported value: ${raw}.` : status.detail,
    version: text(w.version) || 'Not reported', address: text(w.advertise_addr) || 'Not reported',
    heartbeat: text(w.last_heartbeat), revokedAt: text(w.revoked_at),
  };
}

export function workerHeartbeat(value, now = Date.now()) {
  if (!value) return { label: 'Not received', exact: '', iso: '' };
  // The registry stores UTC SQL datetimes without an offset. Normalize them
  // before parsing so browser locale and daylight saving cannot age a live node.
  const raw = String(value);
  const normalized = /^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:\.\d+)?$/.test(raw)
    ? `${raw.replace(' ', 'T')}Z` : raw;
  const date = new Date(normalized);
  if (!Number.isFinite(date.getTime())) return { label: 'Not reported', exact: '', iso: '' };
  const delta = now - date.getTime();
  const seconds = Math.max(0, Math.floor(delta / 1000));
  const label = delta < -60000 ? 'Clock ahead'
    : seconds < 10 ? 'Just now'
      : seconds < 60 ? `${seconds}s ago`
        : seconds < 3600 ? `${Math.floor(seconds / 60)}m ago`
          : seconds < 86400 ? `${Math.floor(seconds / 3600)}h ago`
            : `${Math.floor(seconds / 86400)}d ago`;
  return { label, exact: date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'long' }), iso: date.toISOString() };
}

export function visibleWorkers(workers, query = '', status = '') {
  const needle = query.trim().toLocaleLowerCase();
  return workers.map(workerDisplay).filter((w) => (!status || w.key === status)
    && (!needle || [w.name, w.id, w.tier, w.address, w.version].some((v) => v.toLocaleLowerCase().includes(needle))))
    .sort((a, b) => a.order - b.order || a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
}

// Own requests and DOM for the lifetime of the route. Late responses must not
// paint another route or leak a previous administrator's worker inventory.
export function mountWorkers(ctx) {
  const doc = document;
  const view = doc.getElementById('workers-view');
  const get = (id) => doc.getElementById(`workers-${id}`);
  const refresh = get('refresh'), search = get('search'), filter = get('status-filter');
  const body = get('body'), table = get('table'), loading = get('loading');
  const summary = get('summary'), error = get('error'), empty = get('empty');
  const filters = get('filters'), results = get('results'), noMatches = get('no-matches');
  const updated = get('updated'), live = get('live'), note = get('note');
  const list = createListState({content:table,loading,error,empty,noMatches,busy:view});
  const session = ctx.state.sessionIdentity ?? ctx.state.user;
  const userID = ctx.state.user?.id;
  let disposed = false, pending = null, workers = null, updatedAt = 0, stale = false;
  let stopPoll = null;
  const current = () => !disposed && (ctx.state.sessionIdentity ?? ctx.state.user) === session
    && ctx.state.user?.id === userID && ctx.state.user?.role === 'admin';
  const create = (tag, className, value) => {
    const node = doc.createElement(tag); if (className) node.className = className;
    if (value != null) node.textContent = value; return node;
  };
  const showError = message => setFeedback(error,message,{error:true});

  function clear() {
    body.replaceChildren(); summary.replaceChildren(); updated.textContent = ''; results.textContent = '';
    list.clear();
    for (const node of [filters, summary, note]) node.hidden = true;
    search.value = ''; filter.value = ''; live.textContent = '';
  }

  function checkAccess() {
    if (current()) return true;
    if (!disposed) {
      stop(); workers = null; clear(); showError(''); view.hidden = true;
      ctx.navigate('/apps', { replace: true });
    }
    return false;
  }

  // Age the existing nodes even when requests hang or fail. Do not rebuild
  // rows here: the operator may be reading an open disclosure or using keys.
  function updateTimes() {
    for (const node of body.querySelectorAll('time.worker-heartbeat')) {
      node.textContent = workerHeartbeat(node.dateTime).label;
    }
    updated.textContent = updatedAt
      ? `${stale ? 'Last updated' : 'Updated'} ${workerHeartbeat(new Date(updatedAt).toISOString()).label.toLowerCase()}` : '';
  }

  function render() {
    if (!checkAccess() || !workers) return;
    const focus = focusedKey(body);
    const opened = new Set([...body.querySelectorAll('details[open]')].map((d) => d.dataset.worker));
    const rows = visibleWorkers(workers, search.value, filter.value);
    const counts = new Map();
    for (const w of workers.map(workerDisplay)) counts.set(w.key, (counts.get(w.key) || 0) + 1);
    summary.replaceChildren();
    // Running states lead the compact summary; incidents lead the actual list.
    for (const key of ['up', 'down', 'joining', 'unknown', 'revoked']) {
      if (!counts.has(key)) continue;
      const item = create('span', `workers-count workers-count--${key}`);
      item.append(create('span', 'workers-dot'), create('span', '', `${counts.get(key)} ${STATUS[key].label.toLowerCase()}`));
      summary.append(item);
    }
    summary.hidden = workers.length === 0;
    filters.hidden = workers.length === 0;
    list.results({total:workers.length,visible:rows.length});
    note.hidden = workers.length === 0;
    results.textContent = search.value.trim() || filter.value
      ? `${rows.length} of ${workers.length} workers` : `${workers.length} ${workers.length === 1 ? 'worker' : 'workers'} · Offline first`;
    get('reset').hidden = rows.length === 0 || (!search.value.trim() && !filter.value);
    body.replaceChildren(...rows.map((w) => {
      const row = create('tr');
      const cell = (label) => { const td = create('td'); td.dataset.label = label; row.append(td); return td; };
      const identity = cell('Worker');
      identity.append(create('strong', 'worker-name', w.name));
      const details = create('details', 'worker-details'); details.dataset.worker = w.id; details.open = opened.has(w.id);
      const toggle = create('summary', '', 'Connection details');
      toggle.dataset.focusKey = `worker:${w.id}:details`;
      toggle.setAttribute('aria-label', `Connection details for ${w.name}`); details.append(toggle);
      const list = create('dl');
      for (const [label, value] of [['Node ID', w.id || 'Not reported'], ['Address', w.address]]) {
        list.append(create('dt', '', label), create('dd', 'worker-identifier', value));
      }
      if (w.key === 'revoked' && w.revokedAt) list.append(create('dt', '', 'Revoked'), create('dd', '', workerHeartbeat(w.revokedAt).exact || 'Not reported'));
      details.append(list, create('p', 'worker-explanation', w.detail));
      if (w.key === 'down' || w.key === 'joining' || w.key === 'unknown') {
        const help = create('a', 'worker-help', 'Troubleshoot connection');
        help.href = 'https://shinyhub.dev/recipes/remote-worker/#troubleshooting';
        help.target = '_blank'; help.rel = 'noopener noreferrer'; details.append(help);
      }
      identity.append(details);
      const status = cell('Status');
      status.append(create('span', `badge badge-${w.statusClass} worker-status`, w.statusText));
      cell('Tier').append(create('span', 'worker-tier', w.tier));
      cell('Version').append(create('span', '', w.version));
      const heartbeat = cell('Last heartbeat');
      // Registration stamps last_heartbeat too; joining proves no heartbeat
      // has arrived yet, so do not present registration as a heartbeat.
      const time = workerHeartbeat(w.key === 'joining' ? '' : w.heartbeat);
      const relative = create(time.iso ? 'time' : 'span', 'worker-heartbeat', time.label);
      if (time.iso) { relative.dateTime = time.iso; relative.title = time.exact; }
      heartbeat.append(relative);
      if (time.exact) heartbeat.append(create('span', 'worker-heartbeat-exact', time.exact));
      return row;
    }));
    if (focus && !restoreFocus(body, focus)) search.focus({ preventScroll: true });
    updateTimes();
  }

  async function loadWorkers(manual = false) {
    if (!checkAccess() || pending) return;
    const controller = new AbortController(); pending = controller;
    refresh.setAttribute('aria-disabled', 'true'); refresh.textContent = 'Refreshing…';
    list.pending({retain:workers !== null});
    let deadline;
    try {
      const response = await Promise.race([
        (async () => {
          const resp = await ctx.api('/api/workers', { signal: controller.signal });
          return { resp, data: resp.ok ? await resp.json() : null };
        })(),
        new Promise((_, reject) => {
          controller.signal.addEventListener('abort', () => reject(new Error('aborted')), { once: true });
          deadline = setTimeout(() => controller.abort(), 15000);
        }),
      ]);
      if (!checkAccess()) return;
      const { resp, data } = response;
      if (resp.status === 401) { stop(); ctx.onUnauthorized(); return; }
      if (resp.status === 403) {
        workers = null; clear(); stopPoll?.();
        showError('Administrator access is required to view workers.'); return;
      }
      if (!resp.ok || !Array.isArray(data) || data.some((w) => !w || typeof w !== 'object' || Array.isArray(w) || !text(w.node_id))) throw new Error('invalid response');
      workers = data; updatedAt = Date.now(); stale = false; showError(''); render();
      if (manual) live.textContent = `Workers refreshed. ${workers.length} ${workers.length === 1 ? 'worker' : 'workers'}.`;
    } catch {
      if (!checkAccess()) return;
      list.fail(workers !== null
        ? 'Could not refresh workers. Showing the last loaded list. Select Refresh to try again.'
        : 'Could not load workers. Check your connection and select Refresh to try again.',{retain:workers !== null});
      stale = true; updateTimes();
    } finally {
      clearTimeout(deadline);
      if (pending === controller) pending = null;
      if (current()) {
        refresh.removeAttribute('aria-disabled'); refresh.textContent = 'Refresh';
        list.finish();
      }
    }
  }
  function onFilter() { if (!checkAccess()) return; render(); live.textContent = results.textContent; }
  function onRefresh() { loadWorkers(true); }
  function stop() { disposed = true; stopPoll?.(); pending?.abort(); doc.removeEventListener('session-capabilities-change', checkAccess); }

  clear(); showError(''); refresh.textContent = 'Refresh'; refresh.removeAttribute('aria-disabled');
  view.hidden = false;
  refresh.addEventListener('click', onRefresh);
  const filterBar = bindFilterBar({root:filters,fields:{search,status:filter},clearButtons:[get('clear'),get('reset')],onChange:onFilter});
  doc.addEventListener('session-capabilities-change', checkAccess);
  stopPoll = startVisiblePoll(doc, 10000, () => { if (checkAccess()) { updateTimes(); loadWorkers(); } });
  loadWorkers(); ctx.updateActiveNav(location.pathname);
  return { title: 'Workers', unmount() {
    stop(); refresh.removeEventListener('click', onRefresh);
    filterBar.dispose();
    clear(); showError(''); view.hidden = true;
  } };
}
