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
  const source = provenanceModel(d.provenance);
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
    source,
    restoredFromReleaseNumber: typeof d.restored_from_release_number === 'number' ? d.restored_from_release_number : null,
  };
}

export function provenanceModel(raw) {
  const origin = raw && raw.origin ? raw.origin : {};
  const originKind = origin.kind || (raw && raw.run_id ? 'fleet' : 'legacy');
  if (originKind === 'legacy') {
    return {
      available: false,
      label: 'Source not recorded',
      detail: 'No deployment source was captured',
      url: '',
      change: null,
      provider: '',
      mark: '',
      markIcon: '',
      headerText: '',
      headerDetail: '',
    };
  }
  if (originKind === 'direct' || originKind === 'rollback') {
    const channel = origin.channel || 'api';
    const actor = origin.actor ? String(origin.actor) : '';
    const channelLabels = {
      dashboard: 'Dashboard',
      cli: 'ShinyHub CLI',
      api: 'API',
    };
    const channelLabel = channelLabels[channel] || 'API';
    const isRollback = originKind === 'rollback';
    let label;
    let headerLead;
    let mark;
    let markIcon = '';
    if (isRollback) {
      label = 'Rollback';
      headerLead = 'Rolled back';
      mark = '';
      markIcon = 'rollback';
    } else if (channel === 'dashboard') {
      label = 'Manual deployment';
      headerLead = 'Deployed manually';
      mark = '';
      markIcon = 'manual';
    } else if (channel === 'cli') {
      label = 'CLI deployment';
      headerLead = 'Deployed via ShinyHub CLI';
      mark = 'CLI';
    } else {
      label = 'Direct API deployment';
      headerLead = 'Deployed via API';
      mark = 'API';
    }
    return {
      available: true,
      label,
      detail: actor ? `${actor} · ${channelLabel}` : channelLabel,
      url: '',
      revisionURL: '',
      change: null,
      provider: originKind,
      mark,
      markIcon,
      headerText: actor ? `${headerLead} by ${actor}` : headerLead,
      headerDetail: channelLabel,
    };
  }
  const metadata = raw.metadata || {};
  const revision = metadata.revision || {};
  const source = metadata.source || {};
  const change = metadata.change || {};
  const job = metadata.job || {};
  const shortSHA = revision.sha ? String(revision.sha).slice(0, 8) : '';
  const details = [];
  if (shortSHA) details.push(shortSHA);
  if (revision.ref) details.push(String(revision.ref));
  if (job.label) details.push(String(job.label));
  return {
    available: true,
    label: source.label || 'ShinyHub fleet apply',
    detail: details.join(' · ') || `fleet ${raw.fleet_id || 'run'}`,
    url: source.url || '',
    revisionURL: revision.url || '',
    change: change.label ? { label: String(change.label), url: change.url || '' } : null,
    provider: metadata.provider || '',
    mark: metadata.provider === 'gitlab' ? 'GL' : 'CI',
    markIcon: '',
    headerText: '',
    headerDetail: details.join(' · ') || `fleet ${raw.fleet_id || 'run'}`,
    runID: raw.run_id,
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
