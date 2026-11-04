// Summarise saved server data only. Drafts in independently saved sections must
// never look like an active, combined runtime policy.
const count = value => Number.isInteger(value) && value >= 0 ? value : null;
const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;
export function savedConfigurationBehavior(app, envelope, defaults) {
  const mode = app.worker_isolation || app.effective_worker_isolation || defaults.isolation;
  if (!['multiplex', 'grouped', 'per_session'].includes(mode)) return 'The server has not reported the worker policy.';
  const warm = count(app.min_warm_replicas) ?? 0;
  const idle = app.hibernate_timeout_minutes ?? defaults.hibernation;
  if (mode !== 'multiplex') {
    const max = count(app.worker_max_workers);
    const group = count(app.worker_grouped_size);
    const sharing = mode === 'per_session' ? 'One worker per browser client' : group > 0 ? `Up to ${plural(group, 'browser client')} share each worker` : 'Browser clients share workers in groups';
    const limit = max > 0 ? `, with up to ${plural(max, 'worker')}` : '; the worker limit is unavailable';
    const spares = count(app.worker_warm_spares) ?? 0;
    const timeout = idle == null ? 'Idle timeout unavailable.' : idle === 0 ? 'Automatic hibernation is off.' : `Idle timeout setting: ${plural(idle, 'minute')}.`;
    return `${sharing}${limit}. Workers start on demand; ${spares ? `${plural(spares, 'warm worker')} stay ready for new clients and count toward that limit` : 'a client that needs a new worker waits for it to start'}. Replica autoscale and Keep warm do not control this pool. ${timeout}`;
  }
  const replicas = count(app.replicas);
  const manual = replicas == null ? 'Replica count unavailable.' : `Manual setting: ${plural(replicas, 'replica')}.`;
  const cap = app.max_sessions_per_replica > 0 ? app.max_sessions_per_replica : defaults.sessions;
  const target = app.autoscale_target > 0 ? app.autoscale_target : defaults.target;
  let serving = manual;
  if (app.autoscale_enabled) {
    if (envelope.global_autoscale_enabled === false) serving = `Autoscale is paused server-wide. ${manual}`;
    else if (cap === 0) serving = `Autoscale needs a session cap to adjust replicas. ${manual}`;
    else if (envelope.global_autoscale_enabled == null || cap == null || target == null || target <= 0) serving = 'Autoscale is configured, but its effective policy is unavailable.';
    else {
      const min = count(app.autoscale_min_replicas);
      const max = count(envelope.effective_autoscale_max_replicas) ?? count(app.autoscale_max_replicas);
      if (min == null || max == null || max < 1) serving = 'Autoscale bounds unavailable.';
      else {
        const floor = Math.min(max, Math.max(1, min, warm));
        serving = `Autoscale: ${floor === max ? plural(max, 'replica') : `${floor} to ${max} replicas`} while active.`;
        if (floor > Math.min(max, Math.max(1, min))) serving += ' Keep warm raises the minimum, within the replica ceiling.';
      }
    }
  }
  if (idle == null) return `${serving} Idle timeout unavailable.`;
  if (idle === 0) return `${serving} Automatic hibernation is off.`;
  if (warm > 0) return `${serving} After ${plural(idle, 'idle minute')}, keeps ${plural(warm, 'replica')} warm, up to the current replica count.`;
  return `${serving} Sleeps after ${plural(idle, 'idle minute')}; the next visitor wakes it.`;
}
