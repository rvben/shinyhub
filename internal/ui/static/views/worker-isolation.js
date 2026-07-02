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
