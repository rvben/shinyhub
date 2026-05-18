export function mountUsers(ctx) {
  const view = document.getElementById('users-view');
  view.hidden = false;
  ctx.loadUsers();
  ctx.updateActiveNav(location.pathname);
  return {
    title: 'Users',
    unmount() { view.hidden = true; },
  };
}
