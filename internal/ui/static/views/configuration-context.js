import { savedConfigurationBehavior } from './configuration-behavior.js';
import { refreshDirectSetting } from './direct-setting.js';
// Server defaults and effective values are different: never describe a saved
// override as the value the user would inherit by clearing a field.
const number = value => typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null;
export const usageModeLabel = mode => ({
  unattributed: 'Unattributed totals', pseudonymous: 'Pseudonymous viewers', identified: 'Named viewers',
}[mode] || 'Policy unavailable');
const isolationLabel = mode => ({ multiplex: 'Multiplex', grouped: 'Grouped', per_session: 'Per session' }[mode] || 'unavailable');

export function configurationDefaults(app = {}, envelope = {}) {
  const defaults = envelope.configuration_defaults || {};
  const inherited = (key, effective, isInherited) => number(defaults[key]) ?? (isInherited ? number(effective) : null);
  return {
    hibernation: inherited('hibernate_timeout_minutes', envelope.effective_hibernate_timeout_minutes ?? app.effective_hibernate_timeout_minutes, app.hibernate_timeout_minutes == null),
    sessions: inherited('max_sessions_per_replica', envelope.effective_max_sessions_per_replica, !app.max_sessions_per_replica),
    target: inherited('autoscale_target', envelope.effective_autoscale_target, !app.autoscale_target),
    memory: inherited('memory_limit_mb', app.effective_memory_limit_mb, app.memory_limit_mb == null),
    cpu: inherited('cpu_quota_percent', app.effective_cpu_quota_percent, app.cpu_quota_percent == null),
    isolation: defaults.worker_isolation || (!app.worker_isolation ? app.effective_worker_isolation : '') || '',
    timezone: defaults.schedule_timezone || '',
  };
}

export function applyScheduleTimezone(doc, envelope = {}) {
  const zone = envelope.configuration_defaults?.schedule_timezone;
  const input = doc.getElementById('sched-timezone');
  if (input) input.dataset.defaultTimezone = zone || '';
  const help = doc.getElementById('sched-timezone-help');
  if (help) help.textContent = zone ? `Leave empty to use the server timezone: ${zone}.` : 'Leave empty to use the server timezone. Its value is unavailable.';
}

export function applyConfigurationContext(doc, app, envelope = {}) {
  const d = configurationDefaults(app, envelope);
  const behavior = doc.getElementById('configuration-behavior');
  if (behavior) { behavior.textContent = savedConfigurationBehavior(app, envelope, d); behavior.hidden = false; }
  const behaviorNote = doc.getElementById('configuration-behavior-note');
  if (behaviorNote) behaviorNote.textContent = 'Unsaved changes are not included.';
  const text = (id, value) => { const el = doc.getElementById(id); if (el) el.textContent = value; };
  const dataset = (id, key, value) => { const el = doc.getElementById(id); if (el) el.dataset[key] = value == null ? '' : String(value); };
  text('general-app-path', app.slug ? `/app/${encodeURIComponent(app.slug)}/` : '/app/<slug>/');
  const isolation = app.worker_isolation || app.effective_worker_isolation || d.isolation;
  const elastic = ['grouped', 'per_session'].includes(isolation);
  const process = elastic ? 'worker' : 'replica';
  text('resources-description', `Memory and CPU available to each ${process}.`);
  text('resources-memory-help', `Hard cap for each ${process}. Enter whole MB, for example 1,024.`);
  text('resources-cpu-help', '0.5 core allows half of one CPU core.');
  text('resources-save-help', ['running', 'degraded'].includes(app.status)
    ? 'Changes apply live when supported. Some changes restart running processes.'
    : 'Saved limits are used when the app next starts.');
  const capacity = number(envelope.configuration_host_cpu_cores);
  text('resources-cpu-capacity', capacity > 0
    ? `Host: ${capacity} ${capacity === 1 ? 'core' : 'cores'} total, shared.`
    : envelope.configuration_capacity_scope === 'per_host'
      ? 'Host capacity depends on placement; per-host core counts are not reported here.'
      : 'Host CPU capacity is not reported.');

  text('scaling-replicas-policy', elastic ? 'Isolated workers start on demand. Use Max workers below to set the pool limit.' : app.autoscale_enabled && envelope.global_autoscale_enabled !== false
    ? 'Autoscale is enabled and may adjust a manual replica change on its next check.' : '');
  const autoscaleNote = doc.getElementById('autoscale-policy-note');
  if (autoscaleNote) {
    autoscaleNote.textContent = envelope.global_autoscale_enabled === false
      ? 'Autoscale is paused server-wide. Saved settings will take effect when a server administrator enables it.'
      : ['grouped', 'per_session'].includes(app.effective_worker_isolation || app.worker_isolation || d.isolation)
        ? 'Isolated workers start on demand. Replica autoscale does not control this worker pool.' : '';
    autoscaleNote.hidden = !autoscaleNote.textContent;
  }
  for (const [id, value] of Object.entries({
    'resources-memory': d.memory, 'resources-cpu': d.cpu == null ? null : Number((d.cpu / 100).toFixed(2)), 'scaling-cap': d.sessions,
    'hibernate-custom-minutes': d.hibernation, 'autoscale-target': d.target == null ? null : Number((d.target * 100).toFixed(6)),
  })) {
    dataset(id, 'defaultValue', value);
    // Update inherited displays and default guidance without repopulating drafts.
    refreshDirectSetting(doc, id);
  }
  dataset('resources-memory', 'defaultLimit', d.memory);
  dataset('worker-isolation', 'defaultIsolation', d.isolation);
  const appLink = doc.getElementById('general-app-link');
  if (appLink && app.slug) appLink.href = `/app/${encodeURIComponent(app.slug)}/`;
  const inheritOption = doc.querySelector('#worker-isolation option[value=""]');
  if (inheritOption) inheritOption.textContent = `Inherit server policy · ${isolationLabel(d.isolation)}`;
  applyScheduleTimezone(doc, envelope);
}

// Switching process models suspends irrelevant drafts; returning restores them.
// The submitted form always contains saved values for controls it does not show.
export function switchWorkerFields(doc, saved) {
  const select = doc.getElementById('worker-isolation');
  const mode = select.value || select.dataset.defaultIsolation || 'multiplex';
  const elastic = ['per_session', 'grouped'].includes(mode);
  const replicasVisible = !doc.getElementById('scaling-replica-fields').hidden;
  for (const [id, key, relevant, wasRelevant] of [
    ['worker-grouped-size', 'worker_grouped_size', mode === 'grouped'],
    ['worker-max-workers', 'worker_max_workers', elastic],
    ['worker-warm-spares', 'worker_warm_spares', elastic],
    ['scaling-replicas', 'replicas', !elastic, replicasVisible],
    ['scaling-cap', 'max_sessions_per_replica', !elastic, replicasVisible],
    ['scaling-cap-mode', null, !elastic, replicasVisible],
  ]) {
    const input = doc.getElementById(id);
    if (wasRelevant ?? !input.disabled) input.dataset.modeDraft = input.value;
    const baseline = id === 'scaling-cap-mode' ? (saved.max_sessions_per_replica ? 'custom' : 'inherit')
      : id === 'scaling-cap' && !saved[key] ? doc.getElementById(id).dataset.defaultValue : saved[key];
    input.value = relevant ? (input.dataset.modeDraft ?? input.value) : String(baseline ?? '');
    input.removeAttribute('aria-invalid'); input.removeAttribute('aria-errormessage');
  }
  refreshDirectSetting(doc, 'scaling-cap');
  updateWorkerFields(doc);
}

export function updateWorkerFields(doc) {
  const select = doc.getElementById('worker-isolation');
  if (!select) return;
  const mode = select.value || select.dataset.defaultIsolation || '';
  const replicas = doc.getElementById('scaling-replica-fields');
  if (replicas) replicas.hidden = ['per_session', 'grouped'].includes(mode);
  const help = doc.getElementById('worker-mode-help');
  const explanations = {
    multiplex: 'Share each replica across visitors to use fewer processes. Heavy work in one session can delay others on that replica.',
    per_session: 'Give each browser client its own worker to isolate heavy work. Uses more memory; tabs in the same browser profile share a worker. Single-node hosting only.',
    grouped: 'Share a worker among a small group of browser clients to balance memory use and isolation. Heavy work can delay clients in the same group. Single-node hosting only.',
  };
  const explanation = explanations[mode] || 'The server has not reported its worker policy. Choose an explicit mode or reload to check the inherited policy.';
  if (help && help.textContent !== explanation) help.textContent = explanation;
  for (const [id, relevant] of [
    ['worker-grouped-size', mode === 'grouped'],
    ['worker-max-workers', ['per_session', 'grouped'].includes(mode)],
    ['worker-warm-spares', ['per_session', 'grouped'].includes(mode)],
  ]) {
    const input = doc.getElementById(id);
    if (!input) continue;
    input.closest('.scaling-row').hidden = !relevant;
    input.disabled = select.disabled || !relevant;
  }
}

export function markConfigurationBehaviorStale(doc) {
  const behavior = doc.getElementById('configuration-behavior');
  if (behavior) behavior.hidden = true;
  const note = doc.getElementById('configuration-behavior-note');
  if (note) note.textContent = 'Could not confirm the latest saved settings. Reload to refresh the behavior summary.';
}

// Settings pages share mounted form nodes so navigation never destroys drafts.
export const CONFIGURATION_PAGES = {
  general: ['configuration-general', 'danger-zone'],
  environment: ['configuration-environment'],
  resources: ['configuration-resources', 'configuration-pacing'],
  scaling: ['configuration-scaling', 'autoscale-options', 'configuration-hibernation'],
  privacy: ['usage-privacy-settings'],
};
const pageLabel = page => page[0].toUpperCase() + page.slice(1);
export function configurationPageForSection(id) {
  return Object.keys(CONFIGURATION_PAGES).find(page => CONFIGURATION_PAGES[page].includes(id)) || (CONFIGURATION_PAGES[id] ? id : 'general');
}
export function revealConfigurationSection(view, id) {
  const doc = view.ownerDocument;
  const panel = doc.getElementById('detail-configuration-panel');
  if (!panel) return;
  const page = configurationPageForSection(id);
  panel.dataset.settingsPage = page;
  panel.querySelectorAll(':scope > .settings-block').forEach(el => {
    el.toggleAttribute('data-current-section', CONFIGURATION_PAGES[page].includes(el.id));
  });
  panel.setAttribute('aria-label', `${pageLabel(page)} settings`);
}
export function updateConfigurationDirty(doc, sectionId, dirty) {
  doc.getElementById(sectionId)?.toggleAttribute('data-dirty', dirty);
  const pages = { ...CONFIGURATION_PAGES, access: ['access-visibility'] };
  for (const [page, ids] of Object.entries(pages)) {
    const changed = ids.some(id => doc.getElementById(id)?.hasAttribute('data-dirty'));
    doc.querySelectorAll(`a[data-settings-page="${page}"]`).forEach(link => {
      link.toggleAttribute('data-dirty', changed);
      link.setAttribute('aria-label', `${pageLabel(page)}${changed ? ' (unsaved)' : ''}`);
    });
  }
  const pending = doc.getElementById('configuration-pending');
  if (pending) {
    const names = Object.entries(pages).filter(([, ids]) => ids.some(id => doc.getElementById(id)?.hasAttribute('data-dirty'))).map(([page]) => pageLabel(page));
    const message = names.length ? `Unsaved: ${names.join(', ')}. Open each page to save or discard.` : '';
    if (pending.textContent !== message) pending.textContent = message;
    if (pending.hidden !== !names.length) pending.hidden = !names.length;
  }
}
export function environmentEditValue(variable) {
  return variable && !variable.secret ? variable.value ?? '' : '';
}
