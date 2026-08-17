// Distils the app-list payload and live metrics into the few operational facts
// worth scanning on every card. Resource telemetry belongs on the detail page;
// the index answers release recency, readiness, and exceptional state.

function compactRelativeTime(value, now) {
  const timestamp = new Date(value).getTime();
  if (!Number.isFinite(timestamp)) return '';

  const seconds = Math.max(0, Math.floor((now - timestamp) / 1000));
  if (seconds < 60) return 'just now';
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} h ago`;
  return `${Math.floor(seconds / 86400)} d ago`;
}

function fact(text, tone = '', title = '') {
  return { text, tone, title };
}

export function appCardFacts(app, live = null, now = Date.now()) {
  const releaseNumber = Number(app.release_number) || 0;
  // last_deployed_at dates the newest attempt, including a failure. It is a
  // safe legacy fallback only when that newest attempt succeeded; otherwise it
  // would falsely label a failed-first app as deployed.
  const releasedAt = app.released_at
    || (app.last_deployment_status === 'succeeded' ? app.last_deployed_at : '')
    || '';
  const hasRelease = releaseNumber > 0
    || !!app.released_at
    || app.last_deployment_status === 'succeeded'
    || (Number(app.deploy_count) || 0) > 0;
  const latestFailed = app.last_deployment_status === 'failed';

  if (!hasRelease) {
    return [latestFailed
      ? fact('Latest deployment failed', 'danger')
      : fact('No release deployed', 'attention')];
  }

  const facts = [];
  if (latestFailed) facts.push(fact('Latest deployment failed', 'danger'));
  if (releaseNumber > 0) facts.push(fact(`Release #${releaseNumber}`));

  const relative = compactRelativeTime(releasedAt, now);
  if (relative) {
    const exact = new Date(releasedAt).toLocaleString();
    facts.push(fact(`Deployed ${relative}`, '', exact));
  }

  const status = (live && live.status) || app.status || '';
  const configured = Math.max(1, Number(app.replicas) || 1);
  const replicas = live && Array.isArray(live.replicas) ? live.replicas : null;
  if (app.autoscale_enabled && Number(app.autoscale_min_replicas) === 0
      && (status === 'hibernated' || status === 'idle')) {
    facts.push(fact('Scales to zero'));
  } else if (app.managed_by) {
    facts.push(fact('Fleet managed', '', `Managed by ${app.managed_by}`));
  } else if (replicas && (status === 'running' || status === 'degraded')) {
    const ready = replicas.filter(replica => replica.status === 'running').length;
    if (configured > 1 || ready !== configured) facts.push(fact(`${ready}/${configured} ready`));
  } else if (configured > 1) {
    facts.push(fact(`${configured} instances`));
  }

  return facts.slice(0, 3);
}
