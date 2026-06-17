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
  const h = card.querySelector('h3');
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

test('the cpu sparkline carries an aria-label with the current value', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const svg = card.querySelector('.trend-row[data-metric="cpu"] svg');
  assert.equal(svg.getAttribute('aria-label'), 'CPU 12.5%');
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
