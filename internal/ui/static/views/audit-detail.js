// Small, action-aware audit detail model. Audit detail is persisted JSON or
// a plain "key=value" string (see internal/api/*.go's logAuditEvent calls).
// It is server-authored, not user-controlled, but the renderer still turns
// every field into a plain string so app.js can place it with textContent
// rather than ever interpreting it as markup.

// Turns a detail object's key into a table-friendly label: snake_case and
// dotted keys become capitalized words ("old_role" -> "Old role").
function humanizeDetailKey(key) {
  const words = key.replace(/[._]+/g, ' ').trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function formatDetailValue(value) {
  if (value === null || value === undefined) return 'none';
  if (typeof value === 'object') return JSON.stringify(value);
  return String(value);
}

// Most update_app fields are recorded as {old, new} pairs rather than a bare
// new value (see patchAppAuditDetail in internal/api/apps.go), so a change
// reads as "before -> after" instead of only the after side.
function formatDetailField(value) {
  if (value && typeof value === 'object' && !Array.isArray(value) && ('old' in value || 'new' in value)) {
    return `${formatDetailValue(value.old)} → ${formatDetailValue(value.new)}`;
  }
  return formatDetailValue(value);
}

const KEYVALUE_PATTERN = /^(\w+=\S+)(\s+\w+=\S+)*$/;

export function auditDetailEntries(event) {
  if (!event || !event.detail) return [];
  const raw = event.detail;

  if (['schedule_activation_outcome', 'support_session.start', 'support_session.stop'].includes(event.action)) {
    return structuredAuditDetailEntries(event);
  }

  // Every other action stores detail either as a flat JSON object (the
  // common case: update_app, set_access, env.set, autoscale events, ...) or
  // as a plain "key=value key2=value2" string (grant_access, revoke_access,
  // set_member_role, grant_group_access, create_token, ...). Render both
  // generically so a new action needs no new case here.
  let parsed;
  try {
    parsed = typeof raw === 'string' ? JSON.parse(raw) : raw;
  } catch {
    parsed = null;
  }
  if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
    // detail_error (db.AuditDetail, internal/db/audit_detail.go) means the
    // server meant to record detail and failed to encode it. It replaces the
    // whole object rather than sitting alongside real fields, so nothing else
    // in the row can be trusted either; surface it as the one entry, flagged,
    // so the renderer can show it as a problem rather than ordinary data.
    if ('detail_error' in parsed) {
      return [{label: 'Detail unavailable', value: formatDetailValue(parsed.detail_error), isError: true}];
    }
    return Object.entries(parsed)
      .filter(([, value]) => value !== null && value !== undefined && !(typeof value === 'string' && value.trim() === ''))
      .map(([key, value]) => ({label: humanizeDetailKey(key), value: formatDetailField(value)}));
  }

  if (typeof raw === 'string' && raw.trim() !== '') {
    const trimmed = raw.trim();
    if (KEYVALUE_PATTERN.test(trimmed)) {
      return trimmed.split(/\s+/).map(pair => {
        const eq = pair.indexOf('=');
        return {label: humanizeDetailKey(pair.slice(0, eq)), value: pair.slice(eq + 1)};
      });
    }
    // Free-form text detail (e.g. "used=104857600 bytes, quota=500 MiB"):
    // show it verbatim rather than dropping it for not fitting a stricter
    // shape.
    return [{label: 'Detail', value: trimmed}];
  }

  return [];
}

// Small, action-aware audit detail model. Audit detail is persisted JSON, but
// the browser renders only explicitly allowed fields as text so malformed or
// unexpected payloads cannot become markup or an accidental secret dump.
function structuredAuditDetailEntries(event) {
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
