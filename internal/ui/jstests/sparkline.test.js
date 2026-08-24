import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { sparklinePoints, renderSparkline } from '../static/views/sparkline.js';

const doc = () => new JSDOM('<!DOCTYPE html><body></body>').window.document;

test('empty series yields no points', () => {
  assert.deepEqual(sparklinePoints([], { width: 100, height: 20 }), []);
});

test('a single point sits at the vertical middle', () => {
  assert.deepEqual(sparklinePoints([42], { width: 100, height: 20 }), ['0,10']);
});

test('a flat series renders along the middle (no divide-by-zero)', () => {
  const pts = sparklinePoints([5, 5, 5], { width: 100, height: 20 });
  assert.deepEqual(pts, ['0,10', '50,10', '100,10']);
});

test('scaling maps min to the bottom and max to the top', () => {
  // min (0) -> y=height (bottom); max (10) -> y=0 (top).
  const pts = sparklinePoints([0, 10], { width: 100, height: 20 });
  assert.deepEqual(pts, ['0,20', '100,0']);
});

test('intermediate values scale proportionally', () => {
  const pts = sparklinePoints([0, 5, 10], { width: 100, height: 20 });
  assert.deepEqual(pts, ['0,20', '50,10', '100,0']);
});

test('an explicit domain keeps a series on an honest external scale', () => {
  const pts = sparklinePoints([25, 50], {
    width: 100,
    height: 20,
    domainMin: 0,
    domainMax: 100,
  });
  assert.deepEqual(pts, ['0,15', '100,10']);
});

test('step mode produces a stepped path of 2n-1 points', () => {
  const pts = sparklinePoints([0, 10], { width: 100, height: 20, step: true });
  // (x0,y0) -> (x1,y0) -> (x1,y1)
  assert.deepEqual(pts, ['0,20', '100,20', '100,0']);
});

test('non-finite values are coerced so points never contain NaN', () => {
  const pts = sparklinePoints([NaN, 10, Infinity], { width: 100, height: 20 });
  for (const p of pts) {
    assert.ok(!p.includes('NaN'), `point ${p} must not contain NaN`);
  }
  // NaN/Infinity coerce to 0, so the series behaves like [0, 10, 0].
  assert.deepEqual(pts, ['0,20', '50,0', '100,20']);
});

test('an absent point is skipped, not drawn as zero', () => {
  // The middle tick had no measurement. Plotting it as 0 would drop the line to
  // the floor and read as the app going idle, which is exactly what a restart
  // (the usual cause of a missing sample) does not mean.
  const pts = sparklinePoints([10, null, 10], { width: 100, height: 20 });
  assert.deepEqual(pts, ['0,10', '100,10']);
});

test('an absent point is excluded from the min/max scale', () => {
  // If null counted as 0 it would become the minimum and squash the real values
  // into the top of the box.
  const pts = sparklinePoints([5, null, 10], { width: 100, height: 20 });
  assert.deepEqual(pts, ['0,20', '100,0']);
});

test('surviving points keep the x their index earns', () => {
  // The gap still occupies time. Re-packing the remaining points would slide the
  // series along the x axis and misdate every reading after the gap.
  const pts = sparklinePoints([0, null, null, 10], { width: 90, height: 20 });
  assert.deepEqual(pts, ['0,20', '90,0']);
});

test('undefined is treated as absent, like null', () => {
  assert.deepEqual(sparklinePoints([10, undefined, 10], { width: 100, height: 20 }), [
    '0,10',
    '100,10',
  ]);
});

test('an all-absent series yields no points', () => {
  assert.deepEqual(sparklinePoints([null, null], { width: 100, height: 20 }), []);
});

test('a single absent point yields no points', () => {
  assert.deepEqual(sparklinePoints([null], { width: 100, height: 20 }), []);
});

test('one surviving point sits at the middle, at its own x', () => {
  const pts = sparklinePoints([null, 7, null], { width: 100, height: 20 });
  assert.deepEqual(pts, ['50,10']);
});

test('step mode does not invent a step across a telemetry gap', () => {
  const pts = sparklinePoints([0, null, 10], { width: 100, height: 20, step: true });
  assert.deepEqual(pts, ['0,20', '100,0']);
});

test('renderSparkline omits the polyline when every point is absent', () => {
  const svg = renderSparkline(doc(), [null, null], { width: 100, height: 20 });
  assert.equal(svg.querySelector('polyline'), null);
});

test('renderSparkline builds an accessible svg with a polyline', () => {
  const svg = renderSparkline(doc(), [0, 10], { width: 100, height: 20, ariaLabel: 'CPU 10%' });
  assert.equal(svg.tagName.toLowerCase(), 'svg');
  assert.equal(svg.getAttribute('role'), 'img');
  assert.equal(svg.getAttribute('aria-label'), 'CPU 10%');
  assert.equal(svg.getAttribute('viewBox'), '0 0 100 20');
  const poly = svg.querySelector('polyline');
  assert.ok(poly, 'expected a polyline');
  assert.equal(poly.getAttribute('points'), '0,20 100,0');
  assert.equal(poly.getAttribute('stroke'), 'currentColor');
});

test('renderSparkline can add reference grid lines and a latest-value marker', () => {
  const svg = renderSparkline(doc(), [0, 10], {
    width: 100,
    height: 20,
    grid: true,
    endPoint: true,
  });
  assert.equal(svg.querySelectorAll('.sparkline-grid').length, 3);
  const point = svg.querySelector('.sparkline-endpoint');
  assert.ok(point);
  assert.equal(point.getAttribute('cx'), '100');
  assert.equal(point.getAttribute('cy'), '0');
});

test('renderSparkline breaks the stroke at missing samples', () => {
  const svg = renderSparkline(doc(), [0, 5, null, 10, 15], {
    width: 100,
    height: 20,
    domainMin: 0,
    domainMax: 20,
  });
  const strokes = [...svg.querySelectorAll('.sparkline-series')].map((line) =>
    line.getAttribute('points'));
  assert.deepEqual(strokes, ['0,20 25,15', '75,10 100,5']);
});

test('renderSparkline does not mark a missing latest sample as current', () => {
  const svg = renderSparkline(doc(), [0, 10, null], {
    width: 100,
    height: 20,
    endPoint: true,
  });
  assert.equal(svg.querySelector('.sparkline-endpoint'), null);
});

test('renderSparkline keeps an isolated surviving sample visible', () => {
  const svg = renderSparkline(doc(), [null, 25, null], {
    width: 100,
    height: 20,
    domainMin: 0,
    domainMax: 100,
  });
  const sample = svg.querySelector('.sparkline-sample');
  assert.ok(sample);
  assert.equal(sample.getAttribute('cx'), '50');
  assert.equal(sample.getAttribute('cy'), '15');
});

test('renderSparkline on an empty series omits the polyline', () => {
  const svg = renderSparkline(doc(), [], { width: 100, height: 20 });
  assert.equal(svg.tagName.toLowerCase(), 'svg');
  assert.equal(svg.querySelector('polyline'), null);
});
