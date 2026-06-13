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
  const a = appCardActions({ deploy_count: 2 }, true);
  assert.equal(a.showOpen, true);
  assert.equal(a.deployIsPrimary, false);
  assert.equal(a.showRestart, true);
});

test('a user who cannot manage never sees Restart, even on a deployed app', () => {
  const a = appCardActions({ deploy_count: 2 }, false);
  assert.equal(a.showRestart, false);
});

test('a missing deploy_count is treated as never deployed', () => {
  const a = appCardActions({}, true);
  assert.equal(a.showOpen, false);
  assert.equal(a.deployIsPrimary, true);
});
