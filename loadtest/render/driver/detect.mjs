/**
 * Verdict logic for the render-saturation rig.
 *
 * Kept DOM-free and pure so the rules that decide a run's outcome are unit
 * tested, rather than only exercised by a live browser fleet.
 */

/**
 * Classify one session's outcome.
 *
 * disconnected dominates: a torn-down socket is the failure this rig exists
 * to detect, and a session that completed actions before dropping still
 * dropped. A session that attempted nothing is degraded rather than healthy,
 * because "did nothing" is not evidence of health.
 */
export function classifyOutcome({ disconnected, actionsAttempted, actionsSucceeded }) {
  if (disconnected) return 'disconnected';
  if (actionsAttempted === 0) return 'degraded';
  return actionsSucceeded === actionsAttempted ? 'healthy' : 'degraded';
}

/**
 * Aggregate the fleet. Rates are null for an empty fleet: no observations and
 * a rate of zero are different facts, and collapsing them would let a run that
 * executed nothing read as a clean pass.
 */
export function summarize(sessions) {
  const total = sessions.length;
  let disconnected = 0;
  let healthy = 0;
  let degraded = 0;
  let attempted = 0;
  let succeeded = 0;

  for (const s of sessions) {
    switch (classifyOutcome(s)) {
      case 'disconnected': disconnected++; break;
      case 'healthy': healthy++; break;
      default: degraded++; break;
    }
    attempted += s.actionsAttempted;
    succeeded += s.actionsSucceeded;
  }

  return {
    total,
    disconnected,
    healthy,
    degraded,
    disconnectRate: total === 0 ? null : disconnected / total,
    actionSuccessRate: attempted === 0 ? null : succeeded / attempted,
  };
}
