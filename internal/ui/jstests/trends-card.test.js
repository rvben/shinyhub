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

test('CPU trend distinguishes current capacity from historical capacity', () => {
  const card = renderTrendsCard(doc(), fullHistory(), 1);
  const cpu = card.querySelector('.trend-row[data-metric="cpu"]');
  assert.equal(cpu.querySelectorAll('.trend-capacity-line').length, 1);
  assert.match(card.querySelector('.trends-capacity-note').textContent, /Current CPU capacity: 1 core/);
  assert.match(cpu.querySelector('svg').getAttribute('aria-label'), /Current capacity 1 core; capacity may have differed during this history/);
  assert.equal(cpu.querySelector('.trend-value').textContent, '0.125 cores');
});

test('CPU trend uses the same cores unit as live metrics without inventing capacity', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  assert.match(card.querySelector('.trend-row[data-metric="cpu"] .trend-value').textContent, /cores$/);
  assert.equal(card.querySelector('.trends-capacity-note'),null);
});

test('trends preserve every exact sample in an adjacent semantic data table', () => {
  const history = fullHistory();
  history.series.cpu = [10.12345, null, 12.5];
  const card = renderTrendsCard(doc(), history, 4);
  const table = card.querySelector('table');
  assert.ok(table.parentElement.classList.contains('sr-only'));
  assert.equal(table.parentElement.tagName, 'DIV');
  assert.match(table.querySelector('caption').textContent, /Current CPU capacity is 4 cores; historical capacity may differ/);
  assert.match(table.querySelector('thead').textContent, /CPU \(100% = one core\)/);
  assert.equal(table.querySelectorAll('tbody tr').length, 3);
  assert.equal(table.querySelector('tbody tr th').textContent, '1970-01-01T00:00:01.000Z');
  assert.equal(table.querySelector('tbody tr td').textContent, '10.12345%');
  assert.equal(table.querySelectorAll('tbody tr')[1].querySelector('td').textContent, 'No sample');
  assert.equal(table.querySelectorAll('tbody tr')[2].querySelectorAll('td')[1].textContent, String(210 * MB));
});

test('heading identifies resource history and metadata shows the retention window', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const h = card.querySelector('h2');
  assert.equal(h.textContent, 'Resource history');
  assert.match(card.querySelector('.trends-cadence').textContent, /Last 12h/);
});

test('each row shows the latest value, formatted per metric', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const valueOf = (metric) =>
    card.querySelector(`.trend-row[data-metric="${metric}"] .trend-value`).textContent;
  assert.equal(valueOf('cpu'), '0.125 cores');
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
    assert.doesNotMatch(row.querySelector('.trend-x-axis').textContent, /Now/);
    assert.match(row.querySelector('.trend-x-axis').textContent, /\d{2}:\d{2}/);
    assert.equal(row.querySelectorAll('.sparkline-grid').length, 3);
  }
});

test('charts use a zero-based labelled scale instead of rescaling to the data range', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const cpu = card.querySelector('.trend-row[data-metric="cpu"]');
  assert.equal(cpu.querySelector('.trend-y-axis').textContent, '0.250');
  assert.match(cpu.querySelector('svg').getAttribute('aria-label'), /Scale 0 to 0\.25 cores/);
});

test('memory and count axes use readable rounded ceilings', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const memory = card.querySelector('.trend-row[data-metric="memory"] .trend-y-axis');
  const sessions = card.querySelector('.trend-row[data-metric="sessions"] .trend-y-axis');
  // The floor label is a bare "0", not "0 KB": formatting zero through the
  // memory row's KB/MB-switching formatter would print a smaller unit than
  // the ceiling above it ("256 MB"), a mismatch that a unitless axis origin
  // avoids entirely.
  assert.equal(memory.textContent, '256 MB0');
  assert.equal(sessions.textContent, '50');
});

test('header shows the sample cadence', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  assert.equal(card.querySelector('.trends-cadence').textContent, 'Last 12h · every 15s');
});

test('the cpu sparkline carries an aria-label with the current value', () => {
  const card = renderTrendsCard(doc(), fullHistory());
  const svg = card.querySelector('.trend-row[data-metric="cpu"] svg');
  assert.match(svg.getAttribute('aria-label'), /CPU over 2s\. Current 0\.125 cores\./);
  assert.match(svg.getAttribute('aria-label'), /Increased from 0\.1 cores\./);
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
  // The axis note names what is missing: a bare "1 missing" under a CPU line
  // reads as a missing CPU, not a missing sample.
  assert.equal(row.querySelector('.trend-gap-note').textContent, '1 sample missing');
});

test('the gap note pluralizes the missing sample count', () => {
  const history = fullHistory();
  history.series.cpu = [null, 20, null];
  const card = renderTrendsCard(doc(), history);
  const row = card.querySelector('.trend-row[data-metric="cpu"]');
  assert.equal(row.querySelector('.trend-gap-note').textContent, '2 samples missing');
});

test('a gap inside a series visibly breaks the trend line', () => {
  const history = fullHistory();
  history.series.cpu = [10, 20, null, 15, 12.5];
  history.series.ts = [1, 2, 3, 4, 5];
  const card = renderTrendsCard(doc(), history);
  const row = card.querySelector('.trend-row[data-metric="cpu"]');

  assert.equal(row.querySelectorAll('.sparkline-series').length, 2);
  assert.equal(row.querySelector('.trend-value').textContent, '0.125 cores');
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

function key(document, canvas, value) {
  const event = new document.defaultView.KeyboardEvent('keydown', {key:value, cancelable:true});
  canvas.dispatchEvent(event);
  return event;
}
function pointer(document, canvas, type, options = {}) {
  const event = new document.defaultView.MouseEvent(type, {clientX:10, clientY:20, ...options});
  Object.defineProperties(event, {pointerType:{value:options.pointerType || 'mouse'}, pointerId:{value:1}});
  canvas.dispatchEvent(event);
}
function mountedHistory(history = fullHistory()) {
  const document = doc(), card = renderTrendsCard(document, history, 2);
  document.body.append(card);
  const canvas = card.querySelector('[data-history-chart="cpu"]');
  canvas.getBoundingClientRect = () => ({left:10,width:100,top:10,bottom:106});
  return {document, card, canvas};
}

test('charts are directly keyboard accessible and select the same instant in all four readings', () => {
  const {document, card, canvas} = mountedHistory();
  assert.equal(card.querySelector('input'), null, 'no separate timeline control');
  assert.equal(card.querySelectorAll('[role="slider"][tabindex="0"]').length,4);
  canvas.focus();
  assert.equal(key(document,canvas,'Home').defaultPrevented,true,'navigation does not scroll the page');
  assert.deepEqual([...card.querySelectorAll('.trend-value')].map(e=>e.textContent),['0.1 cores','100 MB','1','1']);
  assert.equal(card.querySelectorAll('.trend-cursor:not([hidden])').length,4);
  assert.match(canvas.getAttribute('aria-valuetext'),/CPU: 0.1 cores.*Memory: 100 MB.*Sessions: 1.*Replicas: 1/);
  assert.equal(card.dataset.inspectTimestamp,'1');
  assert.equal(key(document,canvas,'x').defaultPrevented,false);
  key(document,canvas,'ArrowLeft');assert.equal(canvas.getAttribute('aria-valuenow'),'0');
  key(document,canvas,'ArrowRight');assert.equal(canvas.getAttribute('aria-valuenow'),'1');
  key(document,canvas,'End');assert.equal(canvas.getAttribute('aria-valuenow'),'2');
  key(document,canvas,'Escape');
  assert.equal(document.activeElement,canvas);
  assert.equal(card.querySelectorAll('.trend-cursor:not([hidden])').length,0);
  assert.equal(card.querySelector('.trend-value').textContent,'0.125 cores');
  assert.match(card.querySelector('.trends-inspect-time').textContent,/Latest sample/);
  assert.equal(card.querySelector('.trends-latest').hidden,true);
  key(document,canvas,'Home');canvas.blur();
  assert.equal(card.dataset.inspectActive,'false');
});

test('tabbing between charts preserves the instant; Latest restores values and chart focus', () => {
  const {document,card,canvas} = mountedHistory();
  canvas.focus();key(document,canvas,'Home');
  const memory=card.querySelector('[data-history-chart="memory"]');memory.focus();
  assert.equal(memory.getAttribute('aria-valuenow'),'0');
  const latest=card.querySelector('.trends-latest');latest.focus();
  assert.equal(latest.hidden,false,'button remains available while focused');
  latest.click();
  assert.equal(document.activeElement,memory);
  assert.equal(card.dataset.inspectActive,'false');
  assert.equal(memory.getAttribute('aria-valuenow'),'2');
  assert.equal(latest.hidden,true);
});

test('touch dragging pins an actual sample until Latest, and scrolling cancellation resets inspection', () => {
  const {document,card,canvas} = mountedHistory();
  pointer(document,canvas,'pointerdown',{pointerType:'touch'});
  pointer(document,canvas,'pointermove',{pointerType:'touch',clientX:60});
  pointer(document,canvas,'pointerup',{pointerType:'touch',clientX:60});
  pointer(document,canvas,'pointerleave',{pointerType:'touch'});
  assert.equal(canvas.getAttribute('aria-valuenow'),'1');assert.equal(card.dataset.inspectActive,'true');
  card.querySelector('.trends-latest').click();
  assert.equal(card.dataset.inspectActive,'false');
  pointer(document,canvas,'pointermove',{pointerType:'touch'});
  assert.equal(card.dataset.inspectActive,'false','touch hover cannot change readings');
  pointer(document,canvas,'pointerdown',{pointerType:'touch'});
  pointer(document,canvas,'pointercancel',{pointerType:'touch'});
  assert.equal(card.dataset.inspectActive,'false');
});

test('mouse clicks do not leave stale readings after the pointer leaves', () => {
  const {document,card,canvas}=mountedHistory();
  pointer(document,canvas,'pointerdown');pointer(document,canvas,'pointerup');
  pointer(document,canvas,'pointerleave');
  assert.equal(card.dataset.inspectActive,'false');
  pointer(document,canvas,'pointerdown');
  pointer(document,canvas,'pointerup',{clientX:150});
  assert.equal(card.dataset.inspectActive,'false','release outside a captured chart returns to latest');
});

test('missing samples remain unavailable during inspection and shorter series do not imply current values', () => {
  const history=fullHistory();history.series.cpu=[10,null];
  const {document,card,canvas}=mountedHistory(history);
  canvas.focus();key(document,canvas,'Home');key(document,canvas,'ArrowRight');
  assert.equal(card.querySelector('.trend-value').textContent,'No sample');
  assert.equal(card.querySelector('.trend-point').hidden,true);
  key(document,canvas,'End');
  assert.equal(card.querySelector('.trend-value').textContent,'No sample');
});

test('invalid timestamps use sample positions without presenting fabricated clock times', () => {
  const history=fullHistory();history.series.ts=[3,2,1];
  const card=renderTrendsCard(doc(),history);
  assert.equal(card.querySelector('.trend-x-axis').textContent,'Sample 1Sample 3');
  assert.equal(card.querySelector('.trends-inspect-time').textContent,'Latest sample · Sample 3');
  card.inspectIndex(1);assert.equal(card.querySelector('.trends-inspect-time').textContent,'At Sample 2');
});

test('timeline selection can be restored after a background refresh without losing the chosen instant', () => {
  const card=renderTrendsCard(doc(),fullHistory());card.inspectTime(2);
  assert.equal(card.querySelector('[role="slider"]').getAttribute('aria-valuenow'),'1');
  assert.equal(card.querySelector('.trend-value').textContent,'0.2 cores');
});

test('history uses actual time spacing and breaks a line across collection outages', () => {
  const history=fullHistory();history.interval_seconds=15;history.series.ts=[0,15,90];
  const card=renderTrendsCard(doc(),history);
  const cpu=card.querySelector('.sparkline-cpu');
  assert.equal(cpu.querySelector('.sparkline-series').getAttribute('points').split(' ')[1].split(',')[0],'80');
  assert.equal(cpu.querySelectorAll('.sparkline-sample').length,1);
});

test('interactive history has no automated structural accessibility violations', async () => {
  const axe = (await import('axe-core')).default;
  const dom = new JSDOM('<!doctype html><html lang="en"><title>History</title><body><main></main></body></html>',{runScripts:'outside-only'});
  dom.window.document.querySelector('main').append(renderTrendsCard(dom.window.document, fullHistory(), 2));
  dom.window.eval(axe.source);
  const result=await dom.window.axe.run(dom.window.document,{rules:{'color-contrast':{enabled:false}}});
  assert.equal(result.violations.length,0,JSON.stringify(result.violations.map(v=>({id:v.id,targets:v.nodes.map(n=>n.target)}))));
  dom.window.close();
});

test('pointer inspection links all charts and returns to the latest values when the pointer leaves', () => {
  const document=doc(), card=renderTrendsCard(document,fullHistory());document.body.append(card);
  const canvas=card.querySelector('.trend-canvas');canvas.getBoundingClientRect=()=>({left:10,width:100});
  canvas.dispatchEvent(new document.defaultView.MouseEvent('pointermove',{clientX:10}));
  assert.equal(canvas.getAttribute('aria-valuenow'),'0');
  assert.deepEqual([...card.querySelectorAll('.trend-value')].map(e=>e.textContent),['0.1 cores','100 MB','1','1']);
  canvas.dispatchEvent(new document.defaultView.Event('pointerleave'));
  assert.equal(canvas.getAttribute('aria-valuenow'),'2');assert.equal(card.querySelector('.trend-value').textContent,'0.125 cores');
});
