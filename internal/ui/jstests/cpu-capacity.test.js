import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import { renderReplicaHeat, updateCPUMeter } from '../static/views/cpu-capacity.js';

test('replica diagnostics use a native disclosure available from every app tab', () => {
  const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
  const doc = new JSDOM(html).window.document;
  const strip = doc.getElementById('app-detail-cpu-heat');
  const details = strip.closest('details');
  assert.ok(details.closest('.app-detail-header'));
  assert.equal(details.open, false);
  assert.match(details.querySelector('summary').textContent, /Inspect replica CPU and autoscaling/);
  assert.equal(strip.closest('.settings-tab-panel'), null);
  assert.match(strip.querySelector('.cpu-heat-guide').textContent, /arrow keys/);
  assert.doesNotMatch(strip.querySelector('.cpu-heat-label').textContent, /of limit/);
});

test('shared CPU meter uses 70/90 thresholds and an accessible capacity label', () => {
  const doc = new JSDOM('<meter></meter>').window.document;
  const meter = doc.querySelector('meter');
  updateCPUMeter(meter, 0.92, '10.2 / 11 cores, 92% of CPU capacity');
  assert.equal(meter.low, 0.7);
  assert.equal(meter.high, 0.9);
  assert.equal(meter.value, 0.92);
  assert.match(meter.className, /--critical/);
  assert.match(meter.getAttribute('aria-label'), /10\.2 \/ 11 cores/);
  updateCPUMeter(meter, null, '');
  assert.equal(meter.hidden, true);
});

test('persistent app-header strip identifies hot, unknown, and overfull replicas', () => {
  const doc = new JSDOM(`<div id="app-detail-cpu-heat" hidden>
    <span class="cpu-heat-label">CPU per running replica</span>
    <div class="cpu-heat-cells"></div><span class="cpu-heat-warning" hidden></span>
    <span class="cpu-heat-state" hidden></span>
  </div>`).window.document;
  const strip = doc.querySelector('#app-detail-cpu-heat');
  renderReplicaHeat(strip, { cpu_saturation_available: true, replicas: [
    { index: 0, status: 'running', cpu_percent: 105, rss_bytes: 1048576, sessions: 60, cpu_saturated: true },
    { index: 1, status: 'running', cpu_percent: null, sessions: -1 },
    { index: 2, status: 'stopped', cpu_percent: 0 },
  ] });
  assert.equal(strip.hidden, false);
  assert.equal(strip.querySelectorAll('.cpu-heat-cell').length, 2);
  assert.match(strip.querySelector('.cpu-heat-cell').className, /is-over/);
  assert.match(strip.querySelector('.cpu-heat-cell').className, /is-critical/);
  assert.match(strip.querySelector('.cpu-heat-cell').getAttribute('aria-label'), /60 sessions/);
  assert.equal(strip.querySelector('.cpu-heat-cell').tabIndex, 0);
  assert.equal(strip.querySelector('.cpu-heat-warning').textContent, '1 replica ≥90% for 3 samples');
  assert.equal(strip.querySelector('.cpu-heat-state').textContent, '1 CPU pending');
  strip.querySelector('.cpu-heat-cell').focus();
  renderReplicaHeat(strip, { cpu_saturation_available: true, replicas: [{ index: 0, status: 'running', cpu_percent: 99, sessions: 60 }] });
  assert.equal(doc.activeElement.dataset.replicaIndex, '0');
  renderReplicaHeat(strip, { cpu_saturation_available: false,
    replicas: [{ index: 0, status: 'running', cpu_percent: 95, sessions: 1 }] });
  assert.equal(strip.querySelector('.cpu-heat-warning').textContent, '1 replica ≥90%');
  renderReplicaHeat(strip, { replicas: [] });
  assert.equal(strip.hidden, true);
  assert.equal(strip.querySelectorAll('.cpu-heat-cell').length, 0);
});

test('starting replicas appear as pending without a false zero CPU bar', () => {
  const doc = new JSDOM(`<div class="cpu-heat" hidden>
    <div class="cpu-heat-cells"></div><span class="cpu-heat-warning" hidden></span>
    <span class="cpu-heat-state" hidden></span>
  </div>`).window.document;
  const strip = doc.querySelector('.cpu-heat');
  renderReplicaHeat(strip, { cpu_saturation_available: true, replicas: [
    { index: 0, status: 'running', cpu_percent: 65, sessions: 20 },
    { index: 1, status: 'starting', cpu_percent: null },
  ] });
  assert.equal(strip.querySelectorAll('.cpu-heat-cell').length, 1);
  assert.equal(strip.querySelector('.cpu-heat-state').textContent, '1 starting');
  renderReplicaHeat(strip, { cpu_saturation_available: true, replicas: [
    { index: 0, status: 'running', cpu_percent: 65, sessions: 20 },
    { index: 1, status: 'running', cpu_percent: null },
  ] });
  assert.equal(strip.querySelectorAll('.cpu-heat-cell').length, 2);
  assert.equal(strip.querySelector('.cpu-heat-state').textContent, '1 CPU pending');
  assert.match(strip.querySelectorAll('.cpu-heat-cell')[1].getAttribute('aria-label'), /CPU pending/);
});

test('replica pressure panel exposes the hottest replica and supports selection', () => {
  const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
  const doc = new JSDOM(html).window.document;
  const strip = doc.getElementById('app-detail-cpu-heat');
  renderReplicaHeat(strip, { cpu_saturation_available: true, replicas: [
    { index: 0, status: 'running', cpu_percent: 90, rss_bytes: 1048576, sessions: 12 },
    { index: 1, status: 'running', cpu_percent: 130, cpu_quota_enforced: true,
      effective_cpu_quota_percent: 200, rss_bytes: 2097152, sessions: 8 },
  ] });
  assert.equal(strip.querySelector('.cpu-heat-warning').textContent, '1 replica ≥90%');
  assert.equal(strip.querySelectorAll('.cpu-heat-cell').length, 2);
  assert.equal(strip.querySelectorAll('.cpu-heat-cell.is-critical').length, 1);
  assert.equal(strip.querySelector('.cpu-heat-inspector').hidden, true);
  const first = strip.querySelectorAll('.cpu-heat-cell')[0];
  const second = strip.querySelectorAll('.cpu-heat-cell')[1];
  assert.equal(first.querySelector('.cpu-heat-index').textContent, '#0');
  assert.equal(first.querySelector('.cpu-heat-value').textContent, '90%');
  assert.equal(second.querySelector('.cpu-heat-value').textContent, '65%');
  assert.match(first.getAttribute('aria-label'), /High one-core usage/);
  assert.equal(first.tabIndex, 0);
  assert.equal(second.tabIndex, -1);
  first.focus();
  assert.match(strip.querySelector('.cpu-heat-inspector').textContent, /Replica #0 · 90\.0% CPU · 90% of 1 core/);
  first.dispatchEvent(new doc.defaultView.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
  assert.equal(doc.activeElement, second);
  assert.equal(second.tabIndex, 0);
  assert.equal(first.tabIndex, -1);
  second.click();
  assert.equal(second.getAttribute('aria-pressed'), 'true');
  assert.match(strip.querySelector('.cpu-heat-inspector').textContent, /Replica #1 · 130\.0% CPU · 65% of 2 cores quota/);
  renderReplicaHeat(strip, { cpu_saturation_available: true, replicas: [
    { index: 0, status: 'running', cpu_percent: null },
    { index: 1, status: 'starting', cpu_percent: null },
  ] });
  assert.equal(strip.querySelector('.cpu-heat-state').textContent, '1 starting · 1 CPU pending');
});

test('replica CPU cells remain visible for healthy and hot states', () => {
  const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
  const doc = new JSDOM(html).window.document;
  const strip = doc.getElementById('app-detail-cpu-heat');
  assert.equal(strip.tagName, 'DIV');
  assert.equal(strip.querySelector('summary'), null);
  const sample = cpu => ({ cpu_saturation_available: true, replicas: [
    { index: 0, status: 'running', cpu_percent: cpu, sessions: 12, cpu_saturated: cpu >= 90 },
  ] });
  renderReplicaHeat(strip, sample(45));
  assert.equal(strip.hidden, false);
  assert.equal(strip.querySelector('.cpu-heat-footer').hidden, true);
  assert.equal(strip.querySelectorAll('.cpu-heat-cell').length, 1);
  assert.equal(strip.querySelector('.cpu-heat-warning').hidden, true);
  renderReplicaHeat(strip, sample(95));
  assert.equal(strip.hidden, false);
  assert.equal(strip.querySelector('.cpu-heat-warning').textContent, '1 replica ≥90% for 3 samples');
  assert.equal(strip.querySelector('.cpu-heat-footer').hidden, false);
  assert.equal(strip.querySelectorAll('.cpu-heat-cell.is-critical').length, 1);
});

test('replica bars use each replica’s one-core or quota scale', () => {
  const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
  const doc = new JSDOM(html).window.document;
  const strip = doc.getElementById('app-detail-cpu-heat');
  renderReplicaHeat(strip, { cpu_saturation_available: true, replicas: [
    { index: 0, status: 'running', cpu_percent: 72 },
    { index: 1, status: 'running', cpu_percent: 130, cpu_quota_enforced: true, effective_cpu_quota_percent: 200 },
    { index: 2, status: 'running', cpu_percent: null },
  ] });
  assert.match(strip.querySelectorAll('.cpu-heat-cell')[0].className, /is-warning/);
  assert.match(strip.querySelectorAll('.cpu-heat-cell')[1].className, /is-normal/);
  assert.match(strip.querySelectorAll('.cpu-heat-cell')[1].title, /65% of 2 cores quota/);
  assert.equal(strip.querySelector('.cpu-heat-state').textContent, '1 CPU pending');
  renderReplicaHeat(strip, { cpu_saturation_available: true, replicas: [
    { index: 1, status: 'running', cpu_percent: 130, cpu_quota_enforced: true, effective_cpu_quota_percent: 200 },
  ] });
  assert.match(strip.querySelector('.cpu-heat-cell').title, /65% of 2 cores quota/);
});

test('replica CPU shows every cell and the hot reading in large fleets', () => {
  const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
  const doc = new JSDOM(html).window.document;
  const strip = doc.getElementById('app-detail-cpu-heat');
  const replicas = Array.from({ length: 25 }, (_, index) => ({
    index, status: 'running', cpu_percent: index === 24 ? 96 : 18,
  }));
  renderReplicaHeat(strip, { cpu_saturation_available: true, replicas });
  assert.equal(strip.querySelectorAll('.cpu-heat-cell').length, 25);
  assert.equal(strip.querySelectorAll('.cpu-heat-cell.is-critical').length, 1);
  assert.equal(strip.querySelector('.cpu-heat-warning').textContent, '1 replica ≥90%');
});


test('polling retains focus when the inspected replica disappears', () => {
  const doc = new JSDOM(readFileSync(new URL('../static/index.html', import.meta.url), 'utf8'), { pretendToBeVisual: true }).window.document;
  const strip = doc.getElementById('app-detail-cpu-heat');
  const sample = indexes => ({ replicas: indexes.map(index => ({ index, status: 'running', cpu_percent: 50 })) });
  renderReplicaHeat(strip, sample([0, 1]));
  strip.querySelector('[data-replica-index="1"]').focus();
  renderReplicaHeat(strip, sample([0]));
  assert.equal(doc.activeElement.dataset.replicaIndex, '0');
  assert.equal(doc.activeElement.tabIndex, 0);
  renderReplicaHeat(strip, sample([]));
  assert.equal(doc.activeElement.id, 'app-detail-cpu');
});

test('quota warning differs from one-core reference and unavailable readings are ignored', () => {
  const doc = new JSDOM(readFileSync(new URL('../static/index.html', import.meta.url), 'utf8')).window.document;
  const strip = doc.getElementById('app-detail-cpu-heat');
  renderReplicaHeat(strip, { replicas: [
    { index: 0, status: 'running', cpu_percent: 95 },
    { index: 1, status: 'running', cpu_percent: 190, cpu_quota_enforced: true, effective_cpu_quota_percent: 200 },
    { index: 2, status: 'running', cpu_percent: 99, metrics_available: false },
  ] });
  const cells = strip.querySelectorAll('button');
  assert.match(cells[0].getAttribute('aria-label'), /High one-core usage/);
  assert.doesNotMatch(cells[0].getAttribute('aria-label'), /limit|quota/);
  assert.match(cells[1].getAttribute('aria-label'), /Near CPU quota/);
  assert.equal(cells[2].querySelector('.cpu-heat-value').textContent, '—');
});


test('vertical keys follow visual rows rather than moving sideways', () => {
  const doc = new JSDOM(readFileSync(new URL('../static/index.html', import.meta.url), 'utf8'), { pretendToBeVisual: true }).window.document;
  const strip = doc.getElementById('app-detail-cpu-heat');
  renderReplicaHeat(strip, { replicas: Array.from({ length: 6 }, (_, index) => ({ index, status: 'running', cpu_percent: 10 })) });
  const cells = [...strip.querySelectorAll('button')];
  cells.forEach((cell, index) => { cell.getBoundingClientRect = () => ({ top: Math.floor(index / 3) * 60 }); });
  cells[1].focus();
  cells[1].dispatchEvent(new doc.defaultView.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
  assert.equal(doc.activeElement, cells[4]);
  cells[4].dispatchEvent(new doc.defaultView.KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }));
  assert.equal(doc.activeElement, cells[1]);
});
