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

function shortDate(raw) {
  const date = new Date(`${raw}T00:00:00Z`);
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' });
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
  const width = 760;
  const height = 222;
  const inset = { top: 18, right: 10, bottom: 36, left: 40 };
  const plotWidth = width - inset.left - inset.right;
  const plotHeight = height - inset.top - inset.bottom;
  const max = Math.max(0, ...days.map(day => Number(day.sessions) || 0));
  const ceiling = niceCeiling(max);

  const svg = doc.createElementNS(SVG_NS, 'svg');
  svg.classList.add('usage-chart');
  svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', max > 0
    ? `Daily app sessions. Most opens: ${number(max)} on ${shortDate(days.find(day => Number(day.sessions) === max).date)}. A complete daily data table follows.`
    : 'Daily app sessions. No sessions in this window.');

  const grid = doc.createElementNS(SVG_NS, 'g');
  grid.classList.add('usage-chart-grid');
  for (let i = 0; i <= 2; i += 1) {
    const y = inset.top + (plotHeight * i / 2);
    const line = doc.createElementNS(SVG_NS, 'line');
    line.setAttribute('x1', inset.left);
    line.setAttribute('x2', width - inset.right);
    line.setAttribute('y1', y);
    line.setAttribute('y2', y);
    grid.append(line);
    const label = doc.createElementNS(SVG_NS, 'text');
    label.setAttribute('x', inset.left - 9);
    label.setAttribute('y', y + 4);
    label.setAttribute('text-anchor', 'end');
    label.textContent = number(Math.round(ceiling * (2 - i) / 2));
    grid.append(label);
  }
  svg.append(grid);

  const bars = doc.createElementNS(SVG_NS, 'g');
  bars.classList.add('usage-chart-bars');
  const slot = plotWidth / Math.max(days.length, 1);
  const barWidth = Math.max(2, Math.min(16, slot * 0.64));
  days.forEach((day, index) => {
    const value = Number(day.sessions) || 0;
    const barHeight = value === 0 ? 1 : Math.max(3, plotHeight * value / ceiling);
    const rect = doc.createElementNS(SVG_NS, 'rect');
    rect.classList.add('usage-chart-bar');
    if (value === 0) rect.classList.add('is-zero');
    rect.setAttribute('x', inset.left + slot * index + (slot - barWidth) / 2);
    rect.setAttribute('y', inset.top + plotHeight - barHeight);
    rect.setAttribute('width', barWidth);
    rect.setAttribute('height', barHeight);
    rect.setAttribute('rx', Math.min(3, barWidth / 2));
    const title = doc.createElementNS(SVG_NS, 'title');
    const viewerCopy = day.unique_viewers == null
      ? 'unique viewers not retained'
      : `${number(day.unique_viewers)} unique ${Number(day.unique_viewers) === 1 ? 'viewer' : 'viewers'}`;
    title.textContent = `${shortDate(day.date)}: ${number(value)} ${value === 1 ? 'session' : 'sessions'}, peak ${number(day.peak_concurrent_sessions)} concurrent, ${viewerCopy}`;
    rect.append(title);
    bars.append(rect);
  });
  svg.append(bars);

  const tickIndices = [...new Set([0, Math.floor((days.length - 1) / 2), days.length - 1])];
  for (const index of tickIndices) {
    const label = doc.createElementNS(SVG_NS, 'text');
    label.classList.add('usage-chart-date');
    label.setAttribute('x', inset.left + slot * index + slot / 2);
    label.setAttribute('y', height - 10);
    label.setAttribute('text-anchor', index === 0 ? 'start' : index === days.length - 1 ? 'end' : 'middle');
    label.textContent = shortDate(days[index].date);
    svg.append(label);
  }
  return svg;
}

function makeDailyDataTable(doc, days) {
  const table = doc.createElement('table');
  table.className = 'sr-only usage-daily-data';
  const caption = doc.createElement('caption');
  caption.textContent = 'Daily app usage data';
  const thead = doc.createElement('thead');
  thead.innerHTML = '<tr><th scope="col">Date (UTC)</th><th scope="col">Sessions</th><th scope="col">Peak concurrent</th><th scope="col">Unique viewers</th></tr>';
  const tbody = doc.createElement('tbody');
  for (const day of days) {
    const row = doc.createElement('tr');
    for (const value of [shortDate(day.date), number(day.sessions), number(day.peak_concurrent_sessions), day.unique_viewers == null ? 'Not retained' : number(day.unique_viewers)]) {
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
  const note = doc.createElement('span');
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
  return item.display_name || item.username || 'Anonymous';
}

function buildViewerTable(doc, viewers, generatedAt) {
  const table = doc.createElement('table');
  table.className = 'usage-table';
  table.innerHTML = '<thead><tr><th>Viewer</th><th>Sessions</th><th>Connected</th><th>Last opened</th></tr></thead>';
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
  table.innerHTML = '<thead><tr><th>Viewer</th><th>Opened</th><th>Duration</th><th>Deployment</th></tr></thead>';
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
  button.className = 'env-btn-secondary';
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
  definition.textContent = 'A session begins when the app’s live connection opens. Reloads and reconnects count as new sessions; page assets and failed launches do not.';
  headingCopy.append(h2, definition);
  const mode = data.identity_mode || 'unattributed';
  const modeBadge = doc.createElement('span');
  modeBadge.className = `usage-mode usage-mode-${mode}`;
  modeBadge.textContent = data.enabled ? mode : 'collection paused';
  modeBadge.title = data.policy_source === 'app' ? 'This app uses a stricter override.' : 'This app inherits the hub policy.';
  headingCopy.append(modeBadge);
  const controls = doc.createElement('div');
  controls.className = 'usage-controls';
  const rangeLabel = doc.createElement('label');
  rangeLabel.textContent = 'Window';
  const select = doc.createElement('select');
  select.setAttribute('aria-label', 'Usage reporting window');
  const currentWindow = Number(data.window_days) || 30;
  if (![7, 30, 90, 365].includes(currentWindow)) {
    const retained = doc.createElement('option');
    retained.value = String(currentWindow);
    retained.textContent = `${currentWindow} days (retained)`;
    retained.selected = true;
    select.append(retained);
  }
  for (const days of [7, 30, 90, 365]) {
    const option = doc.createElement('option');
    option.value = String(days);
    option.textContent = `${days} days`;
    option.selected = currentWindow === days;
    if (Number(data.aggregate_retention_days) > 0 && days > Number(data.aggregate_retention_days)) option.disabled = true;
    select.append(option);
  }
  select.addEventListener('change', () => actions.onRangeChange?.(Number(select.value), select));
  rangeLabel.append(select);
  const refresh = doc.createElement('button');
  refresh.type = 'button';
  refresh.className = 'env-btn-secondary';
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
    if (Number(summary.sessions) === 0) {
      panel.append(root);
      return;
    }
  }

  const generatedAt = data.generated_at || new Date().toISOString();
  const capabilities = data.capabilities || {};
  const hasUniqueViewers = capabilities.unique_viewers === true && summary.unique_viewers != null;
  const stats = doc.createElement('dl');
  stats.className = 'usage-summary';
  stats.setAttribute('aria-label', `Usage summary for ${data.window_days || 30} days`);
  stats.append(
    metric(doc, 'Sessions', number(summary.sessions), 'Successful connections'),
    metric(doc, 'Unique viewers', hasUniqueViewers ? number(summary.unique_viewers) : 'Not collected',
      hasUniqueViewers ? 'App-scoped identities' : 'Unavailable in this privacy mode or retention window'),
    metric(doc, 'Peak concurrent', number(summary.peak_concurrent_sessions), 'Simultaneous connections'),
    metric(doc, 'Average connected', formatDuration(summary.average_duration_seconds), 'Per session'),
    metric(doc, 'Last opened', relativeUsageTime(summary.last_opened_at, new Date(generatedAt)),
      Number(summary.active_sessions) > 0 ? `${number(summary.active_sessions)} active now` : 'No active sessions',
      summary.last_opened_at ? new Date(summary.last_opened_at).toLocaleString() : ''),
  );
  root.append(stats);

  const grid = doc.createElement('div');
  grid.className = 'usage-grid';
  const activity = doc.createElement('section');
  activity.className = 'usage-activity';
  const activityHead = doc.createElement('div');
  activityHead.className = 'usage-section-heading';
  const activityTitle = doc.createElement('h3');
  activityTitle.textContent = 'Daily opens';
  const utc = doc.createElement('span');
  utc.textContent = 'UTC';
  activityHead.append(activityTitle, utc);
  activity.append(activityHead);
  const days = filledDays(data.daily, data.window_days, generatedAt);
  if (Number(summary.sessions) > 0) {
    const chartSurface = doc.createElement('div');
    chartSurface.className = 'usage-chart-surface';
    chartSurface.append(makeActivityChart(doc, days), makeDailyDataTable(doc, days));
    activity.append(chartSurface);
  } else {
    activity.append(emptyMessage(doc, 'No sessions in this window', 'Open the app to begin collecting connection-based usage.'));
  }

  const audience = doc.createElement('section');
  audience.className = 'usage-audience';
  const audienceTitle = doc.createElement('h3');
  audienceTitle.textContent = 'Audience mix';
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
  audience.append(bar, legend, audienceHelp);
  grid.append(activity, audience);
  root.append(grid);

  const privacy = doc.createElement('p');
  privacy.className = 'usage-privacy';
  const rawRetention = Number(data.raw_retention_days);
  const aggregateRetention = Number(data.aggregate_retention_days);
  const identityCopy = mode === 'identified'
    ? (capabilities.viewer_detail ? 'Administrators can see account names in raw history.' : 'Account names may be retained, but this role receives aggregates only.')
    : mode === 'pseudonymous'
      ? 'Viewer keys are app-scoped and never shown; account names are not retained.'
      : 'No stable viewer identifier is retained.';
  const rawCopy = rawRetention === 0 ? 'Raw sessions are retained without a time limit' : `Raw sessions are retained for ${rawRetention} days`;
  const aggregateCopy = aggregateRetention === 0 ? 'daily totals are retained without a time limit' : `daily non-identifying totals are retained for ${aggregateRetention} days`;
  privacy.textContent = `${identityCopy} ${rawCopy}; ${aggregateCopy}. No IP address or browser fingerprint is collected for usage analytics.`;
  root.append(privacy);

  if (capabilities.viewer_detail && Number(summary.sessions) > 0) {
    const detail = doc.createElement('div');
    detail.className = 'usage-detail-grid';
    const viewers = doc.createElement('section');
    viewers.className = 'usage-detail-section';
    const viewersTitle = doc.createElement('h3');
    viewersTitle.textContent = 'Known viewers';
    viewers.append(viewersTitle);
    if (Array.isArray(data.viewers) && data.viewers.length > 0) {
      const scroll = doc.createElement('div');
      scroll.className = 'usage-table-wrap';
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
    if (Array.isArray(data.recent_sessions) && data.recent_sessions.length > 0) {
      const allSessions = data.recent_sessions;
      const collapsedCount = 12;
      const scroll = doc.createElement('div');
      scroll.className = 'usage-table-wrap';
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
      p.textContent = 'No identifiable raw sessions are available in this window.';
      recent.append(p);
    }
    detail.append(viewers, recent);
    root.append(detail);
  }

  const updated = doc.createElement('span');
  updated.className = 'sr-only';
  updated.setAttribute('role', 'status');
  updated.textContent = 'Usage analytics updated.';
  root.append(updated);

  panel.append(root);
}
