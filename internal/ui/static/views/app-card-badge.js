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

// updateCardStatusBadge refreshes a card's status badge in place from a freshly
// polled status (the 10s /metrics tick reports a live `status`), so a card
// opened while an app was hibernating reflects wake/sleep transitions without a
// full reload.
//
// It writes the live status onto the app model first, then re-derives the badge
// via appCardBadge. Routing through appCardBadge (rather than setting the badge
// directly from status) preserves the pre-deploy states: a poll reporting
// "stopped" for a never-deployed app must keep rendering "Awaiting deploy"
// / "Failed", not relabel it as "Stopped".
//
// badgeEl is the card's status-badge element; setting className replaces only
// the class attribute, so the data-slug used to locate it survives.
export function updateCardStatusBadge(badgeEl, app, status, formatStatus) {
  if (!badgeEl || !app) return;
  if (status) app.status = status;
  const info = appCardBadge(app, formatStatus);
  badgeEl.className = info.cls;
  badgeEl.textContent = info.text;
}
