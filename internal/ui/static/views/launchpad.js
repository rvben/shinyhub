// Viewer Apps view - an app-forward gallery built for one motion: find an app,
// open it. “Launchpad” remains the internal surface name; the user-facing
// navigation calls the destination Apps. Logic lives in
// launchpad-model.js (DOM-
// free, unit-tested); this file renders it (createElement/textContent only, so
// an app name or description can never inject markup) and tracks recently-opened
// apps in localStorage. No operator chrome (no deploy/metrics/kebab) - launching
// is the only action.
import { buildLaunchpadModel } from './launchpad-model.js';
import { renderAppAvatar } from './app-avatar.js';
import { createGroupDisclosure } from './group-disclosure.js';

const RECENT_KEY = 'shinyhub.recent-apps';
const RECENT_MAX = 6;
const POLL_MS = 20000;

export function mountLaunchpad(ctx) {
  const view = document.getElementById('launchpad-view');
  const body = document.getElementById('launchpad-body');
  view.hidden = false;
  ctx.updateActiveNav(location.pathname);

  // Scope recently-opened to the signed-in user so a shared browser profile does
  // not surface a previous user's launches (which would leak activity on apps
  // both can access). Keyed by stable user id, falling back to username.
  const u = ctx.state && ctx.state.user;
  const recentKey = `${RECENT_KEY}:${(u && (u.id || u.username)) || 'anon'}`;

  let disposed = false;
  let timer = null;
  let query = '';
  let model = null;

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
      const lpBody = (await resp.json()) || [];
      apps = Array.isArray(lpBody) ? lpBody : (Array.isArray(lpBody.items) ? lpBody.items : []);
    } catch {
      if (initial) body.replaceChildren(errorState());
      return;
    }
    if (disposed) return;
    ctx.state.apps = apps;
    if (typeof ctx.syncSidebar === 'function') ctx.syncSidebar();
    model = buildLaunchpadModel(apps, getRecent(recentKey));
    // On the 20s background refresh, re-render only the results region so the
    // header search input keeps its focus, value, and caret while a viewer is
    // typing. A full render runs on first load and on any structural change:
    // the empty state (model.total === 0, e.g. the last app was deleted) and the
    // empty -> populated case (no .lp-results node yet) both need renderEmpty /
    // the header rebuilt, so only take the partial path when apps are present.
    const results = (!initial && model.total > 0) ? body.querySelector('.lp-results') : null;
    if (results) renderResults(results);
    else render();
  }

  function render() {
    const root = el('div', 'lp');
    root.appendChild(renderHeader());
    if (model.total === 0) {
      root.appendChild(renderEmpty());
      body.replaceChildren(root);
      return;
    }
    const results = el('div', 'lp-results');
    renderResults(results);
    root.appendChild(results);
    body.replaceChildren(root);
  }

  function renderHeader() {
    const head = el('div', 'lp-head');
    const greeting = el('div', 'lp-greeting');
    greeting.appendChild(el('p', 'lp-sub', welcomeText()));
    head.appendChild(greeting);

    if (model.total > 0) {
      const search = document.createElement('input');
      search.type = 'search';
      search.className = 'lp-search';
      search.placeholder = 'Search apps…';
      search.setAttribute('aria-label', 'Search apps');
      search.value = query;
      search.addEventListener('input', () => {
        query = search.value;
        const results = body.querySelector('.lp-results');
        if (results) renderResults(results);
      });
      head.appendChild(search);
    }
    return head;
  }

  function renderResults(container) {
    const q = query.trim().toLowerCase();
    container.replaceChildren();
    container.setAttribute('role', 'region');
    container.setAttribute('aria-label', 'Available apps');

    if (q) {
      const matches = model.groups
        .flatMap((g) => g.apps)
        .filter((t) => t.name.toLowerCase().includes(q) || t.description.toLowerCase().includes(q));
      if (matches.length === 0) {
        container.appendChild(el('p', 'lp-noresults', `No apps match "${query.trim()}".`));
        return;
      }
      container.appendChild(grid(matches));
      return;
    }

    if (model.recent.length > 0) {
      container.appendChild(sectionHead('Recently opened'));
      container.appendChild(grid(model.recent));
    }
    for (const g of model.groups) {
      if (g.showHeading) {
        const disclosure = createGroupDisclosure(document, {
          view: 'launchpad',
          groupKey: g.project,
          label: g.name || 'Other apps',
          count: g.apps.length,
          iconEmoji: g.iconEmoji,
          classPrefix: 'lp',
        });
        disclosure.body.appendChild(grid(g.apps));
        container.appendChild(disclosure.root);
      } else if (model.recent.length > 0) {
        // The lone ungrouped group needs no name of its own, but with a
        // "Recently opened" block above it the tiles below would otherwise run
        // on with no label saying what they are.
        container.appendChild(sectionHead('All apps'));
      }
      if (!g.showHeading) container.appendChild(grid(g.apps));
    }
  }

  function grid(tiles) {
    const g = el('div', 'lp-grid');
    for (const t of tiles) g.appendChild(tile(t));
    return g;
  }

  function tile(t) {
    const openable = t.readiness.openable;
    const node = document.createElement(openable ? 'a' : 'div');
    node.className = 'lp-tile' + (openable ? '' : ' lp-tile--down');
    if (openable) {
      node.href = '/app/' + encodeURIComponent(t.slug) + '/';
      // A real navigation to the proxied app (no data-nav), recording the launch
      // first so "Recently opened" reflects it on return.
      node.addEventListener('click', () => pushRecent(recentKey, t.slug));
    }

    node.appendChild(renderAppAvatar(document, {
      iconUrl: t.iconUrl, emoji: t.emoji, initials: t.avatar.initials, hue: t.avatar.hue,
    }, 'lp-avatar'));

    const main = el('div', 'lp-tile-main');
    main.appendChild(el('p', 'lp-tile-name', t.name));
    if (t.description) main.appendChild(el('p', 'lp-tile-desc', t.description));
    // Openable apps carry no status line - the clickable tile + chevron say it
    // all. Only an app the viewer cannot open is flagged (dimmed, no chevron).
    if (t.readiness.label) {
      const ready = el('p', 'lp-ready lp-ready--unavailable');
      ready.appendChild(el('span', 'lp-ready-dot'));
      ready.appendChild(el('span', 'lp-ready-label', t.readiness.label));
      main.appendChild(ready);
    }
    node.appendChild(main);

    if (openable) {
      const chev = el('span', 'lp-tile-go');
      chev.setAttribute('aria-hidden', 'true');
      chev.textContent = '→';
      node.appendChild(chev);
    }
    return node;
  }

  function renderEmpty() {
    const sec = el('section', 'lp-empty');
    sec.appendChild(el('h2', 'lp-empty-title', 'No apps available yet'));
    sec.appendChild(el('p', 'lp-empty-body',
      'Apps you can access will show up here ready to open. Ask your admin if you expected to see one.'));
    return sec;
  }

  function welcomeText() {
    const u = ctx.state && ctx.state.user;
    const name = u && (u.display_name || u.username);
    return name ? `Welcome, ${name}. Open one of your apps.` : 'Open one of your apps.';
  }

  function skeleton() {
    const wrap = el('div', 'lp lp-skeleton');
    wrap.setAttribute('aria-busy', 'true');
    wrap.setAttribute('role', 'status');
    wrap.setAttribute('aria-live', 'polite');
    wrap.appendChild(el('span', 'sr-only', 'Loading apps…'));
    const g = el('div', 'lp-grid');
    for (let i = 0; i < 4; i++) g.appendChild(el('div', 'lp-skel'));
    wrap.appendChild(g);
    return wrap;
  }

  function errorState() {
    const sec = el('section', 'lp-empty');
    sec.setAttribute('role', 'alert');
    sec.appendChild(el('p', 'lp-empty-body', "Couldn't load your apps."));
    const retry = el('button', 'ov-btn', 'Try again');
    retry.type = 'button';
    retry.addEventListener('click', () => { body.replaceChildren(skeleton()); load(true); });
    sec.appendChild(retry);
    return sec;
  }

  function sectionHead(text) {
    return el('h2', 'lp-section', text);
  }

  load(true);
  timer = setInterval(() => { if (!disposed) load(false); }, POLL_MS);

  return {
    title: '',
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

// Recently-opened slugs live in localStorage under a per-user key (client-only;
// no backend). The key is built in mountLaunchpad from the signed-in user id.
function getRecent(key) {
  try {
    const raw = localStorage.getItem(key);
    const arr = raw ? JSON.parse(raw) : [];
    return Array.isArray(arr) ? arr.filter((s) => typeof s === 'string') : [];
  } catch {
    return [];
  }
}

function pushRecent(key, slug) {
  try {
    const next = [slug, ...getRecent(key).filter((s) => s !== slug)].slice(0, RECENT_MAX);
    localStorage.setItem(key, JSON.stringify(next));
  } catch {
    /* storage disabled (private mode) - recently-opened is a nicety, skip */
  }
}
