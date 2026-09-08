// Deployment-history predicates shared by the app-detail tabs.
//
// deploy_count only increments on a SUCCESSFUL deploy (internal/db/queries.go
// IncrementDeployCount, called from the deploy handler after the health check
// passes), so it cannot tell "nobody has ever deployed this app" from "every
// deploy attempt so far crashed on startup" - both read 0. Gating a
// first-deploy empty state on it therefore hides the crash from the operator
// on exactly the app that needs it most.
//
// The honest signals come from the deployment summary the server joins onto
// every app payload (internal/db/queries.go deploymentSummarySQL):
//
//   last_deployment_status  status of the NEWEST deployments row
//                           ('succeeded' | 'failed' | 'pending'), omitted only
//                           when the app has no deployments row at all
//   last_deployed_at        MAX(created_at) over EVERY deployments row,
//                           regardless of status
//   release_number          COUNT of 'succeeded' rows
//   released_at             created_at of the newest 'succeeded' row
//
// Both are present on GET /api/apps/{slug} (handleGetApp -> GetAppBySlug,
// which selects appColumns + deploymentSummarySQL) and on the list/metrics
// payloads the dashboard polls.

// hasSucceededDeploy reports whether a bundle has ever been deployed
// successfully. It reads the same three signals as appStatusView's
// neverSucceeded so the tabs and the status pill cannot disagree.
export function hasSucceededDeploy(app) {
  if (!app) return false;
  return (Number(app.deploy_count) || 0) > 0
    || (Number(app.release_number) || 0) > 0
    || !!app.released_at;
}

// hasDeployAttempt reports whether a deployment has ever been ATTEMPTED,
// successful or not. A deployments row is written before the attempt can
// succeed or fail, so any of the deployment-summary fields being populated
// means somebody has already tried.
export function hasDeployAttempt(app) {
  if (!app) return false;
  if (hasSucceededDeploy(app)) return true;
  if (app.last_deployed_at) return true;
  if (app.last_deployment_status) return true;
  // An in-flight deploy the server is still executing: the pending row exists,
  // and a payload captured mid-flight may carry only this flag.
  return !!app.deploying;
}

// awaitingFirstDeploy is the gate for first-deploy onboarding copy: true only
// for an app nobody has ever tried to deploy. An app whose deploy failed is
// NOT awaiting its first deploy - it is broken, and the surfaces that explain
// why (the log viewer, the failure summary) must render for it.
export function awaitingFirstDeploy(app) {
  return !hasDeployAttempt(app);
}

// firstDeployFailed reports an app that has been deployed at least once, has
// never succeeded, and whose newest attempt failed. This is the crash-on-first-
// deploy case: there is no bundle to run, so the normal "current deployment"
// overview would be empty, but there IS a failure to explain.
export function firstDeployFailed(app) {
  if (!app) return false;
  if (hasSucceededDeploy(app)) return false;
  return String(app.last_deployment_status || '').toLowerCase() === 'failed';
}
