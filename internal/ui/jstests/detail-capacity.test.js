import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import axe from 'axe-core';
import { seedDetailCapacity, renderDetailCapacity, markDetailCapacityError } from '../static/views/detail-capacity.js';

const fixture = () => new JSDOM(readFileSync(new URL('../static/index.html', import.meta.url), 'utf8'), { pretendToBeVisual: true, runScripts: 'outside-only' });
const metrics = { status: 'running', metrics_available: true, replicas_desired: 2, cpu_cores: 1,
  cpu_capacity_cores: 8, cpu_host_cores: 8, cpu_capacity_source: 'host',
  replicas: [0, 1].map(index => ({ index, status: 'running', cpu_percent: 50, rss_bytes: 1024, sessions: 2 })) };
const off = { enabled: false };

test('first failure does not invent running measurements or last-known data', () => {
  const doc = fixture().window.document;
  seedDetailCapacity(doc, { replicas: 2 });
  assert.equal(doc.getElementById('app-detail-replicas').textContent, '—');
  assert.match(doc.getElementById('app-detail-metrics-message').textContent, /Loading/);
  markDetailCapacityError(doc);
  assert.equal(doc.querySelector('.app-detail-capacity').dataset.metricsState, 'unavailable');
  assert.match(doc.getElementById('app-detail-metrics-message').textContent, /No measurements received/);
  assert.doesNotMatch(doc.getElementById('app-detail-metrics-message').textContent, /last known/);
  assert.equal(doc.getElementById('app-detail-metrics-retry').hidden, false);
});

test('failed refresh marks the entire capacity surface stale and retains its timestamp', () => {
  const doc = fixture().window.document;
  seedDetailCapacity(doc, { replicas: 2 });
  renderDetailCapacity(doc, metrics, 2, off, 1720000000000);
  const updated = doc.getElementById('app-detail-metrics-updated').textContent;
  markDetailCapacityError(doc);
  assert.equal(doc.querySelector('.app-detail-capacity').dataset.metricsState, 'stale');
  assert.match(doc.getElementById('app-detail-metrics-message').textContent, /last known measurements/);
  assert.equal(doc.getElementById('app-detail-metrics-updated').textContent, updated);
  doc.getElementById('app-detail-metrics-retry').focus();
  renderDetailCapacity(doc, metrics, 2, off, 1720000010000);
  assert.equal(doc.querySelector('.app-detail-capacity').dataset.metricsState, 'current');
  assert.equal(doc.getElementById('app-detail-metrics-status').hidden, false);
  assert.equal(doc.getElementById('app-detail-metrics-message').textContent, 'Live metrics updated.');
  assert.equal(doc.activeElement.tagName, 'SUMMARY');
});

test('elastic workers use their worker limit and omit desired multiplex counts', () => {
  const doc = fixture().window.document;
  seedDetailCapacity(doc, { replicas: 1, effective_worker_isolation: 'grouped' });
  assert.equal(doc.getElementById('app-detail-capacity-label').textContent, 'Workers');
  assert.equal(doc.getElementById('app-detail-replica-desired').hidden, true);
  renderDetailCapacity(doc, { ...metrics, worker_isolation: 'grouped', workers_running: 3, max_workers: 10, replicas_desired: 1 }, 1, off);
  assert.equal(doc.getElementById('app-detail-replicas').textContent, '3');
  assert.equal(doc.getElementById('app-detail-replica-desired').textContent, '10 worker limit');
  assert.equal(doc.getElementById('app-detail-capacity-details-label').textContent, 'Inspect worker CPU');
  assert.match(doc.querySelector('.cpu-heat-cell').getAttribute('aria-label'), /Worker #0/);
});

test('expanded capacity diagnostics have no automated semantic accessibility violations', async () => {
  const dom = fixture();
  const doc = dom.window.document;
  doc.getElementById('app-detail-view').hidden = false;
  seedDetailCapacity(doc, { replicas: 2 });
  renderDetailCapacity(doc, metrics, 2, off);
  doc.getElementById('app-detail-capacity-details').open = true;
  dom.window.eval(axe.source);
  const result = await dom.window.axe.run(doc.querySelector('.app-detail-capacity'), { rules: { 'color-contrast': { enabled: false } } });
  assert.equal(result.violations.length, 0, JSON.stringify(result.violations.map(v => ({ id: v.id, nodes: v.nodes.map(n => n.target) }))));
});


test('disabled retry focus is restored after the browser drops focus during the request', () => {
  const doc = fixture().window.document;
  seedDetailCapacity(doc, { replicas: 2 });
  markDetailCapacityError(doc);
  const retry = doc.getElementById('app-detail-metrics-retry');
  retry.dataset.restoreFocus = 'true';
  retry.disabled = true;
  renderDetailCapacity(doc, metrics, 2, off);
  assert.equal(doc.activeElement.tagName, 'SUMMARY');
  assert.equal(doc.getElementById('app-detail-metrics-message').textContent, 'Live metrics updated.');
});
