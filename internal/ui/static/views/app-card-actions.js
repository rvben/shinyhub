// appCardActions decides which action controls an app card shows in the grid,
// based on whether the app has ever successfully deployed.
//
// deploy_count only increments on a successful deploy (see app-card-badge.js),
// so deploy_count 0 means "never deployed". A never-deployed app has nothing to
// open yet (the /app/<slug>/ URL would 404), so the grid hides "Open" and makes
// "Deploy" the primary call to action; the Restart kebab only applies once the
// app is live and the viewer can manage it.
//
// Kept DOM-free so it is unit-testable; the caller (renderGridVerbatim in
// app.js) builds the DOM from these booleans.
export function appCardActions(app, canManage) {
  const neverDeployed = (app.deploy_count || 0) === 0;
  return {
    showOpen: !neverDeployed,
    deployIsPrimary: neverDeployed,
    showRestart: !!canManage && !neverDeployed,
  };
}
