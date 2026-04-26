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
export function createRouter() {
  const routes = [];
  let current = null;
  let generation = 0;
  // start() is invoked from two places: the bootstrap path in initialize()
  // and the interactive login submit handler (see app.js). Without this
  // guard, a logout → login cycle would attach a second pair of click /
  // popstate listeners, causing a single SPA navigation to push duplicate
  // history entries and mount the target view twice.
  let started = false;

  function register(pattern, mountFn) {
    const keys = [];
    const rx = new RegExp(
      '^' +
        pattern.replace(/:([a-z]+)/gi, (_, k) => {
          keys.push(k);
          return '([^/]+)';
        }) +
        '$',
    );
    routes.push({ pattern, rx, keys, mountFn });
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

  async function mount(path, search) {
    const gen = ++generation;
    if (current && typeof current.unmount === 'function') {
      try { current.unmount(); } catch (e) { console.error('unmount', e); }
    }
    current = null;
    const hit = match(path);
    if (!hit) {
      if (path !== '/') {
        console.warn('router: no match for', path);
        navigate('/', { replace: true });
      }
      return;
    }
    const view = await hit.route.mountFn(hit.params, search);
    if (gen !== generation) {
      // A later navigation has superseded us. Discard this result.
      if (view && typeof view.unmount === 'function') {
        try { view.unmount(); } catch (e) { console.error('unmount', e); }
      }
      return;
    }
    current = view || {};
    document.title = (current && current.title) || 'ShinyHub';
    const h1 = document.querySelector('main section:not([hidden]) h1');
    if (h1) {
      if (!h1.hasAttribute('tabindex')) h1.setAttribute('tabindex', '-1');
      h1.focus({ preventScroll: true });
    }
  }

  function navigate(path, opts = {}) {
    const full = path + (opts.search || '');
    if (opts.replace) {
      history.replaceState({}, '', full);
    } else {
      history.pushState({}, '', full);
    }
    return mount(location.pathname, location.search);
  }

  function onPopState() {
    mount(location.pathname, location.search);
  }

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

  return { register, navigate, start };
}
