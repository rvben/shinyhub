import { deploymentRowModel, deploymentTimeModel } from './deployment-row.js';

// This is a summary of recent deployments and each schedule's latest run,
// rather than an audit trail. A panel owns every request and its last good data.
export function overviewActivityRows(deployments, schedules, slug) {
  const base = `/apps/${encodeURIComponent(slug)}`;
  const rows = deployments.filter(item => item?.id != null).map(item => {
    const model = deploymentRowModel({...item, status: item.status || 'unknown'});
    return {key: `deployment-${item.id}`, title: model.releaseLabel ? `Release ${model.releaseLabel}` : 'Deployment attempt',
      detail: model.failureReason || (model.source.available ? model.source.label : 'Deployment'),
      status: model.status, time: model.time, href: `${base}/deployments`};
  });
  for (const schedule of schedules) {
    if (schedule.id == null || schedule.retired || !schedule.last_run_status || !schedule.last_run_at) continue;
    rows.push({key: `schedule-${schedule.id}`, title: schedule.name || 'Scheduled job', detail: 'Latest scheduled run',
      status: schedule.last_run_status, time: deploymentTimeModel(schedule.last_run_at),
      href: `${base}/schedules/${encodeURIComponent(schedule.id)}`});
  }
  return rows.sort((a, b) => (Date.parse(b.time?.datetime) || 0) - (Date.parse(a.time?.datetime) || 0)).slice(0, 4);
}

const labels = {succeeded: 'Succeeded', failed: 'Failed', running: 'Running', pending: 'Pending', starting: 'Starting',
  cancelled: 'Cancelled', interrupted: 'Interrupted', timed_out: 'Timed out'};
const tone = status => ['failed', 'timed_out'].includes(status) ? 'danger'
  : status === 'succeeded' ? 'success' : ['running', 'pending', 'starting'].includes(status) ? 'progress' : 'neutral';

export function createOverviewActivity({panel, app, api, canReadSchedules = false, onUnauthorized = () => {}}) {
  const doc = panel.ownerDocument, win = doc.defaultView;
  const base = `/api/apps/${encodeURIComponent(app.slug)}`;
  let alive = true, revision = 0, pending;
  const sources = [{key: 'deployments', label: 'deployments', path: `${base}/deployments?limit=4`, items: null, error: ''}];
  if (canReadSchedules) sources.push({key: 'schedules', label: 'scheduled runs', path: `${base}/schedules`, items: null, error: ''});
  panel.innerHTML = `<div class="overview-card-heading"><div><h2>Recent activity</h2><p class="overview-card-description">${canReadSchedules ? 'Deployments and latest scheduled runs.' : 'Recent deployment results.'}</p></div></div>
    <p class="app-activity-status" role="status">Loading activity…</p><ol class="app-activity-list" aria-label="Recent app activity"></ol>
    <div class="app-activity-footer"><a data-nav>All deployments</a>${canReadSchedules ? '<a data-nav>All schedules</a>' : ''}<button type="button" class="env-btn-secondary" hidden>Retry activity</button></div>`;
  const status = panel.querySelector('[role="status"]'), list = panel.querySelector('ol'), retry = panel.querySelector('button');
  const links = panel.querySelectorAll('a');
  links[0].href = `/apps/${encodeURIComponent(app.slug)}/deployments`;
  if (links[1]) links[1].href = `/apps/${encodeURIComponent(app.slug)}/schedules`;
  function render(manual) {
    const failures = sources.filter(source => source.error);
    const rows = overviewActivityRows(sources[0].items || [], sources[1]?.items || [], app.slug);
    const focused = doc.activeElement?.closest('[data-activity-key]')?.dataset.activityKey;
    list.replaceChildren();
    for (const row of rows) {
      const item = doc.createElement('li'); item.dataset.activityKey = row.key;
      const summary = doc.createElement('div'); summary.className = 'app-activity-summary';
      const link = doc.createElement('a'); link.href = row.href; link.dataset.nav = ''; link.textContent = row.title;
      const outcome = doc.createElement('span'); outcome.className = 'app-activity-outcome'; outcome.dataset.tone = tone(row.status); outcome.textContent = labels[row.status] || 'Unknown';
      summary.append(link, outcome);
      const detail = doc.createElement('p'); detail.textContent = row.detail; detail.title = row.detail;
      item.append(summary, detail);
      if (row.time) { const time = doc.createElement('time'); time.dateTime = row.time.datetime; time.textContent = row.time.absolute; time.title = row.time.relative; item.append(time); }
      list.append(item);
      if (focused === row.key) link.focus({preventScroll: true});
    }
    status.textContent = failures.length ? failures.map(source => source.error).join(' ')
      : !rows.length ? canReadSchedules ? 'No deployments or scheduled runs to show yet.' : 'No deployments to show yet.' : manual ? 'Activity updated.' : '';
    status.hidden = !status.textContent;
    status.classList.toggle('error', !!failures.length);
    retry.hidden = !failures.length;
    return !failures.length;
  }
  async function refresh(manual = false) {
    if (!alive) return false;
    pending?.abort();
    const request = new win.AbortController(); pending = request;
    const stamp = ++revision;
    panel.setAttribute('aria-busy', 'true'); retry.disabled = true;
    const timeout = win.setTimeout(() => request.abort(), 8000);
    try {
      await Promise.all(sources.map(async source => {
        try {
          const response = await api(source.path, {signal: request.signal});
          if (!alive || stamp !== revision) return;
          if (response.status === 401) {
            alive = false; revision++; request.abort();
            for (const entry of sources) entry.items = [];
            list.replaceChildren(); status.hidden = false; status.textContent = 'Your session expired. Sign in again.';
            panel.removeAttribute('aria-busy'); retry.hidden = true;
            onUnauthorized(); return;
          }
          if (response.status === 403 || response.status === 404) {
            source.items = []; source.error = `You no longer have access to ${source.label}.`; return;
          }
          if (!response.ok) throw new Error('Request failed');
          const body = await response.json();
          if (!alive || stamp !== revision || request.signal.aborted) return;
          if (!Array.isArray(body) && !Array.isArray(body?.items)) throw new Error('Invalid activity list');
          source.items = (Array.isArray(body) ? body : body.items).filter(item => item && typeof item === 'object');
          source.error = '';
        } catch {
          if (!alive || stamp !== revision) return;
          source.error = `Could not refresh ${source.label}.${source.items?.length ? ' Showing the last results.' : ' Try again.'}`;
        }
      }));
      if (!alive || stamp !== revision) return false;
      return render(manual);
    } finally {
      win.clearTimeout(timeout);
      if (alive && stamp === revision) { panel.removeAttribute('aria-busy'); retry.disabled = false; }
    }
  }
  retry.addEventListener('click', async () => {
    const restoreFocus = doc.activeElement === retry;
    await refresh(true);
    if (alive && restoreFocus && retry.hidden && (doc.activeElement === retry || doc.activeElement === doc.body)) {
      status.tabIndex = -1; status.focus({preventScroll: true});
    }
  });
  void refresh();
  const dispose = () => { alive = false; revision++; pending?.abort(); };
  dispose.refresh = refresh;
  return dispose;
}
