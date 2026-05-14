import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  formatManifestSummary,
  renderDeployResult,
} from '../static/deploy-summary.js';

test('formatManifestSummary returns [] for absent or non-object manifest', () => {
  assert.deepEqual(formatManifestSummary(null), []);
  assert.deepEqual(formatManifestSummary(undefined), []);
  assert.deepEqual(formatManifestSummary('not-an-object'), []);
  assert.deepEqual(formatManifestSummary({}), []);
});

test('formatManifestSummary renders [app] settings deterministically', () => {
  const lines = formatManifestSummary({
    app: { replicas: 2, hibernate_timeout_minutes: 15 },
  });
  assert.deepEqual(lines, [
    'Applied [app] settings: hibernate_timeout_minutes=15; replicas=2',
  ]);
});

test('formatManifestSummary renders null values as "default"', () => {
  const lines = formatManifestSummary({ app: { memory_limit_mb: null } });
  assert.deepEqual(lines, ['Applied [app] settings: memory_limit_mb=default']);
});

test('formatManifestSummary counts schedule actions', () => {
  const lines = formatManifestSummary({
    schedules: [
      { name: 'a', action: 'created' },
      { name: 'b', action: 'updated' },
      { name: 'c', action: 'updated' },
    ],
  });
  assert.deepEqual(lines, ['Schedules: 1 created, 2 updated']);
});

test('formatManifestSummary combines app + schedules', () => {
  const lines = formatManifestSummary({
    app: { replicas: 3 },
    schedules: [{ name: 'nightly', action: 'created' }],
  });
  assert.deepEqual(lines, [
    'Applied [app] settings: replicas=3',
    'Schedules: 1 created, 0 updated',
  ]);
});

test('formatManifestSummary skips empty app object and empty schedules array', () => {
  assert.deepEqual(formatManifestSummary({ app: {}, schedules: [] }), []);
});

test('renderDeployResult populates list and unhides container', () => {
  const dom = new JSDOM(`
    <div id="result" hidden>
      <ul id="list"></ul>
    </div>
  `);
  const { document } = dom.window;
  const container = document.getElementById('result');
  const list = document.getElementById('list');

  renderDeployResult(container, list, ['line one', 'line two']);

  assert.equal(container.hidden, false);
  const items = list.querySelectorAll('li');
  assert.equal(items.length, 2);
  assert.equal(items[0].textContent, 'line one');
  assert.equal(items[1].textContent, 'line two');
});

test('renderDeployResult replaces existing content on subsequent calls', () => {
  const dom = new JSDOM(`
    <div id="result" hidden>
      <ul id="list"><li>stale</li></ul>
    </div>
  `);
  const { document } = dom.window;
  const container = document.getElementById('result');
  const list = document.getElementById('list');

  renderDeployResult(container, list, ['fresh']);

  const items = list.querySelectorAll('li');
  assert.equal(items.length, 1);
  assert.equal(items[0].textContent, 'fresh');
});

test('renderDeployResult with empty lines clears list and still unhides', () => {
  const dom = new JSDOM(`
    <div id="result" hidden>
      <ul id="list"><li>stale</li></ul>
    </div>
  `);
  const { document } = dom.window;
  const container = document.getElementById('result');
  const list = document.getElementById('list');

  renderDeployResult(container, list, []);

  assert.equal(container.hidden, false);
  assert.equal(list.querySelectorAll('li').length, 0);
});
