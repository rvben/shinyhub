// Overview view - the operator dashboard home. A health-first fleet pulse, the
// apps that need attention, fleet resource pressure, and (for admins) recent
// activity. Logic lives in overview-model.js (DOM-free, unit-tested); this file
// is the renderer + the 10s liveness poll. All app/user-supplied text is set via
// textContent / createElement, never innerHTML, so a malicious slug or crash
// traceback can never inject markup.
import { buildOverviewModel, pulseMeta } from './overview-model.js';
import { formatBytes } from './stat-format.js';
import { formatStatus } from './status-label.js';
import { appCardBadge } from './app-card-badge.js';

const POLL_MS = 10000;

export function mountOverview(ctx) {
  const view = document.getElementById('overview-view');
  const body = document.getElementById('overview-body');
  view.hidden = false;
  ctx.updateActiveNav(location.pathname);

  let disposed = false;
  let timer = null;
  const isAdmin = !!(ctx.state && ctx.state.user && ctx.state.user.role === 'admin');

  // stop ends the liveness poll (on unmount or after a 401), so a logged-out or
  // navigated-away Overview never keeps fetching in the background.
  function stop() {
    disposed = true;
    if (timer) { clearInterval(timer); timer = null; }
  }

  body.replaceChildren(skeleton());

  async function load(initial) {
    let apps = [];
    try {
      const resp = await ctx.api('/api/apps');
      if (disposed) return;
      if (resp.status === 401) { stop(); ctx.onUnauthorized(); return; }
      if (!resp.ok) { if (initial) body.replaceChildren(errorState()); return; }
      // Standard {items,...} list envelope; tolerate a bare array for resilience.
      const ovBody = (await resp.json()) || [];
      apps = Array.isArray(ovBody) ? ovBody : (Array.isArray(ovBody.items) ? ovBody.items : []);
    } catch {
      if (initial) body.replaceChildren(errorState());
      return;
    }
    if (disposed) return;
    ctx.state.apps = apps;
    if (typeof ctx.syncSidebar === 'function') ctx.syncSidebar();

    const metrics = await fetchMetrics(apps.map((a) => a.slug));
    if (disposed) return;
    const events = isAdmin ? await fetchActivity() : null;
    if (disposed) return;

    const model = buildOverviewModel(apps, metrics);
    body.replaceChildren(render(model, events));
  }

  async function fetchMetrics(slugs) {
    if (slugs.length === 0) return {};
    try {
      const qs = encodeURIComponent(slugs.join(','));
      const resp = await ctx.api('/api/apps/metrics?slugs=' + qs);
      if (!resp.ok) return {};
      const b = await resp.json();
      return (b && b.metrics) || {};
    } catch { return {}; }
  }

  async function fetchActivity() {
    try {
      const resp = await ctx.api('/api/audit?limit=6');
      if (!resp.ok) return null;
      const b = await resp.json();
      return (b && Array.isArray(b.events)) ? b.events : null;
    } catch { return null; }
  }

  function render(model, events) {
    const root = el('div', 'ov-grid');
    root.appendChild(renderPulse(model));
    if (model.attention.length > 0) root.appendChild(renderAttention(model.attention));
    else if (model.total === 0) root.appendChild(renderFirstRun());
    root.appendChild(renderFooter(model, events));
    return root;
  }

  // ── Pulse: the fleet verdict + a proportional status bar (the signature). ──
  function renderPulse(model) {
    const sec = el('section', 'ov-pulse ov-pulse--' + model.verdict.tone);
    sec.setAttribute('aria-label', 'Fleet status');

    const verdict = el('div', 'ov-pulse-verdict');
    verdict.appendChild(el('span', 'ov-pulse-dot'));
    const vtext = el('div', 'ov-pulse-text');
    vtext.appendChild(el('p', 'ov-pulse-headline', model.verdict.headline));
    vtext.appendChild(el('p', 'ov-pulse-detail', model.verdict.detail));
    verdict.appendChild(vtext);
    sec.appendChild(verdict);

    const active = model.segments.filter((s) => s.count > 0);
    if (model.total > 0) {
      const bar = el('div', 'ov-bar');
      bar.setAttribute('role', 'img');
      bar.setAttribute('aria-label',
        active.map((s) => `${s.count} ${s.label.toLowerCase()}`).join(', '));
      for (const seg of active) {
        const s = el('span', 'ov-bar-seg');
        s.style.flexGrow = String(seg.count);
        s.style.setProperty('--seg', `var(${seg.cssVar})`);
        bar.appendChild(s);
      }
      sec.appendChild(bar);

      const legend = el('ul', 'ov-legend');
      for (const seg of active) {
        const li = el('li', 'ov-legend-item');
        const dot = el('span', 'ov-legend-dot');
        dot.style.setProperty('--seg', `var(${seg.cssVar})`);
        li.appendChild(dot);
        li.appendChild(el('span', 'ov-legend-label', seg.label));
        li.appendChild(el('b', 'ov-legend-count', String(seg.count)));
        legend.appendChild(li);
      }
      sec.appendChild(legend);
    }
    return sec;
  }

  // ── Attention: actionable rows for crashed / degraded apps. ──
  function renderAttention(items) {
    const sec = el('section', 'ov-panel ov-attention');
    sec.appendChild(sectionTitle('Needs attention', items.length));
    const list = el('ul', 'ov-attn-list');
    for (const a of items) {
      const li = el('li', 'ov-attn-row');
      const main = el('div', 'ov-attn-main');
      const name = el('a', 'ov-attn-name', a.name);
      name.href = '/apps/' + encodeURIComponent(a.slug);
      name.setAttribute('data-nav', '');
      main.appendChild(name);
      main.appendChild(el('span', 'ov-attn-reason', a.reason || formatStatus(a.status)));
      li.appendChild(main);

      const bi = appCardBadge(a.app, formatStatus);
      li.appendChild(el('span', bi.cls, bi.text));

      const actions = el('div', 'ov-attn-actions');
      // Restart is a management action: only offer it to users who can manage
      // this app (the server would reject it for view-only members anyway), the
      // same gate the Apps grid uses.
      if (ctx.canManageApp(ctx.state.user, a.app)) {
        const restart = el('button', 'ov-btn', 'Restart');
        restart.type = 'button';
        restart.addEventListener('click', () => {
          restart.disabled = true;
          restart.textContent = 'Restarting…';
          Promise.resolve(ctx.restart(a.slug)).finally(() => { if (!disposed) load(false); });
        });
        actions.appendChild(restart);
      }
      const open = el('a', 'ov-btn', 'Open');
      open.href = '/apps/' + encodeURIComponent(a.slug);
      open.setAttribute('data-nav', '');
      actions.appendChild(open);
      li.appendChild(actions);

      list.appendChild(li);
    }
    sec.appendChild(list);
    return sec;
  }

  function renderFirstRun() {
    const sec = el('section', 'ov-panel ov-firstrun');
    sec.appendChild(el('h2', 'ov-firstrun-title', 'Deploy your first Shiny app'));
    sec.appendChild(el('p', 'ov-firstrun-body',
      'Apps you deploy appear here with live health and resource usage. Head to Apps to create one.'));
    const cta = el('a', 'btn-primary', 'Go to Apps');
    cta.href = '/apps';
    cta.setAttribute('data-nav', '');
    sec.appendChild(cta);
    return sec;
  }

  // ── Footer: resource pressure + (admin) recent activity. ──
  function renderFooter(model, events) {
    const footer = el('div', 'ov-footer');
    if (!isAdmin) footer.classList.add('ov-footer--single');
    footer.appendChild(renderResources(model.resources));
    if (isAdmin) footer.appendChild(renderActivity(events));
    return footer;
  }

  function renderResources(res) {
    const sec = el('section', 'ov-panel ov-resources');
    sec.appendChild(sectionTitle('Resource pressure'));

    const line = el('p', 'ov-res-line');
    const cpu = el('span', 'ov-res-stat');
    cpu.appendChild(el('b', null, formatCpu(res.cpuPercent)));
    cpu.appendChild(el('span', 'ov-res-stat-label', 'CPU'));
    const ram = el('span', 'ov-res-stat');
    ram.appendChild(el('b', null, res.rssBytes > 0 ? formatBytes(res.rssBytes) : '0 KB'));
    ram.appendChild(el('span', 'ov-res-stat-label', 'RAM'));
    line.appendChild(cpu);
    line.appendChild(el('span', 'ov-res-sep', '·'));
    line.appendChild(ram);
    const across = res.running === 1 ? 'across 1 running app' : `across ${res.running} running apps`;
    line.appendChild(el('span', 'ov-res-across', across));
    sec.appendChild(line);

    if (res.nearLimit.length > 0) {
      sec.appendChild(el('p', 'ov-res-subhead', 'Approaching memory limit'));
      const list = el('ul', 'ov-nearlimit');
      for (const n of res.nearLimit.slice(0, 4)) {
        const li = el('li', 'ov-nearlimit-row');
        const a = el('a', 'ov-nearlimit-name', n.name);
        a.href = '/apps/' + encodeURIComponent(n.slug) + '/configuration';
        a.setAttribute('data-nav', '');
        li.appendChild(a);
        const track = el('span', 'ov-meter' + (n.fraction >= 0.95 ? ' ov-meter--hot' : ''));
        const fill = el('span', 'ov-meter-fill');
        fill.style.width = Math.min(100, Math.round(n.fraction * 100)) + '%';
        track.appendChild(fill);
        li.appendChild(track);
        li.appendChild(el('span', 'ov-nearlimit-val',
          `${formatBytes(n.usedBytes)} / ${formatBytes(n.limitBytes)}`));
        list.appendChild(li);
      }
      sec.appendChild(list);
    } else if (res.running > 0) {
      sec.appendChild(el('p', 'ov-res-clear', 'No apps near their limits.'));
    }
    return sec;
  }

  function renderActivity(events) {
    const sec = el('section', 'ov-panel ov-activity');
    sec.appendChild(sectionTitle('Recent activity'));
    if (!events || events.length === 0) {
      sec.appendChild(el('p', 'ov-empty-note', 'No recent activity.'));
      return sec;
    }
    const list = el('ul', 'ov-timeline');
    for (const ev of events.slice(0, 6)) {
      const li = el('li', 'ov-tl-row');
      li.appendChild(el('span', 'ov-tl-rail'));
      const main = el('div', 'ov-tl-main');
      const head = el('p', 'ov-tl-head');
      head.appendChild(el('b', 'ov-tl-action', humanAction(ev.action)));
      const target = ev.resource_id || ev.resource_type;
      if (target) head.appendChild(el('span', 'ov-tl-target', target));
      main.appendChild(head);
      const meta = el('p', 'ov-tl-meta');
      meta.appendChild(el('span', 'ov-tl-actor', ev.username || 'system'));
      meta.appendChild(el('span', 'ov-tl-time', relTime(ev.created_at)));
      main.appendChild(meta);
      li.appendChild(main);
      list.appendChild(li);
    }
    sec.appendChild(list);
    return sec;
  }

  // ── helpers ──
  function sectionTitle(text, count) {
    const h = el('h2', 'ov-section-title', text);
    if (typeof count === 'number') {
      h.appendChild(el('span', 'ov-section-count', String(count)));
    }
    return h;
  }
  function skeleton() {
    const wrap = el('div', 'ov-grid ov-skeleton');
    wrap.appendChild(el('div', 'ov-skel ov-skel-pulse'));
    const f = el('div', 'ov-footer');
    f.appendChild(el('div', 'ov-skel ov-skel-panel'));
    f.appendChild(el('div', 'ov-skel ov-skel-panel'));
    wrap.appendChild(f);
    wrap.setAttribute('aria-busy', 'true');
    return wrap;
  }
  function errorState() {
    const sec = el('section', 'ov-panel ov-error');
    sec.appendChild(el('p', 'ov-error-text', "Couldn't load the overview."));
    const retry = el('button', 'ov-btn', 'Try again');
    retry.type = 'button';
    retry.addEventListener('click', () => { body.replaceChildren(skeleton()); load(true); });
    sec.appendChild(retry);
    return sec;
  }

  load(true);
  timer = setInterval(() => { if (!disposed) load(false); }, POLL_MS);

  return {
    title: 'Overview',
    unmount() {
      stop();
      view.hidden = true;
    },
  };
}

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}

function formatCpu(pct) {
  const p = Number(pct) || 0;
  if (p >= 100) return (p / 100).toFixed(1) + ' cores';
  return p.toFixed(0) + '%';
}

function humanAction(action) {
  if (!action || typeof action !== 'string') return 'Activity';
  const s = action.replace(/[._]/g, ' ');
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// relTime renders an ISO timestamp as a compact "3m ago" / "2h ago" string.
function relTime(iso) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const secs = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (secs < 60) return 'just now';
  const mins = Math.round(secs / 60);
  if (mins < 60) return mins + 'm ago';
  const hrs = Math.round(mins / 60);
  if (hrs < 24) return hrs + 'h ago';
  const days = Math.round(hrs / 24);
  return days + 'd ago';
}
