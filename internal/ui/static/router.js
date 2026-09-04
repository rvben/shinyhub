// Tiny client-side router built on the browser History API.
//
// Usage:
//   const router = createRouter();
//   router.register('/', mountAppsGrid);
//   router.register('/apps/:slug', mountAppDetail);
//   router.register('/apps/:slug/:tab', mountAppDetail);
//   router.register('/users', mountUsers);
//   router.register('/audit-log', mountAuditLog);
//   router.start();
//
// A mount function receives (params, search) and returns an optional view
// object { unmount, title }. The router calls unmount() on leave and sets
// document.title to `title` on enter.
//
// register() takes per-route options:
//   key(params)    identifies the mounted view. Navigating between two routes
//                    that produce the same key is a change WITHIN one view (the
//                    app-detail tab routes: same app, different tab), so the
//                    router hands it to the view's update(params, search)
//                    instead of unmounting and mounting again. A view that
//                    declares no update() is remounted as usual.
//   params(params) normalizes the matched params before they reach mount() or
//                    update(), so a pattern that implies a value (/apps/:slug
//                    means the overview tab) states it in one place.
export function createRouter(opts = {}) {
  const routes = [];
  let current = null;
  // The key of the mounted view, or null when the view cannot take updates.
  let currentKey = null;
  // Whether what is on screen is the view for the current URL. False before the
  // first mount and after any mount or update that failed, because onError
  // blanks the page sections: navigating to the URL we are already on is only a
  // no-op while the page it names is actually there to look at.
  let viewHealthy = false;
  let generation = 0;
  // onError is invoked when a mount function throws or rejects. It lets the app
  // render a visible error state instead of leaving a blank shell (a single
  // view's throw must never take down the whole dashboard). Defaults to logging.
  const onError =
    typeof opts.onError === 'function'
      ? opts.onError
      : (err) => console.error('router: mount failed', err);
  // onMounted runs after a successful mount so the app can clear a previously
  // shown error state; otherwise the error view would linger beside a healthy
  // page after the user navigates away from a transient failure.
  const onMounted = typeof opts.onMounted === 'function' ? opts.onMounted : () => {};
  // navGuard, when set, is consulted before any navigation (link click, back/
  // forward, or programmatic navigate). It returns true to allow the navigation
  // and false to cancel it (e.g. unsaved edits the user chose to keep). currentPath
  // tracks where we are so a cancelled back/forward can be restored.
  let navGuard = null;
  let currentPath = null;
  // start() is invoked from two places: the bootstrap path in initialize()
  // and the interactive login submit handler (see app.js). Without this
  // guard, a logout → login cycle would attach a second pair of click /
  // popstate listeners, causing a single SPA navigation to push duplicate
  // history entries and mount the target view twice.
  let started = false;

  function register(pattern, mountFn, routeOpts = {}) {
    const keys = [];
    const rx = new RegExp(
      '^' +
        pattern.replace(/:([a-z]+)/gi, (_, k) => {
          keys.push(k);
          return '([^/]+)';
        }) +
        '$',
    );
    routes.push({
      pattern,
      rx,
      keys,
      mountFn,
      keyFn: routeOpts.key || null,
      paramsFn: routeOpts.params || null,
    });
  }

  function match(path) {
    for (const r of routes) {
      const m = r.rx.exec(path);
      if (!m) continue;
      const params = {};
      r.keys.forEach((k, i) => (params[k] = decodeURIComponent(m[i + 1])));
      return { route: r, params };
    }
    return null;
  }

  function paramsFor(route, params) {
    return route.paramsFn ? route.paramsFn(params) : params;
  }

  function applyTitle() {
    const brandTitle = (window.__SHINYHUB_BRANDING__ && window.__SHINYHUB_BRANDING__.site_title) || 'ShinyHub';
    document.title = (current && current.title) ? current.title + ' · ' + brandTitle : brandTitle;
  }

  // The navigation resolves to the view that is already mounted, and that view
  // knows how to change in place. Nothing is torn down: no unmount, no second
  // mount, no frame in which the view is hidden while the new region loads, and
  // no focus grab. A tab switch must leave the visitor's focus and scroll
  // exactly where they were.
  async function update(hit, path, search) {
    const gen = ++generation;
    try {
      await current.update(paramsFor(hit.route, hit.params), search);
    } catch (err) {
      // onError blanks every page section, so a view that failed to update can
      // no longer be assumed to be on screen. Drop its key (but keep the view,
      // so its unmount still runs) and the next navigation rebuilds it through
      // the full mount path.
      currentKey = null;
      viewHealthy = false;
      if (gen === generation) onError(err, path);
      return;
    }
    if (gen !== generation) return;
    viewHealthy = true;
    onMounted();
    applyTitle();
  }

  async function mount(path, search) {
    currentPath = path + (search || '');
    const hit = match(path);
    if (
      hit &&
      currentKey !== null &&
      current &&
      typeof current.update === 'function' &&
      hit.route.keyFn &&
      hit.route.keyFn(hit.params) === currentKey
    ) {
      return update(hit, path, search);
    }
    const gen = ++generation;
    if (current && typeof current.unmount === 'function') {
      try { current.unmount(); } catch (e) { console.error('unmount', e); }
    }
    current = null;
    currentKey = null;
    viewHealthy = false;
    if (!hit) {
      if (path !== '/') {
        console.warn('router: no match for', path);
        navigate('/', { replace: true });
      }
      return;
    }
    let view;
    try {
      view = await hit.route.mountFn(paramsFor(hit.route, hit.params), search);
    } catch (err) {
      // A later navigation may have superseded this one; only surface the error
      // if we are still the current mount, so a stale failure can't clobber a
      // healthy view.
      if (gen === generation) onError(err, path);
      return;
    }
    if (gen !== generation) {
      // A later navigation has superseded us. Discard this result.
      if (view && typeof view.unmount === 'function') {
        try { view.unmount(); } catch (e) { console.error('unmount', e); }
      }
      return;
    }
    current = view || {};
    currentKey = hit.route.keyFn ? hit.route.keyFn(hit.params) : null;
    viewHealthy = true;
    // Clear any prior error state before computing focus, so a recovered
    // navigation neither shows the error panel nor lets its h1 steal focus.
    onMounted();
    applyTitle();
    const h1 = document.querySelector('main section:not([hidden]) h1');
    if (h1) {
      if (!h1.hasAttribute('tabindex')) h1.setAttribute('tabindex', '-1');
      h1.focus({ preventScroll: true });
    }
  }

  function navigate(path, opts = {}) {
    const full = path + (opts.search || '');
    // Clicking the nav item for the page you are already on must do nothing.
    // Running the navigation would tear that page down and build it again: the
    // grid rebuilt card by card, its requests re-issued, focus dragged back to
    // the heading from wherever the visitor put it, and a second identical
    // history entry pushed so that Back appears not to work. Nothing about the
    // page would differ afterwards.
    //
    // Only while the page it names is actually on screen, though: after a mount
    // that failed the sections are blank, so the same click is the visitor
    // asking to try again and has to go through.
    if (viewHealthy && full === location.pathname + location.search) return Promise.resolve();
    // A guard may veto navigation (unsaved edits). replace:true navigations are
    // internal redirects (e.g. viewer bounced off a manager-only tab) and skip
    // the guard so they always complete.
    if (!opts.replace && navGuard && !navGuard()) return Promise.resolve();
    if (opts.replace) {
      history.replaceState({}, '', full);
    } else {
      history.pushState({}, '', full);
    }
    return mount(location.pathname, location.search);
  }

  function onPopState() {
    // The history entry already changed by the time popstate fires. If the guard
    // vetoes, push the previous path back so the URL and view stay put.
    if (navGuard && !navGuard()) {
      if (currentPath != null) history.pushState({}, '', currentPath);
      return;
    }
    mount(location.pathname, location.search);
  }

  function setNavGuard(fn) { navGuard = fn; }

  function onClick(e) {
    if (e.defaultPrevented) return;
    if (e.button !== 0) return;
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.target.closest('a');
    if (!a) return;
    if (!a.hasAttribute('data-nav')) return;
    if (a.target && a.target !== '_self') return;
    const url = new URL(a.href, location.origin);
    if (url.origin !== location.origin) return;
    e.preventDefault();
    navigate(url.pathname + url.search);
  }

  function start() {
    if (!started) {
      window.addEventListener('popstate', onPopState);
      document.addEventListener('click', onClick);
      started = true;
    }
    return mount(location.pathname, location.search);
  }

  return { register, navigate, start, setNavGuard };
}
