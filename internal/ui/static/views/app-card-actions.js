// appCardActions decides which action controls an app card shows in the grid.
//
// Two independent inputs drive it.
//
// deploy_count only increments on a successful deploy (see app-card-badge.js),
// so an app with no successful deployment has nothing to open yet (the
// /app/<slug>/ URL would 404). The grid hides "Open" and shows a direct
// first-deploy action only in that state. Once a bundle exists, redeploy moves
// into the overflow menu: it remains available without looking like unfinished
// setup on every card.
//
// Whether any LIFECYCLE action applies is a separate question, and it is not
// answered by the counter. deploy_count is denormalized and the deploy handler
// increments it with log-and-continue, so a transient DB error can leave a
// deployed, running app reading 0. The durable deployments row decides instead;
// the counter is only the fallback for an older server's payload.
//
// status decides which lifecycle actions apply. An app that is up can be
// restarted, slept or stopped; an app that is down can be started. A
// transitional status (waking, deleting) offers none, because acting mid-flight
// would race the transition, and so does a missing status, which must not be
// read as "live".
//
// A deploy in flight is the transition that never reaches the status column:
// the server leaves the stored status stale ("running" on a redeploy, "stopped"
// on a first deploy) and signals it through the transient `deploying` flag,
// which is what appStatusView reads to render the "Deploying" badge. So the
// flag has to be read here too. Reading only `status` put Sleep and Stop under
// a card that said "Deploying".
//
// Sleep is multiplex-only. Elastic pools (grouped, per_session) keep their live
// backends in a workers map with no replica rows, so the server rejects sleep
// for them with 409; hiding the item keeps the menu honest. Stop and Restart
// still apply, since both tear the whole pool down.
//
// The isolation read is effective_worker_isolation, not worker_isolation. The
// raw column is empty when the app inherits runtime.default_worker_isolation,
// so on an elastic-by-default server reading it raw would advertise Sleep on
// every inheriting app and every click would 409.
//
// Kept DOM-free so it is unit-testable; the caller (renderGridVerbatim in
// app.js) builds the DOM from these booleans.

// Isolation modes whose pools have no replica rows. Mirrors isElasticIsolation
// in internal/lifecycle/elastic.go.
const ELASTIC_ISOLATION = new Set(['grouped', 'per_session']);

// Statuses from which lifecycle actions that assume a live pool apply.
// "degraded" is included because some replicas may still be serving. "idle"
// is the healthy empty state of an elastic pool: there is no worker to sleep,
// but restarting or stopping the configured pool is still meaningful.
const UP_STATUSES = new Set(['running', 'degraded', 'idle']);

// Statuses from which an app can be started back up. "crashed" is included: the
// app is down and a start is the way back.
const DOWN_STATUSES = new Set(['stopped', 'hibernated', 'crashed']);

export function appCardActions(app, canManage) {
  const neverDeployed = (app.deploy_count || 0) === 0;
  // Lifecycle eligibility keys off the durable deployments row rather than the
  // denormalized counter. deploy_count's post-deploy increment is log-and-continue
  // (see the deploy handler), so a transient DB error there leaves a deployed,
  // running app reading deploy_count 0. Gating Start on the counter alone would
  // then hide the only way to bring that app back up once it is stopped.
  // last_deployment_status is the status of the newest deployment row, so
  // "succeeded" proves a bundle was deployed even when the counter missed it.
  // The counter stays as the fallback for a payload from an older server.
  const hasDeployment = (Number(app.release_number) || 0) > 0
    || !!app.released_at
    || app.last_deployment_status === 'succeeded'
    || !neverDeployed;
  const manageable = !!canManage && hasDeployment && !app.deploying;
  const status = app.status || '';
  const isUp = UP_STATUSES.has(status);
  const isDown = DOWN_STATUSES.has(status);
  // Falls back to the raw column so a card served by an older server (which
  // does not send the resolved field) still hides Sleep for an explicitly
  // elastic app.
  const isolation = app.effective_worker_isolation || app.worker_isolation || '';
  const isElastic = ELASTIC_ISOLATION.has(isolation);

  return {
    showOpen: hasDeployment,
    deployIsPrimary: !hasDeployment,
    showRedeploy: !!canManage && hasDeployment && !app.deploying,
    showRestart: manageable && isUp,
    showSleep: manageable && isUp && !isElastic,
    showStop: manageable && isUp,
    showStart: manageable && isDown,
  };
}
