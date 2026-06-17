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

test('renderSparkline on an empty series omits the polyline', () => {
  const svg = renderSparkline(doc(), [], { width: 100, height: 20 });
  assert.equal(svg.tagName.toLowerCase(), 'svg');
  assert.equal(svg.querySelector('polyline'), null);
});
