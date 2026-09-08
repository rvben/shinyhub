// Return paths come from internal/access/middleware.go. Resolve them before
// router.start(): /login is not a SPA route and its fallback discards the query.
const SPA_ROUTE_PREFIXES = ['/launchpad', '/apps/', '/users', '/workers', '/audit-log', '/tokens'];

export async function startAuthenticatedRouter(router, win = window) {
  const params = new URLSearchParams(win.location.search);
  const raw = params.get('next');
  if (params.has('next')) {
    params.delete('next');
    const search = params.toString();
    win.history.replaceState(null, '', win.location.pathname + (search ? '?' + search : '') + win.location.hash);
  }
  if (raw && raw.startsWith('/') && !raw.startsWith('//') && !/[\\\x00-\x20\x7f]/.test(raw)) {
    const target = new URL(raw, win.location.origin);
    if (target.origin === win.location.origin && target.pathname !== '/' && target.pathname !== '/login') {
      const spa = SPA_ROUTE_PREFIXES.some(p => p.endsWith('/')
        ? target.pathname.startsWith(p)
        : target.pathname === p || target.pathname.startsWith(p + '/'));
      if (!spa) {
        win.location.replace(raw);
        return true;
      }
      win.history.replaceState(null, '', raw);
    }
  }
  await router.start();
  return false;
}
