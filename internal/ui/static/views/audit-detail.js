// Small, action-aware audit detail model. Audit detail is persisted JSON, but
// the browser renders only explicitly allowed fields as text so malformed or
// unexpected payloads cannot become markup or an accidental secret dump.
export function auditDetailEntries(event) {
	if (!event || !['schedule_activation_outcome', 'support_session.start', 'support_session.stop'].includes(event.action)) return [];
	let detail;
	try {
		detail = typeof event.detail === 'string' ? JSON.parse(event.detail) : event.detail;
	} catch {
		return [];
	}
	if (!detail || typeof detail !== 'object' || Array.isArray(detail)) return [];
	const support = event.action.startsWith('support_session.');
	const fields = support ? [
		['Administrator', detail.actor_username],
		['Represented user', detail.subject_username],
		['User ID', detail.subject_user_id],
		['App', detail.app_slug],
		['Reason', detail.reason],
		['Session ID', event.resource_id],
		[event.action === 'support_session.start' ? 'Started' : 'Recorded', auditTime(event.created_at)],
		['Deadline', auditTime(detail.expires_at)],
		['Ended', auditTime(detail.stopped_at)],
		['End cause', supportStopCause(detail.stop_reason)],
	] : [
		['Activation', detail.activation_id],
		['Schedule', detail.schedule_name],
		['Source run', detail.schedule_run_id],
		['Generation', detail.target_generation],
		['Outcome', detail.status],
		['Last phase', detail.phase],
		['Error', detail.error],
	];
	return fields
		.filter(([, value]) => ['string', 'number', 'boolean'].includes(typeof value) && String(value).trim() !== '')
		.map(([label, value]) => ({label, value: String(value)}));
}

function auditTime(value) {
  if (typeof value !== 'string' || !value) return '';
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'long' }).format(date);
}

function supportStopCause(value) {
  return ({ ended_by_actor: 'Ended in app', ended_from_dashboard: 'Ended from dashboard',
    expired: 'Expired', activation_abandoned: 'Launch not completed', launch_failed: 'Launch failed',
    activation_failed: 'Activation failed' })[value] || value;
}
