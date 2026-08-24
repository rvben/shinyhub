const MAX_ACTIVITY_GROUPS = 4;

const ACTION_LABELS = new Map([
  ['deploy', 'Deployed'],
  ['restart', 'Restarted'],
  ['rollback', 'Rolled back'],
  ['fleet_apply_started', 'Fleet apply'],
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
  ['schedule_delete', 'Schedule deleted'],
  ['deploy_rejected_quota', 'Deploy rejected'],
  ['autoscale_scale_up', 'Scaled up'],
  ['autoscale_scale_down', 'Scaled down'],
]);

export function buildActivityBrief(events, maxGroups = MAX_ACTIVITY_GROUPS) {
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
    const parent = bucket.events.find((event) => event.action === 'fleet_apply_started') || bucket.events[0];
    const children = bucket.runID && bucket.events.length > 1
      ? bucket.events.filter((event) => event !== parent).sort((a, b) => a.createdMs - b.createdMs)
      : [];
    return {
      key: bucket.key,
      kind: children.length > 0 ? 'group' : 'event',
      runID: bucket.runID,
      action: parent.action,
      actionLabel: actionLabel(parent.action),
      actorLabel: actorLabel(parent.username),
      resourceType: parent.resourceType,
      resourceTypeLabel: resourceTypeLabel(parent.resourceType),
      target: parent.resourceID || parent.resourceType,
      targetHref: resourceHref(parent.resourceType, parent.resourceID),
      tone: actionTone(parent.action),
      icon: actionIcon(parent.action, parent.resourceType),
      createdAt: parent.createdAt,
      createdMs: parent.createdMs,
      sortMs: Math.max(...bucket.events.map((event) => event.createdMs)),
      children: children.map((event) => ({
        ...event,
        actionLabel: actionLabel(event.action),
        actorLabel: actorLabel(event.username),
        resourceTypeLabel: resourceTypeLabel(event.resourceType),
        target: event.resourceID || event.resourceType,
        targetHref: resourceHref(event.resourceType, event.resourceID),
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
  if (/rejected|quota/.test(value)) return { name: 'attention', label: 'Attention' };
  if (/failed|failure|crash|error/.test(value)) return { name: 'critical', label: 'Failed' };
  if (/started|starting|waking/.test(value)) return { name: 'working', label: 'Started' };
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

function resourceHref(resourceType, resourceID) {
  if (resourceType !== 'app' || !resourceID) return '';
  return `/apps/${encodeURIComponent(resourceID)}/overview`;
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
