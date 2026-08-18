// workerCapacityLine computes the human-readable capacity helper string shown
// below the isolation controls on the Configuration -> Scaling fieldset.
//
// The string is empty when the mode is multiplex (or falsy) because the
// per-session and grouped modes are the ones with a meaningful capacity bound.
//
// RAM estimate for per_session: worst-case RAM = maxWorkers * (memMB + 150),
// where 150 MB is the base per-process overhead the Go validator uses.
// The result is rounded to the nearest GB and displayed as "~N GB". If memMB
// is 0 or unknown the RAM clause is omitted.
export function workerCapacityLine(mode, groupedSize, maxWorkers, memMB) {
  if (!mode || mode === 'multiplex') return '';

  const w = Number(maxWorkers) || 0;
  if (w < 1) return '';

  if (mode === 'grouped') {
    const g = Number(groupedSize) || 1;
    return `Up to ${w} workers x ${g} clients = ${w * g} sessions`;
  }

  if (mode === 'per_session') {
    const m = Number(memMB) || 0;
    if (m > 0) {
      const worstMB = w * (m + 150);
      const gb = Math.round(worstMB / 1024);
      return `Up to ${w} isolated workers; worst-case ~${gb} GB`;
    }
    return `Up to ${w} isolated workers`;
  }

  return '';
}

// keepWarmInertNote returns the advisory shown beneath the Keep warm field
// when a positive keep-warm floor cannot take effect: min_warm_replicas keeps
// multiplex replicas running through idle hibernation, but an elastic pool
// (grouped / per_session) has no standing replicas. Its workers boot on demand
// and the app reports idle, which is healthy, with none running, so the floor
// is stored but inert until isolation returns to multiplex. The note names
// Warm workers as the control that does pre-boot elastic workers. Empty when
// the floor is zero or the isolation is multiplex (or unknown).
export function keepWarmInertNote(minWarm, isolation) {
  const floor = Number(minWarm) || 0;
  if (floor <= 0) return '';
  if (isolation !== 'grouped' && isolation !== 'per_session') return '';
  return `Keep warm has no effect under ${isolation} isolation: workers boot on demand and the app reports idle with none running. Use Warm workers (Scaling) to keep workers pre-booted.`;
}
