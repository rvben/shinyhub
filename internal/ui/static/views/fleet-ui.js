// Fleet ownership and convergence surfaces. The server owns drift detection;
// this module only presents its proven state. There are deliberately no apply
// commands or workflow instructions here: the dashboard reports state, while
// CI/CD owns reconciliation.

export function isFleetManaged(app) {
  return !!app && typeof app.managed_by === 'string' && app.managed_by.length > 0;
}

export function fleetID(app) {
  if (!isFleetManaged(app)) return null;
  return app.managed_by.startsWith('fleet:')
    ? app.managed_by.slice('fleet:'.length)
    : app.managed_by;
}

export function fleetBadgeText(app) {
  const id = fleetID(app);
  return id ? `Fleet · ${id}` : null;
}

export const FLEET_BADGE_COMPACT_LABEL = 'fleet';
export const FLEET_BADGE_TOOLTIP = 'This app is managed as part of a fleet.';

export function fleetBadgeTooltip(app) {
  const id = fleetID(app);
  return id ? `Fleet ${id}. ${FLEET_BADGE_TOOLTIP}` : FLEET_BADGE_TOOLTIP;
}

const DIGEST_PREFIX = 'sha256:';
const DIGEST_SHORT_LEN = 12;

export function shortContentDigest(digest) {
  if (typeof digest !== 'string' || digest.length === 0) return null;
  const bare = digest.startsWith(DIGEST_PREFIX)
    ? digest.slice(DIGEST_PREFIX.length)
    : digest;
  return bare.slice(0, DIGEST_SHORT_LEN) || null;
}

export function segmentApps(apps, segment) {
  const list = Array.isArray(apps) ? apps : [];
  if (segment === 'fleet') return list.filter(isFleetManaged);
  if (segment === 'unmanaged') return list.filter((a) => !isFleetManaged(a));
  return list.slice();
}

export function makeFleetBadge(doc, app, opts = {}) {
  if (!isFleetManaged(app)) return null;
  const compact = opts.compact === true;
  const badge = doc.createElement('span');
  badge.className = 'badge badge-fleet';
  badge.textContent = compact ? FLEET_BADGE_COMPACT_LABEL : fleetBadgeText(app);
  badge.title = fleetBadgeTooltip(app);
  return badge;
}

export function makeFleetStateBadge(doc, state) {
  if (!state || !['temporary_changes', 'incomplete'].includes(state.status)) return null;
  const badge = doc.createElement('span');
  badge.className = `badge badge-fleet-state badge-fleet-${state.status}`;
  badge.textContent = state.status === 'incomplete'
    ? 'Convergence incomplete'
    : 'Temporary changes';
  badge.setAttribute('role', 'status');
  return badge;
}

export function renderFleetBadges(container, app, state) {
  container.replaceChildren();
  const ownership = makeFleetBadge(container.ownerDocument, app);
  const convergence = makeFleetStateBadge(container.ownerDocument, state);
  if (ownership) container.append(ownership);
  if (convergence) container.append(convergence);
  container.hidden = !ownership && !convergence;
}

const FIELD_LABELS = {
  source: 'Source bundle',
  visibility: 'Visibility',
  name: 'Name',
  description: 'Description',
  icon: 'Icon',
  project: 'Project',
  hibernate_timeout_minutes: 'Hibernate timeout',
  replicas: 'Replicas',
  max_sessions_per_replica: 'Session cap',
  render_seconds: 'Render pacing',
  identity_headers: 'Identity headers',
  min_warm_replicas: 'Minimum warm replicas',
  memory_limit_mb: 'Memory limit',
  cpu_quota_percent: 'CPU limit',
  worker_isolation: 'Worker isolation',
  worker_grouped_size: 'Worker group size',
  worker_max_workers: 'Maximum workers',
  worker_warm_spares: 'Warm spare workers',
  worker_max_session_lifetime_secs: 'Maximum session lifetime',
  autoscale: 'Autoscale',
};

export function fleetFieldLabel(key) {
  return FIELD_LABELS[key] || String(key || '').replaceAll('_', ' ');
}

function unquote(value) {
  if (typeof value !== 'string' || value.length < 2 || value[0] !== '"' || value.at(-1) !== '"') return value;
  try { return JSON.parse(value); } catch { return value.slice(1, -1); }
}

export function formatFleetValue(key, value) {
  if (key === 'source') return shortContentDigest(value) || 'Not deployed';
  const raw = unquote(value);
  if (key === 'visibility') return raw ? raw[0].toUpperCase() + raw.slice(1) : '—';
  if (key === 'memory_limit_mb' && /^\d+$/.test(raw)) return `${raw} MB`;
  if (key === 'cpu_quota_percent' && /^\d+$/.test(raw)) return `${raw}%`;
  if (key === 'hibernate_timeout_minutes' && /^\d+$/.test(raw)) return `${raw} min`;
  if (key === 'worker_max_session_lifetime_secs' && /^\d+$/.test(raw)) return `${raw} sec`;
  if (raw === '(unset)' || raw === '(default)') return raw.slice(1, -1);
  return raw === '' ? 'None' : raw;
}

function appendCell(doc, row, value, className = '') {
  const cell = doc.createElement('td');
  if (className) cell.className = className;
  cell.textContent = value;
  row.append(cell);
}

function providerLabel(run) {
  if (!run) return '';
  const p = run.provenance || {};
  return (p.source && p.source.label) || (p.job && p.job.label) || p.provider || run.actor || '';
}

function timeLabel(raw, relative) {
  if (!raw) return '';
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return '';
  return typeof relative === 'function' ? relative(d) : d.toLocaleString();
}

function applicationText(state, relative) {
  const app = state && state.application;
  if (!app) return '';
  const when = timeLabel(app.applied_at || app.created_at, relative);
  const source = providerLabel(app);
  return ['Fleet last applied', when, source ? `by ${source}` : ''].filter(Boolean).join(' ');
}

function makeTemporaryCard(doc, state, opts) {
  const card = doc.createElement('section');
  card.className = 'overview-card overview-fleet-state fleet-change-review';
  card.setAttribute('aria-labelledby', 'fleet-change-heading');

  const head = doc.createElement('div');
  head.className = 'fleet-change-head';
  const titleWrap = doc.createElement('div');
  const heading = doc.createElement('h2');
  heading.id = 'fleet-change-heading';
  heading.textContent = 'Temporary changes';
  const copy = doc.createElement('p');
  copy.textContent = `Live values differ from fleet:${state.fleet_id}. The next fleet convergence will restore them.`;
  titleWrap.append(heading, copy);
  const count = doc.createElement('span');
  const total = Array.isArray(state.changes) ? state.changes.length : 0;
  count.className = 'fleet-change-count';
  count.textContent = `${total} ${total === 1 ? 'change' : 'changes'}`;
  head.append(titleWrap, count);

  const tableWrap = doc.createElement('div');
  tableWrap.className = 'fleet-change-table-wrap';
  const table = doc.createElement('table');
  table.className = 'fleet-change-table';
  const thead = doc.createElement('thead');
  thead.innerHTML = '<tr><th scope="col">Setting</th><th scope="col">Current</th><th scope="col">Fleet</th></tr>';
  const tbody = doc.createElement('tbody');
  for (const change of state.changes || []) {
    const row = doc.createElement('tr');
    appendCell(doc, row, fleetFieldLabel(change.key));
    appendCell(doc, row, formatFleetValue(change.key, change.current), 'fleet-change-current');
    appendCell(doc, row, formatFleetValue(change.key, change.fleet), 'fleet-change-declared');
    tbody.append(row);
  }
  table.append(thead, tbody);
  tableWrap.append(table);

  const footer = doc.createElement('div');
  footer.className = 'fleet-change-footer';
  const meta = doc.createElement('span');
  const changedWhen = timeLabel(state.changed_at, opts.relativeTime);
  const changed = state.changed_by
    ? `Changed${changedWhen ? ` ${changedWhen}` : ''} by ${state.changed_by}`
    : (changedWhen ? `Changed ${changedWhen}` : 'Changed outside fleet convergence');
  const applied = applicationText(state, opts.relativeTime);
  meta.textContent = [changed, applied].filter(Boolean).join(' · ');
  footer.append(meta);
  if (opts.configurationHref) {
    const link = doc.createElement('a');
    link.href = opts.configurationHref;
    link.dataset.nav = '';
    link.textContent = 'View configuration';
    footer.append(link);
  }

  card.append(head, tableWrap, footer);
  return card;
}

function makeIncompleteCard(doc, state, opts) {
  const card = doc.createElement('section');
  card.className = 'overview-card overview-fleet-state fleet-convergence-card';
  card.setAttribute('aria-labelledby', 'fleet-convergence-heading');

  const head = doc.createElement('div');
  head.className = 'fleet-convergence-head';
  const icon = doc.createElement('span');
  icon.className = 'fleet-convergence-icon';
  icon.setAttribute('aria-hidden', 'true');
  icon.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M12 7.75v5.5"></path><path d="M12 16.75h.01"></path></svg>';
  const copy = doc.createElement('div');
  const heading = doc.createElement('h2');
  heading.id = 'fleet-convergence-heading';
  heading.textContent = 'Fleet convergence incomplete';
  const body = doc.createElement('p');
  body.textContent = `The latest fleet application did not finish for this app. Live state may not match fleet:${state.fleet_id}.`;
  copy.append(heading, body);
  head.append(icon, copy);

  const details = doc.createElement('details');
  details.className = 'fleet-convergence-details';
  const summary = doc.createElement('summary');
  summary.textContent = 'Review status';
  const dl = doc.createElement('dl');
  const rows = [
    ['Problem', state.error || 'The application did not complete.'],
    ['Fleet run', state.attempt && state.attempt.run_id ? state.attempt.run_id.slice(0, 12) : '—'],
    ['Started', state.attempt ? timeLabel(state.attempt.created_at, opts.relativeTime) : '—'],
    ['Started by', providerLabel(state.attempt) || '—'],
    ['Last successful state', state.application ? 'Still active' : 'Not yet recorded'],
  ];
  for (const [term, description] of rows) {
    const dt = doc.createElement('dt');
    const dd = doc.createElement('dd');
    dt.textContent = term;
    dd.textContent = description;
    dl.append(dt, dd);
  }
  details.append(summary, dl);
  card.append(head, details);
  return card;
}

export function makeFleetStateCard(doc, state, opts = {}) {
  if (!state) return null;
  if (state.status === 'temporary_changes' && Array.isArray(state.changes) && state.changes.length > 0) {
    return makeTemporaryCard(doc, state, opts);
  }
  if (state.status === 'incomplete') return makeIncompleteCard(doc, state, opts);
  return null;
}
