// Schedule list surface helpers. Pure functions + an HTML-string builder
// (the schedule table in app.js is rendered as template strings, not DOM
// nodes), kept here so the DST advisory wiring is unit-testable under jsdom.
//
// The server computes the DST fall-back double-fire advisory and returns it on
// the schedule DTO as `dst_advisory`. This module only decides how to surface
// that string in the list; it never recomputes the advisory.

// Short label shown on the inline warning badge. The full advisory rides in
// the title + aria-label so the cell stays compact.
export const DST_ADVISORY_LABEL = 'fires twice (DST)';

// Returns the trimmed advisory text when the schedule carries one, else null
// so callers branch on a single value.
export function dstAdvisoryText(schedule) {
  if (!schedule || typeof schedule.dst_advisory !== 'string') return null;
  const text = schedule.dst_advisory.trim();
  return text.length > 0 ? text : null;
}

const ESCAPE = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, (c) => ESCAPE[c]);
}

// Returns an inline warning badge as an HTML string for embedding in the cron
// cell, or "" when the schedule has no advisory. The full advisory is escaped
// into both the title (hover) and aria-label (screen readers).
export function dstAdvisoryMarkup(schedule) {
  const text = dstAdvisoryText(schedule);
  if (text === null) return '';
  const safe = escapeHtml(text);
  return `<span class="dst-advisory" title="${safe}" aria-label="${safe}"><svg aria-hidden="true" width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M8 1.5 15 14H1L8 1.5Z"/><path d="M8 5.2v4.2"/><path d="M8 12h.01"/></svg>${DST_ADVISORY_LABEL}</span>`;
}

// scheduleState translates scheduler/storage states into the small vocabulary
// operators need on the list surface. It intentionally describes the job, not
// whether a running app has consumed data the job may have written.
export function scheduleState(schedule) {
  if (!schedule || schedule.enabled === false) {
    return { key: 'paused', label: 'Paused', tone: 'quiet', attention: false };
  }
  if (schedule.refreshing || schedule.active_run_id != null) {
    return { key: 'running', label: 'Running', tone: 'active', attention: false };
  }
  if (schedule.freshness_error) {
    return { key: 'unknown', label: 'Unknown', tone: 'warning', attention: true };
  }
  if (schedule.stale === true) {
    return { key: 'stale', label: 'Stale', tone: 'danger', attention: true };
  }

  switch (schedule.last_run_status) {
    case 'succeeded':
      return { key: 'succeeded', label: 'Succeeded', tone: 'success', attention: false };
    case 'failed':
    case 'timed_out':
    case 'interrupted':
      return { key: schedule.last_run_status, label: schedule.last_run_status === 'timed_out' ? 'Timed out' : schedule.last_run_status === 'interrupted' ? 'Interrupted' : 'Failed', tone: 'danger', attention: true };
    case 'cancelled':
    case 'canceled':
      return { key: 'cancelled', label: 'Cancelled', tone: 'quiet', attention: false };
    case 'skipped':
    case 'skipped_overlap':
      return { key: 'skipped', label: 'Skipped', tone: 'quiet', attention: false };
    case 'queued':
      return { key: 'queued', label: 'Queued', tone: 'active', attention: false };
    default:
      return { key: 'never', label: 'Never run', tone: 'quiet', attention: false };
  }
}

export function runTriggerLabel(trigger) {
  switch (trigger) {
    case 'manual': return 'Manual';
    case 'register': return 'On registration';
    case 'missed': return 'Missed run';
    case 'schedule':
    case 'cron': return 'Scheduled';
    default: return 'Unknown';
  }
}

export function deployTriggerLabel(trigger) {
  switch (trigger) {
    case 'first_deploy': return 'On first deployment';
    case 'bundle_change': return 'On every bundle change';
    case 'never':
    case undefined:
    case null: return 'Only on its cron';
    default: return 'Unsupported policy';
  }
}

// Producer compatibility is deliberately separate from cron/job freshness.
// A successful run can still have produced data for an older bundle.
export function producerCompatibilityState(schedule) {
  if (!schedule || schedule.deploy_trigger === 'never' || !schedule.deploy_trigger) {
    return {
      key: 'not-tracked', label: 'Not tracked', tone: 'quiet', attention: false,
      detail: 'This schedule does not promise that its data matches the deployed bundle.',
    };
  }
  if (schedule.producer_repair_required) {
    return {
      key: 'repair', label: 'Repair required', tone: 'danger', attention: true,
      detail: schedule.convergence_error || 'The last producer write is uncertain and must succeed again before compatibility can be proven.',
    };
  }
  if (schedule.deploy_trigger_satisfied === true) {
    return {
      key: 'ready', label: 'Matches current code', tone: 'success', attention: false,
      detail: 'The current data producer and deployed bundle are proven compatible.',
    };
  }
  if (['pending', 'dispatching', 'running'].includes(schedule.convergence_status)) {
    return {
      key: 'building', label: 'Building for current code', tone: 'active', attention: false,
      detail: 'A deploy-triggered producer run is converging data for the current bundle.',
    };
  }
  return {
    key: 'needs-run', label: 'Does not match current code', tone: 'danger', attention: true,
    detail: schedule.convergence_error || 'The deployed bundle has no proven successful producer result yet.',
  };
}

// Only fields that can change the selected definition, status, edit payload,
// or run history belong in this signature. In particular,
// last_success_age_s advances on every poll and must not discard pagination.
export function scheduleDetailSignature(schedule) {
  if (!schedule) return '';
  return JSON.stringify({
    id: schedule.id,
    app_id: schedule.app_id,
    name: schedule.name,
    cron_expr: schedule.cron_expr,
    command: schedule.command,
    enabled: schedule.enabled,
    timeout_seconds: schedule.timeout_seconds,
    overlap_policy: schedule.overlap_policy,
    missed_policy: schedule.missed_policy,
    timezone: schedule.timezone,
    effective_timezone: schedule.effective_timezone,
    timezone_inherited: schedule.timezone_inherited,
    next_fire: schedule.next_fire,
    dst_advisory: schedule.dst_advisory,
    last_run_id: schedule.last_run_id,
    last_run_at: schedule.last_run_at,
    last_run_status: schedule.last_run_status,
    last_success_at: schedule.last_success_at,
    stale: schedule.stale,
    refreshing: schedule.refreshing,
    active_run_id: schedule.active_run_id,
    freshness_error: schedule.freshness_error,
    on_success: schedule.on_success,
    deploy_trigger: schedule.deploy_trigger,
    deploy_trigger_satisfied: schedule.deploy_trigger_satisfied,
    producer_repair_required: schedule.producer_repair_required,
    current_deployment_id: schedule.current_deployment_id,
    current_app_version: schedule.current_app_version,
    current_content_digest: schedule.current_content_digest,
    producer_deployment_id: schedule.producer_deployment_id,
    producer_app_version: schedule.producer_app_version,
    producer_content_digest: schedule.producer_content_digest,
    producer_fingerprint: schedule.producer_fingerprint,
    producer_published_at: schedule.producer_published_at,
    convergence_obligation_id: schedule.convergence_obligation_id,
    convergence_status: schedule.convergence_status,
    convergence_run_id: schedule.convergence_run_id,
    convergence_error: schedule.convergence_error,
    min_roll_interval_seconds: schedule.min_roll_interval_seconds,
    roll_fallback: schedule.roll_fallback,
    max_defer_age_seconds: schedule.max_defer_age_seconds,
    roll_feasibility_advisory: schedule.roll_feasibility_advisory,
    latest_activation: schedule.latest_activation,
  });
}

export function formatAge(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return '';
  if (seconds < 60) return 'just now';
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

export function afterSuccessLabel(schedule) {
  switch (schedule && schedule.on_success) {
    case 'roll': return 'Roll app';
    case 'none':
    case undefined:
    case null: return 'No future app action';
    default: return 'Unsupported action';
  }
}

function intervalLabel(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '';
  if (seconds % 86400 === 0) return `${seconds / 86400}d`;
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

export function afterSuccessDetail(schedule) {
  if (!schedule || schedule.on_success !== 'roll') {
    return 'A successful job records its result but leaves serving app processes unchanged.';
  }
  const interval = intervalLabel(schedule.min_roll_interval_seconds);
  const damper = interval ? `, at most once every ${interval}` : '';
  const fallback = schedule.roll_fallback === 'restart'
    ? ' If the surge does not fit, it restarts the app in place.'
    : ' If the surge does not fit, it waits for capacity.';
  const maxAge = intervalLabel(schedule.max_defer_age_seconds);
  const expiry = maxAge ? ` Deferred work fails after ${maxAge}.` : '';
  return `Gracefully replaces this app’s replicas after success${damper}. The roll preserves the warm floor and may use one temporary surge replica.${fallback}${expiry}`;
}

export function activationErrorDetail(run) {
  if (!run || typeof run.activation_error !== 'string') return '';
  return run.activation_error.trim();
}

function activationPhaseLabel(phase) {
  const phases = {
    starting_surge: 'starting surge',
    surge_ready: 'surge ready',
    draining_slot: 'draining replica',
    starting_slot: 'starting replica',
    invalidating_warm: 'refreshing warm pool',
    retiring_surge: 'retiring surge',
    stopping_pool: 'stopping pool',
    starting_pool: 'starting pool',
    recovering: 'recovering',
  };
  return phases[phase] || '';
}

export function activationState(schedule) {
	const activation = schedule?.latest_activation;
	// Activation policy is snapshotted when a run starts. A later edit controls
	// future runs but must never hide durable work that is still queued or
	// repairing under the earlier policy.
	if (!activation && (!schedule || schedule.on_success !== 'roll')) {
		return { key: 'none', label: 'Not configured', tone: 'quiet', active: false, attention: false };
	}
  if (!activation) {
    return { key: 'never', label: 'Never activated', tone: 'quiet', active: false, attention: false };
  }
  switch (activation.status) {
    case 'pending': return { key: 'pending', label: 'Queued', tone: 'active', active: true, attention: false };
    case 'deferred_interval': return { key: 'deferred_interval', label: 'Waiting for interval', tone: 'warning', active: true, attention: false };
	case 'deferred_capacity': return { key: 'deferred_capacity', label: 'Waiting for capacity', tone: 'warning', active: true, attention: true };
    case 'repairing': {
      const suffix = activationPhaseLabel(activation.phase);
      return { key: 'repairing', label: suffix ? `Repairing · ${suffix}` : 'Repairing rollout', tone: 'warning', active: true, attention: true };
    }
    case 'running': {
      const suffix = activationPhaseLabel(activation.phase);
      return { key: 'running', label: suffix ? `Rolling · ${suffix}` : 'Rolling', tone: 'active', active: true, attention: false };
    }
    case 'succeeded': return { key: 'succeeded', label: 'Activated', tone: 'success', active: false, attention: false };
    case 'superseded': return { key: 'superseded', label: 'Superseded', tone: 'quiet', active: false, attention: false };
    case 'not_needed': return { key: 'not_needed', label: 'No live roll needed', tone: 'success', active: false, attention: false };
    case 'target_deleted': return { key: 'target_deleted', label: 'App deleted', tone: 'quiet', active: false, attention: false };
    case 'blocked_unsupported': return { key: 'blocked_unsupported', label: 'Unsupported topology', tone: 'danger', active: false, attention: true };
    case 'cancelled': return { key: 'cancelled', label: 'Activation cancelled', tone: 'warning', active: false, attention: true };
    case 'failed': return { key: 'failed', label: 'Activation failed', tone: 'danger', active: false, attention: true };
    default: return { key: 'unknown', label: 'Unknown activation', tone: 'warning', active: false, attention: true };
  }
}

export function canCancelActivation(schedule) {
  return ['pending', 'deferred_interval', 'deferred_capacity']
    .includes(schedule?.latest_activation?.status);
}

// Returns activation snapshots whose source schedule is no longer configured.
// The API deliberately retains these rows after schedule deletion so operators
// do not lose the causal record of a failed or completed rollout.
export function deletedScheduleActivations(activations, schedules) {
  const configured = new Set((Array.isArray(schedules) ? schedules : [])
    .map(schedule => schedule?.id)
    .filter(id => id !== null && id !== undefined)
    .map(String));
  return (Array.isArray(activations) ? activations : [])
    .filter(activation => activation && typeof activation === 'object')
    .filter(activation => activation.schedule_id == null || !configured.has(String(activation.schedule_id)));
}

export function scheduleSummary(schedules) {
  const rows = Array.isArray(schedules) ? schedules : [];
  let enabled = 0;
  let running = 0;
  let attention = 0;
  let compatible = 0;
  let activating = 0;
  let nextFire = null;

  for (const schedule of rows) {
    const state = scheduleState(schedule);
    const activation = activationState(schedule);
    const compatibility = producerCompatibilityState(schedule);
    if (schedule.enabled !== false) enabled += 1;
    if (state.key === 'running') running += 1;
    if (activation.active) activating += 1;
    if (compatibility.key === 'ready') compatible += 1;
    if (state.attention || activation.attention || compatibility.attention) attention += 1;
    if (schedule.enabled !== false && schedule.next_fire) {
      const value = new Date(schedule.next_fire);
      if (!Number.isNaN(value.getTime()) && (nextFire === null || value < nextFire)) nextFire = value;
    }
  }
  return { total: rows.length, enabled, running, activating, compatible, attention, nextFire };
}

export function runDurationSeconds(run) {
  if (!run || !run.started_at || !run.finished_at) return null;
  const started = new Date(run.started_at).getTime();
  const finished = new Date(run.finished_at).getTime();
  if (!Number.isFinite(started) || !Number.isFinite(finished) || finished < started) return null;
  return Math.round((finished - started) / 1000);
}

export function formatDuration(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return '—';
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  if (minutes < 60) return remainder ? `${minutes}m ${remainder}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const minuteRemainder = minutes % 60;
  return minuteRemainder ? `${hours}h ${minuteRemainder}m` : `${hours}h`;
}
