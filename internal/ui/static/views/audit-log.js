export function auditSelection(search = '') {
  const params = new URLSearchParams(search);
  const rawEvent = params.get('event') || '';
  return {
    event: /^\d+$/.test(rawEvent) && rawEvent !== '0' ? rawEvent : '',
    run: (params.get('run') || '').trim(),
    action: (params.get('action') || '').trim(),
  };
}

export function auditListPath(page, selection = {}) {
  const params = new URLSearchParams({
    limit: '100',
    offset: String(Math.max(0, page) * 100),
  });
  if (selection.event) params.set('event', selection.event);
  else if (selection.run) params.set('run', selection.run);
  else if (selection.action) params.set('action', selection.action);
  return `/api/audit?${params.toString()}`;
}

export function auditEmptyMessage(selection = {}) {
  if (selection.event) return `Audit event ${selection.event} is no longer available.`;
  if (selection.run) return 'No audit events were found for this run.';
  if (selection.action) return `No ${selection.action} audit events were found.`;
  return 'No audit events recorded yet — every mutating action will appear here.';
}

export function auditLoadError(selection = {}) {
  return selection.event || selection.run || selection.action
    ? 'Failed to load the selected audit context.'
    : 'Failed to load audit log.';
}

export function auditLoadingMessage(selection = {}) {
  if (selection.event) return `Loading audit event ${selection.event}…`;
  if (selection.run) return 'Loading events for this run…';
  if (selection.action) return `Loading ${selection.action} events…`;
  return 'Loading audit events…';
}

export function createLatestRequestGate() {
  let latest = 0;
  return {
    begin() { latest += 1; return latest; },
    isCurrent(id) { return id === latest; },
    invalidate() { latest += 1; },
  };
}

export function mountAuditLog(ctx, search = '') {
  const view = document.getElementById('audit-view');
  view.hidden = false;
  ctx.loadAuditEvents(0, auditSelection(search));
  ctx.updateActiveNav(location.pathname);
  return {
    title: 'Audit Log',
    unmount() { view.hidden = true; },
  };
}
