// Multi-replica, immutable-run application log viewer. Replica numbers identify
// reusable pool slots; source IDs identify concrete executions of those slots.

export const MAX_RENDERED_LOG_ENTRIES = 2500;
const MAX_CONCURRENT_PROVIDER_READS = 3;

// Matches internal/api/logs.go's defaultLogTail/maxLogTail: a static source's
// first fetch pulls this many lines, and Load older lines can grow the
// request up to this ceiling. Keeping both in one place means neither one can
// drift from what the server actually honors.
export const DEFAULT_LOG_TAIL = 200;
export const MAX_LOG_TAIL = 10000;

// Each click on Load older lines multiplies the requested history rather than
// adding a fixed amount, so a handful of clicks reach the server's ceiling
// instead of trickling toward it one small page at a time.
export function nextLoadMoreTail(current, max = MAX_LOG_TAIL) {
  return Math.min(max, Math.max(current, DEFAULT_LOG_TAIL) * 5);
}

// A source's loaded window may not cover its full retained history: a static
// or provider source's fetch is bounded by its tail/page parameter, and a
// live source's pre-stream burst carries the same bound. Search and the
// toolbar cap notice both read this, so neither one claims a completeness the
// buffer cannot back up (an empty search result is not proof of no match).
export function logsAreTruncated(scoped, { trimmed = 0, fullyLoadedSourceIDs } = {}) {
  if (trimmed > 0) return true;
  const loaded = fullyLoadedSourceIDs instanceof Set ? fullyLoadedSourceIDs : new Set(fullyLoadedSourceIDs || []);
  return scoped.some((source) => !isExternalLogSource(source) && !loaded.has(source.source_id));
}

// The empty-output message shown under the log viewport. Pulled out as a pure
// function so the truncation caveat (and its wording) is unit-tested without
// standing up the whole DOM-driven viewer.
export function logsEmptyMessage({ hasSearch, truncated, canLoadMore, hasScopedSources, allExternal, hasAnySources }) {
  if (hasSearch) {
    if (!truncated) return 'No visible log lines match this search.';
    return canLoadMore
      ? 'No matches in the log history loaded so far. Older output may not be loaded yet, so try Load older lines.'
      : 'No matches in the log history loaded so far. Older output may not be loaded yet.';
  }
  if (hasScopedSources && allExternal) {
    return 'Application output is retained by its provider. Use the access details above.';
  }
  return hasAnySources ? 'Waiting for application output…' : 'No retained log sources were found.';
}

const LIVE_STATUSES = new Set(['running', 'starting', 'deploying', 'waking']);

export function isLiveLogSource(source) {
  return source && source.current !== false &&
    LIVE_STATUSES.has(String(source.status || '').toLowerCase());
}

export function isFollowableLogSource(source) {
  return isLiveLogSource(source) && source.stream_available !== false;
}

export function isInlineProviderLogSource(source) {
  return source && source.stream_available === false && source.inline_available === true;
}

export function isExternalLogSource(source) {
  return source && source.stream_available === false && !isInlineProviderLogSource(source);
}

function shellQuote(value) {
  return `'${String(value).replaceAll("'", `'"'"'`)}'`;
}

export function externalLogsCommand(source) {
  const details = source && source.external_logs;
  if (details?.provider === 'systemd_journal' && /^shinyhub-isolated-[0-9]+-[0-9a-f]{32}\.service$/.test(details.resource || '')) return `journalctl --unit=${shellQuote(details.resource)}`;
  if (!details || details.provider !== 'aws_ecs' || !details.resource || !details.region) return '';
  if (details.log_group && details.log_stream) {
    return `aws logs get-log-events --log-group-name ${shellQuote(details.log_group)} --log-stream-name ${shellQuote(details.log_stream)} --region ${shellQuote(details.region)}`;
  }
  if (!details.cluster) return '';
  return `aws ecs describe-tasks --cluster ${shellQuote(details.cluster)} --tasks ${shellQuote(details.resource)} --region ${shellQuote(details.region)}`;
}

export function safeExternalLogsURL(source) {
  const details = source && source.external_logs;
  const raw = details && (details.log_url || details.console_url);
  if (!raw) return '';
  try {
    const url = new URL(raw);
    const region = String(details.region || '');
    const partition = String(details.resource || '').split(':')[1];
    const domains = {
      aws: ['console.aws.amazon.com', '.console.aws.amazon.com'],
      'aws-cn': ['console.amazonaws.cn', '.console.amazonaws.cn'],
      'aws-us-gov': ['console.amazonaws-us-gov.com', '.console.amazonaws-us-gov.com'],
    };
    const domain = domains[partition];
    const allowed = domain ? [domain[0]] : [];
    if (domain && /^[a-z0-9-]+$/.test(region)) allowed.push(`${region}${domain[1]}`);
    return url.protocol === 'https:' && !url.username && !url.password && !url.port && allowed.includes(url.hostname)
      ? url.href
      : '';
  } catch {
    return '';
  }
}

function externalLogsFallbackURL(source) {
  if (!source || source.provider !== 'fargate') return '';
  const resource = source.external_logs && source.external_logs.resource || '';
  if (resource.startsWith('arn:aws-cn:')) return 'https://console.amazonaws.cn/ecs/v2';
  if (resource.startsWith('arn:aws-us-gov:')) return 'https://console.amazonaws-us-gov.com/ecs/v2';
  return 'https://console.aws.amazon.com/ecs/v2';
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
    return `Replica #${source.replica}: ${titleCase(source.status)}${place ? ` · ${place}` : ''}`;
  }
  const when = source.started_at || source.updated_at;
  const stamp = when ? new Date(when).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : 'Earlier run';
  return `Replica #${source.replica}: ${stamp} · ${titleCase(source.status)}`;
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

// Presentation only: never normalize application text or use inferred dates
// to reorder independent streams. Unknown and multiline formats stay raw.
function logLineParts(line) {
  if (/[\r\n]/.test(line)) return [{ text: line }];
  const parts = [];
  const timestamp = line.match(/^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?(?=[ \t])/);
  let rest = line;
  if (timestamp) {
    parts.push({ text: timestamp[0], role: 'timestamp' });
    rest = line.slice(timestamp[0].length);
  }
  const severity = rest.match(/^([ \t]*)(\[(?:TRACE|DEBUG|INFO|NOTICE|WARN|WARNING|ERROR|FATAL|CRITICAL)\]|TRACE|DEBUG|INFO|NOTICE|WARN|WARNING|ERROR|FATAL|CRITICAL)(?=[ \t:]|$)/i);
  if (severity && (timestamp || !severity[1])) {
    if (severity[1]) parts.push({ text: severity[1] });
    const level = severity[2].replace(/[\[\]]/g, '').toLowerCase();
    parts.push({ text: severity[2], role: 'level', level });
    rest = rest.slice(severity[0].length);
  }
  if (rest) parts.push({ text: rest });
  return parts;
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
    a.current === b.current && a.oom_killed === b.oom_killed &&
    a.exit_code === b.exit_code && a.exit_signal === b.exit_signal &&
    a.exit_reason === b.exit_reason;
}

function sourceExitDiagnostics(source) {
  const details = [];
  if (source.oom_killed) details.push('OOM-killed');
  if (source.exit_signal) details.push(source.exit_signal);
  else if (source.exit_code !== null && source.exit_code !== undefined) details.push(`exit ${source.exit_code}`);
  if (source.exit_reason) details.push(source.exit_reason);
  return details.length ? ` · ${details.join(' · ')}` : '';
}

function sourceStatusEvent(source, previous) {
  if (!source.current) return '';
  if (!previous) return `Replica #${source.replica} run discovered (${titleCase(source.status)})${sourceExitDiagnostics(source)}`;
  if (source.status === previous.status) return '';
  if (isLiveLogSource(source)) return `Replica #${source.replica} started (${titleCase(source.status)})`;
  return `Replica #${source.replica} ${titleCase(source.status).toLocaleLowerCase()}${sourceExitDiagnostics(source)}`;
}

async function mapWithConcurrency(items, limit, mapper) {
  const results = new Array(items.length);
  let next = 0;
  async function worker() {
    while (next < items.length) {
      const index = next++;
      results[index] = await mapper(items[index], index);
    }
  }
  const workers = Array.from({ length: Math.min(limit, items.length) }, () => worker());
  await Promise.all(workers);
  return results;
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
  let developmentSessionID = '';
  try {
    const candidate = new URLSearchParams(win.location && win.location.search || '').get('development_session_id') || '';
    if (/^[a-f0-9]{32}$/.test(candidate)) developmentSessionID = candidate;
  } catch { /* location is optional in embedded/test contexts */ }
  panel.innerHTML = `
    <div class="logs-workspace">
      <header class="app-tab-heading"><h2>Logs</h2></header>
      ${developmentSessionID ? `<div class="observability-scope" role="status">
        <span>Showing logs from one remote development session.</span>
        <a href="/apps/${encodeURIComponent(app.slug)}/logs" data-nav>Show all logs</a>
      </div>` : ''}
      <div class="logs-toolbar" aria-label="Log controls">
        <div class="logs-toolbar-fields">
          <label class="logs-field">
            <span>Source</span>
            <select id="logs-source" aria-label="Log sources"></select>
          </label>
          <label class="logs-field logs-search-field">
            <span>Search</span>
            <input id="logs-search" type="search" placeholder="Search loaded lines" autocomplete="off" aria-describedby="logs-output-summary">
          </label>
        </div>
      </div>
      <div class="logs-pane-header">
        <div class="logs-reading-context">
          <p id="logs-stream-status" class="logs-stream-status">
            <span class="logs-status-dot" aria-hidden="true"></span>
            <span class="logs-status-text">Discovering log sources…</span>
          </p>
          <p id="logs-output-summary" class="logs-output-summary"></p>
          <p id="logs-order" class="logs-order" hidden>Combined output · not globally time-sorted</p>
          <div class="logs-history-actions">
            <button id="logs-load-more" type="button" class="btn-row" hidden>Load older lines</button>
            <button id="logs-clear-search" type="button" class="btn-row" hidden>Clear search</button>
          </div>
        </div>
        <div class="logs-pane-actions">
          <div class="logs-live-actions">
            <button id="logs-jump" type="button" class="logs-jump btn-row" hidden>Jump to latest</button>
            <button id="logs-pause" type="button" class="btn-row" aria-pressed="false">Pause live</button>
          </div>
          <div class="logs-toolbar-actions">
            <button id="logs-copy" type="button" class="btn-row" disabled>Copy visible</button>
            <button id="logs-download" type="button" class="btn-row" disabled>Download visible</button>
          </div>
        </div>
      </div>
      <div id="logs-error" class="logs-error" hidden>
        <p id="logs-error-message" role="alert"></p>
        <button id="logs-retry" type="button" class="btn-row">Retry</button>
      </div>
      <section id="logs-external" class="logs-external" aria-label="External log access" hidden></section>
      <p id="logs-feedback" class="logs-feedback" hidden></p>
      <div id="detail-logs-body" class="detail-logs-body" tabindex="0"
           aria-label="Application log output" aria-describedby="logs-output-summary logs-order"></div>
      <p id="logs-announcement" class="sr-only" role="status" aria-live="polite"></p>
    </div>
  `;

  const sourceSelect = panel.querySelector('#logs-source');
  const searchInput = panel.querySelector('#logs-search');
  const loadMoreButton = panel.querySelector('#logs-load-more');
  const pauseButton = panel.querySelector('#logs-pause');
  const copyButton = panel.querySelector('#logs-copy');
  const downloadButton = panel.querySelector('#logs-download');
  const jumpButton = panel.querySelector('#logs-jump');
  const status = panel.querySelector('#logs-stream-status');
  const statusText = panel.querySelector('.logs-status-text');
  const externalPanel = panel.querySelector('#logs-external');
  const output = panel.querySelector('#detail-logs-body');
  const summary = panel.querySelector('#logs-output-summary');
  const order = panel.querySelector('#logs-order');
  const announcement = panel.querySelector('#logs-announcement');
  const clearSearch = panel.querySelector('#logs-clear-search');
  const errorPanel = panel.querySelector('#logs-error');
  const errorMessage = panel.querySelector('#logs-error-message');
  const retryButton = panel.querySelector('#logs-retry');
  const feedback = panel.querySelector('#logs-feedback');
  const workspace = panel.querySelector('.logs-workspace');

  let sources = normalizeLogSources(initialSources);
  let selected = 'all';
  let entries = [];
  let trimmed = 0;
  // Load older lines only re-fetches the single selected static source (the
  // common case: browsing a stopped/crashed run's history); provider sources
  // paginate forward by cursor only, and a live source keeps growing via its
  // own stream, so neither offers a meaningful "older" fetch to grow.
  let staticTail = DEFAULT_LOG_TAIL;
  const fullyLoadedSourceIDs = new Set();
  let paused = false;
  let pausedEntries = [];
  let discovering = true;
  let discoveryFailed = false;
  let discoveryInFlight = null;
  let loadingHistory = false;
  let downloading = false;
  const sourceFailures = new Map();
  const staticLoading = new Set();
  let pendingWhilePaused = 0;
  let stickToBottom = true;
  let destroyed = false;
  let layoutFrame = null;
  let renderScheduled = false;
  let cancelScheduledRender = null;
  let fullRenderPending = true;
  let renderedEntries = [];
  let renderedQuery = '';
  let refreshTimer = null;
  let providerTimer = null;
  let scopeGeneration = 0;
  const streams = new Map();
  const loadedSources = new Set();
  const connected = new Set();
  const degraded = new Set();
  const providerCursors = new Map();
  const providerLoading = new Map();
  const providerControllers = new Map();
  const providerConnected = new Set();
  const providerDegraded = new Set();
  const providerCompleted = new Set();
  const providerIdlePolls = new Map();
  const providerFailurePolls = new Map();
  const providerNextPollAt = new Map();

  function fitOutput() {
    layoutFrame = null;
    if (destroyed) return;
    // Use the document position so scrolling cannot change the pane's height.
    // Its CSS minimum still allows page scrolling in short/zoomed viewports.
    const top = output.getBoundingClientRect().top + (win.scrollY || 0);
    const bottomSpace = parseFloat(win.getComputedStyle(workspace).paddingBottom) || 24;
    const height = `${Math.max(0, win.innerHeight - top - bottomSpace)}px`;
    if (workspace.style.getPropertyValue('--logs-available-height') !== height) {
      workspace.style.setProperty('--logs-available-height', height);
      if (stickToBottom) {
        output.scrollTop = output.scrollHeight;
        jumpButton.hidden = true;
      }
    }
  }
  function scheduleLayout() {
    if (destroyed || layoutFrame != null) return;
    if (typeof win.requestAnimationFrame === 'function') layoutFrame = win.requestAnimationFrame(fitOutput);
    else fitOutput();
  }
  const layoutObserver = typeof win.ResizeObserver === 'function' ? new win.ResizeObserver(scheduleLayout) : null;
  layoutObserver?.observe(workspace);
  win.addEventListener('resize', scheduleLayout);
  scheduleLayout();

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

  function showFeedback(message) {
    if (destroyed) return;
    feedback.textContent = message;
    feedback.hidden = false;
    announce(message);
  }

  function updateError() {
    if (destroyed) return;
    const failed = scopedSources().filter(source => sourceFailures.has(source.source_id));
    errorMessage.textContent = discoveryFailed
      ? (sources.length ? 'Could not refresh log sources. Showing known runs.' : 'Could not load log sources. Try again.')
      : failed.length ? `Could not load output for ${failed.map(source => `replica #${source.replica}`).join(', ')}. Try again.` : '';
    errorPanel.hidden = !errorMessage.textContent;
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
      updateConnectionStatus();
      return;
    }
    scheduleRender();
  }

  function appendHighlighted(target, text, offset = 0, completeText = text) {
    const query = searchInput.value.trim();
    const index = query ? completeText.toLocaleLowerCase().indexOf(query.toLocaleLowerCase()) : -1;
    const start = Math.max(0, index - offset);
    const end = Math.min(text.length, index + query.length - offset);
    if (index < 0 || end <= start) target.textContent = text;
    else {
      const match = doc.createElement('mark');
      match.textContent = text.slice(start, end);
      target.append(text.slice(0, start), match, text.slice(end));
    }
  }

  function createEntryRow(entry) {
    const row = doc.createElement('div');
    row.className = entry.kind === 'event' ? 'log-entry log-entry-event' : 'log-entry';
    if (entry.kind === 'event') {
      const message = doc.createElement('span');
      appendHighlighted(message, entry.line);
      row.appendChild(message);
    } else {
      const source = doc.createElement('span');
      source.className = 'log-entry-source';
      const sourceText = `#${entry.replica}`;
      const completeText = `${sourceText} ${entry.line}`;
      appendHighlighted(source, sourceText, 0, completeText);
      source.title = `Replica #${entry.replica}`;
      const message = doc.createElement('span');
      message.className = 'log-entry-message';
      let offset = sourceText.length + 1;
      for (const part of logLineParts(entry.line)) {
        const token = doc.createElement('span');
        if (part.role) token.className = `log-entry-${part.role}${part.level ? ` log-level-${part.level}` : ''}`;
        appendHighlighted(token, part.text, offset, completeText);
        message.appendChild(token);
        offset += part.text.length;
      }
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
      const scoped = scopedSources();
      empty.textContent = discovering ? 'Loading log sources…'
        : discoveryFailed && !sources.length ? 'Log sources could not be loaded. Retry above to check again.'
        : staticLoading.size && !entries.length ? 'Loading retained output…'
        : scoped.some(source => sourceFailures.has(source.source_id)) && !entries.length ? 'Output could not be loaded. Retry above to check again.'
        : !searchInput.value.trim() && scoped.length && scoped.every(source => !isLiveLogSource(source) && fullyLoadedSourceIDs.has(source.source_id)) ? 'No output was recorded for this run.'
        : logsEmptyMessage({
        hasSearch: Boolean(searchInput.value.trim()),
        truncated: logsAreTruncated(scoped, { trimmed, fullyLoadedSourceIDs }),
        canLoadMore: !loadMoreButton.hidden,
        hasScopedSources: scoped.length > 0,
        allExternal: scoped.length > 0 && scoped.every(isExternalLogSource),
        hasAnySources: sources.length > 0,
      });
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
    const displayed = paused ? pausedEntries : entries;
    const visible = filterLogEntries(displayed, query);
    const canAppend = !forceFull && query === renderedQuery;
    const wasFollowing = canAppend
      ? appendRenderedEntries(visible, query)
      : replaceRenderedEntries(visible, query);
    if (wasFollowing || stickToBottom) output.scrollTop = output.scrollHeight;
    const parts = [`${visible.length.toLocaleString()} visible line${visible.length === 1 ? '' : 's'}`];
    if (trimmed) parts.push(`${trimmed.toLocaleString()} older lines omitted from this session`);
    if (query.trim()) parts.push(`of ${displayed.length.toLocaleString()} loaded`);
    // Matches are limited to the loaded window too; keep that boundary visible
    // even when a search has results.
    if (logsAreTruncated(scopedSources(), { trimmed: 0, fullyLoadedSourceIDs })) {
      parts.push('showing recent history only');
    }
    summary.textContent = parts.join(' · ');
    order.hidden = scopedSources().filter(source => !isExternalLogSource(source)).length < 2;
    clearSearch.hidden = !query.trim();
    copyButton.disabled = !visible.length;
    updateDownloadControl();
    jumpButton.hidden = stickToBottom;
  }

  function externalResourceLabel(source) {
    const resource = source.external_logs && source.external_logs.resource;
    if (!resource) return '';
    const taskID = resource.split('/').at(-1);
    return source.external_logs.provider === 'systemd_journal' ? `Unit ${resource}` : taskID ? `Task ${taskID}` : resource;
  }

  function renderExternalLogs() {
    const external = scopedSources().filter((source) => source.external_logs && ['aws_ecs', 'systemd_journal'].includes(source.external_logs.provider));
    if (!external.length) {
      externalPanel.hidden = true;
      externalPanel.replaceChildren();
      return;
    }
    const journal = external.some(source => source.external_logs.provider === 'systemd_journal');
    const intro = doc.createElement('div');
    intro.className = 'logs-external-intro';
    const title = doc.createElement('strong');
    title.textContent = journal ? (external.length === 1 ? 'Provider log source' : `${external.length} provider log sources`) : external.length === 1 ? 'AWS log source' : `${external.length} AWS log sources`;
    const explanation = doc.createElement('span');
    explanation.textContent = journal ? 'Read output retained by the log provider. System journal output survives controller restarts.' : external.some(isInlineProviderLogSource)
      ? 'Read on demand from CloudWatch. Direct AWS access remains available as a fallback.'
      : 'ShinyHub cannot read this output directly. Access remains subject to your AWS permissions.';
    intro.append(title, explanation);

    const list = doc.createElement('div');
    list.className = 'logs-external-list';
    for (const source of external) {
      const row = doc.createElement('div');
      row.className = 'logs-external-row';
      const identity = doc.createElement('div');
      identity.className = 'logs-external-identity';
      const name = doc.createElement('strong');
      name.textContent = formatLogSourceLabel(source);
      const details = doc.createElement('span');
      const location = [source.external_logs && source.external_logs.region, source.external_logs && source.external_logs.cluster]
        .filter(Boolean).join(' · ');
      const logIdentity = source.external_logs && source.external_logs.log_group && source.external_logs.log_stream
        ? `${source.external_logs.log_group} · ${source.external_logs.log_stream}`
        : '';
      const resourceLabel = logIdentity || externalResourceLabel(source);
      details.textContent = [resourceLabel, location].filter(Boolean).join(' · ') ||
        'External destination details were not recorded for this run';
      details.title = source.external_logs && source.external_logs.resource || '';
      identity.append(name, details);

      const actions = doc.createElement('div');
      actions.className = 'logs-external-actions';
      const consoleURL = safeExternalLogsURL(source) || externalLogsFallbackURL(source);
      if (consoleURL) {
        const open = doc.createElement('a');
        open.className = 'btn-row';
        open.href = consoleURL;
        open.target = '_blank';
        open.rel = 'noopener noreferrer';
        const directCloudWatch = source.external_logs && source.external_logs.log_url && consoleURL === source.external_logs.log_url;
        open.textContent = directCloudWatch ? 'Open CloudWatch logs' : (safeExternalLogsURL(source) ? 'Open task logs' : 'Open AWS ECS');
        open.setAttribute('aria-label', safeExternalLogsURL(source)
          ? `Open AWS logs for replica ${source.replica}`
          : `Open AWS ECS for replica ${source.replica}`);
        actions.appendChild(open);
      }
      const command = externalLogsCommand(source);
      if (command) {
        const copyCommand = doc.createElement('button');
        copyCommand.type = 'button';
        copyCommand.className = 'btn-row';
        const journalSource = source.external_logs.provider === 'systemd_journal';
        const commandLabel = journalSource ? 'journal' : 'AWS task';
        copyCommand.textContent = journalSource ? 'Copy journal command' : 'Copy AWS command';
        copyCommand.title = journalSource ? 'Copy a command to read this exact worker unit on the host' : 'Copy an AWS CLI command that identifies this exact ECS task';
        copyCommand.addEventListener('click', async () => {
          try {
            await win.navigator.clipboard.writeText(command);
            announce(`${journalSource ? "Journal" : "AWS task"} command copied for replica ${source.replica}.`);
          } catch {
            let fallback = row.querySelector('.logs-external-command');
            if (!fallback) {
              fallback = doc.createElement('code');
              fallback.className = 'logs-external-command';
              fallback.tabIndex = 0;
              row.appendChild(fallback);
            }
            fallback.textContent = command;
            announce(`Could not copy the ${commandLabel} command for replica ${source.replica}. The command is shown for manual copying.`);
          }
        });
        actions.appendChild(copyCommand);
      }
      row.append(identity, actions);
      list.appendChild(row);
    }
    externalPanel.replaceChildren(intro, list);
    externalPanel.hidden = false;
  }

  function renderSourceOptions() {
    const prior = selected;
    const all = doc.createElement('option');
    const currentSources = sources.filter((source) => source.current);
    const liveCount = currentSources.filter(isLiveLogSource).length;
    all.value = 'all';
    all.textContent = sources.length ? `All current replicas (${liveCount} live)` : 'No log sources';
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
    if (currentSources.length === 0 && sources.length > 0 && (prior === 'all' || !sources.some(source => source.source_id === prior))) selected = sources[0].source_id;
    else selected = prior === 'all' || sources.some((s) => s.source_id === prior) ? prior : 'all';
    sourceSelect.value = selected;
    sourceSelect.disabled = !sources.length;
    updateDownloadControl();
    renderExternalLogs();
    return selected !== prior;
  }

  function updateDownloadControl() {
    if (downloading) return;
    const source = sources.find((item) => item.source_id === selected);
    if (selected !== 'all' && source && source.external_logs) {
      downloadButton.disabled = true;
      downloadButton.textContent = source.external_logs.provider === 'systemd_journal' ? 'Journal-retained logs' : isInlineProviderLogSource(source) ? 'AWS-retained logs' : 'External logs';
      downloadButton.title = source.external_logs.provider === 'systemd_journal' ? 'Copy visible output or use the journal command above on the host' : 'Copy visible output or use the AWS access details above';
      return;
    }
    const retainedURL = selected === 'all' ? '' : retainedLogDownloadURL(app.slug, source);
    downloadButton.disabled = selected === 'all' ? !renderedEntries.length : !retainedURL;
    downloadButton.textContent = selected === 'all' ? 'Download visible' : 'Download retained run';
    downloadButton.title = selected === 'all'
      ? 'Save the currently buffered and filtered merged output'
      : (retainedURL ? 'Save every byte retained for this run' : 'This source has no ShinyHub-retained output');
  }

  function updateConnectionStatus() {
    const scoped = scopedSources();
    const live = scoped.filter(isLiveLogSource);
    const externalSources = scoped.filter(isExternalLogSource);
    const followable = live.filter(isFollowableLogSource);
    const inlineProvider = live.filter(isInlineProviderLogSource);
    const logProviderLabel = inlineProvider.some(source => source.external_logs?.provider === 'systemd_journal') ? 'provider' : 'CloudWatch';
    const external = live.length - followable.length - inlineProvider.length;
    const connectedCount = followable.filter((source) => connected.has(source.source_id)).length;
    const degradedCount = followable.filter((source) => degraded.has(source.source_id)).length;
    const providerConnectedCount = inlineProvider.filter((source) => providerConnected.has(source.source_id)).length;
    const providerDegradedCount = inlineProvider.filter((source) => providerDegraded.has(source.source_id)).length;
    const availableCount = connectedCount + providerConnectedCount;
    const availableTotal = followable.length + inlineProvider.length;
    const totalDegraded = degradedCount + providerDegradedCount;
    const disconnectedCount = availableTotal - availableCount;
    status.classList.toggle('is-connected', !paused && !discoveryFailed && availableTotal > 0 && availableCount === availableTotal && totalDegraded === 0);
    status.classList.toggle('is-paused', paused);
    status.classList.toggle('is-degraded', totalDegraded > 0);
    status.classList.toggle('is-reconnecting', totalDegraded === 0 && disconnectedCount > 0);
    pauseButton.disabled = !paused && availableTotal === 0;
    if (paused) {
      statusText.textContent = pendingWhilePaused
        ? `Paused · ${pendingWhilePaused.toLocaleString()} new line${pendingWhilePaused === 1 ? '' : 's'} received${pendingWhilePaused > entries.length ? ` · latest ${entries.length.toLocaleString()} buffered` : ''}`
        : 'Paused · incoming lines will be buffered';
    } else if (discoveryFailed) {
      statusText.textContent = sources.length ? 'Source discovery unavailable · showing known runs' : 'Could not discover log sources';
    } else if (discovering) {
      statusText.textContent = 'Discovering log sources…';
    } else if (live.length === 0) {
      statusText.textContent = scoped.length === 1 && isInlineProviderLogSource(scoped[0])
        ? `${titleCase(scoped[0].status)} · ${scoped[0].external_logs.provider === "systemd_journal" ? "Journal" : "CloudWatch"} logs available`
        : externalSources.length === scoped.length && scoped.length
        ? `${scoped.length === 1 ? titleCase(scoped[0].status) : `${scoped.length} external sources`} · logs retained in AWS`
        : scoped.length
        ? `${scoped.length} retained source${scoped.length === 1 ? '' : 's'} · no live instances`
        : 'No retained log sources';
    } else if (availableTotal === 0) {
      statusText.textContent = `${live.length} live source${live.length === 1 ? '' : 's'} · application logs external`;
    } else if (totalDegraded > 0) {
      statusText.textContent = `Delayed · ${totalDegraded} live source${totalDegraded === 1 ? '' : 's'} waiting for log storage${disconnectedCount ? ` · ${disconnectedCount} reconnecting` : ''}`;
    } else if (availableCount === availableTotal) {
      statusText.textContent = inlineProvider.length === availableTotal
        ? `Live · ${availableCount} ${logProviderLabel} source${availableCount === 1 ? '' : 's'} connected`
        : `Live · ${availableCount} connected source${availableCount === 1 ? '' : 's'}${inlineProvider.length ? ` · ${inlineProvider.length} via ${logProviderLabel}` : ''}${external ? ` · ${external} external` : ''}`;
    } else {
      statusText.textContent = `Reconnecting · ${availableCount} of ${availableTotal} available live sources connected`;
    }
  }

  function staticLogURL(source, tail = DEFAULT_LOG_TAIL) {
    const run = source.legacy ? 'legacy' : source.run_id;
    const runParam = run ? `&run=${encodeURIComponent(run)}` : '';
    return `/api/apps/${encodeURIComponent(app.slug)}/logs?replica=${source.replica}${runParam}&tail=${tail}&follow=false`;
  }

  function providerLogURL(source, cursor = '') {
    const params = new URLSearchParams({
      replica: String(source.replica),
      run: source.run_id,
      provider: 'true',
      tail: '200',
    });
    if (cursor) params.set('cursor', cursor);
    return `/api/apps/${encodeURIComponent(app.slug)}/logs?${params.toString()}`;
  }

  function retryAfterDelayMs(response) {
    if (!response || response.status !== 429 || !response.headers || typeof response.headers.get !== 'function') return 0;
    const raw = String(response.headers.get('Retry-After') || '').trim();
    if (!raw) return 0;
    if (/^\d+$/.test(raw)) return Math.min(60_000, Number(raw) * 1000);
    const retryAt = Date.parse(raw);
    return Number.isFinite(retryAt) ? Math.min(60_000, Math.max(0, retryAt - Date.now())) : 0;
  }

  async function performProviderSourceFetch(source, generation) {
    const cursor = providerCursors.get(source.source_id) || '';
    const controller = typeof win.AbortController === 'function' ? new win.AbortController() : null;
    if (controller) providerControllers.set(source.source_id, controller);
    try {
      const resp = await api(providerLogURL(source, cursor), controller ? { signal: controller.signal } : {});
      if (!resp.ok) {
        const error = new Error(`HTTP ${resp.status}`);
        error.throttled = resp.status === 429;
        error.retryAfterMs = retryAfterDelayMs(resp);
        throw error;
      }
      const page = await resp.json();
      if (destroyed || generation !== scopeGeneration) return { entries: [], outcome: 'aborted' };
      const nextCursor = String(page && page.next_cursor || '');
      // CloudWatch returns the same forward token when the caller is caught up.
      // Treat that as an empty page even if an intermediary replayed the body.
      if (cursor && nextCursor === cursor) {
        if (!isLiveLogSource(source)) providerCompleted.add(source.source_id);
        providerConnected.add(source.source_id);
        providerDegraded.delete(source.source_id);
        updateConnectionStatus();
        return { entries: [], outcome: isLiveLogSource(source) ? 'idle' : 'complete' };
      }
      if (nextCursor) providerCursors.set(source.source_id, nextCursor);
      if (!isLiveLogSource(source)) providerCompleted.add(source.source_id);
      providerConnected.add(source.source_id);
      providerDegraded.delete(source.source_id);
      updateConnectionStatus();
      const entries = (Array.isArray(page && page.events) ? page.events : []).map((event) => ({
        kind: 'line', source_id: source.source_id, replica: source.replica,
        line: String(event && event.message || ''), timestamp: Date.parse(event && event.timestamp || '') || 0,
      }));
      return {
        entries,
        outcome: isLiveLogSource(source) ? (entries.length ? 'active' : 'idle') : 'complete',
      };
    } catch (error) {
      if (destroyed || generation !== scopeGeneration || error && error.name === 'AbortError') {
        return { entries: [], outcome: 'aborted' };
      }
      providerConnected.delete(source.source_id);
      const firstFailure = !providerDegraded.has(source.source_id);
      providerDegraded.add(source.source_id);
      updateConnectionStatus();
      if (firstFailure && !error.throttled) {
        addEntry({
          kind: 'event', replica: source.replica,
          line: source.external_logs?.provider === "systemd_journal" ? `Replica #${source.replica} journal output is unavailable in ShinyHub; use the journal command above on the host` : `Replica #${source.replica} CloudWatch output is unavailable in ShinyHub; use the AWS access above`,
        });
        announce(source.external_logs?.provider === "systemd_journal" ? `Journal output for replica ${source.replica} is unavailable in ShinyHub. The journal command remains available.` : `CloudWatch output for replica ${source.replica} is unavailable in ShinyHub. Direct AWS access remains available.`);
      }
      return {
        entries: [], outcome: error.throttled ? 'throttled' : 'error', retryAfterMs: error.retryAfterMs || 0,
      };
    } finally {
      if (providerControllers.get(source.source_id) === controller) providerControllers.delete(source.source_id);
    }
  }

  async function fetchProviderSource(source, generation, { waitForExisting = false, force = false } = {}) {
    if (providerCompleted.has(source.source_id) && !force) return { entries: [], outcome: 'complete' };
    const existing = providerLoading.get(source.source_id);
    if (existing) {
      if (!waitForExisting) return { entries: [], outcome: 'busy' };
      await existing;
      if (destroyed || generation !== scopeGeneration) return { entries: [], outcome: 'aborted' };
      return fetchProviderSource(source, generation, { force });
    }
    const pending = performProviderSourceFetch(source, generation);
    providerLoading.set(source.source_id, pending);
    try {
      return await pending;
    } finally {
      if (providerLoading.get(source.source_id) === pending) providerLoading.delete(source.source_id);
    }
  }

  function clearProviderTimer() {
    if (providerTimer != null) win.clearTimeout(providerTimer);
    providerTimer = null;
  }

  function scheduleNextProviderPoll() {
    clearProviderTimer();
    if (destroyed || doc.hidden) return;
    const pollable = scopedSources().filter((source) =>
      isInlineProviderLogSource(source) && !providerCompleted.has(source.source_id));
    if (!pollable.length) return;
    const now = Date.now();
    const nextAt = Math.min(...pollable.map((source) => providerNextPollAt.get(source.source_id) || now));
    providerTimer = win.setTimeout(() => {
      providerTimer = null;
      pollProviderSources(scopeGeneration);
    }, Math.max(0, nextAt - now));
  }

  function updateProviderPollSchedule(source, outcome, retryAfterMs = 0) {
    const id = source.source_id;
    if (outcome === 'complete') {
      providerCompleted.add(id);
      providerNextPollAt.delete(id);
      return;
    }
    const base = Math.max(1, refreshEveryMs);
    let delay = base;
    if (outcome === 'active') {
      providerIdlePolls.delete(id);
      providerFailurePolls.delete(id);
    } else if (outcome === 'idle') {
      const idle = (providerIdlePolls.get(id) || 0) + 1;
      providerIdlePolls.set(id, idle);
      providerFailurePolls.delete(id);
      delay = Math.min(30_000, base * (2 ** Math.min(idle, 6)));
    } else if (outcome === 'error' || outcome === 'throttled') {
      const failures = (providerFailurePolls.get(id) || 0) + 1;
      providerFailurePolls.set(id, failures);
      delay = Math.min(60_000, base * (2 ** Math.min(failures, 7)));
      if (outcome === 'throttled') delay = Math.max(delay, retryAfterMs);
    } else if (outcome === 'aborted' || outcome === 'busy') {
      delay = base;
    }
    providerNextPollAt.set(id, Date.now() + delay);
  }

  async function pollProviderSources(generation = scopeGeneration, requestedSources = null) {
    if (destroyed || generation !== scopeGeneration || doc.hidden) return;
    const now = Date.now();
    const candidates = (requestedSources || scopedSources()).filter((source) =>
      isInlineProviderLogSource(source) && !providerCompleted.has(source.source_id));
    const pollable = requestedSources ? candidates : candidates.filter((source) =>
      (providerNextPollAt.get(source.source_id) || 0) <= now);
    if (!pollable.length) {
      scheduleNextProviderPoll();
      return;
    }
    const batches = await mapWithConcurrency(pollable, MAX_CONCURRENT_PROVIDER_READS, async (source) => ({
      source,
      result: destroyed || generation !== scopeGeneration || doc.hidden
        ? { entries: [], outcome: 'aborted' }
        : await fetchProviderSource(source, generation),
    }));
    if (destroyed || generation !== scopeGeneration) return;
    for (const { source, result } of batches) {
      updateProviderPollSchedule(source, result.outcome, result.retryAfterMs);
    }
    const batch = batches.flatMap(({ result }) => result.entries)
      .sort((a, b) => a.timestamp - b.timestamp || a.replica - b.replica);
    for (const entry of batch) addEntry(entry);
    scheduleNextProviderPoll();
  }

  async function reconcileTerminalProviderSource(source, generation, terminalMessage) {
    const result = await fetchProviderSource(source, generation, { waitForExisting: true, force: true });
    if (destroyed || generation !== scopeGeneration) return;
    updateProviderPollSchedule(source, result.outcome, result.retryAfterMs);
    for (const entry of result.entries) addEntry(entry);
    if (terminalMessage) addEntry({ kind: 'event', replica: source.replica, line: terminalMessage });
    scheduleNextProviderPoll();
  }

  async function fetchStaticLines(source, tail = DEFAULT_LOG_TAIL) {
    const resp = await api(staticLogURL(source, tail));
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const text = await resp.text();
    if (!text) return [];
    return (text.endsWith('\n') ? text.slice(0, -1) : text).split('\n');
  }

  async function loadStaticSource(source, generation) {
    if (!source.has_log) {
      addEntry({ kind: 'event', replica: source.replica, line: `Replica #${source.replica} has no retained output` });
      fullyLoadedSourceIDs.add(source.source_id);
      return;
    }
    staticLoading.add(source.source_id);
    updateLoadMoreControl();
    try {
      const lines = await fetchStaticLines(source, DEFAULT_LOG_TAIL);
      if (destroyed || generation !== scopeGeneration) return;
      sourceFailures.delete(source.source_id);
      // Fewer lines than requested means the retained file is shorter than
      // the tail window, so this fetch already holds everything there is.
      if (lines.length < DEFAULT_LOG_TAIL) fullyLoadedSourceIDs.add(source.source_id);
      else fullyLoadedSourceIDs.delete(source.source_id);
      for (const line of lines) {
        addEntry({ kind: 'line', source_id: source.source_id, replica: source.replica, line });
      }
      updateLoadMoreControl();
    } catch {
      if (destroyed || generation !== scopeGeneration) return;
      sourceFailures.set(source.source_id, true);
    } finally {
      if (!destroyed && generation === scopeGeneration) {
        staticLoading.delete(source.source_id);
        updateLoadMoreControl();
        updateError();
        scheduleRender(true);
      }
    }
  }

  // Re-fetches the selected static source with a larger tail and replaces the
  // buffered lines wholesale (a bigger tail is always a superset of a smaller
  // one for the same retained file). Scoped to a single non-live source: a
  // provider source only paginates forward by cursor, and a live source's
  // history keeps growing on its own via the stream, so "older" does not
  // apply to either. Any transient status events buffered for this source
  // (e.g. its initial discovery event) are dropped by the replacement; that
  // is an acceptable trade for a source whose selection already implies it is
  // not actively changing state.
  async function loadMoreStaticHistory() {
    if (selected === 'all') return;
    const source = sources.find((item) => item.source_id === selected);
    if (!source || !source.has_log || isLiveLogSource(source) ||
      isExternalLogSource(source) || isInlineProviderLogSource(source)) return;
    if (staticTail >= MAX_LOG_TAIL || loadingHistory || staticLoading.has(source.source_id)) return;
    const generation = scopeGeneration;
    const requestedTail = nextLoadMoreTail(staticTail);
    loadingHistory = true;
    feedback.hidden = true;
    loadMoreButton.disabled = true;
    loadMoreButton.textContent = 'Loading…';
    try {
      const lines = await fetchStaticLines(source, requestedTail);
      if (destroyed || generation !== scopeGeneration) return;
      staticTail = requestedTail;
      sourceFailures.delete(source.source_id);
      updateError();
      if (lines.length < requestedTail) fullyLoadedSourceIDs.add(source.source_id);
      else fullyLoadedSourceIDs.delete(source.source_id);
      entries = lines.map((line) => ({ kind: 'line', source_id: source.source_id, replica: source.replica, line }));
      trimmed = 0;
      scheduleRender(true);
      showFeedback(`Loaded ${lines.length.toLocaleString()} lines for replica ${source.replica}.`);
    } catch {
      if (!destroyed && generation === scopeGeneration) {
        sourceFailures.set(source.source_id, 'history');
        updateError();
        showFeedback('Could not load older lines. Your loaded output is still available.');
      }
    } finally {
      if (!destroyed && generation === scopeGeneration) {
        loadingHistory = false;
        updateLoadMoreControl();
      }
    }
  }

  function updateLoadMoreControl() {
    const source = selected !== 'all' ? sources.find((item) => item.source_id === selected) : null;
    const eligible = Boolean(source) && source.has_log && !isLiveLogSource(source) &&
      !isExternalLogSource(source) && !isInlineProviderLogSource(source) &&
      !fullyLoadedSourceIDs.has(source.source_id) && staticTail < MAX_LOG_TAIL;
    loadMoreButton.hidden = !eligible;
    if (!eligible) return;
    loadMoreButton.disabled = loadingHistory || staticLoading.has(source.source_id);
    if (loadingHistory) return;
    loadMoreButton.textContent = `Load older lines (up to ${nextLoadMoreTail(staticTail).toLocaleString()})`;
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
    if (isInlineProviderLogSource(source)) return;
    if (isExternalLogSource(source)) return;
    if (isLiveLogSource(source)) openLiveSource(source, generation);
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

  function abortProviderReads() {
    for (const controller of providerControllers.values()) controller.abort();
    providerControllers.clear();
  }

  function resetScope() {
    scopeGeneration++;
    closeStreams();
    abortProviderReads();
    loadedSources.clear();
    sourceFailures.clear();
    staticLoading.clear();
    providerCursors.clear();
    providerLoading.clear();
    providerConnected.clear();
    providerDegraded.clear();
    providerCompleted.clear();
    providerIdlePolls.clear();
    providerFailurePolls.clear();
    providerNextPollAt.clear();
    entries = [];
    trimmed = 0;
    staticTail = DEFAULT_LOG_TAIL;
    loadingHistory = false;
    fullyLoadedSourceIDs.clear();
    pendingWhilePaused = 0;
    paused = false;
    pausedEntries = [];
    stickToBottom = true;
    pauseButton.textContent = 'Pause live';
    pauseButton.setAttribute('aria-pressed', 'false');
    feedback.hidden = true;
    for (const source of scopedSources()) loadSource(source, scopeGeneration);
    updateConnectionStatus();
    updateError();
    updateLoadMoreControl();
    scheduleRender(true);
    // Source identity and output must change atomically. Waiting for the next
    // animation frame leaves the previous run's lines visible under the newly
    // selected source/status, briefly attributing output to the wrong run.
    flushScheduledRender();
    scheduleNextProviderPoll();
  }

  function reconcileSources(nextSources, markChanges) {
    const previous = new Map(sources.map((source) => [source.source_id, source]));
    const priorSelection = selected;
    sources = normalizeLogSources(nextSources);
    const selectionExpired = renderSourceOptions();
    if (selectionExpired) {
      updateURL();
      if (markChanges) {
        resetScope();
        const message = priorSelection === 'all'
          ? 'No current replica runs; showing the latest retained run'
          : 'Selected log run is no longer retained; showing all current replicas';
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
          const terminalMessage = sourceStatusEvent(source, old);
          if (isInlineProviderLogSource(source)) {
            reconcileTerminalProviderSource(source, scopeGeneration, terminalMessage);
          } else {
            reconcileTerminalSource(source, scopeGeneration, terminalMessage);
          }
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
      // A live source can finish (or a terminal source's retained bytes can
      // arrive) without the selection itself changing, so eligibility for
      // Load older lines is re-checked here rather than only from resetScope.
      updateLoadMoreControl();
    }
    updateConnectionStatus();
  }

  async function refreshSources(markChanges = true) {
    if (discoveryInFlight) return discoveryInFlight;
    discoveryInFlight = discoverSources(markChanges);
    try { return await discoveryInFlight; }
    finally { discoveryInFlight = null; }
  }

  async function discoverSources(markChanges) {
    const wasUnavailable = discoveryFailed || discovering;
    try {
      const query = developmentSessionID ? `?development_session_id=${encodeURIComponent(developmentSessionID)}` : '';
      const resp = await api(`/api/apps/${encodeURIComponent(app.slug)}/logs/sources${query}`);
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      const payload = await resp.json();
      if (destroyed) return false;
      if (!Array.isArray(payload?.sources)) throw new Error('Invalid log sources');
      discoveryFailed = false;
      discovering = false;
      reconcileSources(payload.sources, markChanges);
      updateError();
      if (wasUnavailable) scheduleRender(true);
      if (markChanges) scheduleNextProviderPoll();
      return true;
    } catch {
      if (destroyed) return false;
      discovering = false;
      discoveryFailed = true;
      updateConnectionStatus();
      updateError();
      scheduleRender(true);
      return false;
    }
  }

  sourceSelect.addEventListener('change', () => {
    selected = sourceSelect.value;
    updateDownloadControl();
    updateURL();
    renderExternalLogs();
    resetScope();
    const source = sources.find((item) => item.source_id === selected);
    announce(selected === 'all' ? 'Showing all current replica runs' : `Showing ${source ? formatLogSourceLabel(source) : 'selected log run'}`);
  });
  searchInput.addEventListener('input', () => {
    feedback.hidden = true;
    scheduleRender(true);
  });
  clearSearch.addEventListener('click', () => {
    searchInput.value = '';
    scheduleRender(true);
    searchInput.focus();
  });
  retryButton.addEventListener('click', async () => {
    feedback.hidden = true;
    retryButton.disabled = true;
    retryButton.textContent = 'Retrying…';
    const generation = scopeGeneration;
    try {
      await refreshSources(true);
      if (destroyed || generation !== scopeGeneration) return;
      await Promise.all(scopedSources().filter(source => sourceFailures.has(source.source_id)).map(source =>
        sourceFailures.get(source.source_id) === 'history' ? loadMoreStaticHistory() : loadStaticSource(source, generation)));
    } finally {
      if (!destroyed) {
        retryButton.disabled = false;
        retryButton.textContent = 'Retry';
        if (errorPanel.hidden) searchInput.focus();
      }
    }
  });
  loadMoreButton.addEventListener('click', () => {
    loadMoreStaticHistory();
  });
  pauseButton.addEventListener('click', () => {
    // Finish the already-scheduled frame before entering pause. Otherwise a
    // line arriving between the click and that frame would leak into the DOM
    // even though it is correctly counted as buffered.
    if (!paused) flushScheduledRender();
    paused = !paused;
    pauseButton.setAttribute('aria-pressed', String(paused));
    if (paused) {
      pausedEntries = entries.slice();
      pauseButton.textContent = 'Resume';
      announce('Live display paused. Incoming logs will continue buffering.');
    } else {
      const count = pendingWhilePaused;
      const retained = Math.min(count, entries.length);
      pendingWhilePaused = 0;
      pausedEntries = [];
      pauseButton.textContent = 'Pause live';
      stickToBottom = true;
      scheduleRender(true);
      announce(`Live display resumed${retained ? ` with ${retained} buffered lines` : ''}.${count > retained ? ` ${count - retained} older lines were omitted.` : ''}`);
    }
    updateConnectionStatus();
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
  function handleVisibilityChange() {
    if (doc.hidden) {
      clearProviderTimer();
      abortProviderReads();
      return;
    }
    const now = Date.now();
    for (const source of scopedSources()) {
      if (isInlineProviderLogSource(source) && !providerCompleted.has(source.source_id)) {
        providerNextPollAt.set(source.source_id, now);
      }
    }
    scheduleNextProviderPoll();
    refreshSources(true);
  }
  doc.addEventListener('visibilitychange', handleVisibilityChange);
  copyButton.addEventListener('click', async () => {
    flushScheduledRender();
    const text = serializeLogEntries(renderedEntries);
    try {
      await win.navigator.clipboard.writeText(text);
      showFeedback('Visible logs copied.');
    } catch {
      showFeedback('Could not copy logs. Select the output to copy it manually.');
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
        downloading = true;
        downloadButton.disabled = true;
        downloadButton.textContent = 'Downloading…';
        const resp = await api(url);
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const blob = await resp.blob();
        if (destroyed) return;
        const identity = source.run_id || (source.legacy ? 'legacy' : source.source_id);
        saveBlob(blob, `${app.slug}-replica-${source.replica}-${identity}.log`);
        showFeedback('Complete retained run downloaded.');
        return;
      }
      flushScheduledRender();
      const text = serializeLogEntries(renderedEntries);
      const blob = new win.Blob([text + (text ? '\n' : '')], { type: 'text/plain;charset=utf-8' });
      saveBlob(blob, `${app.slug}-logs-visible.log`);
      showFeedback('Visible merged logs downloaded.');
    } catch {
      showFeedback('Could not download logs. Try again.');
    } finally {
      downloading = false;
      if (!destroyed) updateDownloadControl();
    }
  });

  async function start() {
    const discovered = await refreshSources(false);
    if (destroyed) return;
    if (!discovered) renderSourceOptions();
    resetScope();
    if (!discovered && sources.length === 0) scheduleRender(true);
    refreshTimer = win.setInterval(() => {
      if (!doc.hidden) refreshSources(true);
    }, refreshEveryMs);
  }
  start();

  return () => {
    destroyed = true;
    layoutObserver?.disconnect();
    win.removeEventListener('resize', scheduleLayout);
    if (layoutFrame != null) win.cancelAnimationFrame(layoutFrame);
    if (cancelScheduledRender) cancelScheduledRender();
    doc.removeEventListener('visibilitychange', handleVisibilityChange);
    abortProviderReads();
    closeStreams();
    if (refreshTimer != null) win.clearInterval(refreshTimer);
    clearProviderTimer();
  };
}
