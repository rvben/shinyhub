// Small, action-aware audit detail model. Audit detail is persisted JSON, but
// the browser renders only explicitly allowed fields as text so malformed or
// unexpected payloads cannot become markup or an accidental secret dump.
export function auditDetailEntries(event) {
	if (!event || event.action !== 'schedule_activation_outcome') return [];
	let detail;
	try {
		detail = typeof event.detail === 'string' ? JSON.parse(event.detail) : event.detail;
	} catch {
		return [];
	}
	if (!detail || typeof detail !== 'object' || Array.isArray(detail)) return [];
	const fields = [
		['Activation', detail.activation_id],
		['Schedule', detail.schedule_name],
		['Source run', detail.schedule_run_id],
		['Generation', detail.target_generation],
		['Outcome', detail.status],
		['Last phase', detail.phase],
		['Error', detail.error],
	];
	return fields
		.filter(([, value]) => value !== null && value !== undefined && String(value).trim() !== '')
		.map(([label, value]) => ({label, value: String(value)}));
}
