const MAX_ACTIVITY_GROUPS = 4;

const ACTION_LABELS = new Map([
  ['deploy', 'Deployed'],
  ['restart', 'Restarted'],
  ['rollback', 'Rolled back'],
  ['fleet_apply_started', 'Fleet apply'],
  ['fleet_apply_finished', 'Fleet apply'],
  ['create_app', 'Created'],
  ['update_app', 'Updated'],
  ['delete_app', 'Deleted'],
  ['stop', 'Stopped'],
  ['sleep', 'Hibernated'],
  ['set_access', 'Access changed'],
  ['env.set', 'Environment updated'],
  ['env.delete', 'Environment updated'],
  ['data.push', 'Data updated'],
  ['data.delete', 'Data updated'],
  ['schedule_create', 'Schedule created'],
  ['schedule_update', 'Schedule updated'],
  ['schedule_delete', 'Schedule deleted'],
  ['schedule_run_manual', 'Schedule started'],
  ['schedule_run_succeeded', 'Schedule succeeded'],
  ['schedule_run_failed', 'Schedule failed'],
  ['schedule_run_timed_out', 'Schedule timed out'],
  ['schedule_run_cancelled', 'Schedule cancelled'],
  ['schedule_run_interrupted', 'Schedule interrupted'],
  ['schedule_activation_roll', 'Activation started'],
  ['schedule_activation_outcome', 'Activation completed'],
  ['deploy_rejected_quota', 'Deploy rejected'],
  ['autoscale_scale_up', 'Scaled up'],
  ['autoscale_scale_down', 'Scaled down'],
]);

export function buildActivityBrief(events, maxGroups = MAX_ACTIVITY_GROUPS, availableAppSlugs = null, sourceHasMore = false) {
  const normalized = Array.isArray(events)
    ? events.map(normalizeEvent).filter(Boolean)
    : [];
  const buckets = new Map();

  for (const event of normalized) {
    const key = event.runID ? `run:${event.runID}` : `event:${event.id}`;
    if (!buckets.has(key)) buckets.set(key, { key, runID: event.runID, events: [] });
    buckets.get(key).events.push(event);
  }

  const operations = [...buckets.values()].map((bucket) => {
    bucket.events.sort((a, b) => b.createdMs - a.createdMs);
    const start = bucket.events.find((event) => event.action === 'fleet_apply_started');
    const finish = bucket.events.find((event) => event.action === 'fleet_apply_finished');
    const lifecycle = new Set(['fleet_apply_started', 'fleet_apply_finished']);
    const baseParent = start || finish || bucket.events[0];
    const parent = bucket.runID && !start && !finish
      ? { ...baseParent, action: 'fleet_apply_started', resourceType: 'fleet', resourceID: '' }
      : baseParent;
    const children = bucket.runID
      ? bucket.events.filter((event) => !lifecycle.has(event.action))
        .sort((a, b) => a.createdMs - b.createdMs)
      : [];
    const isGroupedRun = !!bucket.runID && (children.length > 0 || !!start || !!finish);
    const hasDisclosure = children.length > 0;
    const childTones = children.map((event) => actionTone(event.action)).filter(Boolean);
    const groupTone = childTones.find((tone) => tone.name === 'critical')
      || childTones.find((tone) => tone.name === 'attention')
      || (finish ? { name: 'neutral', label: 'Completed' } : null);
    return {
      key: bucket.key,
      kind: hasDisclosure ? 'group' : 'event',
      runID: bucket.runID,
      action: parent.action,
      actionLabel: actionLabel(parent.action),
      actorLabel: actorLabel(parent.username),
      resourceType: parent.resourceType,
      resourceTypeLabel: resourceTypeLabel(parent.resourceType),
      target: parent.resourceID || parent.resourceType,
      targetHref: resourceHref(parent.resourceType, parent.resourceID, parent.action, availableAppSlugs),
      auditHref: auditHref(parent.id, bucket.runID),
      tone: isGroupedRun ? groupTone : actionTone(parent.action),
      icon: actionIcon(parent.action, parent.resourceType),
      createdAt: finish ? finish.createdAt : parent.createdAt,
      createdMs: finish ? finish.createdMs : parent.createdMs,
      sortMs: Math.max(...bucket.events.map((event) => event.createdMs)),
      truncated: !!sourceHasMore && !!bucket.runID && !start,
      children: children.map((event) => ({
        ...event,
        actionLabel: actionLabel(event.action),
        actorLabel: actorLabel(event.username),
        resourceTypeLabel: resourceTypeLabel(event.resourceType),
        target: event.resourceID || event.resourceType,
        targetHref: resourceHref(event.resourceType, event.resourceID, event.action, availableAppSlugs),
        auditHref: auditHref(event.id),
        tone: actionTone(event.action),
        icon: actionIcon(event.action, event.resourceType),
      })),
    };
  }).sort((a, b) => b.sortMs - a.sortMs).slice(0, Math.max(1, maxGroups));

  return {
    operations,
    sourceCount: normalized.length,
    grouped: operations.some((operation) => operation.kind === 'group'),
  };
}

export function actorLabel(username) {
  const actor = stringValue(username);
  if (!actor || actor === 'system') return 'System';
  if (actor === '__deploy__') return 'Deployment automation';
  return actor;
}

export function actionLabel(action) {
  const value = stringValue(action);
  if (!value) return 'Activity';
  if (ACTION_LABELS.has(value)) return ACTION_LABELS.get(value);
  const words = value.replace(/[._-]+/g, ' ');
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function activityTime(iso, nowMs = Date.now()) {
  const createdMs = Date.parse(iso);
  if (!Number.isFinite(createdMs)) {
    return { valid: false, datetime: '', exact: 'Unknown time', relative: '' };
  }
  const date = new Date(createdMs);
  const exact = new Intl.DateTimeFormat(undefined, {
    hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date);
  const title = new Intl.DateTimeFormat(undefined, {
    dateStyle: 'long', timeStyle: 'short',
  }).format(date);
  return {
    valid: true,
    datetime: date.toISOString(),
    exact,
    relative: relativeTime(createdMs, nowMs),
    title,
  };
}

function normalizeEvent(raw, index) {
  if (!raw || typeof raw !== 'object') return null;
  const createdAt = stringValue(raw.created_at);
  const parsed = Date.parse(createdAt);
  const createdMs = Number.isFinite(parsed) ? parsed : 0;
  const id = raw.id == null ? `${createdAt}:${stringValue(raw.action)}:${index}` : String(raw.id);
  return {
    id,
    action: stringValue(raw.action),
    username: stringValue(raw.username),
    resourceType: stringValue(raw.resource_type),
    resourceID: stringValue(raw.resource_id),
    runID: stringValue(raw.run_id),
    createdAt,
    createdMs,
  };
}

function actionTone(action) {
  const value = stringValue(action).toLowerCase();
  if (/succeeded/.test(value)) return { name: 'success', label: 'Succeeded' };
  if (/timed_out/.test(value)) return { name: 'critical', label: 'Timed out' };
  if (/cancelled/.test(value)) return { name: 'neutral', label: 'Cancelled' };
  if (/interrupted/.test(value)) return { name: 'attention', label: 'Interrupted' };
  if (/rejected|quota/.test(value)) return { name: 'attention', label: 'Attention' };
  if (/failed|failure|crash|error/.test(value)) return { name: 'critical', label: 'Failed' };
  if (value === 'fleet_apply_started') return null;
  if (/started|starting|waking|schedule_run_manual|schedule_activation_roll/.test(value)) {
    return { name: 'working', label: 'In progress' };
  }
  return null;
}

function actionIcon(action, resourceType) {
  const value = stringValue(action).toLowerCase();
  if (resourceType === 'fleet' || value.startsWith('fleet_')) return 'fleet';
  if (/failed|failure|crash|error|rejected|quota/.test(value)) return 'attention';
  if (value === 'restart') return 'restart';
  if (value === 'deploy' || value === 'rollback') return 'deploy';
  if (/login|logout|access|token/.test(value)) return 'security';
  return 'change';
}

function resourceTypeLabel(resourceType) {
  const value = stringValue(resourceType);
  if (!value) return '';
  return value.charAt(0).toUpperCase() + value.slice(1).replace(/[._-]+/g, ' ');
}

function resourceHref(resourceType, resourceID, action, availableAppSlugs) {
  if (resourceType !== 'app' || !resourceID || action === 'delete_app') return '';
  if (availableAppSlugs instanceof Set && !availableAppSlugs.has(resourceID)) return '';
  const value = stringValue(action).toLowerCase();
  let tab = 'overview';
  if (/^(deploy|rollback|deploy_rejected_quota)$/.test(value)) tab = 'deployments';
  else if (/^env\./.test(value) || value === 'update_app') tab = 'configuration';
  else if (/^(data\.|shared_data_)/.test(value)) tab = 'data';
  else if (/access|member|group/.test(value)) tab = 'access';
  else if (/^schedule_/.test(value)) tab = 'schedules';
  return `/apps/${encodeURIComponent(resourceID)}/${tab}`;
}

function auditHref(eventID, runID = '') {
  if (runID) return `/audit-log?run=${encodeURIComponent(runID)}`;
  if (eventID) return `/audit-log?event=${encodeURIComponent(eventID)}`;
  return '/audit-log';
}

function relativeTime(createdMs, nowMs) {
  const seconds = Math.max(0, Math.round((nowMs - createdMs) / 1000));
  if (seconds < 60) return 'just now';
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return `${days}d ago`;
}

function stringValue(value) {
  return typeof value === 'string' ? value.trim() : '';
}
