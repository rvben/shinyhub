import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { releaseStripModel, renderReleaseStrip } from '../static/views/release-strip.js';
import { deploymentTimeModel } from '../static/views/deployment-row.js';

// The release strip is the single place on the app detail page that says what
// is live: version, when it was deployed (exact and relative), who or what
// deployed it, and how many deploys the app has had.

const NOW = Date.UTC(2026, 8, 23, 15, 30, 0);
const DEPLOYED_AT = '2026-09-23T15:27:00Z';

function doc() {
  return new JSDOM('<!DOCTYPE html><body><div id="strip"></div></body>').window.document;
}

function app(overrides = {}) {
  return {
    slug: 'demo',
    deploy_count: 1,
    release_number: 1,
    released_version: '1790170029453',
    released_at: DEPLOYED_AT,
    deployment_provenance: { origin: { kind: 'direct', channel: 'cli', actor: 'admin' } },
    ...overrides,
  };
}

const CI_PROVENANCE = {
  run_id: '0123456789abcdef0123456789abcdef',
  fleet_id: 'prod-eu',
  metadata: {
    provider: 'gitlab',
    source: { label: 'GitLab pipeline #412', url: 'https://gitlab.example/pipelines/412' },
    revision: { sha: 'abcdef1234567890', ref: 'main' },
    change: { label: 'MR !87', url: 'https://gitlab.example/mr/87' },
  },
};

function render(model, markFor) {
  const d = doc();
  const host = d.getElementById('strip');
  renderReleaseStrip(d, host, model, { markFor });
  return host;
}

test('an app that has never been deployed has no release strip', () => {
  assert.equal(releaseStripModel(app({ deploy_count: 0, release_number: null, released_at: null }), NOW), null);
  assert.equal(releaseStripModel(null, NOW), null);

  const host = render(null);
  assert.equal(host.hidden, true);
  assert.equal(host.childNodes.length, 0);
});

test('a CLI deploy reads as one sentence with the exact time, age, and actor', () => {
  const model = releaseStripModel(app(), NOW);
  assert.equal(model.versionLabel, 'v1');
  assert.equal(model.versionTitle, 'bundle 1790170029453');
  assert.equal(model.verb, 'Deployed');
  assert.equal(model.countLabel, '1 deploy');
  assert.equal(model.historyHref, '/apps/demo/deployments');

  const host = render(model);
  const expected = deploymentTimeModel(DEPLOYED_AT, NOW);
  assert.equal(host.hidden, false);
  assert.equal(host.querySelector('.release-version').textContent, 'v1');
  assert.equal(host.querySelector('.release-version').title, 'bundle 1790170029453');
  const time = host.querySelector('time');
  assert.equal(time.getAttribute('datetime'), expected.datetime);
  assert.equal(time.textContent, expected.absolute);
  assert.equal(
    host.querySelector('.release-summary').textContent.replace(/\s+/g, ' ').trim(),
    `Deployed ${expected.absolute} (3m ago) via ShinyHub CLI by admin`,
  );
  const history = host.querySelector('a.release-history');
  assert.equal(history.getAttribute('href'), '/apps/demo/deployments');
  assert.ok(history.hasAttribute('data-nav'), 'history link routes through the SPA router');
  assert.equal(history.textContent.replace(/\s+/g, ' ').trim(), '1 deploy · History');
});

test('the deploy source is never repeated inside the strip', () => {
  const host = render(releaseStripModel(app(), NOW));
  const matches = host.textContent.match(/ShinyHub CLI/g) || [];
  assert.equal(matches.length, 1);
});

test('a CI deploy links the pipeline and shows the revision and change once', () => {
  const host = render(releaseStripModel(app({ deploy_count: 14, release_number: 14, deployment_provenance: CI_PROVENANCE }), NOW));
  const summary = host.querySelector('.release-summary');
  const links = [...summary.querySelectorAll('a')];
  assert.deepEqual(links.map(a => [a.textContent, a.getAttribute('href')]), [
    ['GitLab pipeline #412', 'https://gitlab.example/pipelines/412'],
    ['MR !87', 'https://gitlab.example/mr/87'],
  ]);
  for (const a of links) {
    assert.equal(a.getAttribute('target'), '_blank');
    assert.match(a.getAttribute('rel'), /noopener/);
  }
  assert.match(summary.textContent.replace(/\s+/g, ' '), /\(3m ago\) by GitLab pipeline #412 abcdef12 · main · MR !87$/);
  assert.equal(host.querySelector('.release-revision').textContent, 'abcdef12 · main');
  assert.equal(host.querySelector('a.release-history').textContent.replace(/\s+/g, ' ').trim(), '14 deploys · History');
  assert.equal(host.textContent.includes('Open pipeline'), false, 'the pipeline label is already the link');
});

test('a rollback reads as a rollback, not a deploy', () => {
  const model = releaseStripModel(app({
    release_number: 4,
    deployment_provenance: { origin: { kind: 'rollback', channel: 'dashboard', actor: 'admin' } },
  }), NOW);
  assert.equal(model.verb, 'Rolled back');
  const host = render(model);
  assert.match(host.querySelector('.release-summary').textContent.replace(/\s+/g, ' ').trim(), /^Rolled back .+ \(3m ago\) by admin$/);
});

test('a deploy whose source was not recorded still shows version and time, without a source', () => {
  const host = render(releaseStripModel(app({ deployment_provenance: null }), NOW));
  assert.equal(host.hidden, false);
  assert.equal(host.querySelector('.release-source'), null);
  assert.equal(host.textContent.includes('Source not recorded'), false);
  assert.match(host.querySelector('.release-summary').textContent.replace(/\s+/g, ' ').trim(), /^Deployed .+ \(3m ago\)$/);
});

test('missing release number and timestamp are omitted, not rendered as placeholders', () => {
  const model = releaseStripModel(app({ release_number: null, released_at: null, last_deployed_at: null }), NOW);
  assert.equal(model.versionLabel, '');
  assert.equal(model.time, null);
  const host = render(model);
  assert.equal(host.querySelector('.release-version'), null);
  assert.equal(host.querySelector('time'), null);
  assert.equal(host.textContent.includes('\u2014'), false);
  assert.equal(host.textContent.includes('undefined'), false);
  assert.match(host.querySelector('.release-summary').textContent.replace(/\s+/g, ' ').trim(), /^Deployed via ShinyHub CLI by admin$/);
});

test('last_deployed_at is the fallback when released_at is absent', () => {
  const model = releaseStripModel(app({ released_at: null, last_deployed_at: DEPLOYED_AT }), NOW);
  assert.equal(model.time.datetime, new Date(DEPLOYED_AT).toISOString());
});

test('the provider mark comes from the injected builder and sits with the source', () => {
  const seen = [];
  const d = doc();
  const host = d.getElementById('strip');
  renderReleaseStrip(d, host, releaseStripModel(app(), NOW), {
    markFor: (provenance) => {
      seen.push(provenance.mark);
      const el = d.createElement('span');
      el.className = 'test-mark';
      return el;
    },
  });
  assert.deepEqual(seen, ['CLI']);
  const mark = host.querySelector('.release-source > .test-mark');
  assert.ok(mark);
  assert.equal(mark.getAttribute('aria-hidden'), 'true', 'the sentence already names the source');
});

test('re-rendering an unchanged release keeps the existing nodes', () => {
  const d = doc();
  const host = d.getElementById('strip');
  renderReleaseStrip(d, host, releaseStripModel(app(), NOW));
  const before = host.querySelector('time');
  renderReleaseStrip(d, host, releaseStripModel(app(), NOW));
  assert.equal(host.querySelector('time'), before, 'a background refresh must not drop a selection inside the strip');
  renderReleaseStrip(d, host, releaseStripModel(app({ deploy_count: 2, release_number: 2 }), NOW));
  assert.notEqual(host.querySelector('time'), before);
  assert.equal(host.querySelector('.release-version').textContent, 'v2');
});
