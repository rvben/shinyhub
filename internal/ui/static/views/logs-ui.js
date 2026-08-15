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

export function retainedLogDownloadURL(slug, source) {
  if (!source || !Number.isInteger(source.replica) || source.replica < 0 || !source.has_log) return '';
  const params = new URLSearchParams({
    replica: String(source.replica),
    download: 'true',
  });
  const run = source.legacy ? 'legacy' : source.run_id;
  if (run) params.set('run', run);
  return `/api/apps/${encodeURIComponent(slug)}/logs?${params.toString()}`;
}

// Return only snapshot lines that were not already observed at the end of a
// live stream. The largest suffix/prefix overlap handles repeated log messages
// without blindly appending the entire terminal tail.
export function unseenLogSuffix(observed, snapshot) {
  const max = Math.min(observed.length, snapshot.length);
  for (let overlap = max; overlap > 0; overlap--) {
    let matches = true;
    for (let i = 0; i < overlap; i++) {
      if (observed[observed.length - overlap + i] !== snapshot[i]) {
        matches = false;
        break;
      }
    }
    if (matches) return snapshot.slice(overlap);
  }
  return snapshot.slice();
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
  maxRenderedEntries = MAX_RENDERED_LOG_ENTRIES,
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
          <button id="logs-download" type="button" class="btn-row">Download visible</button>
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
  let cancelScheduledRender = null;
  let fullRenderPending = true;
  let renderedEntries = [];
  let renderedQuery = '';
  let refreshTimer = null;
  let scopeGeneration = 0;
  const streams = new Map();
  const loadedSources = new Set();
  const connected = new Set();
  const degraded = new Set();

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
    if (force) fullRenderPending = true;
    if ((paused && !force) || renderScheduled || destroyed) return;
    renderScheduled = true;
    const run = () => {
      renderScheduled = false;
      cancelScheduledRender = null;
      if (!destroyed) {
        const full = fullRenderPending;
        fullRenderPending = false;
        renderEntries(full);
      }
    };
    if (typeof win.requestAnimationFrame === 'function') {
      const frame = win.requestAnimationFrame(run);
      cancelScheduledRender = () => win.cancelAnimationFrame(frame);
    } else {
      const timer = win.setTimeout(run, 0);
      cancelScheduledRender = () => win.clearTimeout(timer);
    }
  }

  function flushScheduledRender() {
    if (!renderScheduled) return;
    if (cancelScheduledRender) cancelScheduledRender();
    renderScheduled = false;
    cancelScheduledRender = null;
    const full = fullRenderPending;
    fullRenderPending = false;
    renderEntries(full);
  }

  function addEntry(entry) {
    trimmed += appendBoundedLogEntry(entries, entry, maxRenderedEntries);
    if (paused) {
      pendingWhilePaused++;
      pauseButton.textContent = `Resume (${pendingWhilePaused})`;
      return;
    }
    scheduleRender();
  }

  function createEntryRow(entry) {
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
    return row;
  }

  function replaceRenderedEntries(visible, query) {
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
      for (const entry of visible) fragment.appendChild(createEntryRow(entry));
    }
    output.replaceChildren(fragment);
    renderedEntries = visible.slice();
    renderedQuery = query;
    return wasFollowing;
  }

  function appendRenderedEntries(visible, query) {
    const wasFollowing = stickToBottom && atBottom();
    if (visible.length === 0) {
      if (renderedEntries.length === 0) return wasFollowing;
      return replaceRenderedEntries(visible, query);
    }
    if (renderedEntries.length === 0) {
      const fragment = doc.createDocumentFragment();
      for (const entry of visible) fragment.appendChild(createEntryRow(entry));
      output.replaceChildren(fragment);
      renderedEntries = visible.slice();
      return wasFollowing;
    }

    // Between full renders the visible sequence can only lose entries from the
    // front and gain entries at the end. Reconcile that overlap in place so a
    // busy stream never rebuilds thousands of unchanged rows.
    const overlapStart = renderedEntries.indexOf(visible[0]);
    const retained = overlapStart < 0 ? 0 : renderedEntries.length - overlapStart;
    const aligned = overlapStart >= 0 && retained <= visible.length &&
      (retained === 0 || renderedEntries[renderedEntries.length - 1] === visible[retained - 1]);
    if (!aligned) return replaceRenderedEntries(visible, query);

    for (let i = 0; i < overlapStart; i++) output.firstElementChild?.remove();
    const fragment = doc.createDocumentFragment();
    for (const entry of visible.slice(retained)) fragment.appendChild(createEntryRow(entry));
    output.appendChild(fragment);
    renderedEntries = visible.slice();
    return wasFollowing;
  }

  function renderEntries(forceFull = false) {
    const query = searchInput.value;
    const visible = filterLogEntries(entries, query);
    const canAppend = !forceFull && query === renderedQuery;
    const wasFollowing = canAppend
      ? appendRenderedEntries(visible, query)
      : replaceRenderedEntries(visible, query);
    if (wasFollowing || stickToBottom) output.scrollTop = output.scrollHeight;
    const parts = [`${visible.length.toLocaleString()} visible line${visible.length === 1 ? '' : 's'}`];
    if (trimmed) parts.push(`${trimmed.toLocaleString()} older lines omitted from this session`);
    if (query) parts.push(`${entries.length.toLocaleString()} buffered`);
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
    updateDownloadControl();
    return selected !== prior;
  }

  function updateDownloadControl() {
    const source = sources.find((item) => item.source_id === selected);
    const retainedURL = selected === 'all' ? '' : retainedLogDownloadURL(app.slug, source);
    downloadButton.disabled = selected !== 'all' && !retainedURL;
    downloadButton.textContent = selected === 'all' ? 'Download visible' : 'Download retained run';
    downloadButton.title = selected === 'all'
      ? 'Save the currently buffered and filtered merged output'
      : (retainedURL ? 'Save every byte retained for this run' : 'This source has no ShinyHub-retained output');
  }

  function updateConnectionStatus() {
    const scoped = scopedSources();
    const live = scoped.filter(isLiveLogSource);
    const followable = live.filter(isFollowableLogSource);
    const external = live.length - followable.length;
    const connectedCount = followable.filter((source) => connected.has(source.source_id)).length;
    const degradedCount = followable.filter((source) => degraded.has(source.source_id)).length;
    const disconnectedCount = followable.length - connectedCount;
    status.classList.toggle('is-connected', followable.length > 0 && connectedCount === followable.length && degradedCount === 0);
    status.classList.toggle('is-degraded', degradedCount > 0);
    status.classList.toggle('is-reconnecting', degradedCount === 0 && disconnectedCount > 0);
    pauseButton.disabled = followable.length === 0;
    if (live.length === 0) {
      statusText.textContent = scoped.length
        ? `${scoped.length} retained source${scoped.length === 1 ? '' : 's'} · no live instances`
        : 'No retained log sources';
    } else if (followable.length === 0) {
      statusText.textContent = `${live.length} live source${live.length === 1 ? '' : 's'} · application logs external`;
    } else if (degradedCount > 0) {
      statusText.textContent = `Delayed · ${degradedCount} live source${degradedCount === 1 ? '' : 's'} waiting for log storage${disconnectedCount ? ` · ${disconnectedCount} reconnecting` : ''}`;
    } else if (connectedCount === followable.length) {
      statusText.textContent = `Live · ${connectedCount} connected source${connectedCount === 1 ? '' : 's'}${external ? ` · ${external} external` : ''}`;
    } else {
      statusText.textContent = `Reconnecting · ${connectedCount} of ${followable.length} available live sources connected`;
    }
  }

  function staticLogURL(source) {
    const run = source.legacy ? 'legacy' : source.run_id;
    const runParam = run ? `&run=${encodeURIComponent(run)}` : '';
    return `/api/apps/${encodeURIComponent(app.slug)}/logs?replica=${source.replica}${runParam}&tail=200&follow=false`;
  }

  async function fetchStaticLines(source) {
    const resp = await api(staticLogURL(source));
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const text = await resp.text();
    if (!text) return [];
    return (text.endsWith('\n') ? text.slice(0, -1) : text).split('\n');
  }

  async function loadStaticSource(source, generation) {
    if (!source.has_log) {
      addEntry({ kind: 'event', replica: source.replica, line: `Replica #${source.replica} has no retained output` });
      return;
    }
    try {
      const lines = await fetchStaticLines(source);
      if (destroyed || generation !== scopeGeneration) return;
      for (const line of lines) {
        addEntry({ kind: 'line', source_id: source.source_id, replica: source.replica, line });
      }
    } catch {
      if (destroyed || generation !== scopeGeneration) return;
      addEntry({ kind: 'event', replica: source.replica, line: `Replica #${source.replica} log is unavailable` });
    }
  }

  async function reconcileTerminalSource(source, generation, terminalMessage = '') {
    try {
      if (source.has_log && source.stream_available !== false) {
        const snapshot = await fetchStaticLines(source);
        if (destroyed || generation !== scopeGeneration) return;
        const observed = entries
          .filter((entry) => entry.kind === 'line' && entry.source_id === source.source_id)
          .map((entry) => entry.line);
        for (const line of unseenLogSuffix(observed, snapshot)) {
          addEntry({ kind: 'line', source_id: source.source_id, replica: source.replica, line });
        }
      }
    } catch {
      if (destroyed || generation !== scopeGeneration) return;
      addEntry({
        kind: 'event', replica: source.replica,
        line: `Replica #${source.replica} final retained output could not be reconciled`,
      });
    } finally {
      if (terminalMessage && !destroyed && generation === scopeGeneration) {
        addEntry({ kind: 'event', replica: source.replica, line: terminalMessage });
      }
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
        addEntry({ kind: 'line', source_id: source.source_id, replica: source.replica, line: event.data });
      }
    };
    if (typeof stream.addEventListener === 'function') {
      stream.addEventListener('retention-gap', () => {
        if (!destroyed && generation === scopeGeneration) {
          addEntry({
            kind: 'event', replica: source.replica,
            line: `Replica #${source.replica} reconnected after older output was no longer retained`,
          });
          announce(`Some earlier output from replica ${source.replica} is no longer retained.`);
        }
      });
      stream.addEventListener('stream-degraded', () => {
        if (destroyed || generation !== scopeGeneration || degraded.has(source.source_id)) return;
        degraded.add(source.source_id);
        updateConnectionStatus();
        addEntry({
          kind: 'event', replica: source.replica,
          line: `Replica #${source.replica} live output delayed while retained log storage recovers`,
        });
        announce(`Live output from replica ${source.replica} is temporarily delayed. It will catch up automatically.`);
      });
      stream.addEventListener('stream-recovered', () => {
        if (destroyed || generation !== scopeGeneration || !degraded.delete(source.source_id)) return;
        updateConnectionStatus();
        addEntry({
          kind: 'event', replica: source.replica,
          line: `Replica #${source.replica} live output delivery recovered`,
        });
        announce(`Live output from replica ${source.replica} recovered and is catching up.`);
      });
    }
    stream.onerror = () => {
      if (destroyed || generation !== scopeGeneration) return;
      connected.delete(source.source_id);
      degraded.delete(source.source_id);
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
    degraded.clear();
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
    const selectionExpired = renderSourceOptions();
    if (selectionExpired) {
      updateURL();
      if (markChanges) {
        resetScope();
        const message = 'Selected log run is no longer retained; showing all current replicas';
        addEntry({ kind: 'event', replica: null, line: message });
        announce(`${message}.`);
      }
    }
    if (markChanges) {
      for (const source of sources) {
        const old = previous.get(source.source_id);
        const becameTerminal = old && isLiveLogSource(old) && !isLiveLogSource(source);
        if (!sameSourceState(source, old)) {
          const message = sourceStatusEvent(source, old);
          if (message && !becameTerminal) addEntry({ kind: 'event', replica: source.replica, line: message });
        }
        if (selected !== 'all' && selected !== source.source_id) continue;
        if (selected === 'all' && !source.current) continue;
        if (becameTerminal) {
          const stream = streams.get(source.source_id);
          if (stream) stream.close();
          streams.delete(source.source_id);
          connected.delete(source.source_id);
          degraded.delete(source.source_id);
          reconcileTerminalSource(source, scopeGeneration, sourceStatusEvent(source, old));
        } else if (old && !isLiveLogSource(source) && source.has_log &&
          (!old.has_log || source.size_bytes !== old.size_bytes)) {
          // The terminal metadata can become visible before the writer's final
          // flush. Reconcile again when retained bytes arrive on a later poll.
          reconcileTerminalSource(source, scopeGeneration);
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
    updateDownloadControl();
    updateURL();
    resetScope();
    const source = sources.find((item) => item.source_id === selected);
    announce(selected === 'all' ? 'Showing all current replica runs' : `Showing ${source ? formatLogSourceLabel(source) : 'selected log run'}`);
  });
  searchInput.addEventListener('input', () => scheduleRender(true));
  pauseButton.addEventListener('click', () => {
    // Finish the already-scheduled frame before entering pause. Otherwise a
    // line arriving between the click and that frame would leak into the DOM
    // even though it is correctly counted as buffered.
    if (!paused) flushScheduledRender();
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
  function saveBlob(blob, filename) {
    const url = win.URL.createObjectURL(blob);
    const a = doc.createElement('a');
    a.href = url;
    a.download = filename;
    a.click();
    win.URL.revokeObjectURL(url);
  }

  downloadButton.addEventListener('click', async () => {
    try {
      if (selected !== 'all') {
        const source = sources.find((item) => item.source_id === selected);
        const url = retainedLogDownloadURL(app.slug, source);
        if (!url) throw new Error('no retained log');
        downloadButton.disabled = true;
        downloadButton.textContent = 'Downloading…';
        const resp = await api(url);
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const blob = await resp.blob();
        if (destroyed) return;
        const identity = source.run_id || (source.legacy ? 'legacy' : source.source_id);
        saveBlob(blob, `${app.slug}-replica-${source.replica}-${identity}.log`);
        announce('Complete retained run downloaded.');
        return;
      }
      const text = serializeLogEntries(filterLogEntries(entries, searchInput.value));
      const blob = new win.Blob([text + (text ? '\n' : '')], { type: 'text/plain;charset=utf-8' });
      saveBlob(blob, `${app.slug}-logs-visible.log`);
      announce('Visible merged logs downloaded.');
    } catch {
      announce('Could not download logs.');
    } finally {
      if (!destroyed) updateDownloadControl();
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
    if (cancelScheduledRender) cancelScheduledRender();
    closeStreams();
    if (refreshTimer != null) win.clearInterval(refreshTimer);
  };
}
