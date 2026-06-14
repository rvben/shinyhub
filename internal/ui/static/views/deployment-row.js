// Pure presentation logic for a row in the Deployments tab. Kept DOM-free so it
// can be unit-tested with node:test; the view (app-detail.js) turns the model
// into elements.

// relativeTime renders a compact "Xs/m/h/d ago" string. Mirrors the formatter
// in app.js so timestamps read consistently across the dashboard (Users, Audit,
// Deployments).
export function relativeTime(date, now = Date.now()) {
  if (!date) return '';
  const t = date instanceof Date ? date.getTime() : new Date(date).getTime();
  if (!Number.isFinite(t)) return '';
  const diff = Math.floor((now - t) / 1000);
  if (diff < 0)     return 'just now';
  if (diff < 60)    return `${diff}s ago`;
  if (diff < 3600)  return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

// deploymentRowModel turns one raw deployment record into the fields the row
// needs. `releaseLabel` is the human-friendly version ("v3") from the server's
// release_number (rank among succeeded deploys); it is empty for failed/pending
// rows, which carry a status badge instead and never get a release number. The
// epoch `version` is kept for the hover/title. `isCurrent` (set by
// deploymentListModels) flags the live bundle so the view can badge it and
// suppress its Roll back button. Roll back is offered only on a non-current
// *succeeded* deployment — you can't roll back to a failed or in-flight bundle.
export function deploymentRowModel(d, { isCurrent = false, now = Date.now() } = {}) {
  const version = String(d.version);
  const status = d.status || 'succeeded';
  const releaseNumber = typeof d.release_number === 'number' ? d.release_number : null;
  const created = d.created_at ? new Date(d.created_at) : null;
  const createdValid = created && Number.isFinite(created.getTime());
  return {
    id: d.id,
    version,
    status,
    releaseNumber,
    releaseLabel: releaseNumber != null ? `v${releaseNumber}` : '',
    failureReason: d.failure_reason || '',
    isCurrent,
    canRollback: status === 'succeeded' && !isCurrent,
    relWhen: createdValid ? relativeTime(created, now) : '',
    absWhen: createdValid ? created.toLocaleString() : '',
  };
}

// deploymentListModels maps a newest-first list of raw deployments to row models
// and marks the LIVE deployment. The live bundle is the newest *succeeded*
// deployment — a failed or pending newest attempt does NOT change what is running
// (ShinyHub auto-reverts a failed deploy), so position- or current_version-based
// marking would badge the wrong row. Rows arrive newest-first (id DESC), matching
// the server's release ranking.
export function deploymentListModels(rows, now = Date.now()) {
  const liveIdx = rows.findIndex(d => (d.status || 'succeeded') === 'succeeded');
  return rows.map((d, i) =>
    deploymentRowModel(d, { isCurrent: i === liveIdx, now }),
  );
}
