import { scaleOutThreshold, formatCountdown } from './autoscale.js';

const number = value => Number(value).toLocaleString('en-US');
const position = (value, min, max) => max > min
  ? Math.max(0, Math.min(100, (value - min) / (max - min) * 100)) : 100;

// The range shows running capacity and the controller's desired capacity.
// The session track starts at the previous scale-out boundary.
export function renderAutoscaleVisual(root, summary, activeSessions, runningCount) {
  if (!root) return;
  root.hidden = !summary?.enabled || summary.isElastic === true;
  if (root.hidden) return;

  const set = (selector, value) => {
    const el = root.querySelector(selector);
    if (el) el.textContent = value;
    return el;
  };
  const { min, max, current } = summary;
  const running = Number.isInteger(runningCount) && runningCount >= 0 ? runningCount : current;
  set('.autoscale-range-heading', `Replica range · ${running} running · ${current} desired${running !== current ? ' (solid: running; outline: desired)' : ''}`);
  const track = root.querySelector('.autoscale-range-track');
  if (track) {
    track.style.setProperty('--replica-running-position', `${position(running, min, max)}%`);
    track.style.setProperty('--replica-desired-position', `${position(current, min, max)}%`);
    track.classList.toggle('is-pending', running !== current);
    track.setAttribute('aria-label', `Autoscale range: minimum ${min}, ${running} running, ${current} desired, maximum ${max} replicas`);
    const segments = track.querySelector('.autoscale-range-segments');
    const slotCount = max - min + 1;
    const segmented = segments && Number.isInteger(slotCount) && slotCount > 1 && slotCount <= 32;
    track.classList.toggle('is-segmented', Boolean(segmented));
    if (segments) {
      segments.replaceChildren();
      if (segmented) {
        track.style.setProperty('--replica-slots', String(slotCount));
        for (let replicaCount = min; replicaCount <= max; replicaCount++) {
          const slot = root.ownerDocument.createElement('span');
          if (replicaCount <= running) slot.classList.add('is-running');
          if (replicaCount === current && current !== running) slot.classList.add('is-desired');
          segments.appendChild(slot);
        }
      }
    }
  }
  set('.autoscale-min', `Min ${min}`);
  set('.autoscale-max', `Max ${max}`);
  const trigger = scaleOutThreshold(summary);
  root.classList.toggle('is-max', trigger.state === 'max');
  root.classList.toggle('is-paused', trigger.state === 'paused');
  const next = root.querySelector('.autoscale-next');
  const now = root.querySelector('.autoscale-session-now');
  const target = root.querySelector('.autoscale-session-target');
  const start = root.querySelector('.autoscale-band-start');
  const status = root.querySelector('.autoscale-session-state');
  const loadTrack = root.querySelector('.autoscale-load-track');
  const loadLabels = root.querySelector('.autoscale-load-labels');
  const load = typeof activeSessions === 'number' && Number.isFinite(activeSessions) && activeSessions >= 0
    ? Math.floor(activeSessions) : null;
  if (trigger.state !== 'ready') {
    if (next) next.title = '';
    set('.autoscale-next', trigger.state === 'max'
      ? current > max ? 'Above maximum desired capacity' : 'Maximum desired capacity'
      : trigger.text);
    if (now) now.textContent = '';
    if (start) start.textContent = '';
    if (target) target.textContent = '';
    if (status) status.textContent = summary.runtimeCapped ? `Runtime limit: ${max} replicas (configured ${summary.configuredMax})` : '';
    if (loadTrack) loadTrack.hidden = true;
    if (loadLabels) loadLabels.hidden = true;
    return;
  }
  const floor = Math.min(max, Math.max(1, Number(summary.min) || 1, Number(summary.minWarm) || 1));
  const bandStart = current > floor
    ? scaleOutThreshold({ ...summary, current: current - 1 }).threshold : 0;
  if (next) next.textContent = `Next: ${current + 1} replicas`;
  if (next) next.title = 'Session-load threshold. Pool rejections can trigger sooner; cooldown and scan timing can delay scaling.';
  if (start) start.textContent = current > floor ? `${current} at ${number(bandStart)}` : `Min ${current}`;
  if (target) target.textContent = `${current + 1} at ${number(trigger.threshold)} sessions`;
  const guidance = summary.inCooldown
    ? `Cooldown ${formatCountdown(Date.now(), summary.cooldownUntil?.getTime()) || 'active'}; threshold does not mean immediate scaling.`
    : 'Session threshold; pool rejections can trigger sooner. Scaling follows the controller scan.';
  if (status) status.textContent = guidance;
  if (loadLabels) loadLabels.hidden = false;
  if (load === null) {
    if (now) now.textContent = 'Waiting for session load';
    if (loadTrack) loadTrack.hidden = true;
    return;
  }
  const remaining = Math.max(0, trigger.threshold - load);
  if (now) now.textContent = `${number(load)} active`;
  if (loadTrack) {
    loadTrack.hidden = false;
    const progress = (load - bandStart) / Math.max(1, trigger.threshold - bandStart) * 100;
    loadTrack.style.setProperty('--session-position', `${Math.max(0, Math.min(100, progress))}%`);
    loadTrack.setAttribute('aria-label', `${number(load)} active sessions; scale to ${current + 1} at ${number(trigger.threshold)} sessions; ${number(remaining)} remaining; current band starts at ${number(bandStart)}`);
  }
  if (status && remaining === 0) status.textContent = `Threshold reached. ${guidance}`;
}
