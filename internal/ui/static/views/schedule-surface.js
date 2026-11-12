import {
  activationState,
  activationErrorDetail,
  canCancelActivation,
  deletedScheduleActivations,
  deployTriggerLabel,
  afterSuccessDetail,
  afterSuccessLabel,
  dstAdvisoryMarkup,
  formatAge,
  formatDuration,
  runTriggerLabel,
  runDurationSeconds,
  scheduleState,
  scheduleDetailSignature,
  scheduleSummary,
  scheduleTimingLabel,
  createScheduleRequestGate,
  producerCompatibilityState,
} from './schedule-ui.js';
import { escapeHtml } from './html-escape.js';

// createScheduleSurface owns the Schedules tab's read side: the list, the
// selected schedule's detail and run history, polling, and the mount/stop
// lifecycle that keeps a late response from painting an app the user has left.
//
// Mutations are not its concern. Each one (run, edit, delete, cancel, retry)
// and the run-log pane is reached through `actions`, so the shell keeps its
// confirm dialogs and forms while this module stays loadable under jsdom.
//
// Exactly one surface is mounted at a time; load() with options replaces it.
export function createScheduleSurface({
  document,
  api,
  handleUnauthorized,
  router,
  flashToast,
  kebabControls,
  wireKebab,
  actions,
  setTimeout = (callback, delay) => globalThis.setTimeout(callback, delay),
  clearTimeout = id => globalThis.clearTimeout(id),
}) {
  let mountedScheduleSurface = null;

  function scheduleDate(value, timezone, withDate = true) {
    if (!value) return '—';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '—';
    try {
      return new Intl.DateTimeFormat(undefined, {
        ...(withDate ? {year: 'numeric', month: 'short', day: 'numeric'} : {}),
        hour: '2-digit', minute: '2-digit',
        timeZone: timezone || undefined,
        timeZoneName: 'short',
      }).format(date);
    } catch {
      return date.toLocaleString();
    }
  }

  function scheduleStatusMarkup(state) {
    return `<span class="schedule-state schedule-state-${escapeHtml(state.tone)}"><span aria-hidden="true"></span>${escapeHtml(state.label)}</span>`;
  }

  function stopScheduleSurface(surface = mountedScheduleSurface) {
    if (!surface || surface !== mountedScheduleSurface) return;
    surface.stopped = true;
    surface.requests.invalidate();
    surface.historyRequests.invalidate();
    if (surface.timer) clearTimeout(surface.timer);
    if (surface.visibilityHandler) document.removeEventListener('visibilitychange', surface.visibilityHandler);
    mountedScheduleSurface = null;
  }

  function scheduleNextPoll(surface, schedules) {
    if (surface.stopped || surface !== mountedScheduleSurface || surface.accessDenied || document.hidden) return;
    if (surface.timer) clearTimeout(surface.timer);
    const active = schedules.some(s => scheduleState(s).key === 'running' || activationState(s).active);
    surface.timer = setTimeout(() => refreshScheduleSurface(surface), active ? 5000 : 30000);
  }

  function loadSchedules(slug, options) {
    let surface = mountedScheduleSurface;
    if (options) {
      stopScheduleSurface();
      surface = {
        slug,
        options: options || {},
        timer: null,
        stopped: false,
        visibilityHandler: null,
        requests: createScheduleRequestGate(),
        historyRequests: createScheduleRequestGate(),
      };
      surface.visibilityHandler = () => {
        if (!document.hidden && !surface.stopped) refreshScheduleSurface(surface);
        else if (surface.timer) clearTimeout(surface.timer);
      };
      document.addEventListener('visibilitychange', surface.visibilityHandler);
      mountedScheduleSurface = surface;
    }
    // Mutations finishing after navigation must not remount an old app.
    if (!surface || surface.stopped || surface.slug !== slug) return;
    const addBtn = document.getElementById('schedules-add-btn');
    if (addBtn) addBtn.hidden = !surface.options.canManage;
    if (!options) return refreshScheduleSurface(surface, {announce: true});
    document.getElementById('schedules-workspace')?.classList.toggle('has-detail', options.selectedScheduleID != null);
    document.getElementById('schedule-detail').hidden = true;
    document.getElementById('schedules-summary').hidden = true;
    document.getElementById('schedules-list').innerHTML = '<p class="schedule-loading">Loading scheduled jobs…</p>';
    document.getElementById('schedules-error').hidden = true;
    refreshScheduleSurface(surface);
    // Return cleanup before the first request settles.
    return () => stopScheduleSurface(surface);
  }

  function clearScheduleAccess(surface) {
    surface.accessDenied = true;
    surface.requests.invalidate();
    surface.historyRequests.invalidate();
    if (surface.timer) clearTimeout(surface.timer);
    surface.schedules = null;
    surface.renderedDetailID = null;
    surface.renderedDetailSignature = null;
    surface.detailRuns = [];
    surface.detailTotal = 0;
    const list = document.getElementById('schedules-list');
    list.replaceChildren();
    list.setAttribute('aria-busy', 'false');
    const detail = document.getElementById('schedule-detail');
    detail.replaceChildren();
    detail.hidden = true;
    document.getElementById('schedules-summary').hidden = true;
    document.getElementById('schedules-add-btn').hidden = true;
    document.getElementById('schedules-surface-status').textContent = '';
    const refresh = document.getElementById('schedules-refresh-btn');
    refresh.disabled = false;
    refresh.textContent = 'Refresh';
    const error = document.getElementById('schedules-error');
    error.hidden = false;
    error.querySelector('p').textContent = 'Your access to schedules changed. Ask an app manager for access.';
    error.querySelector('button').onclick = () => refreshScheduleSurface(surface, {announce: true});
  }

  async function refreshScheduleSurface(surface, {announce = false} = {}) {
    if (!surface || surface.stopped || surface !== mountedScheduleSurface) return;
    const request = surface.requests.begin();
    const current = () => !surface.stopped && surface === mountedScheduleSurface && surface.requests.isCurrent(request);
    const container = document.getElementById('schedules-list');
    const statusEl = document.getElementById('schedules-surface-status');
    const error = document.getElementById('schedules-error');
    const refresh = document.getElementById('schedules-refresh-btn');
    const focused = document.activeElement;
    const focusedScheduleKey = focused?.dataset?.scheduleFocus || null;
    let keepFocus = !!focusedScheduleKey;
    const focusMoved = event => { if (event.target !== focused && !focused?.contains(event.target)) keepFocus = false; };
    if (!container) return;
    document.addEventListener('focusin', focusMoved);
    document.addEventListener('pointerdown', focusMoved, true);
    if (surface.timer) clearTimeout(surface.timer);
    container.setAttribute('aria-busy', 'true');
    if (refresh) { refresh.disabled = true; refresh.textContent = 'Refreshing…'; }
    if (announce && statusEl) statusEl.textContent = 'Refreshing schedule status…';
    try {
      const resp = await api(`/api/apps/${encodeURIComponent(surface.slug)}/schedules`, {signal: request.signal});
      if (!current()) return;
      if (resp.status === 401) { await handleUnauthorized(); return; }
      if (!resp.ok) throw Object.assign(new Error('response'), {status: resp.status});
      const schedBody = await resp.json();
      if (!current()) return;
      const schedules = Array.isArray(schedBody) ? schedBody : schedBody?.items;
      if (!Array.isArray(schedules)) throw new Error('Invalid schedule response');
      surface.accessDenied = false;
      surface.schedules = schedules;
      error.hidden = true;
      renderScheduleSummary(schedules);
      document.getElementById('schedules-summary').hidden = !schedules.length || surface.options.selectedScheduleID != null;
      renderScheduleList(surface, schedules);
      if (statusEl) statusEl.textContent = announce ? 'Schedule status updated.' : '';
      const selected = surface.options.selectedScheduleID == null
        ? null
        : schedules.find(s => String(s.id) === String(surface.options.selectedScheduleID));
      if (surface.options.selectedScheduleID != null && !selected) {
        router.navigate(`/apps/${encodeURIComponent(surface.slug)}/schedules`, {replace: true});
        return;
      }
      const forceDetail = announce || (selected && scheduleState(selected).key === 'running');
      await renderScheduleDetail(surface, selected, forceDetail);
      if (!current()) return;
      if (keepFocus && focusedScheduleKey) {
        const replacement = Array.from(document.querySelectorAll('[data-schedule-focus]'))
          .find(el => el.dataset.scheduleFocus === focusedScheduleKey);
        replacement?.focus({preventScroll: true});
      }
    } catch (err) {
      if (!current()) return;
      if (err?.status === 403) { clearScheduleAccess(surface); return; }
      if (!surface.schedules) container.replaceChildren();
      error.hidden = false;
      error.querySelector('p').textContent = err?.status === 403
        ? 'Your access to schedules changed. Ask an app manager for access.'
        : `Could not update schedules. ${surface.schedules ? 'The previous status is still shown. ' : ''}Check your connection and try again.`;
      error.querySelector('button').onclick = () => refreshScheduleSurface(surface, {announce: true});
      if (statusEl) statusEl.textContent = '';
    } finally {
      document.removeEventListener('focusin', focusMoved);
      document.removeEventListener('pointerdown', focusMoved, true);
      if (current()) {
        container.setAttribute('aria-busy', 'false');
        if (refresh) { refresh.disabled = false; refresh.textContent = 'Refresh'; }
        scheduleNextPoll(surface, surface.schedules || []);
      }
    }
  }

  function renderScheduleSummary(schedules) {
    const summary = scheduleSummary(schedules);
    const set = (id, value, attention = false) => {
      const el = document.getElementById(id);
      if (!el) return;
      el.textContent = value;
      el.classList.toggle('has-attention', attention);
    };
    set('schedules-summary-attention', String(summary.attention), summary.attention > 0);
    set('schedules-summary-total', String(summary.total));
    set('schedules-summary-enabled', String(summary.enabled));
    set('schedules-summary-active', String(summary.activeCount));
  }

	function deletedActivationHistoryMarkup(surface, schedules) {
		const retained = deletedScheduleActivations(surface.options.latestActivations, schedules);
		if (retained.length === 0) return '';
		const rows = retained.map(activation => {
			const state = activationState({latest_activation: activation});
			const run = activation.schedule_run_id ? `Run #${activation.schedule_run_id}` : 'Source run retained as a snapshot';
			const generation = Number.isFinite(Number(activation.target_generation))
				? `Generation ${activation.target_generation}`
				: 'Generation unavailable';
			const occurredAt = activation.finished_at || activation.updated_at || activation.created_at;
			const identity = activation.id ? `Activation #${activation.id}` : 'Activation ID unavailable';
			const when = occurredAt ? scheduleDate(occurredAt) : 'Time unavailable';
			return `<li>
				<div class="schedule-retained-head"><strong>${escapeHtml(activation.schedule_name || 'Deleted schedule')}</strong>${scheduleStatusMarkup(state)}</div>
				<p>${escapeHtml(`${identity} · ${run} · ${generation}`)}</p>
				<p>${occurredAt ? `<time datetime="${escapeHtml(occurredAt)}">${escapeHtml(when)}</time> · ` : ''}source schedule deleted</p>
				${activation.last_error ? `<p class="schedule-retained-error">${escapeHtml(activation.last_error)}</p>` : ''}
			</li>`;
		}).join('');
		return `<section class="schedule-retained-history" aria-labelledby="schedule-retained-title">
			<div class="schedule-history-heading"><div><h3 id="schedule-retained-title">Retained serving-data activation outcomes</h3><p>Durable outcomes retained after their source schedules were deleted.</p></div></div>
			<ul>${rows}</ul>
		</section>`;
	}

  function renderScheduleList(surface, schedules) {
    const container = document.getElementById('schedules-list');
    const addBtn = document.getElementById('schedules-add-btn');
    if (!container) return;
    if (addBtn) addBtn.hidden = !surface.options.canManage;
    // A poll that changes nothing must leave the rendered nodes alone: an open
    // actions menu lives in them and would otherwise close under the user.
    // The root check re-renders when anything else has replaced the list.
    const unchanged = markup => surface.renderedListMarkup === markup
      && surface.renderedListRoot != null && container.firstElementChild === surface.renderedListRoot;
    const commit = markup => {
      container.querySelectorAll('.schedule-actions-menu').forEach(menu => kebabControls.get(menu)?.close());
      container.innerHTML = markup;
      surface.renderedListMarkup = markup;
      surface.renderedListRoot = container.firstElementChild;
    };
    if (schedules.length === 0) {
			const retained = deletedActivationHistoryMarkup(surface, schedules);
      const emptyMarkup = `<div class="schedules-empty"><h3>No scheduled jobs yet</h3><p>Schedules run data preparation and maintenance commands independently of the app process.</p>${surface.options.canManage ? '<button type="button" class="btn-primary" data-schedule-empty-add>Add first schedule</button>' : ''}</div>${retained}`;
      if (unchanged(emptyMarkup)) return;
      commit(emptyMarkup);
      container.querySelector('[data-schedule-empty-add]')?.addEventListener('click', () => actions.openScheduleForm(surface.slug, null));
      return;
    }

    const rows = schedules.map(s => {
      const state = scheduleState(s);
      const activation = activationState(s);
      const compatibility = producerCompatibilityState(s);
      const tz = s.effective_timezone || 'UTC';
      const lastRun = s.last_run_at ? scheduleDate(s.last_run_at, tz) : 'No run recorded';
      const lastSuccess = Number.isFinite(s.last_success_age_s) ? `Last success ${formatAge(s.last_success_age_s)}` : 'No successful run recorded';
      const nextFire = s.retired ? 'Retired' : s.enabled === false ? 'Paused' : scheduleDate(s.next_fire, tz);
      const selected = String(s.id) === String(surface.options.selectedScheduleID);
      const actions = surface.options.canManage ? `
        <div class="kebab-menu schedule-actions-menu">
          <button type="button" aria-haspopup="menu" aria-expanded="false" aria-label="More actions for ${escapeHtml(s.name)}">
            <svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3" cy="8" r="1.25"/><circle cx="8" cy="8" r="1.25"/><circle cx="13" cy="8" r="1.25"/></svg>
          </button>
          <ul class="kebab-list" role="menu" hidden>
            ${s.retired ? '' : `<li role="none"><button type="button" role="menuitem" data-action="run" data-id="${s.id}" data-name="${escapeHtml(s.name)}" data-schedule-focus="schedule-${s.id}-run">Run now</button></li>`}
            ${s.retired ? '' : `<li role="none"><button type="button" role="menuitem" data-action="edit" data-id="${s.id}" data-schedule-focus="schedule-${s.id}-edit">Edit schedule</button></li>`}
            <li role="none" class="schedule-menu-danger"><button type="button" role="menuitem" data-action="delete" data-id="${s.id}" data-name="${escapeHtml(s.name)}" data-schedule-focus="schedule-${s.id}-delete">Delete schedule</button></li>
          </ul>
        </div>` : '';
      return `<tr${selected ? ' class="is-selected" aria-current="true"' : ''}>
        <td data-label="Schedule">
          <div class="schedule-cell-stack">
            <a class="schedule-name-link" data-nav href="/apps/${encodeURIComponent(surface.slug)}/schedules/${s.id}" data-schedule-focus="schedule-${s.id}-open">${escapeHtml(s.name)}</a>
            <span class="schedule-cell-primary">${escapeHtml(scheduleTimingLabel(s.cron_expr))}</span>
            <span class="schedule-definition"><code>${escapeHtml(s.cron_expr)}</code> · ${escapeHtml(tz)}${s.timezone_inherited ? ' (default)' : ''}</span>
            ${dstAdvisoryMarkup(s)}
          </div>
        </td>
        <td data-label="Job status">
          <div class="schedule-cell-stack">
            ${scheduleStatusMarkup(state)}
            <span class="schedule-cell-primary">${escapeHtml(lastRun)}</span>
            ${state.attention ? `<span class="schedule-cell-secondary">${escapeHtml(lastSuccess)}</span>` : ''}
            ${compatibility.key !== 'not-tracked' && compatibility.key !== 'retired' ? `<span class="schedule-compatibility-label is-${compatibility.tone}">Data: ${escapeHtml(compatibility.label)}</span>` : ''}
            ${activation.key !== 'none' && activation.key !== 'never' ? `<span class="schedule-cell-secondary">App rollout: ${escapeHtml(activation.label)}</span>` : ''}
          </div>
        </td>
        <td data-label="Next run">
          <div class="schedule-cell-stack"><span class="schedule-cell-primary">${escapeHtml(nextFire)}</span>${s.enabled === false && !s.retired ? '<span class="schedule-cell-secondary">Manual runs available</span>' : ''}</div>
        </td>
        <td class="table-actions" data-label="Actions"><div class="schedule-row-actions">${actions}</div></td>
      </tr>`;
    }).join('');
    const listMarkup = `<table>
      <thead><tr><th scope="col">Schedule</th><th scope="col">Job status</th><th scope="col">Next run</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>${rows}</tbody>
    </table>${deletedActivationHistoryMarkup(surface, schedules)}`;
    if (unchanged(listMarkup)) return;
    commit(listMarkup);

    container.querySelectorAll('.schedule-actions-menu').forEach(menu => {
      kebabControls.set(menu, wireKebab(menu.querySelector(':scope > button'), menu.querySelector('.kebab-list'), menu.closest('tr')));
    });

    container.querySelectorAll('[data-action]').forEach(btn => {
      btn.addEventListener('click', () => {
        const action = btn.dataset.action;
        const id = parseInt(btn.dataset.id, 10);
        if (action === 'run') actions.runScheduleNow(surface.slug, id, btn.dataset.name, btn);
        else if (action === 'delete') actions.deleteSchedule(surface.slug, id, btn.dataset.name, btn);
        else if (action === 'edit') {
          // Read at click time: rows outlive the poll that rendered them, so
          // the form opens on the schedule as last reported, not as rendered.
          const current = (surface.schedules || []).find(item => item.id === id);
          if (current) actions.openScheduleForm(surface.slug, current);
        }
      });
    });
  }

  async function renderScheduleDetail(surface, schedule, force = false) {
    const workspace = document.getElementById('schedules-workspace');
    const detail = document.getElementById('schedule-detail');
    if (!workspace || !detail) return;
    workspace.classList.toggle('has-detail', !!schedule);
    detail.hidden = !schedule;
    if (!schedule) {
      surface.renderedDetailID = null;
      surface.renderedDetailSignature = null;
      detail.replaceChildren();
      return;
    }
    const detailSignature = scheduleDetailSignature(schedule);
    if (!force && String(surface.renderedDetailID) === String(schedule.id) && surface.renderedDetailSignature === detailSignature) return;
    surface.historyRequests.invalidate();
    const openDiagnostics = [...detail.querySelectorAll('details[open]')].map(node => node.querySelector('summary')?.textContent);
    surface.renderedDetailID = schedule.id;
    surface.renderedDetailSignature = detailSignature;

    const state = scheduleState(schedule);
    const activation = activationState(schedule);
    const compatibility = producerCompatibilityState(schedule);
    const latestActivation = schedule.latest_activation;
    const timezone = schedule.effective_timezone || 'UTC';
    detail.setAttribute('aria-busy', 'true');
    detail.querySelectorAll('.schedule-actions-menu').forEach(menu => kebabControls.get(menu)?.close());
    const compatibilityMarkup = `
      ${schedule.deploy_trigger && schedule.deploy_trigger !== 'never' ? `<section class="schedule-compatibility-panel schedule-compatibility-${compatibility.tone}" aria-labelledby="schedule-compatibility-title">
        <div class="schedule-history-heading">
          <div><h3 id="schedule-compatibility-title">Code and data compatibility</h3><p>${escapeHtml(compatibility.detail)}</p>${schedule.convergence_error ? `<p class="schedule-detail-error">${escapeHtml(schedule.convergence_error)}</p>` : ''}</div>
          <div class="schedule-activation-actions">${scheduleStatusMarkup(compatibility)}${surface.options.canManage && compatibility.attention && schedule.convergence_obligation_id && schedule.convergence_status === 'failed' && schedule.enabled !== false && !schedule.retired ? `<button type="button" class="btn-primary" data-detail-action="retry-convergence">Retry producer</button>` : ''}</div>
        </div>
        <details class="schedule-diagnostics"><summary>Producer details</summary>\n        <dl class="schedule-compatibility-facts">
          <div><dt>Deployment policy</dt><dd>${escapeHtml(deployTriggerLabel(schedule.deploy_trigger))}</dd></div>
          <div><dt>Convergence</dt><dd>${escapeHtml(schedule.convergence_status || (schedule.deploy_trigger_satisfied ? 'satisfied' : 'not requested'))}</dd></div>
          <div><dt>Current code</dt><dd><strong>${escapeHtml(schedule.current_app_version || 'Unknown version')}</strong><code>${escapeHtml(schedule.current_content_digest || 'Digest unavailable')}</code></dd></div>
          <div><dt>Data producer</dt><dd><strong>${escapeHtml(schedule.producer_app_version || 'No proven producer')}</strong><code>${escapeHtml(schedule.producer_content_digest || 'Digest unavailable')}</code></dd></div>
          ${schedule.producer_published_at ? `<div class="schedule-activation-wide"><dt>Published</dt><dd>${escapeHtml(scheduleDate(schedule.producer_published_at, timezone))}</dd></div>` : ''}
          ${schedule.convergence_error ? `<div class="schedule-activation-wide schedule-activation-error"><dt>Last error</dt><dd>${escapeHtml(schedule.convergence_error)}</dd></div>` : ''}
        </dl>\n        </details>
      </section>` : ''}`;
    detail.innerHTML = `
      <div class="schedule-detail-head">
        <a class="schedule-detail-back" data-nav href="/apps/${encodeURIComponent(surface.slug)}/schedules">All schedules</a>
        <div class="schedule-detail-title-row">
          <div><h2 id="schedule-detail-title">${escapeHtml(schedule.name)}</h2><div class="schedule-detail-states">${scheduleStatusMarkup(state)}${compatibility.key !== 'not-tracked' && compatibility.key !== 'retired' ? `<span class="schedule-compatibility-label is-${compatibility.tone}">Data: ${escapeHtml(compatibility.label)}</span>` : ''}</div></div>
          ${surface.options.canManage ? `<div class="schedule-detail-actions">
            ${schedule.retired ? '' : `<button type="button" class="schedule-run-btn" data-detail-action="run" data-schedule-focus="schedule-${schedule.id}-detail-run"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M5.25 3.5 12 8l-6.75 4.5z"/></svg><span>Run now</span></button>`}
            <div class="kebab-menu schedule-actions-menu" data-schedule-actions-menu>
              <button type="button" aria-haspopup="menu" aria-expanded="false" aria-label="More actions for ${escapeHtml(schedule.name)}"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3" cy="8" r="1.25"/><circle cx="8" cy="8" r="1.25"/><circle cx="13" cy="8" r="1.25"/></svg></button>
              <ul class="kebab-list" role="menu" hidden>
                ${schedule.retired ? '' : `<li role="none"><button type="button" role="menuitem" data-detail-action="edit" data-schedule-focus="schedule-${schedule.id}-detail-edit">Edit schedule</button></li>`}
                <li role="none" class="schedule-menu-danger"><button type="button" role="menuitem" data-detail-action="delete" data-schedule-focus="schedule-${schedule.id}-detail-delete">Delete schedule</button></li>
              </ul>
            </div>
          </div>` : ''}
        </div>
      </div>
      ${schedule.roll_feasibility_advisory ? `<div class="schedule-roll-advisory"><strong>Roll capacity warning</strong><span>${escapeHtml(schedule.roll_feasibility_advisory)}</span></div>` : ''}
      <dl class="schedule-definition-grid">
        <div><dt>Timing</dt><dd><code>${escapeHtml(schedule.cron_expr)}</code></dd></div>
        <div><dt>Timezone</dt><dd>${escapeHtml(timezone)}${schedule.timezone_inherited ? ' <span>(inherited)</span>' : ''}</dd></div>
        <div><dt>Next run</dt><dd>${escapeHtml(schedule.retired ? 'Retired' : schedule.enabled === false ? 'Paused' : scheduleDate(schedule.next_fire, timezone))}</dd></div>
        <div><dt>After success</dt><dd>${escapeHtml(afterSuccessLabel(schedule))}</dd></div>
        <div class="schedule-definition-command"><dt>Command</dt><dd><code>${escapeHtml((schedule.command || []).join(' '))}</code></dd></div>
      </dl>
	  ${latestActivation || schedule.on_success === 'roll' ? `<section class="schedule-activation-panel" aria-labelledby="schedule-activation-title">
		<div class="schedule-history-heading"><div><h3 id="schedule-activation-title">Serving-data activation</h3><p>${schedule.on_success === 'roll' ? 'Job health and activation health are reported separately.' : 'Earlier policy · this durable activation was requested before rollout was disabled for future runs.'}</p></div><div class="schedule-activation-actions">${scheduleStatusMarkup(activation)}${surface.options.canManage && canCancelActivation(schedule) ? `<button type="button" class="env-btn-secondary" data-detail-action="cancel-activation" data-schedule-focus="schedule-${schedule.id}-cancel-activation">Cancel activation</button>` : ''}</div></div>
        ${latestActivation ? `<dl class="schedule-activation-facts">
          <div><dt>Requested by</dt><dd>${latestActivation.schedule_run_id ? `Run #${escapeHtml(latestActivation.schedule_run_id)}` : 'Retained activation record'}</dd></div>
          <div><dt>Target generation</dt><dd>${escapeHtml(latestActivation.target_generation)}</dd></div>
          <div><dt>Phase</dt><dd>${escapeHtml(latestActivation.phase || 'pending')}</dd></div>
          <div><dt>Due</dt><dd>${escapeHtml(scheduleDate(latestActivation.due_at, timezone))}</dd></div>
          ${latestActivation.finished_at ? `<div><dt>Finished</dt><dd>${escapeHtml(scheduleDate(latestActivation.finished_at, timezone))}</dd></div>` : ''}
          ${latestActivation.defer_reason ? `<div class="schedule-activation-wide"><dt>Deferred</dt><dd>${escapeHtml(latestActivation.defer_reason)}</dd></div>` : ''}
          ${latestActivation.last_error ? `<div class="schedule-activation-wide schedule-activation-error"><dt>Last error</dt><dd>${escapeHtml(latestActivation.last_error)}</dd></div>` : ''}
        </dl>` : '<p class="schedule-activation-empty">No successful run has requested an activation yet.</p>'}
      </section>` : ''}
      ${compatibility.attention ? compatibilityMarkup : ''}
      <div class="schedule-history-heading"><div><h3>Run history</h3><p>Newest runs first. Times use ${escapeHtml(timezone)}.</p></div></div>
      <div class="schedule-history" data-schedule-history role="region" aria-label="Schedule run history" tabindex="0"><p class="schedule-loading">Loading run history…</p></div>
      ${!compatibility.attention ? compatibilityMarkup : ''}
      <details class="schedule-diagnostics"><summary>Execution details</summary>
        <dl class="schedule-activation-facts">
          <div><dt>Deployment policy</dt><dd>${escapeHtml(deployTriggerLabel(schedule.deploy_trigger))}</dd></div>
          <div><dt>Timeout</dt><dd>${escapeHtml(formatDuration(schedule.timeout_seconds))}</dd></div>
          <div><dt>Overlapping runs</dt><dd>${escapeHtml(schedule.overlap_policy || 'skip')}</dd></div>
          <div><dt>Missed runs</dt><dd>${escapeHtml(schedule.missed_policy || 'skip')}</dd></div>
          <div class="schedule-activation-wide"><dt>After success</dt><dd>${escapeHtml(afterSuccessDetail(schedule))}</dd></div>
        </dl>
      </details>
      ${schedule.dst_advisory ? `<p class="schedule-roll-advisory">${escapeHtml(schedule.dst_advisory)}</p>` : ''}`;

    for (const node of detail.querySelectorAll('details')) if (openDiagnostics.includes(node.querySelector('summary')?.textContent)) node.open = true;
    const detailActionsMenu = detail.querySelector('[data-schedule-actions-menu]');
    if (detailActionsMenu) {
      kebabControls.set(detailActionsMenu, wireKebab(detailActionsMenu.querySelector(':scope > button'), detailActionsMenu.querySelector('.kebab-list'), detailActionsMenu));
    }

    detail.querySelector('[data-detail-action="run"]')?.addEventListener('click', e => {
      actions.runScheduleNow(surface.slug, schedule.id, schedule.name, e.currentTarget);
    });
    detail.querySelector('[data-detail-action="edit"]')?.addEventListener('click', () => actions.openScheduleForm(surface.slug, schedule));
    detail.querySelector('[data-detail-action="delete"]')?.addEventListener('click', e => {
      actions.deleteSchedule(surface.slug, schedule.id, schedule.name, e.currentTarget);
    });
    detail.querySelector('[data-detail-action="cancel-activation"]')?.addEventListener('click', e => {
      actions.cancelScheduleActivation(surface.slug, schedule.id, schedule.name, e.currentTarget);
    });
    detail.querySelector('[data-detail-action="retry-convergence"]')?.addEventListener('click', e => {
      actions.retryScheduleConvergence(surface.slug, schedule.id, schedule.name, e.currentTarget);
    });

    await loadScheduleRunPage(surface, schedule, 0, true);
    if (!surface.stopped && surface === mountedScheduleSurface && String(surface.renderedDetailID) === String(schedule.id)) detail.setAttribute('aria-busy', 'false');
  }

  async function loadScheduleRunPage(surface, schedule, offset, replace) {
    const host = document.querySelector('[data-schedule-history]');
    if (!host || surface.stopped || surface !== mountedScheduleSurface) return;
    const request = surface.historyRequests.begin();
    const focused = document.activeElement;
    let keepFocus = host.contains(focused);
    const focusMoved = event => { if (event.target !== focused && !focused?.contains(event.target)) keepFocus = false; };
    document.addEventListener('focusin', focusMoved);
    document.addEventListener('pointerdown', focusMoved, true);
    const current = () => !surface.stopped && surface === mountedScheduleSurface && surface.historyRequests.isCurrent(request)
      && host.isConnected && host === document.querySelector('[data-schedule-history]');
    host.setAttribute('aria-busy', 'true');
    try {
      const response = await api(`/api/apps/${encodeURIComponent(surface.slug)}/schedules/${schedule.id}/runs?limit=10&offset=${offset}`, {signal: request.signal});
      if (!current()) return;
      if (response.status === 401) { await handleUnauthorized(); return; }
      if (response.status === 403) { clearScheduleAccess(surface); return; }
      if (!response.ok) throw new Error('response');
      const body = await response.json();
      if (!current()) return;
      const page = Array.isArray(body) ? body : body?.items;
      if (!Array.isArray(page)) throw new Error('Invalid run history');
      const total = Number.isFinite(body.total) ? body.total : page.length;
      surface.detailTotal = total;
      surface.detailRuns = replace ? page : [...(surface.detailRuns || []), ...page];
      renderScheduleRuns(surface, schedule, total);
    } catch {
      if (!current()) return;
      if (replace) host.innerHTML = '<div class="schedule-load-error" role="alert"><p>Run history could not be loaded.</p><button type="button" class="env-btn-secondary" data-run-history-retry>Try again</button></div>';
      else {
        flashToast('More run history could not be loaded. Try again.', 'error');
        renderScheduleRuns(surface, schedule, surface.detailTotal || (surface.detailRuns || []).length);
      }
      host.querySelector('[data-run-history-retry]')?.addEventListener('click', () => loadScheduleRunPage(surface, schedule, 0, true));
    } finally {
      document.removeEventListener('focusin', focusMoved);
      document.removeEventListener('pointerdown', focusMoved, true);
      if (current()) {
        host.setAttribute('aria-busy', 'false');
        if (keepFocus) host.focus({preventScroll: true});
      }
    }
  }

  function renderScheduleRuns(surface, schedule, total) {
    const host = document.querySelector('[data-schedule-history]');
    const runs = surface.detailRuns || [];
    if (!host) return;
    if (runs.length === 0) {
      host.innerHTML = '<div class="schedule-runs-empty"><h4>No runs yet</h4><p>The first scheduled or manual run will appear here.</p></div>';
      return;
    }
    const rows = runs.map(run => {
      const runState = scheduleState({
        enabled: true,
        active_run_id: run.status === 'running' ? run.id : null,
        last_run_status: run.status,
      });
      const duration = formatDuration(runDurationSeconds(run));
      const trigger = runTriggerLabel(run.trigger);
      const exit = run.exit_code == null ? '—' : String(run.exit_code);
      const activationResult = run.on_success === 'roll'
        ? activationState({on_success: 'roll', latest_activation: run.activation_status ? {
            status: run.activation_status,
            phase: run.activation_phase,
          } : null})
        : null;
	  const activationError = activationErrorDetail(run);
      const actions = [
        surface.options.canReadLogs ? `<button type="button" class="env-btn-secondary" data-run-action="logs" data-run-id="${run.id}" data-schedule-focus="run-${run.id}-logs">View logs</button>` : '',
        surface.options.canCancel && run.status === 'running' ? `<button type="button" class="btn-danger-sm" data-run-action="cancel" data-run-id="${run.id}" data-schedule-focus="run-${run.id}-cancel">Cancel</button>` : '',
      ].join('');
      return `<tr>
        <td data-label="Result"><div class="schedule-cell-stack">${scheduleStatusMarkup(runState)}</div></td>
        <td data-label="Started"><div class="schedule-cell-stack"><time datetime="${escapeHtml(run.started_at || '')}">${escapeHtml(scheduleDate(run.started_at, schedule.effective_timezone || 'UTC'))}</time></div></td>
        <td data-label="Duration"><div class="schedule-cell-stack">${escapeHtml(duration)}</div></td>
        <td data-label="Trigger"><div class="schedule-cell-stack">${escapeHtml(trigger)}</div></td>
        <td data-label="Exit"><div class="schedule-cell-stack">${escapeHtml(exit)}</div></td>
		<td data-label="Activation"${!activationResult && !activationError ? ' class="schedule-run-no-activation"' : ''}><div class="schedule-cell-stack">${activationResult ? scheduleStatusMarkup(activationResult) : '—'}${run.target_generation ? `<span>Generation ${escapeHtml(run.target_generation)}</span>` : ''}${activationError ? `<small class="schedule-run-activation-error"><strong>Activation error:</strong> ${escapeHtml(activationError)}</small>` : ''}</div></td>
        <td class="table-actions" data-label="Actions"><div class="schedule-row-actions">${actions}</div></td>
      </tr>`;
    }).join('');
    const remaining = Math.max(0, total - runs.length);
    host.innerHTML = `<table>
      <thead><tr><th scope="col">Job result</th><th scope="col">Started</th><th scope="col">Duration</th><th scope="col">Trigger</th><th scope="col">Exit</th><th scope="col">Activation</th><th scope="col"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    <div class="schedule-history-footer"><span>Showing ${runs.length} of ${total}</span>${remaining ? `<button type="button" class="env-btn-secondary" data-load-more-runs data-schedule-focus="schedule-${schedule.id}-load-more">Load ${Math.min(10, remaining)} more</button>` : ''}</div>`;

    host.querySelectorAll('[data-run-action]').forEach(btn => {
      btn.addEventListener('click', () => {
        const runID = Number.parseInt(btn.dataset.runId, 10);
        if (btn.dataset.runAction === 'logs') actions.openScheduleRunLogs(surface.slug, schedule.id, runID);
        if (btn.dataset.runAction === 'cancel') actions.cancelScheduleRun(surface.slug, schedule.id, runID, btn);
      });
    });
    host.querySelector('[data-load-more-runs]')?.addEventListener('click', e => {
      e.currentTarget.disabled = true;
      loadScheduleRunPage(surface, schedule, runs.length, false);
    });
  }

  return {
    // load mounts a surface when options are given and returns its cleanup;
    // without options it refreshes the mounted surface if it still shows slug.
    load: loadSchedules,
    stop: stopScheduleSurface,
    // refresh re-reads the mounted surface; it is a no-op when none is mounted.
    refresh: options => refreshScheduleSurface(mountedScheduleSurface, options),
    // mounted returns the live surface record, or null between mounts.
    mounted: () => mountedScheduleSurface,
  };
}
