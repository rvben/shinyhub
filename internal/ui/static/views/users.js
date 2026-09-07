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
    title: 'Users',
    unmount() { view.hidden = true; },
  };
}
