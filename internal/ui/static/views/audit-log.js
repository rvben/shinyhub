// Every action the server can record in an audit event. This is the single
// source of truth for both the Action column's badge styling (app.js) and the
// Audit Log page's action filter dropdown, so the two cannot drift apart: a
// new action added to one list would otherwise render as an unstyled default
// badge, or be unfilterable, without any test catching the gap.
//
// The list is pinned against the server's own emitters by
// TestAuditActionListCoversEveryServerAction (internal/ui/contract_test.go).
// An action the server records but this list omits is the defect that test
// exists to catch: it is silently absent from the filter dropdown, so an
// operator filtering the log can never select it and reads its absence as
// "that never happened".
export const AUDIT_ACTIONS = [
  // Deployment actions (green)
  'draft_create', 'draft_preview_create', 'draft_promote', 'draft_delete',
  'deploy', 'restart', 'rollback', 'fleet_apply_started', 'fleet_apply_finished',
  // Auth actions
  'login', 'login_failed', 'logout', 'logout_handoff', 'connect_cli',
  'change_own_password', 'update_profile',
  // App lifecycle (blue - config)
  'create_app', 'update_app', 'delete_app', 'stop', 'sleep', 'set_access',
  'app_crashed', 'transfer_ownership',
  'app.icon.set', 'app.icon.clear', 'app.icon.emoji',
  // Projects (blue - config)
  'project.create', 'project.update', 'project.delete',
  // User management (blue - config)
  'create_user', 'update_user', 'delete_user', 'reset_user_password', 'revoke_sessions',
  'support_session.start', 'support_session.stop',
  // Token and service-credential management (amber - security)
  'create_token', 'delete_token', 'trusted_publish',
  'create_service_credential', 'delete_service_credential',
  // Environment (blue - config)
  'env.set', 'env.delete',
  // Data (blue - config)
  'data.push', 'data.pull', 'data.delete',
  // Schedules (blue - config)
  'schedule_refresh_stale', 'schedule_create', 'schedule_update', 'schedule_delete', 'schedule_run_manual',
  'schedule_run_succeeded', 'schedule_run_failed', 'schedule_run_timed_out',
  'schedule_run_cancelled', 'schedule_run_interrupted',
  'schedule_activation_roll', 'schedule_activation_outcome',
  'schedule_activation_restart', 'schedule_activation_cancel',
  'schedule_convergence_retry',
  // Access management (amber - security)
  'grant_access', 'revoke_access', 'set_member_role',
  // Group-access management (amber - security)
  'grant_group_access', 'revoke_group_access', 'reconcile_group_access',
  // Shared data (blue - config)
  'shared_data_grant', 'shared_data_revoke',
  // Capacity events (blue - config)
  'autoscale_scale_up', 'autoscale_scale_down', 'scale_up',
  'warm_expand', 'warm_shrink', 'revoke_worker',
  // Development sessions (blue - config)
  'end_development_session', 'development_app_expired',
  // Deploy quota rejection (red)
  'deploy_rejected_quota',
];

// The date range the API accepts, and the value an <input type="date"> emits.
// Anything else is dropped rather than sent, so a hand-edited URL cannot make
// the page ask for a range the server will reject.
const AUDIT_DATE = /^\d{4}-\d{2}-\d{2}$/;

function auditDate(raw) {
  const value = (raw || '').trim();
  return AUDIT_DATE.test(value) ? value : '';
}

export function auditSelection(search = '') {
  const params = new URLSearchParams(search);
  const rawEvent = params.get('event') || '';
  const since = auditDate(params.get('since'));
  const until = auditDate(params.get('until'));
  // An inverted range matches nothing and the server rejects it outright, so
  // treat it as no range at all rather than showing the operator an error for
  // a link they were given.
  const inverted = since && until && until < since;
  return {
    event: /^\d+$/.test(rawEvent) && rawEvent !== '0' ? rawEvent : '',
    run: (params.get('run') || '').trim(),
    action: (params.get('action') || '').trim(),
    since: inverted ? '' : since,
    until: inverted ? '' : until,
  };
}

export function auditListPath(page, selection = {}) {
  const params = new URLSearchParams({
    limit: '100',
    offset: String(Math.max(0, page) * 100),
  });
  // event and run are deep links into one specific context, so they replace
  // the browsing filters rather than narrowing them further.
  if (selection.event) params.set('event', selection.event);
  else if (selection.run) params.set('run', selection.run);
  else {
    if (selection.action) params.set('action', selection.action);
    if (auditDate(selection.since)) params.set('since', selection.since);
    if (auditDate(selection.until)) params.set('until', selection.until);
  }
  return `/api/audit?${params.toString()}`;
}

// auditRangeSuffix describes the active date range in prose, so an empty
// listing says which window was searched instead of implying the log is empty.
export function auditRangeSuffix(selection = {}) {
  const since = auditDate(selection.since);
  const until = auditDate(selection.until);
  if (since && until) return ` between ${since} and ${until}`;
  if (since) return ` on or after ${since}`;
  if (until) return ` on or before ${until}`;
  return '';
}

export function auditEmptyMessage(selection = {}) {
  if (selection.event) return `Audit event ${selection.event} is no longer available.`;
  if (selection.run) return 'No audit events were found for this run.';
  const range = auditRangeSuffix(selection);
  if (selection.action) return `No ${selection.action} audit events were found${range}.`;
  if (range) return `No audit events were recorded${range}.`;
  return 'No audit events recorded yet. Every mutating action will appear here.';
}

export function auditLoadError(selection = {}) {
  return selection.event || selection.run || selection.action
    || auditDate(selection.since) || auditDate(selection.until)
    ? 'Failed to load the selected audit context.'
    : 'Failed to load audit log.';
}

export function auditLoadingMessage(selection = {}) {
  if (selection.event) return `Loading audit event ${selection.event}…`;
  if (selection.run) return 'Loading events for this run…';
  const range = auditRangeSuffix(selection);
  if (selection.action) return `Loading ${selection.action} events${range}…`;
  if (range) return `Loading audit events${range}…`;
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
