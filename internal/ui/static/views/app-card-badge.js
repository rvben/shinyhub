// appCardBadge decides the status badge for an app card.
//
// deploy_count only increments on a *successful* deploy, so it cannot tell a
// failed-only deploy (an attempt that crash-looped) from an app that was never
// deployed - both have deploy_count 0. last_deployment_status disambiguates:
// a failed latest deployment renders "Failed" (error styling) instead of the
// benign "Awaiting deploy", so a broken app is not mistaken for a pending one.
//
// formatStatus is injected (app.js owns it) to keep this module DOM-free and
// unit-testable.
export function appCardBadge(app, formatStatus) {
  const neverSucceeded = (app.deploy_count || 0) === 0;
  if (neverSucceeded && app.last_deployment_status === 'failed') {
    return { cls: 'badge badge-failed', text: 'Failed' };
  }
  if (neverSucceeded) {
    return { cls: 'badge badge-new', text: 'Awaiting deploy' };
  }
  return { cls: `badge badge-${app.status}`, text: formatStatus(app.status) };
}
