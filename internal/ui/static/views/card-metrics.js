// Dashboard-card metrics + instance-count helpers. DOM-free so they unit-test
// without jsdom, matching app-card-badge.js and friends.
import { headerStats } from './stat-format.js';

// cardMetricsLabel returns the card's "CPU x · y RAM" line, or "" when the app is
// not running (so the line stays empty and reserves its height). CPU/RAM are
// SUMMED across all replicas - the same aggregate the detail header shows - so a
// scaled app reports its true total, not just the first replica's slice.
export function cardMetricsLabel(m, configured) {
  if (!m || m.status !== 'running') return '';
  const s = headerStats(m, configured);
  return `CPU ${s.cpu} · ${s.ram} RAM`;
}

// instanceCountLabel returns "N instances" when an app is configured for more
// than one replica, else "" (a single-replica app needs no chip). Reads the
// configured replica count from the apps-list payload (db.App.Replicas), so it
// renders immediately without waiting for a metrics poll.
export function instanceCountLabel(app) {
  const n = (app && app.replicas) || 1;
  return n > 1 ? `${n} instances` : '';
}
