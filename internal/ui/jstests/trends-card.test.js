import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { renderTrendsCard } from '../static/views/trends-card.js';

const doc = () => new JSDOM('<!DOCTYPE html><body></body>').window.document;

const MB = 1 << 20;

const fullHistory = () => ({
  window_seconds: 43200,
  interval_seconds: 15,
  series: {
    ts: [1, 2, 3],
    cpu: [10, 20, 12.5],
    rss: [100 * MB, 150 * MB, 210 * MB],
    sessions: [1, 2, 3],
    instances: [1, 1, 2],
  },
});

test('renders four labelled trend rows from a populated history', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  assert.equal(card.className, 'trends-card');
  const rows = card.querySelectorAll('.trend-row');
  assert.equal(rows.length, 4);
  const metrics = [...rows].map((r) => r.dataset.metric);
  assert.deepEqual(metrics, ['cpu', 'memory', 'sessions', 'instances']);
});

test('heading shows the retention window', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const h = card.querySelector('h2');
  assert.match(h.textContent, /Trends \(last 12h\)/);
});

test('each row shows the latest value, formatted per metric', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const valueOf = (metric) =>
    card.querySelector(`.trend-row[data-metric="${metric}"] .trend-value`).textContent;
  assert.equal(valueOf('cpu'), '12.5%');
  assert.equal(valueOf('memory'), '210 MB');
  assert.equal(valueOf('sessions'), '3');
  assert.equal(valueOf('instances'), '2');
});

test('each row contains a sparkline svg; instances uses the step variant', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  for (const m of ['cpu', 'memory', 'sessions', 'instances']) {
    const svg = card.querySelector(`.trend-row[data-metric="${m}"] svg`);
    assert.ok(svg, `expected a sparkline svg for ${m}`);
  }
  const inst = card.querySelector('.trend-row[data-metric="instances"] svg');
  assert.match(inst.getAttribute('class'), /sparkline-instances/);
});

test('each metric has readable x and y axes', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  for (const metric of ['cpu', 'memory', 'sessions', 'instances']) {
    const row = card.querySelector(`.trend-row[data-metric="${metric}"]`);
    assert.ok(row.querySelector('.trend-y-axis').textContent.length > 0);
    assert.equal(row.querySelector('.trend-x-axis').textContent, '2s agoNow');
    assert.equal(row.querySelectorAll('.sparkline-grid').length, 3);
  }
});

test('charts use a zero-based labelled scale instead of rescaling to the data range', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const cpu = card.querySelector('.trend-row[data-metric="cpu"]');
  assert.equal(cpu.querySelector('.trend-y-axis').textContent, '25.0%0.0%');
  assert.match(cpu.querySelector('svg').getAttribute('aria-label'), /Scale 0 to 25\.0%/);
});

test('memory and count axes use readable rounded ceilings', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const memory = card.querySelector('.trend-row[data-metric="memory"] .trend-y-axis');
  const sessions = card.querySelector('.trend-row[data-metric="sessions"] .trend-y-axis');
  assert.equal(memory.textContent, '256 MB0 KB');
  assert.equal(sessions.textContent, '50');
});

test('header shows the sample cadence', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  assert.equal(card.querySelector('.trends-cadence').textContent, 'Every 15s');
});

test('the cpu sparkline carries an aria-label with the current value', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const svg = card.querySelector('.trend-row[data-metric="cpu"] svg');
  assert.match(svg.getAttribute('aria-label'), /CPU over 2s\. Current 12\.5%\./);
});

test('a missing latest sample is shown as unavailable, never as zero', () => {
  const history = fullHistory();
  history.series.cpu = [10, 20, null];
  const card = renderTrendsCard(doc(), history);
  const row = card.querySelector('.trend-row[data-metric="cpu"]');
  const value = row.querySelector('.trend-value');
  const svg = row.querySelector('svg');

  assert.equal(value.textContent, 'No sample');
  assert.ok(value.classList.contains('is-missing'));
  assert.match(svg.getAttribute('aria-label'), /Latest sample unavailable\./);
  assert.match(svg.getAttribute('aria-label'), /1 of 3 samples unavailable\./);
  assert.equal(svg.querySelector('.sparkline-endpoint'), null);
  assert.equal(row.querySelector('.trend-gap-note').textContent, '1 missing');
});

test('a gap inside a series visibly breaks the trend line', () => {
  const history = fullHistory();
  history.series.cpu = [10, 20, null, 15, 12.5];
  history.series.ts = [1, 2, 3, 4, 5];
  const card = renderTrendsCard(doc(), history);
  const row = card.querySelector('.trend-row[data-metric="cpu"]');

  assert.equal(row.querySelectorAll('.sparkline-series').length, 2);
  assert.equal(row.querySelector('.trend-value').textContent, '12.5%');
  assert.match(row.querySelector('svg').getAttribute('aria-label'), /1 of 5 samples unavailable\./);
});

test('numeric samples remain supported while absent samples stay absent', () => {
  const history = fullHistory();
  history.series.sessions = ['1', null, '3'];
  const card = renderTrendsCard(doc(), history);
  const row = card.querySelector('.trend-row[data-metric="sessions"]');

  assert.equal(row.querySelector('.trend-value').textContent, '3');
  assert.equal(row.querySelectorAll('.sparkline-series').length, 0);
  assert.equal(row.querySelectorAll('.sparkline-sample').length, 2);
});

test('fewer than two samples shows a Collecting placeholder, no rows', () => {
  const card = renderTrendsCard(doc(), {
    window_seconds: 43200,
    interval_seconds: 15,
    series: { ts: [1], cpu: [10], rss: [100], sessions: [1], instances: [1] },
  });
  assert.equal(card.querySelectorAll('.trend-row').length, 0);
  const empty = card.querySelector('.trends-empty');
  assert.ok(empty, 'expected a Collecting placeholder');
  assert.match(empty.textContent, /Collecting/);
});

test('a disabled history (window 0) returns null so the card can be hidden', () => {
  const card = renderTrendsCard(doc(), {
    window_seconds: 0,
    interval_seconds: 0,
    series: { ts: [], cpu: [], rss: [], sessions: [], instances: [] },
  });
  assert.equal(card, null);
});

test('an enabled history with no samples yet shows Collecting (not hidden)', () => {
  const card = renderTrendsCard(doc(), {
    window_seconds: 43200,
    interval_seconds: 15,
    series: { ts: [], cpu: [], rss: [], sessions: [], instances: [] },
  });
  assert.ok(card, 'enabled history must render a card, not null');
  assert.ok(card.querySelector('.trends-empty'));
});
