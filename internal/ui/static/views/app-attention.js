// Exception-first model + renderer for the operator Apps index. The rail is
// deliberately strict: it contains only conditions that require an operator's
// attention now, never routine stopped/hibernated state or a failed redeploy
// whose previous release is still serving successfully without a repair fence.
import { avatarView, renderAppAvatar } from './app-avatar.js';

function successfulRelease(app) {
  return (Number(app.release_number) || 0) > 0
    || (Number(app.deploy_count) || 0) > 0
    || !!app.released_at
    || app.last_deployment_status === 'succeeded';
}

function readiness(app, live) {
  if (!live || !Array.isArray(live.replicas)) return null;
  const configured = Math.max(1, Number(app.replicas) || 1);
  const ready = live.replicas.filter(replica => replica.status === 'running').length;
  return { ready, configured };
}

export function appAttention(app, live = null) {
  if (!app || app.deploying || (live && live.deploying)) return null;

  const status = (live && live.status) || app.status || '';
  const capacity = readiness(app, live);

  if (app.deployment_repair_required) {
    return {
      severity: status === 'running' ? 'warning' : 'danger',
      label: 'Deployment repair required',
      detail: status === 'running'
        ? 'The app is serving, but cannot recover after a restart. Successfully redeploy its bundle and required data producers; allow downtime when replacing the working version.'
        : 'Successfully redeploy the desired bundle and its required data producers to clear the deployment fence.',
    };
  }

  if (!successfulRelease(app) && app.last_deployment_status === 'failed') {
    return {
      severity: 'danger',
      label: 'Deployment failed',
      detail: 'No successful release is available. Review the failed deployment.',
    };
  }
  if (status === 'crashed') {
    return {
      severity: 'danger',
      label: 'Crashed',
      detail: 'The app is unavailable. Review its logs and restart when ready.',
    };
  }
  if (status === 'failed') {
    return {
      severity: 'danger',
      label: 'Unavailable',
      detail: 'A producer or deployment failed. Review the app details and repair the failure.',
    };
  }
  if (status === 'degraded') {
    const detail = capacity
      ? `${capacity.ready} of ${capacity.configured} replicas are ready.`
      : 'The app is serving with reduced capacity.';
    return {
      severity: capacity && capacity.ready === 0 ? 'danger' : 'warning',
      label: 'Degraded',
      detail,
    };
  }
  if (status === 'running' && capacity && capacity.configured > 1
      && capacity.ready < capacity.configured) {
    return {
      severity: capacity.ready === 0 ? 'danger' : 'warning',
      label: 'Replica capacity reduced',
      detail: `${capacity.ready} of ${capacity.configured} replicas are ready.`,
    };
  }
  return null;
}

export function attentionApps(apps, liveBySlug = new Map()) {
  const severityRank = { danger: 0, warning: 1 };
  return (apps || [])
    .map(app => ({ app, attention: appAttention(app, liveBySlug.get(app.slug)) }))
    .filter(item => item.attention)
    .sort((a, b) => (severityRank[a.attention.severity] - severityRank[b.attention.severity])
      || a.app.name.localeCompare(b.app.name));
}

export function renderAppAttention(doc, apps, liveBySlug = new Map()) {
  const items = attentionApps(apps, liveBySlug);
  if (items.length === 0) return null;

  const section = doc.createElement('details');
  section.className = 'app-attention';
  section.setAttribute('aria-labelledby', 'app-attention-heading');
  section.dataset.signature = JSON.stringify(items.map(({ app, attention }) => [
    app.slug, attention.severity, attention.label, attention.detail,
  ]));

  const header = doc.createElement('summary');
  header.className = 'app-attention-header';
  header.dataset.focusKey = 'attention:summary';
  const heading = doc.createElement('h2');
  heading.id = 'app-attention-heading';
  heading.textContent = `${items.length} ${items.length === 1 ? 'app needs' : 'apps need'} attention`;
  header.append(heading);

  const list = doc.createElement('div');
  list.className = 'app-attention-list';
  list.setAttribute('role', 'list');

  for (const { app, attention } of items) {
    const item = doc.createElement('article');
    item.className = `app-attention-item is-${attention.severity}`;
    item.dataset.slug = app.slug;
    item.setAttribute('role', 'listitem');

    const identity = doc.createElement('div');
    identity.className = 'app-attention-identity';
    identity.appendChild(renderAppAvatar(doc, avatarView(app), 'app-attention-avatar'));
    const identityText = doc.createElement('div');
    identityText.className = 'app-attention-identity-text';
    const name = doc.createElement('a');
    name.href = `/apps/${encodeURIComponent(app.slug)}`;
    name.setAttribute('data-nav', '');
    name.dataset.focusKey = `attention:${app.slug}:name`;
    name.className = 'app-attention-name';
    name.textContent = app.name;
    name.setAttribute('aria-label', `Inspect ${app.name}`);
    const meta = doc.createElement('p');
    meta.className = 'app-attention-meta';
    meta.textContent = `/${app.slug}${app.project_name ? ` · ${app.project_name}` : ''}`;
    identityText.append(name, meta);
    identity.appendChild(identityText);

    const problem = doc.createElement('div');
    problem.className = 'app-attention-problem';
    const label = doc.createElement('strong');
    label.textContent = attention.label;
    const detail = doc.createElement('p');
    detail.textContent = attention.detail;
    problem.append(label, detail);

    const action = doc.createElement('a');
    action.href = `/apps/${encodeURIComponent(app.slug)}`;
    action.setAttribute('data-nav', '');
    action.dataset.focusKey = `attention:${app.slug}:inspect`;
    action.className = 'app-attention-action';
    action.textContent = 'Inspect app';
    action.setAttribute('aria-label', `Inspect ${app.name} recovery options`);

    item.append(identity, problem, action);
    list.appendChild(item);
  }

  section.append(header, list);
  return section;
}
