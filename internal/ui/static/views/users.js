export function mountUsers(ctx) {
  const view = document.getElementById('users-view');
  view.hidden = false;
  ctx.loadUsers();
  const params = new URLSearchParams(location.search);
  if (params.get('support') === 'ended') {
    ctx.flashToast?.('Returned to ShinyHub. Your administrator identity is active.');
    params.delete('support');
    const query = params.toString();
    document.defaultView.history.replaceState(document.defaultView.history.state, '', location.pathname + (query ? `?${query}` : '') + location.hash);
  }
  ctx.updateActiveNav(location.pathname);
  return {
    // The page is called Identity in the navigation and in its <h1>; the tab
    // said Users, which is the old name for a page that now also covers
    // service accounts.
    title: 'Identity',
    unmount() { view.hidden = true; },
  };
}
