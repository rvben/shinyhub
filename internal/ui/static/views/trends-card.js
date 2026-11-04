// trends-card.js - builds the Overview "Trends" card from a metrics-history
// response. Pure (only an injected `document`), so it unit-tests under jsdom and
// embeds with no build step. Renders one labelled sparkline per metric (CPU,
// Memory, Sessions, Instances) showing the latest value plus the trend line.

import { renderSparkline } from './sparkline.js';
import { formatBytes, formatCoreCount } from './stat-format.js';
import { setMetricValue } from './metric-value.js';

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

function fmtCores(v) {
  return formatCoreCount(v).replace(/\.0 cores$/, ' cores').replace(/^1 cores$/, '1 core');
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
    return Number.isFinite(numeric) && numeric >= 0 ? numeric : null;
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
export function renderTrendsCard(document, history, capacityCores = null) {
  const series = (history && history.series) || {};
  const windowSeconds = Number(history && history.window_seconds) || 0;

  // window 0 means history collection is disabled server-side; signal the caller
  // to hide the card rather than showing a perpetual "Collecting..." placeholder.
  if (windowSeconds === 0) return null;

  const section = document.createElement('section');
  section.className = 'trends-card';

  const heading = document.createElement('h2');
  heading.id = 'trends-heading';
  heading.textContent = 'Resource history';

  const intervalSeconds = Number(history && history.interval_seconds) || 0;
  const header = document.createElement('div');
  header.className = 'trends-header';
  header.appendChild(heading);
  if (intervalSeconds > 0) {
    const cadence = document.createElement('span');
    cadence.className = 'trends-cadence';
    cadence.textContent = `Last ${formatWindow(windowSeconds)} · every ${formatWindow(intervalSeconds)}`;
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
    placeholder.textContent = 'Collecting history. Charts appear after two measurements.';
    section.appendChild(placeholder);
    return section;
  }

  const rows = [
    { key: 'cpu', label: 'CPU', values: normalizeSamples(cpu).map(v => v == null ? null : v / 100), format: fmtCores, step: false, integer: false,
      capacity: Number.isFinite(capacityCores) && capacityCores > 0 ? capacityCores : null },
    { key: 'memory', label: 'Memory', values: rss, format: formatBytes, step: false, scale: niceMemoryMax },
    { key: 'sessions', label: 'Sessions', values: sessions, format: fmtInt, step: false, integer: true },
    { key: 'instances', label: 'Replicas', values: instances, format: fmtInt, step: true, integer: true },
  ];
  const grid = document.createElement('div');
  grid.className = 'trends-grid';
  const coverage = formatCoverage(timestamps, windowSeconds);
  const timeline = timestamps.length === count && timestamps.every((ts, i) => Number.isFinite(ts)
    && Number.isFinite(new Date(ts * 1000).getTime()) && (i === 0 || ts > timestamps[i - 1])) ? timestamps : null;
  const charts = rows.map(row => {
    row.values = Array.from({length: count}, (_, i) => row.values[i] ?? null);
    const chart = renderTrendRow(document, row, coverage, timeline, intervalSeconds);
    grid.appendChild(chart.wrap);
    return chart;
  });
  section.appendChild(grid);
  const capacity = rows[0].capacity;
  if (capacity) {
    const note = document.createElement('p'); note.className = 'trends-capacity-note';
    note.textContent = `Current CPU capacity: ${fmtCores(capacity)}`;
    const explanation = document.createElement('span'); explanation.className = 'sr-only';
    explanation.textContent = '. Dashed line; capacity may have changed during this window.';
    note.append(explanation); note.title = 'Dashed line; capacity may have changed during this window.';
    section.append(note);
  }
  addHistoryInspector(document, section, charts, timeline, count);
  // Clip the table inside a block: a table's intrinsic width can otherwise
  // escape a width:1px visually-hidden rule and widen narrow viewports.
  const dataTwin = document.createElement('div');
  dataTwin.className = 'sr-only';
  const table = document.createElement('table');
  const tableCaption = document.createElement('caption');
  tableCaption.textContent = `Exact trend samples; timestamps in UTC.${Number.isFinite(capacityCores) && capacityCores > 0
    ? ` Current CPU capacity is ${capacityCores} cores; historical capacity may differ.` : ''}`;
  table.appendChild(tableCaption);
  const thead = document.createElement('thead');
  const headers = document.createElement('tr');
  for (const label of ['Timestamp (UTC)', 'CPU (100% = one core)', 'Memory (bytes)', 'Sessions', 'Instances']) {
    const th = document.createElement('th');
    th.scope = 'col';
    th.textContent = label;
    headers.appendChild(th);
  }
  thead.appendChild(headers);
  table.appendChild(thead);
  const tbody = document.createElement('tbody');
  const values = [cpu, rss, sessions, instances].map(normalizeSamples);
  for (let index = 0; index < count; index++) {
    const tr = document.createElement('tr');
    const timestamp = document.createElement('th');
    timestamp.scope = 'row';
    const ts = timestamps[index];
    timestamp.textContent = Number.isFinite(ts) && Number.isFinite(new Date(ts * 1000).getTime())
      ? new Date(ts * 1000).toISOString() : `Sample ${index + 1}; timestamp unavailable`;
    tr.appendChild(timestamp);
    for (let column = 0; column < values.length; column++) {
      const td = document.createElement('td');
      const sample = values[column][index];
      td.textContent = sample == null ? 'No sample' : `${sample}${column === 0 ? '%' : ''}`;
      tr.appendChild(td);
    }
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);
  dataTwin.appendChild(table);
  section.appendChild(dataTwin);
  return section;
}

function renderTrendRow(document, row, coverage, timestamps, intervalSeconds) {
  const samples = normalizeSamples(row.values);
  const latestSample = samples.length > 0 ? samples[samples.length - 1] : null;
  const latestAvailable = latestSample !== null;
  const valueText = latestAvailable ? row.format(latestSample) : 'No sample';
  const availableCount = samples.filter((sample) => sample !== null).length;
  const missingCount = samples.length - availableCount;
  const elapsedGaps = timestamps && intervalSeconds > 0
    ? timestamps.filter((ts, i) => i > 0 && ts - timestamps[i - 1] > intervalSeconds * 1.5).length : 0;
  const scaleMax = Math.max(row.scale ? row.scale(samples) : niceMax(samples, row.integer), row.capacity ? niceMax([row.capacity], false) : 0);
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
  setMetricValue(value, valueText);
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
  yMax.textContent = row.key === 'cpu' ? String(Number(scaleMax.toPrecision(3))) : scaleMaxText;
  // The axis floor is always zero, and zero needs no unit: formatting it
  // through row.format would print "0 KB" under a "256 MB" ceiling on the
  // same axis, a unit mismatch that formatBytes' KB/MB switch makes visible
  // in a way fmtPercent and fmtInt do not.
  const yMin = document.createElement('span');
  yMin.textContent = '0';
  yAxis.appendChild(yMax);
  yAxis.appendChild(yMin);

  const currentDescription = latestAvailable
    ? `Current ${valueText}.`
    : 'Latest sample unavailable.';
  const firstAvailable = samples.find((sample) => sample !== null);
  const direction = latestAvailable && firstAvailable !== undefined
    ? latestSample > firstAvailable ? 'Increased' : latestSample < firstAvailable ? 'Decreased' : 'Unchanged'
    : '';
  const missingDescription = missingCount > 0
    ? ` ${missingCount} of ${samples.length} samples unavailable.`
    : '';
  const collectionDescription = elapsedGaps ? ` ${elapsedGaps} collection gaps.` : '';
  const spark = renderSparkline(document, samples, {
    width: 480,
    height: 96,
    timestamps,
    gapThreshold: intervalSeconds > 0 ? intervalSeconds * 1.5 : Infinity,
    step: row.step,
    domainMin: 0,
    domainMax: scaleMax,
    grid: true,
    endPoint: false,
    ariaLabel: `${row.label} over ${coverage}. ${currentDescription} ${direction ? `${direction} from ${row.format(firstAvailable)}. ` : ''}Scale 0 to ${scaleMaxText}.${missingDescription}${collectionDescription}`,
    className: `sparkline sparkline-${row.key}`,
  });
  if (row.capacity) {
    const line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
    const y = 96 - row.capacity / scaleMax * 96;
    line.setAttribute('class', 'trend-capacity-line');
    line.setAttribute('x1', '0');
    line.setAttribute('x2', '480');
    line.setAttribute('y1', String(y));
    line.setAttribute('y2', String(y));
    line.setAttribute('vector-effect', 'non-scaling-stroke');
    spark.appendChild(line);
    spark.setAttribute('aria-label', `${spark.getAttribute('aria-label')} Current capacity ${row.format(row.capacity)}; capacity may have differed during this history.`);

  }

  const xAxis = document.createElement('div');
  xAxis.className = 'trend-x-axis';
  xAxis.setAttribute('aria-hidden', 'true');
  const xStart = document.createElement('span');
  xStart.textContent = timestamps ? timeLabel(timestamps[0]) : 'Sample 1';
  const gapNote = document.createElement('span');
  gapNote.className = 'trend-gap-note';
  if (missingCount > 0) {
    gapNote.textContent = `${missingCount} ${missingCount === 1 ? 'sample' : 'samples'} missing`;
    gapNote.title = `${missingCount} of ${samples.length} samples unavailable`;
  }
  if (elapsedGaps) gapNote.textContent += `${missingCount ? ' · ' : ''}${elapsedGaps} collection ${elapsedGaps === 1 ? 'gap' : 'gaps'}`;
  const xEnd = document.createElement('span');
  xEnd.textContent = timestamps ? timeLabel(timestamps[timestamps.length - 1]) : `Sample ${samples.length}`;
  xAxis.appendChild(xStart);
  xAxis.appendChild(gapNote);
  xAxis.appendChild(xEnd);

  plot.appendChild(yAxis);
  const canvas = document.createElement('div'); canvas.className = 'trend-canvas';
  const cursor = document.createElement('span'); cursor.className = 'trend-cursor'; cursor.hidden = true;
  const point = document.createElement('span'); point.className = 'trend-point'; point.hidden = !latestAvailable;
  point.style.left = '100%';
  if (latestAvailable) point.style.top = `${(1 - latestSample / scaleMax) * 100}%`;
  canvas.append(spark, cursor, point);
  plot.appendChild(canvas);
  plot.appendChild(xAxis);
  wrap.appendChild(caption);
  wrap.appendChild(plot);
  return {wrap, canvas, cursor, point, value, samples, scaleMax, key: row.key, format: row.format, label: row.label};
}


function timeLabel(timestamp, seconds = false) {
  return new Date(timestamp * 1000).toLocaleTimeString([], {hour: '2-digit', minute: '2-digit', ...(seconds ? {second:'2-digit'} : {})});
}

// Inspect real samples directly on any plot; the shared cursor keeps all
// readings at the same instant. No document listeners survive a card refresh.
function addHistoryInspector(document, section, charts, timestamps, count) {
  const readout = document.createElement('div'); readout.className = 'trends-readout';
  const time = document.createElement('span'); time.className = 'trends-inspect-time';
  const hint = document.createElement('span'); hint.className = 'trends-inspect-hint';
  const pointerHint = document.createElement('span'); pointerHint.className = 'trends-pointer-hint'; pointerHint.textContent = 'Hover or drag to compare';
  const touchHint = document.createElement('span'); touchHint.className = 'trends-touch-hint'; touchHint.textContent = 'Drag a chart to compare';
  hint.append(pointerHint, touchHint);
  const latest = document.createElement('button'); latest.type = 'button'; latest.className = 'trends-latest';
  latest.textContent = 'Latest'; latest.setAttribute('aria-label', 'Return to latest measurements'); latest.hidden = true;
  const help = document.createElement('p'); help.className = 'sr-only'; help.id = 'trends-inspect-help';
  help.textContent = 'Compare all four readings at the same time. Use Left and Right arrow keys to move between samples, Home and End to reach the first and last sample, and Escape to return to latest.';
  readout.append(time, hint, latest, help);
  section.insertBefore(readout, section.querySelector('.trends-grid'));
  let selected = count - 1, mode = 'idle', dragging = null, suppressFocus = false;
  let focusChart = charts[0].canvas;

  function inspect(index, active = true) {
    selected = Math.max(0, Math.min(count - 1, Math.round(index)));
    const stamp = timestamps ? timeLabel(timestamps[selected], true) : `Sample ${selected + 1}`;
    time.textContent = `${active ? 'At' : 'Latest sample ·'} ${stamp}`;
    latest.hidden = !active;
    hint.hidden = active;
    section.dataset.inspectActive = String(active);
    section.dataset.inspectIndex = String(selected);
    section.dataset.inspectTimestamp = active && timestamps ? String(timestamps[selected]) : '';
    const x = timestamps ? (timestamps[selected] - timestamps[0]) / (timestamps[count - 1] - timestamps[0]) * 100 : selected / (count - 1) * 100;
    const summary = charts.map(chart => {
      const sample = chart.samples[selected];
      const text = sample == null ? 'No sample' : chart.format(sample);
      setMetricValue(chart.value, text); chart.value.classList.toggle('is-missing', sample == null);
      chart.value.title = active ? `At ${stamp}` : sample == null ? 'No measurement in the latest sample' : 'Latest sample';
      chart.cursor.hidden = !active; chart.cursor.style.left = `${x}%`;
      chart.point.hidden = sample == null; chart.point.style.left = `${x}%`;
      if (sample != null) chart.point.style.top = `${(1 - sample / chart.scaleMax) * 100}%`;
      return `${chart.label}: ${text}`;
    });
    for (const {canvas} of charts) {
      canvas.setAttribute('aria-valuenow', String(selected));
      canvas.setAttribute('aria-valuetext', `${active ? '' : 'Latest sample. '}${stamp}. ${summary.join('. ')}`);
    }
  }
  function finish() { mode = 'idle'; dragging = null; inspect(count - 1, false); }
  function nearest(fraction) {
    if (!timestamps) return Math.round(fraction * (count - 1));
    const target = timestamps[0] + fraction * (timestamps[count - 1] - timestamps[0]);
    return timestamps.reduce((best, ts, i) => Math.abs(ts - target) < Math.abs(timestamps[best] - target) ? i : best, 0);
  }
  function insideControls(target) { return target === latest || charts.some(chart => chart.canvas === target); }
  function blur(event) { if (!insideControls(event.relatedTarget)) finish(); }
  for (const chart of charts) {
    const {canvas} = chart;
    canvas.tabIndex = 0; canvas.dataset.historyChart = chart.key;
    canvas.setAttribute('role', 'slider'); canvas.setAttribute('aria-label', `${chart.label} history`);
    canvas.setAttribute('aria-valuemin', '0'); canvas.setAttribute('aria-valuemax', String(count - 1));
    canvas.setAttribute('aria-orientation', 'horizontal'); canvas.setAttribute('aria-describedby', help.id);
    function pointAt(event) {
      const bounds = canvas.getBoundingClientRect();
      if (bounds.width) inspect(nearest(Math.max(0, Math.min(1, (event.clientX - bounds.left) / bounds.width))));
    }
    canvas.addEventListener('focus', () => {
      focusChart = canvas;
      latest.dataset.returnChart = chart.key;
      if (!suppressFocus && !dragging) { mode = 'keyboard'; inspect(selected); }
    });
    canvas.addEventListener('blur', blur);
    canvas.addEventListener('keydown', event => {
      const steps = {ArrowLeft: -1, ArrowDown: -1, ArrowRight: 1, ArrowUp: 1, PageDown: -10, PageUp: 10};
      if (event.key === 'Escape') { event.preventDefault(); finish(); return; }
      if (!(event.key in steps) && event.key !== 'Home' && event.key !== 'End') return;
      event.preventDefault(); mode = 'keyboard';
      inspect(event.key === 'Home' ? 0 : event.key === 'End' ? count - 1 : selected + steps[event.key]);
    });
    canvas.addEventListener('pointerdown', event => {
      if (event.button != null && event.button !== 0) return;
      mode = event.pointerType === 'touch' ? 'touch' : 'pointer';
      dragging = event.pointerId;
      suppressFocus = true; canvas.focus({preventScroll: true}); suppressFocus = false;
      canvas.setPointerCapture?.(event.pointerId);
      pointAt(event);
    });
    canvas.addEventListener('pointermove', event => {
      if (event.pointerType === 'touch' && dragging !== event.pointerId) return;
      mode = event.pointerType === 'touch' ? 'touch' : 'pointer'; pointAt(event);
    });
    canvas.addEventListener('pointerup', event => {
      if (dragging !== event.pointerId) return;
      dragging = null;
      if (event.pointerType !== 'touch') {
        const bounds = canvas.getBoundingClientRect();
        if (event.clientX < bounds.left || event.clientX > bounds.left + bounds.width || event.clientY < bounds.top || event.clientY > bounds.bottom) finish();
      }
    });
    canvas.addEventListener('pointercancel', finish);
    canvas.addEventListener('lostpointercapture', () => { if (dragging != null) finish(); });
    canvas.addEventListener('pointerleave', () => { if (mode === 'pointer' && dragging == null) finish(); });
  }
  latest.addEventListener('blur', blur);
  latest.addEventListener('click', () => {
    suppressFocus = true; focusChart.focus({preventScroll: true}); suppressFocus = false;
    finish();
  });
  section.inspectIndex = index => { mode = 'keyboard'; inspect(index); };
  section.finishInspection = finish;
  section.inspectTime = timestamp => {
    if (!timestamps) return;
    const fraction = (timestamp - timestamps[0]) / (timestamps[count - 1] - timestamps[0]);
    mode = 'keyboard'; inspect(nearest(Math.max(0, Math.min(1, fraction))));
  };
  inspect(count - 1, false);
}
