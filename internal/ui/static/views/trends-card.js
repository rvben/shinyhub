// trends-card.js - builds the Overview "Trends" card from a metrics-history
// response. Pure (only an injected `document`), so it unit-tests under jsdom and
// embeds with no build step. Renders one labelled sparkline per metric (CPU,
// Memory, Sessions, Instances) showing the latest value plus the trend line.

import { renderSparkline } from './sparkline.js';
import { formatBytes } from './stat-format.js';

// At least two samples are needed to draw a meaningful trend; below that the
// card shows a Collecting placeholder.
const MIN_POINTS = 2;

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

function finiteValues(values) {
  return values
    .filter((value) => value !== null && value !== undefined)
    .map(Number)
    .filter(Number.isFinite);
}

function normalizeSamples(values) {
  return values.map((value) => {
    if (value === null || value === undefined) return null;
    const numeric = Number(value);
    return Number.isFinite(numeric) ? numeric : null;
  });
}

// niceMax keeps every small multiple on an honest zero-based scale while
// rounding the ceiling to a value an operator can read at a glance.
function niceMax(values, integer) {
  const max = Math.max(0, ...finiteValues(values));
  if (max === 0) return 1;

  const target = max * 1.1;
  const magnitude = 10 ** Math.floor(Math.log10(target));
  const normalized = target / magnitude;
  const step = [1, 2, 2.5, 5, 10].find((candidate) => normalized <= candidate) || 10;
  const result = step * magnitude;
  return integer ? Math.max(1, Math.ceil(result)) : result;
}

function niceMemoryMax(values) {
  const max = Math.max(0, ...finiteValues(values));
  if (max === 0) return 1024 * 1024;

  const units = [1024, 1024 ** 2, 1024 ** 3, 1024 ** 4];
  const unit = [...units].reverse().find((candidate) => max >= candidate) || 1;
  const target = (max / unit) * 1.1;
  const ceiling = [1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024]
    .find((candidate) => target <= candidate) || 1024;
  return ceiling * unit;
}

function formatCoverage(timestamps, fallbackSeconds) {
  const values = finiteValues(timestamps);
  const seconds = values.length > 1
    ? Math.max(0, values[values.length - 1] - values[0])
    : fallbackSeconds;
  return formatWindow(seconds || fallbackSeconds);
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

  const heading = document.createElement('h2');
  heading.id = 'trends-heading';
  heading.textContent = windowSeconds
    ? `Trends (last ${formatWindow(windowSeconds)})`
    : 'Trends';

  const intervalSeconds = Number(history && history.interval_seconds) || 0;
  const header = document.createElement('div');
  header.className = 'trends-header';
  header.appendChild(heading);
  if (intervalSeconds > 0) {
    const cadence = document.createElement('span');
    cadence.className = 'trends-cadence';
    cadence.textContent = `Every ${formatWindow(intervalSeconds)}`;
    header.appendChild(cadence);
  }
  section.setAttribute('aria-labelledby', heading.id);
  section.appendChild(header);

  const cpu = series.cpu || [];
  const rss = series.rss || [];
  const sessions = series.sessions || [];
  const instances = series.instances || [];
  const timestamps = series.ts || [];
  const count = Math.max(cpu.length, rss.length, sessions.length, instances.length);

  if (count < MIN_POINTS) {
    const placeholder = document.createElement('p');
    placeholder.className = 'trends-empty';
    placeholder.textContent = 'Collecting...';
    section.appendChild(placeholder);
    return section;
  }

  const rows = [
    { key: 'cpu', label: 'CPU', values: cpu, format: fmtPercent, step: false, integer: false },
    { key: 'memory', label: 'Memory', values: rss, format: formatBytes, step: false, scale: niceMemoryMax },
    { key: 'sessions', label: 'Sessions', values: sessions, format: fmtInt, step: false, integer: true },
    { key: 'instances', label: 'Instances', values: instances, format: fmtInt, step: true, integer: true },
  ];
  const grid = document.createElement('div');
  grid.className = 'trends-grid';
  const coverage = formatCoverage(timestamps, windowSeconds);
  for (const row of rows) {
    grid.appendChild(renderTrendRow(document, row, coverage));
  }
  section.appendChild(grid);
  return section;
}

function renderTrendRow(document, row, coverage) {
  const samples = normalizeSamples(row.values);
  const latestSample = samples.length > 0 ? samples[samples.length - 1] : null;
  const latestAvailable = latestSample !== null;
  const valueText = latestAvailable ? row.format(latestSample) : 'No sample';
  const availableCount = samples.filter((sample) => sample !== null).length;
  const missingCount = samples.length - availableCount;
  const scaleMax = row.scale ? row.scale(samples) : niceMax(samples, row.integer);
  const scaleMaxText = row.format(scaleMax);

  const wrap = document.createElement('figure');
  wrap.className = 'trend-row';
  wrap.dataset.metric = row.key;

  const caption = document.createElement('figcaption');
  caption.className = 'trend-caption';

  const label = document.createElement('span');
  label.className = 'trend-label';
  label.textContent = row.label;

  const value = document.createElement('span');
  value.className = 'trend-value';
  value.textContent = valueText;
  if (!latestAvailable) {
    value.classList.add('is-missing');
    value.title = 'No measurement in the latest sample';
  }

  caption.appendChild(label);
  caption.appendChild(value);

  const plot = document.createElement('div');
  plot.className = 'trend-plot';

  const yAxis = document.createElement('div');
  yAxis.className = 'trend-y-axis';
  yAxis.setAttribute('aria-hidden', 'true');
  const yMax = document.createElement('span');
  yMax.textContent = scaleMaxText;
  const yMin = document.createElement('span');
  yMin.textContent = row.format(0);
  yAxis.appendChild(yMax);
  yAxis.appendChild(yMin);

  const currentDescription = latestAvailable
    ? `Current ${valueText}.`
    : 'Latest sample unavailable.';
  const missingDescription = missingCount > 0
    ? ` ${missingCount} of ${samples.length} samples unavailable.`
    : '';
  const spark = renderSparkline(document, samples, {
    width: 160,
    height: 54,
    step: row.step,
    domainMin: 0,
    domainMax: scaleMax,
    grid: true,
    endPoint: true,
    ariaLabel: `${row.label} over ${coverage}. ${currentDescription} Scale 0 to ${scaleMaxText}.${missingDescription}`,
    className: `sparkline sparkline-${row.key}`,
  });

  const xAxis = document.createElement('div');
  xAxis.className = 'trend-x-axis';
  xAxis.setAttribute('aria-hidden', 'true');
  const xStart = document.createElement('span');
  xStart.textContent = `${coverage} ago`;
  const gapNote = document.createElement('span');
  gapNote.className = 'trend-gap-note';
  if (missingCount > 0) {
    gapNote.textContent = `${missingCount} missing`;
    gapNote.title = `${missingCount} of ${samples.length} samples unavailable`;
  }
  const xEnd = document.createElement('span');
  xEnd.textContent = 'Now';
  xAxis.appendChild(xStart);
  xAxis.appendChild(gapNote);
  xAxis.appendChild(xEnd);

  plot.appendChild(yAxis);
  plot.appendChild(spark);
  plot.appendChild(xAxis);
  wrap.appendChild(caption);
  wrap.appendChild(plot);
  return wrap;
}
