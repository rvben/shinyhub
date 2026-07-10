import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  appCardBadge,
  updateCardStatusBadge,
  updateStatusPill,
  appStatusView,
} from '../static/views/app-card-badge.js';

// Stub for app.js's formatStatus so the helper stays pure and testable.
const fmt = (s) => `S:${s}`;

// appStatusView is the shared status decision the card badge AND the detail-
// header pill both consume, so the same app cannot read "Failed" on its card
// while reading "Awaiting deploy" on its detail page.
test('appStatusView reports a never-deployed crash-looped app as failed, not new', () => {
  const v = appStatusView(
    { deploy_count: 0, last_deployment_status: 'failed', status: 'stopped' },
    fmt,
  );
  assert.deepEqual(v, { state: 'failed', text: 'Failed' });
});

test('appStatusView reports a never-deployed app as awaiting its first deploy', () => {
  const v = appStatusView({ deploy_count: 0, last_deployment_status: '', status: 'stopped' }, fmt);
  assert.deepEqual(v, { state: 'new', text: 'Awaiting deploy' });
});

test('appStatusView reports a deployed app by its live status', () => {
  const v = appStatusView({ deploy_count: 3, status: 'hibernated' }, fmt);
  assert.deepEqual(v, { state: 'hibernated', text: 'S:hibernated' });
});

// The server sets `deploying` only while a deployment is actively executing
// (pending row + held deploy lock), so it outranks every other state: a first
// deploy must read "Deploying", not "Awaiting deploy"; a redeploy must read
// "Deploying", not the stale "Running"; a fix for a failed attempt must read
// "Deploying", not "Failed".
test('appStatusView: deploying outranks awaiting-deploy on a first deploy', () => {
  const v = appStatusView(
    { deploying: true, deploy_count: 0, last_deployment_status: 'pending', status: 'stopped' },
    fmt,
  );
  assert.deepEqual(v, { state: 'deploying', text: 'Deploying' });
});

test('appStatusView: deploying outranks the stale live status on a redeploy', () => {
  const v = appStatusView({ deploying: true, deploy_count: 4, status: 'running' }, fmt);
  assert.deepEqual(v, { state: 'deploying', text: 'Deploying' });
});

test('appStatusView: deploying outranks failed while the retry deploys', () => {
  const v = appStatusView(
    { deploying: true, deploy_count: 0, last_deployment_status: 'pending', status: 'stopped' },
    fmt,
  );
  assert.equal(v.state, 'deploying');
});

// A status badge as renderGridVerbatim builds it: a span with the badge classes
// plus the data-slug the metrics poll uses to locate it.
function badgeSpan(app) {
  const doc = new JSDOM('<!DOCTYPE html><span></span>').window.document;
  const el = doc.querySelector('span');
  const info = appCardBadge(app, fmt);
  el.className = info.cls;
  el.textContent = info.text;
  el.dataset.slug = app.slug;
  return el;
}

test('a failed-only deploy badges as Failed, not Awaiting deploy', () => {
  const b = appCardBadge(
    { deploy_count: 0, last_deployment_status: 'failed', status: 'stopped' },
    fmt,
  );
  assert.equal(b.text, 'Failed');
  assert.equal(b.cls, 'badge badge-failed');
});

test('a never-deployed app badges as Awaiting deploy', () => {
  const b = appCardBadge(
    { deploy_count: 0, last_deployment_status: '', status: 'stopped' },
    fmt,
  );
  assert.equal(b.text, 'Awaiting deploy');
  assert.equal(b.cls, 'badge badge-new');
});

test('a successfully deployed app uses its live status', () => {
  const b = appCardBadge({ deploy_count: 2, status: 'running' }, fmt);
  assert.equal(b.text, 'S:running');
  assert.equal(b.cls, 'badge badge-running');
});

test('a later failed deploy on a live app keeps the live status', () => {
  const b = appCardBadge(
    { deploy_count: 1, last_deployment_status: 'failed', status: 'running' },
    fmt,
  );
  assert.equal(b.text, 'S:running');
});

test('updateCardStatusBadge refreshes a stale badge from a fresh poll status', () => {
  // Card opened while the app was hibernating (stopped); the wake transition
  // arrives via the next /metrics tick as running.
  const app = { slug: 'demo', deploy_count: 3, status: 'stopped' };
  const el = badgeSpan(app);
  assert.equal(el.textContent, 'S:stopped');
  assert.equal(el.className, 'badge badge-stopped');

  updateCardStatusBadge(el, app, { status: 'running' }, fmt);

  assert.equal(el.textContent, 'S:running');
  assert.equal(el.className, 'badge badge-running');
  // The live model is updated too, so a later re-render carries the fresh status.
  assert.equal(app.status, 'running');
  // The data-slug locator survives the className rewrite.
  assert.equal(el.dataset.slug, 'demo');
});

test('updateCardStatusBadge never relabels a never-deployed app from a poll', () => {
  // A poll reporting "stopped" must not turn "Awaiting deploy" into "Stopped".
  const app = { slug: 'fresh', deploy_count: 0, last_deployment_status: '', status: 'stopped' };
  const el = badgeSpan(app);
  assert.equal(el.textContent, 'Awaiting deploy');

  updateCardStatusBadge(el, app, { status: 'stopped', deploying: false }, fmt);

  assert.equal(el.textContent, 'Awaiting deploy');
  assert.equal(el.className, 'badge badge-new');
});

test('updateCardStatusBadge is a no-op on a missing badge element', () => {
  const app = { slug: 'demo', deploy_count: 1, status: 'running' };
  assert.doesNotThrow(() => updateCardStatusBadge(null, app, { status: 'stopped' }, fmt));
  // The model is left untouched when there is no element to update.
  assert.equal(app.status, 'running');
});

test('updateCardStatusBadge flips the badge to Deploying and back over a redeploy', () => {
  const app = { slug: 'demo', deploy_count: 2, status: 'running' };
  const el = badgeSpan(app);
  assert.equal(el.textContent, 'S:running');

  // Mid-window tick: the pool is torn down (status stale) but a deploy runs.
  updateCardStatusBadge(el, app, { status: 'running', deploying: true }, fmt);
  assert.equal(el.textContent, 'Deploying');
  assert.equal(el.className, 'badge badge-deploying');

  // Deploy finished: the next tick clears the flag and reports running.
  updateCardStatusBadge(el, app, { status: 'running', deploying: false }, fmt);
  assert.equal(el.textContent, 'S:running');
  assert.equal(el.className, 'badge badge-running');
});

test('a first deploy observed via polls ends on Running, not Awaiting deploy', () => {
  // CLI-driven first deploy: the grid model still has deploy_count 0. The
  // badge must go Awaiting deploy -> Deploying -> Running; falling back to
  // "Awaiting deploy" after a deploy the poll just watched succeed would be
  // false (a running app has, by definition, a succeeded deploy).
  const app = { slug: 'fresh', deploy_count: 0, last_deployment_status: '', status: 'stopped' };
  const el = badgeSpan(app);
  assert.equal(el.textContent, 'Awaiting deploy');

  updateCardStatusBadge(el, app, { status: 'stopped', deploying: true }, fmt);
  assert.equal(el.textContent, 'Deploying');

  updateCardStatusBadge(el, app, { status: 'running', deploying: false }, fmt);
  assert.equal(el.textContent, 'S:running');
  assert.equal(el.className, 'badge badge-running');
});

test('a FAILED first deploy observed via polls ends on Failed, not Awaiting deploy', () => {
  // The poll carries last_deployment_status so a watched failure surfaces:
  // reverting to "Awaiting deploy" would suggest the deploy never happened.
  const app = { slug: 'fresh', deploy_count: 0, last_deployment_status: '', status: 'stopped' };
  const el = badgeSpan(app);
  assert.equal(el.textContent, 'Awaiting deploy');

  updateCardStatusBadge(
    el, app,
    { status: 'stopped', deploying: true, last_deployment_status: 'pending' },
    fmt,
  );
  assert.equal(el.textContent, 'Deploying');

  updateCardStatusBadge(
    el, app,
    { status: 'stopped', deploying: false, last_deployment_status: 'failed' },
    fmt,
  );
  assert.equal(el.textContent, 'Failed');
  assert.equal(el.className, 'badge badge-failed');
});

test('updateStatusPill keeps the detail header pill on the same lifecycle', () => {
  const doc = new JSDOM('<!DOCTYPE html><span id="pill"></span>').window.document;
  const pill = doc.getElementById('pill');
  const app = { slug: 'demo', deploy_count: 2, status: 'running' };

  updateStatusPill(pill, app, { status: 'running', deploying: true }, fmt);
  assert.equal(pill.textContent, 'Deploying');
  assert.equal(pill.className, 'status-pill status-deploying');

  updateStatusPill(pill, app, { status: 'running', deploying: false }, fmt);
  assert.equal(pill.textContent, 'S:running');
  // Running gets the is-live pulse, matching statusPillClass.
  assert.equal(pill.className, 'status-pill status-running is-live');
});
