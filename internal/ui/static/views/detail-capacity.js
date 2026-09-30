import { headerStats } from './stat-format.js';
import { renderReplicaHeat, updateCPUMeter } from './cpu-capacity.js';
import { renderAutoscaleVisual } from './autoscale-visual.js';

const write = (doc, id, value) => {
  const el = doc.getElementById(id);
  if (!el) return;
  if (el.textContent !== value) el.textContent = value;
  el.classList.toggle('is-empty', value === '—');
};

export function seedDetailCapacity(doc, app) {
  const surface = doc.querySelector('.app-detail-capacity');
  if (!surface) return;
  surface.dataset.metricsState = 'loading';
  delete surface.dataset.measuredAt;
  doc.querySelector('.app-detail-stats')?.classList.remove('is-stale');
  for (const id of ['cpu', 'ram', 'sessions', 'replicas']) write(doc, `app-detail-${id}`, '—');
  for (const id of ['cpu', 'ram', 'sessions', 'replicas']) doc.getElementById(`app-detail-${id}`)?.removeAttribute('title');
  const elastic = ['grouped', 'per_session'].includes(app.effective_worker_isolation || app.worker_isolation);
  write(doc, 'app-detail-capacity-label', elastic ? 'Workers' : 'Replicas');
  const desired = doc.getElementById('app-detail-replica-desired');
  if (desired) {
    desired.hidden = elastic;
    desired.textContent = `${app.replicas ?? 1} desired`;
  }
  write(doc, 'app-detail-cpu-context', '');
  doc.getElementById('app-detail-cpu-meter').hidden = true;
  const heat = doc.getElementById('app-detail-cpu-heat');
  delete heat.dataset.selectedReplicaIndex;
  renderReplicaHeat(heat, { replicas: [] });
  doc.getElementById('app-detail-autoscale-visual').hidden = true;
  const details = doc.getElementById('app-detail-capacity-details');
  details.open = false;
  details.hidden = true;
  write(doc, 'app-detail-capacity-signal', '');
  write(doc, 'app-detail-metrics-message', 'Loading live metrics…');
  doc.getElementById('app-detail-metrics-status').hidden = false;
  doc.getElementById('app-detail-metrics-retry').hidden = true;
  doc.getElementById('app-detail-metrics-updated').hidden = true;
}

export function renderDetailCapacity(doc, metrics, configured, autoscale, now = Date.now()) {
  const surface = doc.querySelector('.app-detail-capacity');
  const stats = headerStats(metrics, configured);
  if (!surface) return stats;
  surface.dataset.metricsState = 'current';
  surface.dataset.measuredAt = String(now);
  doc.querySelector('.app-detail-stats')?.classList.remove('is-stale');
  const retry = doc.getElementById('app-detail-metrics-retry');
  const retryFocused = doc.activeElement === retry || retry.dataset.restoreFocus === 'true';
  write(doc, 'app-detail-metrics-message', retryFocused ? 'Live metrics updated.' : '');
  doc.getElementById('app-detail-metrics-status').hidden = !retryFocused;
  retry.hidden = true;
  const updated = doc.getElementById('app-detail-metrics-updated');
  updated.hidden = false;
  updated.textContent = `Updated ${new Date(now).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`;
  const unavailableNote = 'Live CPU/memory unavailable for this backend. Check the worker host or CloudWatch.';
  for (const [id, value] of [['cpu', stats.cpu], ['ram', stats.ram], ['sessions', stats.sessions], ['replicas', String(stats.runningCount)]]) {
    write(doc, `app-detail-${id}`, value);
    doc.getElementById(`app-detail-${id}`).title = ['cpu', 'ram'].includes(id)
      ? stats.metricsTransient ? stats.cpuContext : !stats.metricsAvailable ? unavailableNote : '' : '';
  }
  write(doc, 'app-detail-capacity-label', stats.capacityLabel);
  write(doc, 'app-detail-cpu-context', stats.cpuContext || (stats.running && !stats.metricsAvailable
    ? 'Live CPU/memory are unavailable on this backend. Check worker monitoring or CloudWatch.'
    : stats.running && stats.cpu === '—' ? 'Waiting for a complete CPU sample.' : ''));
  const meter = doc.getElementById('app-detail-cpu-meter');
  meter.hidden = stats.cpuFraction === null;
  updateCPUMeter(meter.querySelector('meter'), stats.cpuFraction, `${stats.cpu}, ${Math.round((stats.cpuFraction || 0) * 100)}% of CPU capacity`);
  const desired = doc.getElementById('app-detail-replica-desired');
  desired.textContent = stats.isElastic ? (stats.workerMax ? `${stats.workerMax} worker limit` : '') : `${stats.desiredCount} desired`;
  desired.hidden = stats.isElastic ? !stats.workerMax : stats.desiredCount === stats.runningCount;
  doc.getElementById('app-detail-replicas').title = stats.isElastic
    ? `${stats.runningCount} running workers${stats.workerMax ? `, limit ${stats.workerMax}` : ''}`
    : `${stats.runningCount} running, ${stats.desiredCount} desired`;
  const heat = doc.getElementById('app-detail-cpu-heat');
  renderReplicaHeat(heat, metrics);
  const visual = doc.getElementById('app-detail-autoscale-visual');
  renderAutoscaleVisual(visual, autoscale, metrics.autoscale_active_sessions, stats.runningCount);
  const details = doc.getElementById('app-detail-capacity-details');
  const detailsFocused = details.contains(doc.activeElement);
  details.hidden = heat.hidden && visual.hidden;
  write(doc, 'app-detail-capacity-details-label', stats.isElastic ? 'Inspect worker CPU' : 'Inspect replica CPU and autoscaling');
  if (details.hidden && detailsFocused) doc.getElementById('app-detail-cpu').focus({ preventScroll: true });
  const warning = heat.querySelector('.cpu-heat-warning');
  write(doc, 'app-detail-capacity-signal', warning.hidden ? '' : warning.textContent);
  if (retryFocused) (details.hidden ? doc.getElementById('app-detail-cpu') : details.querySelector('summary')).focus({ preventScroll: true });
  return stats;
}

export function markDetailCapacityError(doc) {
  const surface = doc.querySelector('.app-detail-capacity');
  if (!surface) return;
  const measured = !!surface.dataset.measuredAt;
  surface.dataset.metricsState = measured ? 'stale' : 'unavailable';
  doc.querySelector('.app-detail-stats')?.classList.add('is-stale');
  write(doc, 'app-detail-metrics-message', measured
    ? 'Live metrics unavailable. Values below are last known measurements. Retry or wait for the next check.'
    : 'Live metrics unavailable. No measurements received yet. Retry or wait for the next check.');
  doc.getElementById('app-detail-metrics-status').hidden = false;
  doc.getElementById('app-detail-metrics-retry').hidden = false;
  write(doc, 'app-detail-capacity-signal', measured ? 'Stale measurements' : '');
}
