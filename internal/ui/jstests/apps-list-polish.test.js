import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { appCardActions } from '../static/views/app-card-actions.js';
import { appListSummary } from '../static/views/app-card-badge.js';
import { summariseFleetHealth, degradedTooltip, activationAttentionTooltip } from '../static/views/fleet-health.js';

const source = readFileSync(new URL('../static/app.js', import.meta.url), 'utf8');
const fmt = status => status === 'hibernated' ? 'Sleeping' : status;
const deployed = status => ({ slug: 'sales', status, release_number: 1, deploy_count: 1 });

function shippedFunction(name, next, scope) {
  const start = source.indexOf(`  function ${name}(`);
  const end = source.indexOf(`  ${next}`, start);
  assert.ok(start >= 0 && end > start);
  return new Function(...Object.keys(scope), source.slice(start, end) + `\nreturn ${name};`)(...Object.values(scope));
}

test('list accounts for sleeping, stopped and undeployed apps without claiming health', () => {
  const apps = [deployed('hibernated'), deployed('hibernated'), deployed('stopped'),
    { status: 'stopped' }, { status: 'stopped' }];
  assert.equal(appListSummary(apps, fmt), '2 awaiting deploy · 2 sleeping · 1 stopped');
  assert.equal(appListSummary([apps[2]], fmt), '1 stopped', 'Filtered counts describe the filtered apps');
  assert.equal(appListSummary([], fmt), '');
});

test('summary preserves failed-first and deploying states, including retained releases', () => {
  assert.equal(appListSummary([
    { status: 'stopped', last_deployment_status: 'failed' },
    { status: 'stopped', deploying: true },
    { ...deployed('running'), last_deployment_status: 'failed' },
  ], fmt), '1 deploying · 1 failed · 1 running');
});

for (const status of ['stopped', 'crashed']) {
  test(`${status} app offers managers Start and viewers an overview, never a stopped URL`, () => {
    const manager = appCardActions(deployed(status), true);
    assert.equal(manager.startIsPrimary, true);
    assert.equal(manager.showOpen, false);
    assert.equal(manager.showOverview, false);
    const viewer = appCardActions(deployed(status), false);
    assert.equal(viewer.startIsPrimary, false);
    assert.equal(viewer.showOpen, false);
    assert.equal(viewer.showOverview, true);
  });
}

test('sleeping apps remain wakeable; in-flight deployments offer an overview', () => {
  assert.equal(appCardActions(deployed('hibernated'), true).showOpen, true);
  const pending = appCardActions({ ...deployed('running'), deploying: true }, true);
  assert.equal(pending.showOpen, false);
  assert.equal(pending.startIsPrimary, false);
  assert.equal(pending.showOverview, true);
});

test('live card changes replace the primary action and retain keyboard focus', t => {
  const dom = new JSDOM('<div class="app-card"><div class="app-actions"><a href="/app/sales/" data-card-primary="open">Open app</a><button data-card-primary="start">Start app</button><a href="/apps/sales" data-card-primary="overview">View app</a></div></div>', { pretendToBeVisual: true });
  t.after(() => dom.window.close());
  const card = dom.window.document.querySelector('.app-card');
  const sync = shippedFunction('syncCardPrimaryActions', 'function applyRestartFeedbackToCard', {
    appCardActions, state: { user: {} }, canManageApp: () => true,
  });
  const open = card.querySelector('[data-card-primary="open"]');
  const start = card.querySelector('[data-card-primary="start"]');
  sync(card, deployed('stopped'));
  assert.equal(open.hidden, true);
  assert.equal(start.hidden, false);
  start.focus();
  sync(card, deployed('running'));
  assert.equal(start.hidden, true);
  assert.equal(open.hidden, false);
  assert.equal(dom.window.document.activeElement, open);
  sync(card, { ...deployed('running'), deploying: true });
  assert.equal(dom.window.document.activeElement.dataset.cardPrimary, 'overview');
});

test('healthy infrastructure does not label undeployed apps healthy; real incidents remain visible', t => {
  const dom = new JSDOM('<div id="fleet-health"></div>');
  t.after(() => dom.window.close());
  const document = dom.window.document;
  const fleetHealthEl = document.getElementById('fleet-health');
  const render = shippedFunction('renderFleetHealth', 'async function loadApps', {
    document, fleetHealthEl, degradedTooltip, activationAttentionTooltip,
  });
  render(summariseFleetHealth({ apps: { total: 5, running: 0 } }));
  assert.equal(fleetHealthEl.hidden, true);
  render(summariseFleetHealth({ apps: { total: 5, running: 0 }, workers: { total: 1, up: 0, down: 1 } }));
  assert.equal(fleetHealthEl.hidden, false);
  assert.match(fleetHealthEl.textContent, /warning/);
  render(summariseFleetHealth({ complete: false, unavailable_components: ['workers'] }));
  assert.equal(fleetHealthEl.hidden, false);
  assert.match(fleetHealthEl.textContent, /unknown/);
});
