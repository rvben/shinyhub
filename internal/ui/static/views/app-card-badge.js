// appStatusView is the canonical status decision an app card AND the detail-
// header pill both consume, so the same app cannot read "Failed" on its card
// while reading "Awaiting deploy" on its detail page. It returns a state key
// (used to build `badge-<state>` / `status-<state>` classes) and the label.
//
// deploy_count only increments on a *successful* deploy, so it cannot tell a
// failed-only deploy (an attempt that crash-looped) from an app that was never
// deployed - both have deploy_count 0. last_deployment_status disambiguates:
// a failed latest deployment renders "Failed" (error styling) instead of the
// benign "Awaiting deploy", so a broken app is not mistaken for a pending one.
//
// formatStatus is injected (app.js owns it) to keep this module DOM-free and
// unit-testable.
export function appStatusView(app, formatStatus) {
  const neverSucceeded = (app.deploy_count || 0) === 0;
  if (neverSucceeded && app.last_deployment_status === 'failed') {
    return { state: 'failed', text: 'Failed' };
  }
  if (neverSucceeded) {
    return { state: 'new', text: 'Awaiting deploy' };
  }
  return { state: app.status, text: formatStatus(app.status) };
}

// appCardBadge maps the shared status view onto an app-card badge class.
export function appCardBadge(app, formatStatus) {
  const { state, text } = appStatusView(app, formatStatus);
  return { cls: `badge badge-${state}`, text };
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
