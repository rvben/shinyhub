import { startVisiblePoll } from './visible-poll.js';

// A bounded diagnostic request sample. Presentation, inspection, and polling
// share one panel-scoped lifecycle; the server remains the source of retention
// and sampling policy.

// formatTraceWhen renders a span's start time for the "Started" column. The ring
// buffer can span days, so a same-day span shows time only while any other day
// includes the date - otherwise yesterday's 09:30 error is indistinguishable
// from today's. now is injected for deterministic tests.
export function formatTraceWhen(startedAt, now = new Date()) {
  if (!startedAt) return '—';
  const d = new Date(startedAt);
  if (Number.isNaN(d.getTime())) return '—';
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  return sameDay ? d.toLocaleTimeString() : d.toLocaleString();
}

// formatPollStatus describes how fresh the last successful poll is, for the
// traces-status element. Returns '' for an absent/invalid timestamp so the
// element stays blank before the first load. now is injected for tests.
export function formatPollStatus(lastUpdated, now = new Date()) {
  if (!lastUpdated) return '';
  const then = lastUpdated instanceof Date ? lastUpdated : new Date(lastUpdated);
  if (Number.isNaN(then.getTime())) return '';
  const secs = Math.max(0, Math.round((now.getTime() - then.getTime()) / 1000));
  if (secs < 5) return 'updated just now';
  if (secs < 60) return `updated ${secs}s ago`;
  return `updated ${Math.floor(secs / 60)}m ago`;
}

export const UNSAMPLED_TITLE =
  'This request was not sampled, so no trace was exported to your backend. ' +
  'A deep link would resolve to "trace not found", so the id is shown without one.';

export function traceFailed(span) {
  return Number(span.status) >= 500 || Boolean(span.error);
}

export function traceKey(span) {
  return JSON.stringify([span.trace_id, span.span_id, span.started_at, span.replica, span.method, span.path]);
}

export function selectTraceEntries(spans, { query = '', failuresOnly = false, sort = 'newest' } = {}) {
  const needle = query.trim().toLocaleLowerCase();
  const selected = spans.filter(span => (!failuresOnly || traceFailed(span)) && (!needle ||
    [span.method, span.path, span.trace_id, span.span_id, span.error].join(' ').toLocaleLowerCase().includes(needle)));
  // The API is newest-captured first, not request-start order. Preserve that
  // order unless the operator explicitly asks to compare durations.
  if (sort === 'slowest') selected.sort((a, b) => (Number(b.duration_ms) || 0) - (Number(a.duration_ms) || 0));
  return selected;
}

export function formatTraceDuration(value) {
  if (value == null || value === '' || !Number.isFinite(Number(value)) || Number(value) < 0) return '—';
  const ms = Number(value);
  return ms >= 1000 ? `${(ms / 1000).toLocaleString(undefined, { maximumFractionDigits: 2 })} s` : `${ms.toLocaleString()} ms`;
}

export function safeTraceURL(span, template, baseURL) {
  if (span.sampled !== true || !/^[a-f0-9]{32}$/i.test(span.trace_id || '') ||
      typeof template !== 'string' || !template.includes('{trace_id}')) return '';
  try {
    const url = new URL(template.replaceAll('{trace_id}', span.trace_id), baseURL);
    return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password ? url.href : '';
  } catch { return ''; }
}

export function makeTraceRow(doc, span, _linkTpl, now = new Date(), { onInspect, selected = false } = {}) {
  const tr = doc.createElement('tr');
  tr.className = `trace-row${traceFailed(span) ? ' is-failed' : ''}${selected ? ' is-selected' : ''}`;
  const request = doc.createElement('td');
  const inspect = doc.createElement('button');
  inspect.type = 'button';
  inspect.className = 'trace-inspect';
  inspect.dataset.traceKey = traceKey(span);
  inspect.setAttribute('aria-expanded', String(selected));
  if (selected) inspect.setAttribute('aria-controls', 'traces-detail');
  inspect.setAttribute('aria-label', `Inspect ${span.method || 'request'} ${span.path || '/'}`);
  const method = doc.createElement('span');
  method.className = 'trace-method';
  method.textContent = span.method || 'Request';
  const path = doc.createElement('code');
  path.textContent = span.path || '/';
  inspect.append(method, path);
  if (onInspect) inspect.addEventListener('click', () => onInspect(span));
  request.appendChild(inspect);

  const result = doc.createElement('td');
  result.className = 'trace-result';
  const status = doc.createElement('strong');
  status.textContent = Number(span.status) > 0 ? String(span.status) : 'No response';
  result.appendChild(status);
  if (traceFailed(span)) {
    const label = doc.createElement('span');
    label.textContent = Number(span.status) >= 500 ? 'Server error' : 'Request error';
    result.appendChild(label);
  }
  const duration = doc.createElement('td');
  duration.className = 'trace-duration';
  duration.textContent = formatTraceDuration(span.duration_ms);
  const when = doc.createElement('td');
  when.className = 'trace-when';
  when.textContent = formatTraceWhen(span.started_at, now);
  const date = new Date(span.started_at || NaN);
  if (!Number.isNaN(date.getTime())) when.title = date.toLocaleString();
  tr.append(request, result, duration, when);
  return tr;
}

export function makeTraceDetails(doc, span, linkTpl, { onClose, onFeedback = () => {} } = {}) {
  const section = doc.createElement('section');
  section.id = 'traces-detail';
  section.className = 'trace-detail';
  section.setAttribute('aria-label', 'Request details');
  const header = doc.createElement('header');
  const title = doc.createElement('h3');
  title.textContent = 'Request details';
  title.tabIndex = -1;
  const close = doc.createElement('button');
  close.type = 'button';
  close.className = 'btn-row';
  close.textContent = 'Close details';
  if (onClose) close.addEventListener('click', onClose);
  header.append(title, close);
  const request = doc.createElement('p');
  request.className = 'trace-detail-request';
  request.textContent = `${span.method || 'Request'} ${span.path || '/'}`;
  section.append(header, request);
  if (span.error) {
    const error = doc.createElement('p');
    error.className = 'trace-detail-error';
    error.textContent = String(span.error);
    section.appendChild(error);
  }
  const list = doc.createElement('dl');
  function field(label, value) {
    const item = doc.createElement('div');
    const term = doc.createElement('dt');
    term.textContent = label;
    const detail = doc.createElement('dd');
    detail.textContent = value;
    item.append(term, detail);
    list.appendChild(item);
  }
  const date = new Date(span.started_at || NaN);
  field('Started', Number.isNaN(date.getTime()) ? 'Unavailable' : `${date.toLocaleString()} · ${date.toISOString()}`);
  field('Result', `${Number(span.status) > 0 ? `HTTP ${span.status}` : 'No response'}${traceFailed(span) ? ' · Failed' : ''}`);
  field('Duration', formatTraceDuration(span.duration_ms) === '—' ? 'Unavailable' : `${Number(span.duration_ms)} ms`);
  field('Replica', Number.isInteger(span.replica) && span.replica >= 0 ? `#${span.replica}` : 'Unavailable');
  if (span.deployment_id) field('Deployment ID', String(span.deployment_id));
  for (const [label, value] of [['Trace ID', span.trace_id], ['Span ID', span.span_id], ['Parent span ID', span.parent_id]]) {
    if (!value && label === 'Parent span ID') continue;
    field(label, value || 'Unavailable');
  }
  section.appendChild(list);
  const sampling = doc.createElement('p');
  sampling.className = 'trace-sampling';
  if (span.sampled === false) sampling.title = UNSAMPLED_TITLE;
  sampling.textContent = span.sampled === false
    ? 'Not exported. This request was not sampled; its diagnostic record is available here.'
    : span.sampled === true ? 'Sampled for export. Trace availability depends on your tracing backend.'
    : 'Sampling information is unavailable.';
  section.appendChild(sampling);
  const actions = doc.createElement('div');
  actions.className = 'trace-detail-actions';
  const copy = doc.createElement('button');
  copy.type = 'button';
  copy.className = 'btn-row';
  copy.textContent = 'Copy request details';
  copy.addEventListener('click', async () => {
    try {
      await doc.defaultView.navigator.clipboard.writeText(JSON.stringify(span, null, 2));
      onFeedback('Request details copied.');
    } catch { onFeedback('Could not copy details. Select the values above to copy them manually.'); }
  });
  actions.appendChild(copy);
  const url = safeTraceURL(span, linkTpl, doc.baseURI);
  if (url) {
    const open = doc.createElement('a');
    open.className = 'btn-row';
    open.href = url;
    open.target = '_blank';
    open.rel = 'noopener noreferrer';
    open.textContent = 'Open trace in backend';
    actions.appendChild(open);
  }
  section.appendChild(actions);
  return section;
}

export function createTracesViewer({ panel, app, api, onUnauthorized = () => {}, refreshEveryMs = 5000 }) {
  const doc = panel.ownerDocument;
  const win = doc.defaultView;
  const candidate = new URLSearchParams(win.location.search).get('development_session_id') || '';
  const session = /^[a-f0-9]{32}$/.test(candidate) ? candidate : '';
  panel.innerHTML = `
    <div class="traces-workspace">
      <header class="app-tab-heading"><div><h2>Traces</h2><p>A recent diagnostic sample of slow or failed requests.</p></div></header>
      <div id="traces-session" class="observability-scope" role="status" hidden>
        <span>Showing traces from one remote development session.</span>
        <a data-nav>Show all traces</a>
      </div>
      <div class="traces-filters" aria-label="Trace controls">
        <label class="trace-search-field"><span>Search requests</span><input id="traces-search" type="search" placeholder="Search path, ID or error" aria-describedby="traces-summary" autocomplete="off"></label>
        <label><span>Requests</span><select id="traces-filter"><option value="all">All captured</option><option value="failed">Failures</option></select></label>
        <label><span>Order</span><select id="traces-sort"><option value="newest">Latest captured</option><option value="slowest">Longest first</option></select></label>
      </div>
      <div class="traces-toolbar">
        <div class="traces-context"><p id="traces-summary"></p><p id="traces-status">Loading traces…</p></div>
        <div class="traces-actions">
          <button id="traces-pause" type="button" class="btn-row" aria-pressed="false">Pause updates</button>
          <button id="traces-refresh" type="button" class="btn-row">Refresh</button>
        </div>
      </div>
      <div id="traces-error" class="traces-error" hidden><p role="alert"></p><button id="traces-retry" type="button" class="btn-row">Retry</button></div>
      <p id="traces-feedback" class="traces-feedback" role="status" hidden></p>
      <div id="traces-empty" class="traces-empty" hidden><h3></h3><p></p><button id="traces-clear" type="button" class="btn-row" hidden>Clear filters</button></div>
      <table id="traces-table" class="traces-table" hidden>
        <caption class="sr-only">Captured requests. Select a request to inspect its details.</caption>
        <thead><tr><th scope="col">Request</th><th scope="col">Result</th><th scope="col">Duration</th><th scope="col" class="trace-when">Started</th></tr></thead>
        <tbody id="traces-tbody"></tbody>
      </table>
      <div id="traces-detail-slot"></div>
    </div>`;
  const query = selector => panel.querySelector(selector);
  const search = query('#traces-search');
  const filter = query('#traces-filter');
  const sort = query('#traces-sort');
  const summary = query('#traces-summary');
  const status = query('#traces-status');
  const pause = query('#traces-pause');
  const refresh = query('#traces-refresh');
  const retry = query('#traces-retry');
  const error = query('#traces-error');
  const empty = query('#traces-empty');
  const clear = query('#traces-clear');
  const table = query('#traces-table');
  const tbody = query('#traces-tbody');
  const detailSlot = query('#traces-detail-slot');
  const feedback = query('#traces-feedback');
  if (session) {
    query('#traces-session').hidden = false;
    query('#traces-session a').href = `/apps/${encodeURIComponent(app.slug)}/traces`;
  }

  let destroyed = false;
  let enabled = null;
  let records = [];
  let linkTemplate = '';
  let lastLoaded = null;
  let failure = '';
  let paused = false;
  let inFlight = false;
  let generation = 0;
  let controller = null;
  let inspected = null;
  let detailSection = null;
  let renderedSignature = '';

  function showFeedback(message) {
    if (destroyed) return;
    feedback.textContent = message;
    feedback.hidden = false;
  }
  function paintStatus() {
    if (destroyed) return;
    const fresh = formatPollStatus(lastLoaded);
    status.textContent = failure ? `Updates interrupted${fresh ? ` · last ${fresh}` : ''}`
      : inFlight ? (lastLoaded ? `Refreshing · last ${fresh}` : 'Loading traces…')
      : enabled === false ? `Tracing disabled${fresh ? ` · ${fresh}` : ''}`
      : paused ? `Paused${fresh ? ` · ${fresh}` : ''}`
      : lastLoaded ? `Auto-refresh every 5s · ${fresh}` : 'Loading traces…';
    pause.textContent = paused ? 'Resume updates' : 'Pause updates';
    pause.setAttribute('aria-pressed', String(paused));
    pause.disabled = !lastLoaded || enabled === false;
    refresh.disabled = inFlight;
    retry.disabled = inFlight;
    search.disabled = filter.disabled = sort.disabled = enabled === false;
    error.hidden = !failure;
    error.querySelector('p').textContent = failure;
  }
  function pauseUpdates() {
    paused = true;
    generation++;
    controller?.abort();
    controller = null;
    inFlight = false;
    paintStatus();
  }
  function inspect(span) {
    if (inspected && traceKey(inspected) === traceKey(span)) { closeDetails(); return; }
    inspected = span;
    pauseUpdates();
    showFeedback('Updates paused while you inspect this request.');
    render();
    renderDetails();
    detailSection.querySelector('h3').focus({ preventScroll: true });
    const bounds = detailSection.getBoundingClientRect();
    if (bounds.top < 0 || bounds.bottom > win.innerHeight) detailSection.scrollIntoView?.({ block: 'nearest' });
  }
  function closeDetails() {
    const key = inspected && traceKey(inspected);
    inspected = null;
    detailSection?.remove();
    detailSection = null;
    detailSlot.replaceChildren();
    feedback.hidden = true;
    render();
    [...tbody.querySelectorAll('button')].find(button => button.dataset.traceKey === key)?.focus({ preventScroll: true });
  }
  function placeDetails() {
    if (!detailSection || !inspected) return;
    tbody.querySelector('.trace-detail-row')?.remove();
    const button = [...tbody.querySelectorAll('.trace-inspect')].find(item => item.dataset.traceKey === traceKey(inspected));
    if (!button) { detailSlot.replaceChildren(detailSection); return; }
    const row = doc.createElement('tr');
    row.className = 'trace-detail-row';
    const cell = doc.createElement('td');
    cell.colSpan = 4;
    cell.appendChild(detailSection);
    row.appendChild(cell);
    button.closest('tr').after(row);
  }
  function renderDetails() {
    if (!inspected) { detailSlot.replaceChildren(); return; }
    const key = traceKey(inspected);
    const section = makeTraceDetails(doc, inspected, linkTemplate, {
      onFeedback: showFeedback,
      onClose: closeDetails,
    });
    if (!records.some(span => traceKey(span) === key)) {
      const note = doc.createElement('p');
      note.className = 'trace-sampling';
      note.textContent = 'This request is no longer in the recent sample. Its inspected details are preserved here.';
      section.appendChild(note);
    }
    detailSection = section;
    placeDetails();
  }
  function render() {
    if (destroyed) return;
    const visible = selectTraceEntries(records, { query: search.value, failuresOnly: filter.value === 'failed', sort: sort.value });
    const filtered = Boolean(search.value.trim()) || filter.value !== 'all';
    summary.textContent = enabled === null ? '' : enabled === false ? '' : filtered
      ? `${visible.length.toLocaleString()} of ${records.length.toLocaleString()} captured requests`
      : `${records.length.toLocaleString()} captured request${records.length === 1 ? '' : 's'}`;
    clear.hidden = !filtered;
    table.hidden = enabled !== true || !visible.length;
    empty.hidden = enabled === null && !failure || enabled === true && visible.length > 0;
    if (!empty.hidden) {
      const [title, message] = enabled === false
        ? ['Tracing is disabled', 'Ask a server administrator to enable request tracing for this server.']
        : !lastLoaded ? ['Traces could not be loaded', 'Retry above to load the recent diagnostic sample.']
        : records.length && filtered ? ['No matching requests', 'Try another search or clear the filters to see all captured requests.']
        : ['No requests captured yet', 'ShinyHub keeps a recent, in-memory sample of requests that reach the slow-request threshold, return a server error (5xx), or encounter a proxy error.'];
      empty.querySelector('h3').textContent = title;
      empty.querySelector('p').textContent = message;
    }
    const signature = JSON.stringify([visible, inspected && traceKey(inspected), sort.value, lastLoaded?.toLocaleDateString()]);
    if (signature !== renderedSignature) {
      const focusedKey = doc.activeElement?.dataset.traceKey;
      const detailFocus = detailSection?.contains(doc.activeElement) ? doc.activeElement : null;
      tbody.replaceChildren(...visible.map(span => makeTraceRow(doc, span, linkTemplate, lastLoaded || new Date(), {
        onInspect: inspect, selected: !!inspected && traceKey(span) === traceKey(inspected),
      })));
      renderedSignature = signature;
      placeDetails();
      detailFocus?.focus({ preventScroll: true });
      if (focusedKey) [...tbody.querySelectorAll('button')].find(button => button.dataset.traceKey === focusedKey)?.focus({ preventScroll: true });
    }
    paintStatus();
  }
  async function load({ manual = false } = {}) {
    if (destroyed || inFlight || paused && !manual) return;
    inFlight = true;
    const ticket = ++generation;
    controller = new win.AbortController();
    paintStatus();
    try {
      const suffix = session ? `?development_session_id=${encodeURIComponent(session)}` : '';
      const response = await api(`/api/apps/${encodeURIComponent(app.slug)}/traces${suffix}`, { signal: controller.signal, cache: 'no-store' });
      if (destroyed || ticket !== generation) return;
      if (response.status === 401) { onUnauthorized(); return; }
      if (!response.ok) throw new Error(response.status === 403 ? 'permission' : 'response');
      const body = await response.json();
      if (destroyed || ticket !== generation) return;
      if (!body || typeof body.enabled !== 'boolean' || !Array.isArray(body.spans)) throw new Error('invalid');
      enabled = body.enabled;
      records = body.spans.filter(span => span && typeof span === 'object' && !Array.isArray(span));
      linkTemplate = typeof body.trace_link_template === 'string' ? body.trace_link_template : '';
      lastLoaded = new Date();
      failure = '';
      render();
      // Keep inspection stable on automatic ticks. Only the explicit refresh
      // can change its coverage note or configured backend link.
      if (inspected && manual) renderDetails();
    } catch (err) {
      if (destroyed || ticket !== generation) return;
      failure = err?.message === 'permission' ? 'You no longer have access to request traces.'
        : lastLoaded ? 'Could not refresh traces. The last successful sample is still shown.'
        : 'Could not load traces. Check your connection and retry.';
      render();
    } finally {
      if (!destroyed && ticket === generation) {
        inFlight = false;
        controller = null;
        paintStatus();
      }
    }
  }
  for (const control of [search, filter, sort]) control.addEventListener(control === search ? 'input' : 'change', () => {
    feedback.hidden = true;
    render();
  });
  clear.addEventListener('click', () => {
    search.value = '';
    filter.value = 'all';
    feedback.hidden = true;
    render();
    search.focus();
  });
  pause.addEventListener('click', () => {
    feedback.hidden = true;
    if (paused) { paused = false; paintStatus(); load(); }
    else pauseUpdates();
  });
  refresh.addEventListener('click', () => { feedback.hidden = true; load({ manual: true }); });
  retry.addEventListener('click', () => load({ manual: true }));
  const stopPoll = startVisiblePoll(doc, refreshEveryMs, load);
  // The relative-time label only matters while the tab is visible; the poll
  // disarms in the background and repaints once on return.
  const stopStatusTick = startVisiblePoll(doc, 1000, paintStatus, win);
  load();
  return () => {
    destroyed = true;
    generation++;
    controller?.abort();
    stopPoll();
    stopStatusTick();
  };
}
