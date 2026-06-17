// trends-card.js - builds the Overview "Trends" card from a metrics-history
// response. Pure (only an injected `document`), so it unit-tests under jsdom and
// embeds with no build step. Renders one labelled sparkline per metric (CPU,
// Memory, Sessions, Instances) showing the latest value plus the trend line.

import { renderSparkline } from './sparkline.js';
import { formatBytes } from './stat-format.js';

// At least two samples are needed to draw a meaningful trend; below that the
// card shows a Collecting placeholder.
const MIN_POINTS = 2;

function last(arr, fallback) {
  return arr && arr.length ? arr[arr.length - 1] : fallback;
}

// formatWindow renders a whole-second window compactly (43200 -> "12h").
function formatWindow(seconds) {
  const s = Number(seconds) || 0;
  if (s % 3600 === 0) return `${s / 3600}h`;
  if (s % 60 === 0) return `${s / 60}m`;
  return `${s}s`;
}

function fmtPercent(v) {
  return `${(Number(v) || 0).toFixed(1)}%`;
}

function fmtInt(v) {
  return String(Math.round(Number(v) || 0));
}

// renderTrendsCard returns a <section> with one sparkline per metric, or a
// Collecting placeholder when there are too few samples.
export function renderTrendsCard(document, history) {
  const series = (history && history.series) || {};
  const windowSeconds = Number(history && history.window_seconds) || 0;

  // window 0 means history collection is disabled server-side; signal the caller
  // to hide the card rather than showing a perpetual "Collecting..." placeholder.
  if (windowSeconds === 0) return null;

  const section = document.createElement('section');
  section.className = 'trends-card';

  const heading = document.createElement('h3');
  heading.textContent = windowSeconds
    ? `Trends (last ${formatWindow(windowSeconds)})`
    : 'Trends';
  section.appendChild(heading);

  const cpu = series.cpu || [];
  const rss = series.rss || [];
  const sessions = series.sessions || [];
  const instances = series.instances || [];
  const count = Math.max(cpu.length, rss.length, sessions.length, instances.length);

  if (count < MIN_POINTS) {
    const placeholder = document.createElement('p');
    placeholder.className = 'trends-empty';
    placeholder.textContent = 'Collecting...';
    section.appendChild(placeholder);
    return section;
  }

  const rows = [
    { key: 'cpu', label: 'CPU', values: cpu, format: fmtPercent, step: false },
    { key: 'memory', label: 'Memory', values: rss, format: formatBytes, step: false },
    { key: 'sessions', label: 'Sessions', values: sessions, format: fmtInt, step: false },
    { key: 'instances', label: 'Instances', values: instances, format: fmtInt, step: true },
  ];
  for (const row of rows) {
    section.appendChild(renderTrendRow(document, row));
  }
  return section;
}

function renderTrendRow(document, row) {
  const valueText = row.format(last(row.values, 0));

  const wrap = document.createElement('div');
  wrap.className = 'trend-row';
  wrap.dataset.metric = row.key;

  const label = document.createElement('span');
  label.className = 'trend-label';
  label.textContent = row.label;

  const value = document.createElement('span');
  value.className = 'trend-value';
  value.textContent = valueText;

  const spark = renderSparkline(document, row.values.map(Number), {
    step: row.step,
    ariaLabel: `${row.label} ${valueText}`,
    className: `sparkline sparkline-${row.key}`,
  });

  wrap.appendChild(label);
  wrap.appendChild(value);
  wrap.appendChild(spark);
  return wrap;
}
