import { cpuSeverity, formatBytes } from './stat-format.js';

// Both the app tile and Overview use the same native meter, thresholds, and
// accessible reading. The caller owns only its surrounding layout.
export function updateCPUMeter(meter, fraction, label) {
  const available = Number.isFinite(fraction);
  meter.hidden = !available;
  if (!available) return;
  meter.min = 0;
  meter.max = 1;
  meter.low = 0.7;
  meter.high = 0.9;
  meter.optimum = 0;
  meter.value = Math.max(0, Math.min(1, fraction));
  meter.className = `ov-capacity-meter ov-capacity-meter--${cpuSeverity(fraction)}`;
  meter.setAttribute('aria-label', label);
  meter.textContent = `${Math.round(fraction * 100)}%`;
}

// Replica inspection is available from the compact header on every app tab.
export function renderReplicaHeat(container, metrics) {
  if (!container) return;
  const cells = container.querySelector('.cpu-heat-cells');
  const warning = container.querySelector('.cpu-heat-warning');
  const state = container.querySelector('.cpu-heat-state');
  const inspector = container.querySelector('.cpu-heat-inspector');
  const replicas = Array.isArray(metrics?.replicas) ? metrics.replicas : [];
  const unit = ['grouped', 'per_session'].includes(metrics?.worker_isolation) ? 'Worker' : 'Replica';
  cells.setAttribute('aria-label', `CPU use by running ${unit.toLowerCase()}s`);
  const running = replicas.filter(r => r.status === 'running');
  const quotaCount = running.filter(r => r.cpu_quota_enforced && r.effective_cpu_quota_percent > 0).length;
  const heading = container.querySelector('.cpu-heat-title');
  if (heading) heading.textContent = `${unit} CPU`;
  const unitLabel = container.querySelector('.cpu-heat-unit');
  if (unitLabel) unitLabel.textContent = quotaCount === 0 ? '% of one core' : quotaCount === running.length ? '% of quota' : '% of core / quota';
  const starting = replicas.filter(r => r.status === 'starting' || r.status === 'booting').length;
  const pending = running.filter(r => r.cpu_percent == null && r.metrics_available !== false).length;
  const active = container.ownerDocument.activeElement;
  const focusedIndex = container.contains(active) ? active?.dataset?.replicaIndex : null;
  const focusedPosition = focusedIndex == null ? -1 : [...cells.children].indexOf(active);
  container.hidden = running.length === 0 && starting === 0;
  cells.replaceChildren();
  const measuredCount = running.filter(r => r.metrics_available !== false && Number.isFinite(r.cpu_percent) && r.cpu_percent >= 0).length;
  const currentHot = running.filter(r => r.metrics_available !== false && Number.isFinite(r.cpu_percent) && r.cpu_percent >= 0 &&
    r.cpu_percent >= 0.9 * (r.cpu_quota_enforced && r.effective_cpu_quota_percent > 0
      ? r.effective_cpu_quota_percent : 100)).length;
  const saturated = running.filter(r => r.metrics_available !== false && Number.isFinite(r.cpu_percent) && r.cpu_percent >= 0 && r.cpu_saturated).length;
  const details = new Map();
  for (const replica of running) {
    const cell = container.ownerDocument.createElement('button');
    cell.type = 'button';
    const ceiling = replica.cpu_quota_enforced && replica.effective_cpu_quota_percent > 0
      ? replica.effective_cpu_quota_percent : 100;
    const measured = replica.metrics_available !== false && typeof replica.cpu_percent === 'number' && Number.isFinite(replica.cpu_percent) && replica.cpu_percent >= 0;
    const fraction = measured ? replica.cpu_percent / ceiling : null;
    const severity = cpuSeverity(fraction);
    cell.className = `cpu-heat-cell${measured ? ` is-${severity}` : ' is-unknown'}${fraction > 1 ? ' is-over' : ''}`;
    const index = container.ownerDocument.createElement('span');
    index.className = 'cpu-heat-index';
    index.setAttribute('aria-hidden', 'true');
    index.textContent = `#${replica.index}`;
    const value = container.ownerDocument.createElement('span');
    value.className = 'cpu-heat-value';
    value.setAttribute('aria-hidden', 'true');
    value.textContent = measured ? `${Math.round(fraction * 100)}%` : '—';
    cell.append(index, value);
    const sessions = replica.sessions == null ? NaN : Number(replica.sessions);
    const rss = Number(replica.rss_bytes) > 0 ? `${formatBytes(replica.rss_bytes)} RSS` : 'RSS unavailable';
    const quota = replica.cpu_quota_enforced && replica.effective_cpu_quota_percent > 0;
    const cores = ceiling / 100;
    const scale = quota ? `${cores} ${cores === 1 ? 'core' : 'cores'} quota` : '1 core';
    const detail = `${unit} #${replica.index} · ${measured
      ? `${replica.cpu_percent.toFixed(1)}% CPU · ${Math.round(fraction * 100)}% of ${scale}`
      : replica.metrics_available === false ? 'CPU unavailable' : 'CPU pending'} · ${rss} · ${Number.isFinite(sessions) && sessions >= 0 ? `${sessions} sessions` : 'sessions unavailable'}`;
    details.set(String(replica.index), detail);
    cell.title = detail;
    cell.dataset.replicaIndex = String(replica.index);
    cell.setAttribute('aria-label', `${fraction >= 0.9 ? (quota ? 'Near CPU quota · ' : 'High one-core usage · ') : fraction >= 0.7 ? 'Elevated CPU · ' : ''}${detail}`);
    cells.appendChild(cell);
  }
  const selected = running.find(r => String(r.index) === container.dataset.selectedReplicaIndex);
  const selectedIndex = selected ? String(selected.index) : null;
  if (selectedIndex === null) delete container.dataset.selectedReplicaIndex;
  else container.dataset.selectedReplicaIndex = selectedIndex;
  const showDetail = (index) => {
    if (!inspector) return;
    const detail = details.get(index) || '';
    if (inspector.textContent !== detail) inspector.textContent = detail;
    inspector.hidden = !detail;
  };
  for (const cell of cells.children) {
    const index = cell.dataset.replicaIndex;
    const isSelected = index === selectedIndex;
    cell.classList.toggle('is-selected', isSelected);
    cell.setAttribute('aria-pressed', String(isSelected));
    cell.tabIndex = isSelected || (!selectedIndex && cell === cells.firstElementChild) ? 0 : -1;
    cell.addEventListener('mouseenter', () => showDetail(index));
    cell.addEventListener('mouseleave', () => showDetail(container.dataset.selectedReplicaIndex));
    cell.addEventListener('focus', () => showDetail(index));
    cell.addEventListener('blur', () => showDetail(container.dataset.selectedReplicaIndex));
    cell.addEventListener('click', () => {
      container.dataset.selectedReplicaIndex = index;
      for (const other of cells.children) {
        const selectedCell = other === cell;
        other.classList.toggle('is-selected', selectedCell);
        other.setAttribute('aria-pressed', String(selectedCell));
        other.tabIndex = selectedCell ? 0 : -1;
      }
      showDetail(index);
    });
    cell.addEventListener('keydown', event => {
      const all = [...cells.children];
      const current = all.indexOf(cell);
      const firstTop = all[0]?.getBoundingClientRect().top;
      const columns = Math.max(1, all.filter(candidate => Math.abs(candidate.getBoundingClientRect().top - firstTop) < 1).length);
      const destination = event.key === 'ArrowRight' ? current + 1
        : event.key === 'ArrowLeft' ? current - 1
          : event.key === 'ArrowDown' ? (current + columns < all.length ? current + columns : -1)
            : event.key === 'ArrowUp' ? current - columns
              : event.key === 'Home' ? 0 : event.key === 'End' ? all.length - 1 : -1;
      if (!['ArrowRight', 'ArrowLeft', 'ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
      event.preventDefault();
      if (destination < 0 || destination >= all.length) return;
      all[destination].click();
      all[destination].focus();
    });
  }
  showDetail(focusedIndex && details.has(focusedIndex) ? focusedIndex : selectedIndex);
  const saturationAvailable = metrics?.cpu_saturation_available === true;
  const warningCount = saturationAvailable && saturated ? saturated : currentHot;
  const warningText = warningCount
    ? `${warningCount} ${unit.toLowerCase()}${warningCount === 1 ? '' : 's'} ≥90%${saturated && saturationAvailable ? ' for 3 samples' : ''}` : '';
  if (warning.textContent !== warningText) warning.textContent = warningText;
  warning.hidden = !warningText;
  if (state) {
    const stateText = [starting ? `${starting} starting` : '',
      pending ? `${pending} CPU pending` : '',
      running.length && measuredCount === 0 && !pending ? 'CPU unavailable' : '',
      measuredCount && measuredCount < running.length && !pending ? `${measuredCount}/${running.length} reporting` : ''].filter(Boolean).join(' · ');
    if (state.textContent !== stateText) state.textContent = stateText;
    state.hidden = !stateText;
  }
  const footer = container.querySelector('.cpu-heat-footer');
  if (footer) footer.hidden = warning.hidden && (!state || state.hidden);
  if (focusedIndex != null) {
    const replacement = [...cells.children].find(cell => cell.dataset.replicaIndex === focusedIndex)
      || cells.children[Math.min(focusedPosition, cells.children.length - 1)];
    if (replacement) {
      for (const cell of cells.children) cell.tabIndex = cell === replacement ? 0 : -1;
      replacement.focus({ preventScroll: true });
    } else {
      container.ownerDocument.getElementById('app-detail-cpu')?.focus({ preventScroll: true });
    }
  }
}
