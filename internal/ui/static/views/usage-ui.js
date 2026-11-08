// Durable app-usage presentation. A session is one successful proxied
// WebSocket connection, so static assets and failed launches never inflate the
// numbers. This module owns presentation only; app-detail.js owns API calls.

const SVG_NS = 'http://www.w3.org/2000/svg';

// Exactly one Usage request may own the surface. Starting a newer request
// aborts its predecessor so rapid range changes neither waste server work nor
// let a slower, stale response overwrite the latest selection.
export function createUsageRequestGate() {
  let current = null;
  return {
    begin() {
      current?.abort();
      current = new AbortController();
      return current;
    },
    isCurrent(controller) {
      return current === controller && !controller.signal.aborted;
    },
    invalidate() {
      current?.abort();
      current = null;
    },
  };
}

function number(value) {
  return new Intl.NumberFormat().format(Number(value) || 0);
}

export function formatDuration(seconds) {
  const total = Math.max(0, Math.round(Number(seconds) || 0));
  if (total < 60) return `${total}s`;
  const minutes = Math.floor(total / 60);
  if (minutes < 60) return `${minutes}m ${total % 60}s`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h`;
}

export function relativeUsageTime(raw, now = new Date()) {
  if (!raw) return 'Never';
  const value = new Date(raw);
  if (Number.isNaN(value.getTime())) return 'Unknown';
  const seconds = Math.max(0, Math.round((now.getTime() - value.getTime()) / 1000));
  if (seconds < 60) return 'Just now';
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  if (seconds < 7 * 86400) return `${Math.floor(seconds / 86400)}d ago`;
  return value.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
}

function shortDate(raw, includeYear = false) {
  const date = new Date(`${raw}T00:00:00Z`);
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', ...(includeYear ? { year: 'numeric' } : {}), timeZone: 'UTC' });
}

function filledDays(daily, count, generatedAt) {
  const values = new Map((Array.isArray(daily) ? daily : []).map(day => [day.date, day]));
  const end = generatedAt ? new Date(generatedAt) : new Date();
  const cursor = new Date(Date.UTC(end.getUTCFullYear(), end.getUTCMonth(), end.getUTCDate()));
  const result = [];
  for (let offset = Math.max(1, Number(count) || 30) - 1; offset >= 0; offset -= 1) {
    const date = new Date(cursor);
    date.setUTCDate(cursor.getUTCDate() - offset);
    const key = date.toISOString().slice(0, 10);
    result.push(values.get(key) || {
      date: key, sessions: 0, unique_viewers: null,
      peak_concurrent_sessions: 0,
      authenticated_sessions: 0, anonymous_sessions: 0, service_sessions: 0,
    });
  }
  return result;
}

function niceCeiling(value) {
  if (value <= 4) return Math.max(1, value);
  const magnitude = 10 ** Math.floor(Math.log10(value));
  const normalized = value / magnitude;
  const step = [1, 2, 2.5, 5, 10].find(candidate => normalized <= candidate) || 10;
  return step * magnitude;
}

function makeActivityChart(doc, days) {
  const width = 1000;
  const height = 180;
  const max = Math.max(0, ...days.map(day => Number(day.sessions) || 0));
  const ceiling = niceCeiling(max);
  const ticks = ceiling <= 1 ? [ceiling, 0] : [ceiling, Math.floor(ceiling / 2), 0];
  const plot = doc.createElement('div');
  plot.className = 'usage-plot';
  const axis = doc.createElement('div');
  axis.className = 'usage-chart-axis';
  axis.setAttribute('aria-hidden', 'true');
  const svg = doc.createElementNS(SVG_NS, 'svg');
  svg.classList.add('usage-chart');
  svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', max > 0
    ? `Daily app sessions. Most opens: ${number(max)} on ${shortDate(days.find(day => Number(day.sessions) === max).date)}. Exact values are available in View daily data.`
    : 'Daily app sessions. No sessions recorded in this window.');
  const grid = doc.createElementNS(SVG_NS, 'g');
  grid.classList.add('usage-chart-grid');
  grid.setAttribute('aria-hidden', 'true');
  for (const value of ticks) {
    const y = height * (1 - value / ceiling);
    const line = doc.createElementNS(SVG_NS, 'line');
    for (const [key, val] of Object.entries({ x1: 0, x2: width, y1: y, y2: y })) line.setAttribute(key, val);
    grid.append(line);
    const label = doc.createElement('span');
    label.style.top = `${100 * (1 - value / ceiling)}%`;
    label.textContent = number(value);
    axis.append(label);
  }
  svg.append(grid);
  const bars = doc.createElementNS(SVG_NS, 'g');
  bars.classList.add('usage-chart-bars');
  bars.setAttribute('aria-hidden', 'true');
  const slot = width / Math.max(days.length, 1);
  const barWidth = Math.min(22, slot * 0.7);
  days.forEach((day, index) => {
    const value = Number(day.sessions) || 0;
    const barHeight = value === 0 ? 1 : Math.max(2, height * value / ceiling);
    const rect = doc.createElementNS(SVG_NS, 'rect');
    rect.classList.add('usage-chart-bar');
    if (value === 0) rect.classList.add('is-zero');
    for (const [key, val] of Object.entries({ x: slot * index + (slot - barWidth) / 2, y: height - barHeight, width: barWidth, height: barHeight, rx: Math.min(2, barWidth / 2) })) rect.setAttribute(key, val);
    const title = doc.createElementNS(SVG_NS, 'title');
    title.textContent = `${shortDate(day.date)}: ${number(value)} sessions, peak ${number(day.peak_concurrent_sessions)} concurrent, ${day.unique_viewers == null ? 'unique viewers unavailable' : `${number(day.unique_viewers)} unique viewers`}`;
    rect.append(title);
    bars.append(rect);
  });
  svg.append(bars);
  const dates = doc.createElement('div');
  dates.className = 'usage-chart-dates';
  dates.setAttribute('aria-hidden', 'true');
  for (const index of new Set([0, Math.floor((days.length - 1) / 2), days.length - 1])) {
    const label = doc.createElement('span');
    label.textContent = shortDate(days[index].date);
    dates.append(label);
  }
  plot.append(axis, svg, dates);
  return plot;
}

function makeDailyDataTable(doc, days) {
  const table = doc.createElement('table');
  table.className = 'usage-daily-data';
  const caption = doc.createElement('caption');
  caption.textContent = 'Daily app usage data';
  const thead = doc.createElement('thead');
  thead.innerHTML = '<tr><th scope="col">Date (UTC)</th><th scope="col">Sessions</th><th scope="col">Peak concurrent</th><th scope="col">Unique viewers</th></tr>';
  const tbody = doc.createElement('tbody');
  for (const day of days) {
    const row = doc.createElement('tr');
    for (const value of [shortDate(day.date, true), number(day.sessions), number(day.peak_concurrent_sessions), day.unique_viewers == null ? 'Unavailable' : number(day.unique_viewers)]) {
      const cell = doc.createElement('td');
      cell.textContent = value;
      row.append(cell);
    }
    tbody.append(row);
  }
  table.append(caption, thead, tbody);
  return table;
}

function metric(doc, label, value, help, title = '') {
  const wrap = doc.createElement('div');
  const dt = doc.createElement('dt');
  dt.textContent = label;
  const dd = doc.createElement('dd');
  dd.textContent = value;
  if (title) dd.title = title;
  const note = doc.createElement('dd');
  note.className = 'usage-metric-note';
  note.textContent = help;
  wrap.append(dt, dd, note);
  return wrap;
}

function emptyMessage(doc, heading, copy) {
  const wrap = doc.createElement('div');
  wrap.className = 'usage-empty';
  const h = doc.createElement('h3');
  h.textContent = heading;
  const p = doc.createElement('p');
  p.textContent = copy;
  wrap.append(h, p);
  return wrap;
}

function personName(item) {
  if (item.principal_kind === 'service_account') return 'Service account';
  if (item.principal_kind === 'anonymous') return 'Anonymous';
  return item.display_name || item.username || 'Person';
}

function buildViewerTable(doc, viewers, generatedAt) {
  const table = doc.createElement('table');
  table.className = 'usage-table';
  table.innerHTML = '<caption class="sr-only">Top retained named viewers, up to 50</caption><thead><tr><th scope="col">Viewer</th><th scope="col">Sessions</th><th scope="col">Connected</th><th scope="col">Last opened</th></tr></thead>';
  const tbody = doc.createElement('tbody');
  for (const viewer of viewers) {
    const tr = doc.createElement('tr');
    const who = doc.createElement('td');
    const strong = doc.createElement('strong');
    strong.textContent = personName(viewer);
    who.append(strong);
    if (viewer.display_name && viewer.username) {
      const handle = doc.createElement('span');
      handle.textContent = viewer.username;
      who.append(handle);
    }
    const sessions = doc.createElement('td');
    sessions.textContent = number(viewer.sessions);
    const duration = doc.createElement('td');
    duration.textContent = formatDuration(viewer.total_duration_seconds);
    const last = doc.createElement('td');
    last.textContent = relativeUsageTime(viewer.last_opened_at, new Date(generatedAt));
    last.title = new Date(viewer.last_opened_at).toLocaleString();
    tr.append(who, sessions, duration, last);
    tbody.append(tr);
  }
  table.append(tbody);
  return table;
}

function buildRecentTable(doc, sessions, generatedAt) {
  const table = doc.createElement('table');
  table.className = 'usage-table usage-recent-table';
  table.innerHTML = '<caption class="sr-only">Newest retained sessions, up to 50</caption><thead><tr><th scope="col">Viewer</th><th scope="col">Opened</th><th scope="col">Duration</th><th scope="col">Deployment</th></tr></thead>';
  const tbody = doc.createElement('tbody');
  for (const session of sessions) {
    const tr = doc.createElement('tr');
    const who = doc.createElement('td');
    who.textContent = personName(session);
    const opened = doc.createElement('td');
    opened.textContent = relativeUsageTime(session.started_at, new Date(generatedAt));
    opened.title = new Date(session.started_at).toLocaleString();
    const duration = doc.createElement('td');
    if (session.active) {
      const live = doc.createElement('span');
      live.className = 'usage-live';
      live.textContent = `Active · ${formatDuration(session.duration_seconds)}`;
      duration.append(live);
    } else {
      duration.textContent = formatDuration(session.duration_seconds);
    }
    const deployment = doc.createElement('td');
    deployment.textContent = session.deployment_id ? `#${session.deployment_id}` : '—';
    tr.append(who, opened, duration, deployment);
    tbody.append(tr);
  }
  table.append(tbody);
  return table;
}

export function renderUsageLoading(doc, panel) {
  panel.setAttribute('aria-busy', 'true');
  panel.innerHTML = `
    <div class="usage-loading" role="status">
      <span class="usage-loading-line"></span>
      <span class="usage-loading-block"></span>
      <span class="sr-only">Loading usage analytics…</span>
    </div>`;
}

export function renderUsageError(doc, panel, message, onRetry) {
  panel.removeAttribute('aria-busy');
  panel.replaceChildren(emptyMessage(doc, 'Usage could not be loaded', message));
  panel.firstElementChild.setAttribute('role', 'alert');
  const button = doc.createElement('button');
  button.type = 'button';
  button.className = 'btn-row usage-retry';
  button.textContent = 'Try again';
  button.addEventListener('click', onRetry);
  panel.firstElementChild.append(button);
}

export function renderUsageDashboard(doc, panel, data, actions = {}) {
  panel.removeAttribute('aria-busy');
  panel.replaceChildren();
  const root = doc.createElement('div');
  root.className = 'usage-surface';

  const header = doc.createElement('header');
  header.className = 'usage-heading';
  const headingCopy = doc.createElement('div');
  const h2 = doc.createElement('h2');
  h2.textContent = 'Usage';
  const definition = doc.createElement('p');
  definition.textContent = 'Recorded live connections to this app.';
  headingCopy.append(h2, definition);
  const mode = data.identity_mode || 'unattributed';
  const controls = doc.createElement('div');
  controls.className = 'usage-controls';
  const rangeLabel = doc.createElement('label');
  rangeLabel.textContent = 'Date range';
  const select = doc.createElement('select');
  select.setAttribute('aria-label', 'Usage reporting window');
  const currentWindow = Number(data.window_days) || 30;
  const retention = Number(data.aggregate_retention_days) || Infinity;
  const ranges = new Map();
  for (const requested of [7, 30, 90, 365]) {
    const effective = Math.min(requested, retention);
    if (ranges.has(effective)) continue;
    ranges.set(effective, requested);
    const option = doc.createElement('option');
    option.value = String(requested);
    option.textContent = effective < requested ? `${effective} days (retained)` : `Last ${effective} days`;
    option.selected = currentWindow === effective;
    select.append(option);
  }
  select.addEventListener('change', () => actions.onRangeChange?.(Number(select.value), select));
  rangeLabel.append(select);
  const refresh = doc.createElement('button');
  refresh.type = 'button';
  refresh.className = 'btn-row';
  refresh.textContent = 'Refresh';
  refresh.addEventListener('click', () => actions.onRefresh?.(refresh));
  controls.append(rangeLabel, refresh);
  header.append(headingCopy, controls);
  root.append(header);

  const summary = data.summary || {};
  if (!data.enabled) {
    const disabled = emptyMessage(doc, 'New collection is paused', Number(summary.sessions) > 0
      ? 'The retained totals below remain available until their retention period expires. No new sessions are being recorded.'
      : 'No new sessions are being recorded. Historical activity cannot be reconstructed later.');
    disabled.classList.add('usage-disabled');
    root.append(disabled);
  }

  const generatedAt = data.generated_at || new Date().toISOString();
  const capabilities = data.capabilities || {};
  const hasUniqueViewers = capabilities.unique_viewers === true && summary.unique_viewers != null;
  const stats = doc.createElement('dl');
  stats.className = 'usage-summary';
  stats.setAttribute('aria-label', `Usage summary for ${data.window_days || 30} days`);
  stats.append(
    metric(doc, 'Sessions', number(summary.sessions), 'Successful connections'),
    metric(doc, 'Unique viewers', hasUniqueViewers ? number(summary.unique_viewers) : 'Unavailable',
      hasUniqueViewers ? 'Authenticated people' : mode === 'unattributed' ? 'Viewer identity is not collected' : 'Cannot be derived from retained history'),
    metric(doc, 'Peak concurrent', number(summary.peak_concurrent_sessions), 'Simultaneous connections'),
    metric(doc, 'Average connected', formatDuration(summary.average_duration_seconds), 'Recorded time per session'),
  );
  root.append(stats);
  const metadata = doc.createElement('div');
  metadata.className = 'usage-metadata';
  const last = doc.createElement('span');
  last.textContent = `Last opened: ${relativeUsageTime(summary.last_opened_at, new Date(generatedAt))}`;
  if (summary.last_opened_at) last.title = new Date(summary.last_opened_at).toLocaleString();
  const active = doc.createElement('span');
  active.textContent = `${number(summary.active_sessions)} active from this window`;
  const generated = doc.createElement('time');
  generated.dateTime = generatedAt;
  generated.textContent = `As of ${new Date(generatedAt).toLocaleString()}`;
  metadata.append(last, active, generated);
  root.append(metadata);

  const grid = doc.createElement('div');
  grid.className = 'usage-grid';
  const activity = doc.createElement('section');
  activity.className = 'usage-activity';
  const activityHead = doc.createElement('div');
  activityHead.className = 'usage-section-heading';
  const activityTitle = doc.createElement('h3');
  activityTitle.textContent = 'Daily sessions';
  const utc = doc.createElement('span');
  utc.textContent = 'UTC';
  activityHead.append(activityTitle, utc);
  activity.append(activityHead);
  const days = filledDays(data.daily, data.window_days, generatedAt);
  if (Number(summary.sessions) > 0 || days.some(day => Number(day.peak_concurrent_sessions) > 0)) {
    const chartSurface = doc.createElement('div');
    chartSurface.className = 'usage-chart-surface';
    chartSurface.append(makeActivityChart(doc, days));
    const dates = doc.createElement('p');
    dates.className = 'usage-chart-window';
    dates.textContent = `${shortDate(days[0].date, true)} to ${shortDate(days.at(-1).date, true)} · Today is in progress.`;
    const values = doc.createElement('details');
    values.className = 'usage-daily-disclosure';
    const summary = doc.createElement('summary');
    summary.textContent = 'View daily data';
    values.append(summary, scrollTable(doc, makeDailyDataTable(doc, days), 'Daily app usage data'));
    chartSurface.append(dates, values);
    activity.append(chartSurface);
  } else {
    activity.append(emptyMessage(doc, 'No sessions in this window', data.enabled
      ? 'Choose another range, or open the app to record a new session.'
      : 'Choose another range to inspect retained activity. New collection is paused.'));
  }

  const audience = doc.createElement('section');
  audience.className = 'usage-audience';
  const audienceTitle = doc.createElement('h3');
  audienceTitle.textContent = 'Sessions by audience';
  audience.append(audienceTitle);
  const authSessions = Number(summary.authenticated_sessions) || 0;
  const anonymousSessions = Number(summary.anonymous_sessions) || 0;
  const serviceSessions = Number(summary.service_sessions) || 0;
  const total = authSessions + anonymousSessions + serviceSessions;
  const knownShare = total > 0 ? 100 * authSessions / total : 0;
  const anonymousShare = total > 0 ? 100 * anonymousSessions / total : 0;
  const bar = doc.createElement('div');
  bar.className = 'usage-audience-bar';
  bar.setAttribute('role', 'img');
  bar.setAttribute('aria-label', `${number(authSessions)} person sessions, ${number(anonymousSessions)} anonymous sessions, and ${number(serviceSessions)} service-account sessions`);
  const known = doc.createElement('span');
  known.className = 'usage-audience-known';
  known.style.width = `${knownShare}%`;
  const anonymous = doc.createElement('span');
  anonymous.className = 'usage-audience-anonymous';
  anonymous.style.width = `${anonymousShare}%`;
  const service = doc.createElement('span');
  service.className = 'usage-audience-service';
  service.style.width = `${total > 0 ? 100 - knownShare - anonymousShare : 0}%`;
  bar.append(known, anonymous, service);
  const legend = doc.createElement('dl');
  legend.className = 'usage-audience-legend';
  legend.innerHTML = `<div><dt><i class="is-known"></i>People</dt><dd>${number(authSessions)}</dd></div><div><dt><i class="is-anonymous"></i>Anonymous</dt><dd>${number(anonymousSessions)}</dd></div><div><dt><i class="is-service"></i>Service accounts</dt><dd>${number(serviceSessions)}</dd></div>`;
  const audienceHelp = doc.createElement('p');
  audienceHelp.textContent = 'Audience type is kept separately from identity. Anonymous visitors and service accounts are never counted as people.';
  if (total > 0) audience.append(bar, legend, audienceHelp);
  else audience.append(emptyMessage(doc, 'No audience activity', 'No sessions were recorded in this window.'));
  grid.append(activity, audience);
  root.append(grid);

  const privacy = doc.createElement('p');
  privacy.className = 'usage-privacy';
  const rawRetention = data.raw_retention_days == null ? NaN : Number(data.raw_retention_days);
  const aggregateRetention = data.aggregate_retention_days == null ? NaN : Number(data.aggregate_retention_days);
  const identityCopy = mode === 'identified'
    ? (capabilities.viewer_detail ? 'Administrators can see account names in raw history.' : 'Account names may be retained, but this role receives aggregates only.')
    : mode === 'pseudonymous'
      ? 'Viewer keys are app-scoped and never shown; account names are not retained.'
      : 'No stable viewer identifier is retained.';
  const rawCopy = !Number.isFinite(rawRetention) || rawRetention < 0 ? 'Raw-session retention is unavailable' : rawRetention === 0 ? 'Raw sessions are retained without a time limit' : `Raw sessions are retained for ${rawRetention} days`;
  const aggregateCopy = !Number.isFinite(aggregateRetention) || aggregateRetention < 0 ? 'daily-total retention is unavailable' : aggregateRetention === 0 ? 'daily totals are retained without a time limit' : `daily non-identifying totals are retained for ${aggregateRetention} days`;
  privacy.textContent = `${identityCopy} ${rawCopy}; ${aggregateCopy}. No IP address or browser fingerprint is collected for usage analytics.`;
  const measurement = doc.createElement('details');
  measurement.className = 'usage-measurement';
  const measurementTitle = doc.createElement('summary');
  measurementTitle.textContent = `Privacy & measurement · ${mode.charAt(0).toUpperCase() + mode.slice(1)}`;
  const definitionDetail = doc.createElement('p');
  definitionDetail.textContent = 'A session begins when the app’s live connection opens. Reloads and reconnects count as new sessions; page assets and failed launches do not. Unique viewers counts authenticated people, excluding anonymous visitors and service accounts. Connected time includes active sessions through their latest heartbeat. Peaks include sessions opened before the selected window.';
  measurement.append(measurementTitle, definitionDetail, privacy);
  root.append(measurement);

  if ((capabilities.viewer_detail || capabilities.recent_sessions) && Number(summary.sessions) > 0) {
    const detail = doc.createElement('div');
    detail.className = 'usage-detail-grid';
    const viewers = doc.createElement('section');
    viewers.className = 'usage-detail-section';
    const viewersTitle = doc.createElement('h3');
    viewersTitle.textContent = 'Top named viewers';
    viewers.append(viewersTitle);
    const viewerLimit = doc.createElement('p');
    viewerLimit.className = 'usage-detail-empty';
    viewerLimit.textContent = 'Up to 50 retained accounts, ordered by session count.';
    viewers.append(viewerLimit);
    if (Array.isArray(data.viewers) && data.viewers.length > 0) {
      const scroll = doc.createElement('div');
      scroll.className = 'usage-table-wrap';
      scroll.tabIndex = 0;
      scroll.setAttribute('role', 'region');
      scroll.setAttribute('aria-label', 'Retained account usage');
      scroll.append(buildViewerTable(doc, data.viewers, generatedAt));
      viewers.append(scroll);
    } else {
      const p = doc.createElement('p');
      p.className = 'usage-detail-empty';
      p.textContent = 'No named raw sessions are available. Sessions may be anonymous, service-account activity, or older totals that have already been rolled up.';
      viewers.append(p);
    }

    const recent = doc.createElement('section');
    recent.className = 'usage-detail-section';
    const recentTitle = doc.createElement('h3');
    recentTitle.textContent = 'Recent sessions';
    recent.append(recentTitle);
    const recentLimit = doc.createElement('p');
    recentLimit.className = 'usage-detail-empty';
    recentLimit.textContent = 'Up to 50 newest retained sessions, including anonymous and service-account activity.';
    recent.append(recentLimit);
    if (Array.isArray(data.recent_sessions) && data.recent_sessions.length > 0) {
      const allSessions = data.recent_sessions;
      const collapsedCount = 12;
      const scroll = doc.createElement('div');
      scroll.className = 'usage-table-wrap';
      scroll.tabIndex = 0;
      scroll.setAttribute('role', 'region');
      scroll.setAttribute('aria-label', 'Recent retained sessions');
      scroll.id = 'usage-recent-sessions';
      scroll.append(buildRecentTable(doc, allSessions.slice(0, collapsedCount), generatedAt));
      recent.append(scroll);
      if (allSessions.length > collapsedCount) {
        let expanded = false;
        const more = doc.createElement('button');
        more.type = 'button';
        more.className = 'usage-show-more';
        more.setAttribute('aria-controls', scroll.id);
        more.setAttribute('aria-expanded', 'false');
        more.textContent = `Show ${number(allSessions.length - collapsedCount)} more`;
        more.addEventListener('click', () => {
          expanded = !expanded;
          const visible = expanded ? allSessions : allSessions.slice(0, collapsedCount);
          scroll.replaceChildren(buildRecentTable(doc, visible, generatedAt));
          more.setAttribute('aria-expanded', String(expanded));
          more.textContent = expanded ? 'Show fewer' : `Show ${number(allSessions.length - collapsedCount)} more`;
        });
        recent.append(more);
      }
    } else {
      const p = doc.createElement('p');
      p.className = 'usage-detail-empty';
      p.textContent = 'No retained sessions are available in this window.';
      recent.append(p);
    }
    if (capabilities.viewer_detail) detail.append(viewers);
    if (capabilities.recent_sessions) detail.append(recent);
    root.append(detail);
  }

  const updated = doc.createElement('span');
  updated.className = 'usage-request-status';
  updated.setAttribute('role', 'status');
  updated.textContent = 'Usage analytics updated.';
  root.append(updated);

  panel.append(root);
}

function scrollTable(doc, table, label) {
  const wrap = doc.createElement('div');
  wrap.className = 'usage-table-wrap';
  wrap.tabIndex = 0;
  wrap.setAttribute('role', 'region');
  wrap.setAttribute('aria-label', label);
  wrap.append(table);
  return wrap;
}

// The selected API range and effective retained window are distinct. Controls
// always send a supported API value, even when the server caps it to 14 days.
export function createUsageViewer({ panel, app, api, onUnauthorized = () => {} }) {
  const doc = panel.ownerDocument;
  const requests = createUsageRequestGate();
  let disposed = false;
  let requested = 30;
  let displayedRequest = null;
  let displayedSelect = '';
  let report = null;

  function errorMessage(status) {
    return status === 403 ? 'Your access to this app changed. Ask an app manager for access.'
      : status === 429 ? 'Usage reports are temporarily busy. Try again in a moment.'
      : 'Could not load usage. Check your connection and try again.';
  }
  function paintError(message, failedRange) {
    const retry = event => load(failedRange, 'retry', event.currentTarget);
    if (!report) { renderUsageError(doc, panel, message, retry); return; }
    panel.querySelector('.usage-request-error')?.remove();
    const error = doc.createElement('div');
    error.className = 'usage-request-error';
    error.setAttribute('role', 'alert');
    const text = doc.createElement('p');
    text.textContent = `${message} The previous ${report.window_days}-day report is still shown.`;
    const button = doc.createElement('button');
    button.type = 'button';
    button.className = 'btn-row usage-retry';
    button.textContent = 'Retry';
    button.addEventListener('click', retry);
    error.append(text, button);
    panel.querySelector('.usage-heading').after(error);
    panel.querySelector('.usage-controls select').value = displayedSelect;
    const status = panel.querySelector('.usage-request-status');
    if (status) status.textContent = 'Update interrupted. Showing the previous report.';
  }
  async function load(nextRange = requested, source = '', trigger = null) {
    if (disposed) return;
    requested = [7, 30, 90, 365].includes(Number(nextRange)) ? Number(nextRange) : 30;
    const range = requested;
    const request = requests.begin();
    let keepFocus = !!trigger && doc.activeElement === trigger;
    const focusChanged = event => { if (event.target !== trigger && !trigger?.contains(event.target)) keepFocus = false; };
    doc.addEventListener('focusin', focusChanged);
    doc.addEventListener('pointerdown', focusChanged, true);
    if (!report && !source) renderUsageLoading(doc, panel);
    panel.setAttribute('aria-busy', 'true');
    const refresh = panel.querySelector('.usage-controls button');
    if (refresh) { refresh.disabled = true; refresh.textContent = 'Refreshing…'; }
    const retryButton = panel.querySelector('.usage-retry');
    if (retryButton) retryButton.disabled = true;
    const status = panel.querySelector('.usage-request-status');
    if (status) status.textContent = source === 'range' ? 'Loading the selected range. The previous report remains below.' : 'Refreshing usage…';
    try {
      const response = await api(`/api/apps/${encodeURIComponent(app.slug)}/usage?days=${range}`, { signal: request.signal, cache: 'no-store' });
      if (disposed || !requests.isCurrent(request)) return;
      if (response.status === 401) { onUnauthorized(); return; }
      if (!response.ok) throw Object.assign(new Error('response'), { status: response.status });
      const data = await response.json();
      if (disposed || !requests.isCurrent(request)) return;
      if (!data || typeof data.enabled !== 'boolean' || !Number.isInteger(data.window_days) || data.window_days < 1 || data.window_days > 365 ||
          !data.summary || typeof data.summary !== 'object' || Array.isArray(data.summary) || !Array.isArray(data.daily) ||
          !data.generated_at || Number.isNaN(new Date(data.generated_at).getTime())) throw new Error('invalid');
      const open = [...panel.querySelectorAll('details[open]')].map(node => node.className);
      const expanded = panel.querySelector('.usage-show-more')?.getAttribute('aria-expanded') === 'true';
      report = data;
      displayedRequest = range;
      renderUsageDashboard(doc, panel, data, {
        onRangeChange: (days, control) => load(days, 'range', control),
        onRefresh: control => load(displayedRequest, 'refresh', control),
      });
      displayedSelect = panel.querySelector('.usage-controls select').value;
      for (const node of panel.querySelectorAll('details')) if (open.includes(node.className)) node.open = true;
      if (expanded) panel.querySelector('.usage-show-more')?.click();
      if (keepFocus) panel.querySelector(source === 'range' ? '.usage-controls select' : '.usage-controls button')?.focus({ preventScroll: true });
    } catch (err) {
      if (disposed || !requests.isCurrent(request)) return;
      // Access revocation must remove retained account names and exact times.
      if (err?.status === 403) report = null;
      paintError(errorMessage(err?.status), range);
      if (keepFocus) panel.querySelector('.usage-retry')?.focus({ preventScroll: true });
    } finally {
      doc.removeEventListener('focusin', focusChanged);
      doc.removeEventListener('pointerdown', focusChanged, true);
      if (!disposed && requests.isCurrent(request)) {
        panel.removeAttribute('aria-busy');
        const button = panel.querySelector('.usage-controls button');
        if (button) { button.disabled = false; button.textContent = 'Refresh'; }
        const retry = panel.querySelector('.usage-retry');
        if (retry) retry.disabled = false;
      }
    }
  }
  load();
  return () => { disposed = true; requests.invalidate(); };
}
