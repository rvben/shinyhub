import { test } from 'node:test';
import assert from 'node:assert/strict';
import { appCardActions } from '../static/views/app-card-actions.js';

// renderGridVerbatim (app.js) decides, per app card, whether to show the
// "Open" link, style "Deploy" as the primary CTA, and show the Restart
// kebab. All three are keyed off whether the app has ever successfully
// deployed. deploy_count only increments on a successful deploy (see
// app-card-badge.js), so deploy_count 0 means "never deployed".
//
// This logic previously lived inline in the app.js IIFE and referenced an
// undeclared `neverDeployed`, throwing ReferenceError on the first card and
// aborting the whole grid render. Extracting it here makes it unit-testable.

test('a never-deployed app hides Open, makes Deploy primary, hides Restart', () => {
  const a = appCardActions({ deploy_count: 0 }, true);
  assert.equal(a.showOpen, false);
  assert.equal(a.deployIsPrimary, true);
  assert.equal(a.showRestart, false);
});

test('a deployed app shows Open, de-emphasizes Deploy, shows Restart when manageable', () => {
  const a = appCardActions({ deploy_count: 2, status: 'running' }, true);
  assert.equal(a.showOpen, true);
  assert.equal(a.deployIsPrimary, false);
  assert.equal(a.showRestart, true);
});

test('a user who cannot manage never sees Restart, even on a deployed app', () => {
  const a = appCardActions({ deploy_count: 2, status: 'running' }, false);
  assert.equal(a.showRestart, false);
});

test('a missing deploy_count is treated as never deployed', () => {
  const a = appCardActions({}, true);
  assert.equal(a.showOpen, false);
  assert.equal(a.deployIsPrimary, true);
});

// Sleep, Stop and Start are lifecycle controls, so they depend on what the app
// is doing right now, not only on whether it has ever deployed.
//
// Sleep is multiplex-only: elastic pools (grouped, per_session) hold their live
// backends in a workers map with no replica rows, so the server rejects sleep
// for them with 409. Hiding the item keeps the menu honest rather than offering
// an action that always fails.

test('a running app offers Restart, Sleep and Stop but not Start', () => {
  const a = appCardActions({ deploy_count: 1, status: 'running' }, true);
  assert.equal(a.showRestart, true);
  assert.equal(a.showSleep, true);
  assert.equal(a.showStop, true);
  assert.equal(a.showStart, false);
});

test('a stopped app offers only Start', () => {
  const a = appCardActions({ deploy_count: 1, status: 'stopped' }, true);
  assert.equal(a.showStart, true);
  assert.equal(a.showSleep, false);
  assert.equal(a.showStop, false);
  assert.equal(a.showRestart, false);
});

test('a sleeping app offers only Start', () => {
  const a = appCardActions({ deploy_count: 1, status: 'hibernated' }, true);
  assert.equal(a.showStart, true);
  assert.equal(a.showSleep, false);
  assert.equal(a.showStop, false);
  assert.equal(a.showRestart, false);
});

// A crashed app is down and restartable, so Start is the way back up.
test('a crashed app offers Start', () => {
  const a = appCardActions({ deploy_count: 1, status: 'crashed' }, true);
  assert.equal(a.showStart, true);
  assert.equal(a.showStop, false);
  assert.equal(a.showSleep, false);
});

test('an elastic app hides Sleep but keeps Stop and Restart', () => {
  for (const iso of ['grouped', 'per_session']) {
    const a = appCardActions({ deploy_count: 1, status: 'running', worker_isolation: iso }, true);
    assert.equal(a.showSleep, false, `${iso} must not offer Sleep`);
    assert.equal(a.showStop, true, `${iso} must still offer Stop`);
    assert.equal(a.showRestart, true, `${iso} must still offer Restart`);
  }
});

test('an explicit multiplex app offers Sleep', () => {
  const a = appCardActions({ deploy_count: 1, status: 'running', worker_isolation: 'multiplex' }, true);
  assert.equal(a.showSleep, true);
});

// An app that leaves worker_isolation unset inherits runtime.default_worker_isolation.
// Reading the raw column would see '' and offer Sleep, and every click would 409.
test('an app inheriting an elastic fleet default hides Sleep', () => {
  for (const iso of ['grouped', 'per_session']) {
    const a = appCardActions(
      { deploy_count: 1, status: 'running', worker_isolation: '', effective_worker_isolation: iso },
      true,
    );
    assert.equal(a.showSleep, false, `inherited ${iso} must not offer Sleep`);
    assert.equal(a.showStop, true, `inherited ${iso} must still offer Stop`);
  }
});

// The negative control for the inherit case: an unset column on a default
// server resolves to multiplex, which still sleeps.
test('an app inheriting a multiplex fleet default offers Sleep', () => {
  const a = appCardActions(
    { deploy_count: 1, status: 'running', worker_isolation: '', effective_worker_isolation: 'multiplex' },
    true,
  );
  assert.equal(a.showSleep, true);
});

// An older server sends no effective_worker_isolation at all. The raw column is
// the fallback, so an explicitly elastic app still hides Sleep.
test('an explicitly elastic app hides Sleep without the resolved field', () => {
  const a = appCardActions({ deploy_count: 1, status: 'running', worker_isolation: 'per_session' }, true);
  assert.equal(a.showSleep, false);
});

test('a user who cannot manage sees no lifecycle actions', () => {
  const running = appCardActions({ deploy_count: 1, status: 'running' }, false);
  assert.equal(running.showSleep, false);
  assert.equal(running.showStop, false);
  const stopped = appCardActions({ deploy_count: 1, status: 'stopped' }, false);
  assert.equal(stopped.showStart, false);
});

test('a never-deployed app offers no lifecycle actions even when stopped', () => {
  const a = appCardActions({ deploy_count: 0, status: 'stopped' }, true);
  assert.equal(a.showStart, false);
  assert.equal(a.showStop, false);
  assert.equal(a.showSleep, false);
  assert.equal(a.showRestart, false);
});

// A transitional status is neither up nor down. Offering an action mid-flight
// would race the transition, so the menu shows none.
test('a transitional app offers no lifecycle actions', () => {
  for (const status of ['deploying', 'waking', 'deleting']) {
    const a = appCardActions({ deploy_count: 1, status }, true);
    assert.equal(a.showSleep, false, `${status} must not offer Sleep`);
    assert.equal(a.showStop, false, `${status} must not offer Stop`);
    assert.equal(a.showStart, false, `${status} must not offer Start`);
    assert.equal(a.showRestart, false, `${status} must not offer Restart`);
  }
});

// A degraded app is partly up: some replicas are serving, so Stop and Sleep
// still apply and Start does not.
test('a degraded app offers Restart, Sleep and Stop', () => {
  const a = appCardActions({ deploy_count: 1, status: 'degraded' }, true);
  assert.equal(a.showRestart, true);
  assert.equal(a.showSleep, true);
  assert.equal(a.showStop, true);
  assert.equal(a.showStart, false);
});

// A deploy in flight never reaches the status column. The server leaves the
// stored status stale ("running" on a redeploy, "stopped" on a first deploy)
// and reports the transition through the transient `deploying` flag, which is
// what appStatusView already reads to render the "Deploying" badge. The menu
// has to read the same flag, or a card labelled "Deploying" offers Sleep and
// Stop over a deploy that is still running.
test('a redeploying app offers no lifecycle actions', () => {
  const a = appCardActions({ deploy_count: 5, status: 'running', deploying: true }, true);
  assert.equal(a.showRestart, false);
  assert.equal(a.showSleep, false);
  assert.equal(a.showStop, false);
  assert.equal(a.showStart, false);
});

// A first deploy leaves the stored status at "stopped", which would otherwise
// offer Start while the deploy is mid-flight.
test('an app deploying for the first time does not offer Start', () => {
  const a = appCardActions({ deploy_count: 1, status: 'stopped', deploying: true }, true);
  assert.equal(a.showStart, false);
});

// The negative control for both: the same app with the flag cleared keeps its
// full menu, so neither test above can be passing on an unreachable input.
test('the same app regains its menu once the deploy finishes', () => {
  const a = appCardActions({ deploy_count: 5, status: 'running', deploying: false }, true);
  assert.equal(a.showRestart, true);
  assert.equal(a.showSleep, true);
  assert.equal(a.showStop, true);
});

// deploy_count is denormalized: the deploy handler increments it with
// log-and-continue, so a transient DB error leaves a deployed, running app at
// deploy_count 0. Gating the lifecycle menu on the counter alone would hide
// Start on exactly that app once it was stopped, with no way back up from the
// dashboard. The durable deployments row is the signal that matters.
test('a stopped app whose deploy_count increment was lost still offers Start', () => {
  const a = appCardActions(
    { deploy_count: 0, status: 'stopped', last_deployment_status: 'succeeded' },
    true,
  );
  assert.equal(a.showStart, true);
});

test('a running app whose deploy_count increment was lost still offers Sleep and Stop', () => {
  const a = appCardActions(
    { deploy_count: 0, status: 'running', last_deployment_status: 'succeeded' },
    true,
  );
  assert.equal(a.showSleep, true);
  assert.equal(a.showStop, true);
  assert.equal(a.showRestart, true);
});

// The negative control: a genuinely never-deployed app has no deployment row at
// all, so it still offers nothing. Without this the test above would pass on an
// implementation that simply dropped the never-deployed gate.
test('an app with no deployment row offers no lifecycle actions', () => {
  const a = appCardActions({ deploy_count: 0, status: 'stopped' }, true);
  assert.equal(a.showStart, false);
  assert.equal(a.showStop, false);
});

// A failed first deploy leaves a row behind, but no bundle ever ran. Starting it
// would fail, so the menu must stay empty.
test('an app whose only deployment failed offers no lifecycle actions', () => {
  const a = appCardActions(
    { deploy_count: 0, status: 'stopped', last_deployment_status: 'failed' },
    true,
  );
  assert.equal(a.showStart, false);
  assert.equal(a.showStop, false);
});

// A missing status must not be read as a live app: acting on it would race
// whatever the server actually has.
test('a missing status offers no lifecycle actions', () => {
  const a = appCardActions({ deploy_count: 1 }, true);
  assert.equal(a.showRestart, false);
  assert.equal(a.showSleep, false);
  assert.equal(a.showStop, false);
  assert.equal(a.showStart, false);
});
