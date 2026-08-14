// Multi-replica, immutable-run application log viewer. Replica numbers identify
// reusable pool slots; source IDs identify concrete executions of those slots.

export const MAX_RENDERED_LOG_ENTRIES = 2500;

const LIVE_STATUSES = new Set(['running', 'starting', 'deploying', 'waking']);

export function isLiveLogSource(source) {
  return source && source.current !== false &&
    LIVE_STATUSES.has(String(source.status || '').toLowerCase());
}

export function isFollowableLogSource(source) {
  return isLiveLogSource(source) && source.stream_available !== false;
}

export function normalizeLogSources(raw = []) {
  const bySource = new Map();
  for (const item of Array.isArray(raw) ? raw : []) {
    const replica = Number(item && (item.replica ?? item.index));
    if (!Number.isInteger(replica) || replica < 0) continue;
    const sourceID = String(item.source_id || item.run_id || `replica-${replica}`);
    const previous = bySource.get(sourceID) || {};
    bySource.set(sourceID, {
      ...previous,
      ...item,
      replica,
      source_id: sourceID,
      current: item.current === undefined ? (previous.current ?? true) : Boolean(item.current),
      status: String(item.status || previous.status || 'unknown').toLowerCase(),
      has_log: item.has_log === undefined ? previous.has_log : Boolean(item.has_log),
    });
  }
  return [...bySource.values()].sort((a, b) => {
    if (a.current !== b.current) return a.current ? -1 : 1;
    if (a.current && a.replica !== b.replica) return a.replica - b.replica;
    return String(b.started_at || b.updated_at || '').localeCompare(String(a.started_at || a.updated_at || ''));
  });
}

function titleCase(value) {
  const s = String(value || 'unknown').replaceAll('_', ' ');
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export function formatLogSourceLabel(source) {
  const place = source.tier || source.provider || '';
  if (source.current !== false) {
    return `Replica #${source.replica} — ${titleCase(source.status)}${place ? ` · ${place}` : ''}`;
  }
  const when = source.started_at || source.updated_at;
  const stamp = when ? new Date(when).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : 'Earlier run';
  return `Replica #${source.replica} — ${stamp} · ${titleCase(source.status)}`;
}

export function filterLogEntries(entries, query) {
  const needle = String(query || '').trim().toLocaleLowerCase();
  if (!needle) return entries;
  return entries.filter((entry) => {
    const source = entry.replica == null ? '' : `#${entry.replica}`;
    return `${source} ${entry.line}`.toLocaleLowerCase().includes(needle);
  });
}

export function serializeLogEntries(entries) {
  return entries.map((entry) => {
    if (entry.kind === 'event') return `--- ${entry.line} ---`;
    return `${entry.replica == null ? '' : `#${entry.replica}  `}${entry.line}`;
  }).join('\n');
}

export function appendBoundedLogEntry(entries, entry, max = MAX_RENDERED_LOG_ENTRIES) {
  entries.push(entry);
  const removed = Math.max(0, entries.length - max);
  if (removed) entries.splice(0, removed);
  return removed;
}

function sameSourceState(a, b) {
  return a && b && a.status === b.status && a.has_log === b.has_log &&
    a.provider === b.provider && a.tier === b.tier && a.size_bytes === b.size_bytes &&
    a.current === b.current;
}

function sourceStatusEvent(source, previous) {
  if (!source.current) return '';
  if (!previous) return `Replica #${source.replica} run discovered (${titleCase(source.status)})`;
  if (source.status === previous.status) return '';
  if (isLiveLogSource(source)) return `Replica #${source.replica} started (${titleCase(source.status)})`;
  return `Replica #${source.replica} ${titleCase(source.status).toLocaleLowerCase()}`;
}

export function createLogsViewer({
  panel,
  app,
  initialSources = [],
  api,
  EventSourceClass = globalThis.EventSource,
  refreshEveryMs = 5000,
}) {
  const doc = panel.ownerDocument;
  const win = doc.defaultView || globalThis;
  panel.innerHTML = `
    <div class="logs-workspace">
      <div class="logs-toolbar" aria-label="Log controls">
        <div class="logs-toolbar-fields">
          <label class="logs-field">
            <span>Sources</span>
            <select id="logs-source" aria-label="Log sources"></select>
          </label>
          <label class="logs-field logs-search-field">
            <span>Search</span>
            <input id="logs-search" type="search" placeholder="Filter visible logs" autocomplete="off">
          </label>
        </div>
        <div class="logs-toolbar-actions">
          <button id="logs-pause" type="button" class="btn-row" aria-pressed="false">Pause live</button>
          <button id="logs-copy" type="button" class="btn-row">Copy visible</button>
          <button id="logs-download" type="button" class="btn-row">Download</button>
        </div>
      </div>
      <div class="logs-stream-bar">
        <p id="logs-stream-status" class="logs-stream-status">
          <span class="logs-status-dot" aria-hidden="true"></span>
          <span class="logs-status-text">Discovering log sources…</span>
        </p>
        <button id="logs-jump" type="button" class="logs-jump btn-row" hidden>Jump to latest</button>
      </div>
      <div id="detail-logs-body" class="detail-logs-body" tabindex="0"
           aria-label="Application log output"></div>
      <p id="logs-output-summary" class="logs-output-summary"></p>
      <p id="logs-announcement" class="sr-only" role="status" aria-live="polite"></p>
    </div>
  `;

  const sourceSelect = panel.querySelector('#logs-source');
  const searchInput = panel.querySelector('#logs-search');
  const pauseButton = panel.querySelector('#logs-pause');
  const copyButton = panel.querySelector('#logs-copy');
  const downloadButton = panel.querySelector('#logs-download');
  const jumpButton = panel.querySelector('#logs-jump');
  const status = panel.querySelector('#logs-stream-status');
  const statusText = panel.querySelector('.logs-status-text');
  const output = panel.querySelector('#detail-logs-body');
  const summary = panel.querySelector('#logs-output-summary');
  const announcement = panel.querySelector('#logs-announcement');

  let sources = normalizeLogSources(initialSources);
  let selected = 'all';
  let entries = [];
  let trimmed = 0;
  let paused = false;
  let pendingWhilePaused = 0;
  let stickToBottom = true;
  let destroyed = false;
  let renderScheduled = false;
  let refreshTimer = null;
  let scopeGeneration = 0;
  const streams = new Map();
  const loadedSources = new Set();
  const connected = new Set();

  try {
    const requested = new URLSearchParams(win.location && win.location.search || '').get('log_source');
    if (requested === 'all') selected = requested;
    else if (/^\d+$/.test(requested || '')) selected = `replica-${requested}`;
    else if (/^[a-z0-9-]+$/.test(requested || '')) selected = requested;
  } catch { /* non-browser test environment */ }

  function scopedSources() {
    if (selected === 'all') return sources.filter((source) => source.current);
    return sources.filter((source) => source.source_id === selected);
  }

  function updateURL() {
    try {
      const url = new URL(win.location.href);
      if (selected === 'all') url.searchParams.delete('log_source');
      else url.searchParams.set('log_source', selected);
      win.history.replaceState(null, '', url.pathname + url.search + url.hash);
    } catch { /* history is optional in embedded/test contexts */ }
  }

  function announce(message) {
    announcement.textContent = '';
    win.setTimeout(() => { if (!destroyed) announcement.textContent = message; }, 0);
  }

  function atBottom() {
    return output.scrollHeight - Math.ceil(output.scrollTop) <= output.clientHeight + 2;
  }

  function scheduleRender(force = false) {
    if ((paused && !force) || renderScheduled || destroyed) return;
    renderScheduled = true;
    const run = () => {
      renderScheduled = false;
      if (!destroyed) renderEntries();
    };
    if (typeof win.requestAnimationFrame === 'function') win.requestAnimationFrame(run);
    else win.setTimeout(run, 0);
  }

  function addEntry(entry) {
    trimmed += appendBoundedLogEntry(entries, entry);
    if (paused) {
      pendingWhilePaused++;
      pauseButton.textContent = `Resume (${pendingWhilePaused})`;
      return;
    }
    scheduleRender();
  }

  function renderEntries() {
    const visible = filterLogEntries(entries, searchInput.value);
    const wasFollowing = stickToBottom && atBottom();
    const fragment = doc.createDocumentFragment();
    if (visible.length === 0) {
      const empty = doc.createElement('p');
      empty.className = 'logs-output-empty';
      empty.textContent = searchInput.value
        ? 'No visible log lines match this search.'
        : (sources.length ? 'Waiting for application output…' : 'No retained log sources were found.');
      fragment.appendChild(empty);
    } else {
      for (const entry of visible) {
        const row = doc.createElement('div');
        row.className = entry.kind === 'event' ? 'log-entry log-entry-event' : 'log-entry';
        if (entry.kind === 'event') {
          const message = doc.createElement('span');
          message.textContent = entry.line;
          row.appendChild(message);
        } else {
          const source = doc.createElement('span');
          source.className = `log-entry-source log-source-${entry.replica % 6}`;
          source.textContent = `#${entry.replica}`;
          source.title = `Replica #${entry.replica}`;
          const message = doc.createElement('span');
          message.className = 'log-entry-message';
          message.textContent = entry.line;
          row.append(source, message);
        }
        fragment.appendChild(row);
      }
    }
    output.replaceChildren(fragment);
    if (wasFollowing || stickToBottom) output.scrollTop = output.scrollHeight;
    const parts = [`${visible.length.toLocaleString()} visible line${visible.length === 1 ? '' : 's'}`];
    if (trimmed) parts.push(`${trimmed.toLocaleString()} older lines omitted from this session`);
    if (searchInput.value) parts.push(`${entries.length.toLocaleString()} buffered`);
    summary.textContent = parts.join(' · ');
    jumpButton.hidden = stickToBottom;
  }

  function renderSourceOptions() {
    const prior = selected;
    const all = doc.createElement('option');
    const currentSources = sources.filter((source) => source.current);
    const liveCount = currentSources.filter(isLiveLogSource).length;
    all.value = 'all';
    all.textContent = `All current replicas (${liveCount} live, ${currentSources.length} total)`;
    const options = [all];
    for (const [label, grouped] of [
      ['Current runs', currentSources],
      ['Run history', sources.filter((source) => !source.current)],
    ]) {
      if (!grouped.length) continue;
      const group = doc.createElement('optgroup');
      group.label = label;
      for (const source of grouped) {
        const option = doc.createElement('option');
        option.value = source.source_id;
        option.textContent = formatLogSourceLabel(source);
        group.appendChild(option);
      }
      options.push(group);
    }
    sourceSelect.replaceChildren(...options);
    selected = prior === 'all' || sources.some((s) => s.source_id === prior) ? prior : 'all';
    sourceSelect.value = selected;
  }

  function updateConnectionStatus() {
    const scoped = scopedSources();
    const live = scoped.filter(isLiveLogSource);
    const followable = live.filter(isFollowableLogSource);
    const external = live.length - followable.length;
    const connectedCount = followable.filter((source) => connected.has(source.source_id)).length;
    status.classList.toggle('is-connected', followable.length > 0 && connectedCount === followable.length);
    status.classList.toggle('is-reconnecting', followable.length > connectedCount);
    pauseButton.disabled = followable.length === 0;
    if (live.length === 0) {
      statusText.textContent = scoped.length
        ? `${scoped.length} retained source${scoped.length === 1 ? '' : 's'} · no live instances`
        : 'No retained log sources';
    } else if (followable.length === 0) {
      statusText.textContent = `${live.length} live source${live.length === 1 ? '' : 's'} · application logs external`;
    } else if (connectedCount === followable.length) {
      statusText.textContent = `Live · ${connectedCount} connected source${connectedCount === 1 ? '' : 's'}${external ? ` · ${external} external` : ''}`;
    } else {
      statusText.textContent = `Reconnecting · ${connectedCount} of ${followable.length} available live sources connected`;
    }
  }

  async function loadStaticSource(source, generation) {
    if (!source.has_log) {
      addEntry({ kind: 'event', replica: source.replica, line: `Replica #${source.replica} has no retained output` });
      return;
    }
    try {
      const run = source.legacy ? 'legacy' : source.run_id;
      const runParam = run ? `&run=${encodeURIComponent(run)}` : '';
      const resp = await api(`/api/apps/${encodeURIComponent(app.slug)}/logs?replica=${source.replica}${runParam}&tail=200&follow=false`);
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      const text = await resp.text();
      if (destroyed || generation !== scopeGeneration) return;
      for (const line of text.replace(/\n$/, '').split('\n')) {
        if (line || text) addEntry({ kind: 'line', replica: source.replica, line });
      }
    } catch {
      addEntry({ kind: 'event', replica: source.replica, line: `Replica #${source.replica} log is unavailable` });
    }
  }

  function openLiveSource(source, generation) {
    if (!EventSourceClass) {
      addEntry({ kind: 'event', replica: source.replica, line: 'Live streaming is not supported by this browser' });
      return;
    }
    const runParam = source.run_id ? `&run=${encodeURIComponent(source.run_id)}` : '';
    const url = `/api/apps/${encodeURIComponent(app.slug)}/logs?replica=${source.replica}${runParam}&tail=200`;
    const stream = new EventSourceClass(url, { withCredentials: true });
    streams.set(source.source_id, stream);
    stream.onopen = () => {
      if (destroyed || generation !== scopeGeneration) return;
      connected.add(source.source_id);
      updateConnectionStatus();
    };
    stream.onmessage = (event) => {
      if (!destroyed && generation === scopeGeneration) {
        addEntry({ kind: 'line', replica: source.replica, line: event.data });
      }
    };
    stream.onerror = () => {
      if (destroyed || generation !== scopeGeneration) return;
      connected.delete(source.source_id);
      updateConnectionStatus();
    };
  }

  function loadSource(source, generation = scopeGeneration) {
    if (loadedSources.has(source.source_id)) return;
    loadedSources.add(source.source_id);
    if (isLiveLogSource(source) && source.stream_available === false) {
      addEntry({
        kind: 'event', replica: source.replica,
        line: `Replica #${source.replica} application output is retained by its external runtime`,
      });
    } else if (isLiveLogSource(source)) openLiveSource(source, generation);
    else loadStaticSource(source, generation);
  }

  function closeStreams() {
    for (const stream of streams.values()) {
      stream.onopen = null;
      stream.onmessage = null;
      stream.onerror = null;
      stream.close();
    }
    streams.clear();
    connected.clear();
  }

  function resetScope() {
    scopeGeneration++;
    closeStreams();
    loadedSources.clear();
    entries = [];
    trimmed = 0;
    pendingWhilePaused = 0;
    pauseButton.textContent = paused ? 'Resume' : 'Pause live';
    for (const source of scopedSources()) loadSource(source, scopeGeneration);
    updateConnectionStatus();
    scheduleRender(true);
  }

  function reconcileSources(nextSources, markChanges) {
    const previous = new Map(sources.map((source) => [source.source_id, source]));
    sources = normalizeLogSources(nextSources);
    renderSourceOptions();
    if (markChanges) {
      for (const source of sources) {
        const old = previous.get(source.source_id);
        if (!sameSourceState(source, old)) {
          const message = sourceStatusEvent(source, old);
          if (message) addEntry({ kind: 'event', replica: source.replica, line: message });
        }
        if (selected !== 'all' && selected !== source.source_id) continue;
        if (selected === 'all' && !source.current) continue;
        if (old && isLiveLogSource(old) && !isLiveLogSource(source)) {
          const stream = streams.get(source.source_id);
          if (stream) stream.close();
          streams.delete(source.source_id);
          connected.delete(source.source_id);
        } else if ((!old || !isLiveLogSource(old)) && isLiveLogSource(source)) {
          loadedSources.delete(source.source_id);
          loadSource(source, scopeGeneration);
        } else if (!old) {
          loadSource(source, scopeGeneration);
        }
      }
    }
    updateConnectionStatus();
  }

  async function refreshSources(markChanges = true) {
    try {
      const resp = await api(`/api/apps/${encodeURIComponent(app.slug)}/logs/sources`);
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      const payload = await resp.json();
      if (destroyed) return false;
      reconcileSources(payload && payload.sources, markChanges);
      return true;
    } catch {
      status.classList.add('is-reconnecting');
      statusText.textContent = sources.length
        ? 'Source discovery unavailable · showing known instances'
        : 'Could not discover log sources';
      return false;
    }
  }

  sourceSelect.addEventListener('change', () => {
    selected = sourceSelect.value;
    updateURL();
    resetScope();
    const source = sources.find((item) => item.source_id === selected);
    announce(selected === 'all' ? 'Showing all current replica runs' : `Showing ${source ? formatLogSourceLabel(source) : 'selected log run'}`);
  });
  searchInput.addEventListener('input', () => scheduleRender(true));
  pauseButton.addEventListener('click', () => {
    paused = !paused;
    pauseButton.setAttribute('aria-pressed', String(paused));
    if (paused) {
      pauseButton.textContent = 'Resume';
      announce('Live display paused. Incoming logs will continue buffering.');
    } else {
      const count = pendingWhilePaused;
      pendingWhilePaused = 0;
      pauseButton.textContent = 'Pause live';
      stickToBottom = true;
      scheduleRender(true);
      announce(`Live display resumed${count ? ` with ${count} buffered lines` : ''}.`);
    }
  });
  output.addEventListener('scroll', () => {
    stickToBottom = atBottom();
    jumpButton.hidden = stickToBottom;
  }, { passive: true });
  jumpButton.addEventListener('click', () => {
    stickToBottom = true;
    output.scrollTop = output.scrollHeight;
    jumpButton.hidden = true;
  });
  copyButton.addEventListener('click', async () => {
    const text = serializeLogEntries(filterLogEntries(entries, searchInput.value));
    try {
      await win.navigator.clipboard.writeText(text);
      announce('Visible logs copied.');
    } catch {
      announce('Could not copy logs.');
    }
  });
  downloadButton.addEventListener('click', () => {
    const text = serializeLogEntries(filterLogEntries(entries, searchInput.value));
    try {
      const blob = new win.Blob([text + (text ? '\n' : '')], { type: 'text/plain;charset=utf-8' });
      const url = win.URL.createObjectURL(blob);
      const a = doc.createElement('a');
      a.href = url;
      a.download = `${app.slug}-logs-${selected === 'all' ? 'current' : selected}.log`;
      a.click();
      win.URL.revokeObjectURL(url);
      announce('Log download started.');
    } catch {
      announce('Could not download logs.');
    }
  });

  async function start() {
    const discovered = await refreshSources(false);
    if (destroyed) return;
    if (!discovered) renderSourceOptions();
    resetScope();
    if (!discovered && sources.length === 0) scheduleRender(true);
    refreshTimer = win.setInterval(() => refreshSources(true), refreshEveryMs);
  }
  start();

  return () => {
    destroyed = true;
    closeStreams();
    if (refreshTimer != null) win.clearInterval(refreshTimer);
  };
}
