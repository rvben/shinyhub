// status-label.js — the single source of truth for turning a lowercase wire
// status (`running`, `hibernated`, …) into the human label shown on app cards,
// the detail-header pill, sidebar rows, and per-replica badges. Kept DOM-free so
// it is unit-testable and so app.js and app-detail.js share ONE definition
// (they each previously carried a private copy that could silently drift).
//
// Most statuses read as the plain ops word an operator already knows. Two are
// deliberately re-voiced from their internal names so the label tells the truth
// from the user's side of the screen:
//
//   hibernated → "Sleeping"  An idle app stopped to free resources, woken on the
//                            next request. Pairs with the "Waking" resume and the
//                            indigo standby color; "Hibernated" implied a deep,
//                            slow freeze that fights the near-instant warm wake.
//   suspended  → "Paused"    A frozen-but-resumable replica. "Suspended" is the
//                            system's word; "Paused" is the operator's.
//
// Everything else keeps its standard name (Running, Deploying, Degraded, …) —
// excellent copy leaves clear, recognized vocabulary alone.
const STATUS_LABELS = {
  running:    'Running',
  healthy:    'Healthy',
  deploying:  'Deploying',
  waking:     'Waking',
  degraded:   'Degraded',
  crashed:    'Crashed',
  hibernated: 'Sleeping',
  stopped:    'Stopped',
  suspended:  'Paused',
  unknown:    'Unknown',
};

// formatStatus returns the display label for a wire status. An unmapped status
// falls back to sentence case so a newly added backend state still reads
// reasonably until it earns an explicit, considered label here.
export function formatStatus(status) {
  if (!status) return '';
  return STATUS_LABELS[status] || (status.charAt(0).toUpperCase() + status.slice(1));
}
