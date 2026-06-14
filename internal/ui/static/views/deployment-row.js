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
// needs. `deployNumber` is a human-friendly sequence position (newest highest);
// `isCurrent` (set by deploymentListModels) flags the live bundle so the view
// can badge it and suppress its Roll back button. Roll back is offered only on a
// non-current *succeeded* deployment — you can't roll back to a failed or
// in-flight bundle.
export function deploymentRowModel(d, { deployNumber, isCurrent = false, now = Date.now() } = {}) {
  const version = String(d.version);
  const status = d.status || 'succeeded';
  const created = d.created_at ? new Date(d.created_at) : null;
  const createdValid = created && Number.isFinite(created.getTime());
  return {
    id: d.id,
    version,
    status,
    failureReason: d.failure_reason || '',
    deployNumber: deployNumber != null ? `#${deployNumber}` : '',
    isCurrent,
    canRollback: status === 'succeeded' && !isCurrent,
    relWhen: createdValid ? relativeTime(created, now) : '',
    absWhen: createdValid ? created.toLocaleString() : '',
  };
}

// deploymentListModels maps a newest-first list of raw deployments to row models,
// assigning descending deploy numbers (newest = total) and marking the LIVE
// deployment. The live bundle is the newest *succeeded* deployment — a failed or
// pending newest attempt does NOT change what is running (ShinyHub auto-reverts a
// failed deploy), so position-based or current_version-based marking would badge
// the wrong row.
export function deploymentListModels(rows, now = Date.now()) {
  const total = rows.length;
  const liveIdx = rows.findIndex(d => (d.status || 'succeeded') === 'succeeded');
  return rows.map((d, i) =>
    deploymentRowModel(d, { deployNumber: total - i, isCurrent: i === liveIdx, now }),
  );
}
