import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { renderAutoscaleVisual } from '../static/views/autoscale-visual.js';

function fixture() {
  const dom = new JSDOM(`<section id="app-detail-autoscale-visual" hidden>
    <div class="autoscale-range-track"><span class="autoscale-range-segments"></span></div>
    <span class="autoscale-min"></span><span class="autoscale-max"></span>
    <strong class="autoscale-next"></strong><span class="autoscale-session-now"></span><span class="autoscale-session-state"></span>
    <div class="autoscale-load-track" hidden><span class="autoscale-load-fill"></span></div>
    <div class="autoscale-load-labels"><span class="autoscale-band-start"></span><span class="autoscale-session-target"></span></div>
  </section>`);
  return dom.window.document.querySelector('section');
}

const state = {
  enabled: true, globalEnabled: true, min: 2, max: 24, current: 16,
  effectiveCap: 60, effectiveTarget: 0.8,
};

test('autoscale visual shows bounds, desired count, and live load toward next replica', () => {
  const root = fixture();
  renderAutoscaleVisual(root, state, 640, 16);
  assert.equal(root.hidden, false);
  assert.equal(root.querySelector('.autoscale-min').textContent, 'Min 2');
  assert.equal(root.querySelector('.autoscale-max').textContent, 'Max 24');
  assert.match(root.querySelector('.autoscale-range-track').getAttribute('aria-label'), /minimum 2, 16 running, 16 desired, maximum 24/);
  assert.equal(root.querySelectorAll('.autoscale-range-segments span.is-running').length, 15);
  assert.equal(root.querySelector('.autoscale-next').textContent, 'Next: 17 replicas');
  assert.equal(root.querySelector('.autoscale-session-now').textContent, '640 active');
  assert.equal(root.querySelector('.autoscale-band-start').textContent, '16 at 721');
  assert.equal(root.querySelector('.autoscale-session-target').textContent, '17 at 769 sessions');
  assert.equal(root.querySelector('.autoscale-load-track').hidden, false);
  assert.equal(root.querySelector('.autoscale-load-labels').hidden, false);
  assert.match(root.querySelector('.autoscale-load-track').getAttribute('aria-label'), /640 active sessions; scale to 17 at 769 sessions; 129 remaining/);
  assert.equal(root.querySelector('.autoscale-load-track').style.getPropertyValue('--session-position'), '0%');
});

test('autoscale visual uses the desired count for the range during scale-out', () => {
  const root = fixture();
  renderAutoscaleVisual(root, { ...state, current: 17 }, 780, 16);
  assert.match(root.querySelector('.autoscale-range-track').getAttribute('aria-label'), /16 running, 17 desired/);
  assert.equal(root.querySelector('.autoscale-next').textContent, 'Next: 18 replicas');
  assert.equal(root.querySelector('.autoscale-session-now').textContent, '780 active');
  assert.equal(root.querySelector('.autoscale-band-start').textContent, '17 at 769');
  assert.equal(root.querySelector('.autoscale-session-target').textContent, '18 at 817 sessions');
  assert.match(root.querySelector('.autoscale-load-track').style.getPropertyValue('--session-position'), /^22\.9/);
  assert.equal(root.querySelector('.autoscale-range-track').classList.contains('is-pending'), true);
  assert.equal(root.querySelectorAll('.autoscale-range-segments span.is-desired').length, 1);
});

test('autoscale visual handles reached, missing, paused, maximum and off states', () => {
  const root = fixture();
  renderAutoscaleVisual(root, state, 800);
  assert.match(root.querySelector('.autoscale-session-state').textContent, /Threshold reached/);
  assert.equal(root.querySelector('.autoscale-load-track').style.getPropertyValue('--session-position'), '100%');
  renderAutoscaleVisual(root, state, null);
  assert.match(root.textContent, /Waiting for session load/);
  assert.equal(root.querySelector('.autoscale-load-track').hidden, true);
  renderAutoscaleVisual(root, { ...state, globalEnabled: false }, 640);
  assert.match(root.textContent, /Autoscale paused/);
  assert.equal(root.querySelector('.autoscale-session-target').textContent, '');
  assert.equal(root.querySelector('.autoscale-load-track').hidden, true);
  assert.equal(root.querySelector('.autoscale-load-labels').hidden, true);
  renderAutoscaleVisual(root, { ...state, current: 24 }, 640);
  assert.match(root.textContent, /Maximum desired capacity/);
  renderAutoscaleVisual(root, { ...state, enabled: false }, 640);
  assert.equal(root.hidden, true);
});

test('maximum state distinguishes desired capacity from replicas still starting', () => {
  const root = fixture();
  renderAutoscaleVisual(root, { ...state, max: 4, current: 4, runtimeCapped: true, configuredMax: 24 }, 200, 2);
  assert.match(root.textContent, /Maximum desired capacity/);
  assert.match(root.querySelector('.autoscale-session-state').textContent, /Runtime limit: 4 replicas \(configured 24\)/);
  assert.match(root.querySelector('.autoscale-range-track').getAttribute('aria-label'), /2 running, 4 desired, maximum 4/);
});

test('cooldown and controller timing are readable without hovering', () => {
  const root = fixture();
  renderAutoscaleVisual(root, { ...state, inCooldown: true,
    cooldownUntil: new Date(Date.now() + 60000) }, 400, 16);
  assert.match(root.querySelector('.autoscale-session-state').textContent, /Cooldown.*threshold does not mean immediate scaling/);
  renderAutoscaleVisual(root, state, 400, 16);
  assert.match(root.querySelector('.autoscale-session-state').textContent, /pool rejections can trigger sooner.*controller scan/);
  renderAutoscaleVisual(root, { ...state, isElastic: true }, 400, 16);
  assert.equal(root.hidden, true);
});
