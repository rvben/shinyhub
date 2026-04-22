export function mountAuditLog(ctx) {
  const view = document.getElementById('audit-view');
  view.hidden = false;
  ctx.loadAuditEvents(0);
  ctx.updateActiveNav(location.pathname);
  return {
    title: 'Audit Log · ShinyHub',
    unmount() { view.hidden = true; },
  };
}
