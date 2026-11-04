import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { installMobileDisclosures } from '../static/views/mobile-disclosure.js';

function setup(mobile = true) {
  const { window } = new JSDOM(`<body><details class="page-secondary" data-mobile-disclosure open><summary>More</summary><div><button id="action">Edit</button></div></details><input id="dialog"><button id="outside">Outside</button></body>`);
  const media = new window.EventTarget();
  media.matches = mobile;
  window.matchMedia = () => media;
  const doc = window.document;
  const dispose = installMobileDisclosures(doc, window);
  return { window, doc, media, dispose, detail: doc.querySelector('details'),
    resize(mobile) { media.matches = mobile; media.dispatchEvent(new window.Event('change')); } };
}

test('mobile starts compact, desktop exposes the same controls and listeners', () => {
  const { doc, detail, resize, dispose } = setup();
  const action = doc.getElementById('action');
  let clicks = 0;
  action.addEventListener('click', () => clicks++);
  assert.equal(detail.open, false);
  resize(false);
  assert.equal(detail.open, true);
  assert.equal(doc.getElementById('action'), action);
  action.click();
  assert.equal(clicks, 1);
  dispose();
});

test('resizing to mobile and Escape never leave focus in collapsed content', () => {
  const { window, doc, detail, resize, dispose } = setup(false);
  doc.getElementById('action').focus();
  resize(true);
  assert.equal(doc.activeElement, detail.querySelector('summary'));
  detail.open = true;
  doc.getElementById('action').focus();
  doc.activeElement.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(detail.open, false);
  assert.equal(doc.activeElement, detail.querySelector('summary'));
  dispose();
});

test('a dialog keeps its opener available for focus restoration', () => {
  const { doc, detail, dispose } = setup();
  detail.open = true;
  const action = doc.getElementById('action');
  action.addEventListener('click', () => doc.getElementById('dialog').focus());
  action.focus(); action.click();
  assert.equal(detail.open, true);
  assert.equal(doc.activeElement.id, 'dialog');
  dispose();
});

test('newly mounted disclosures start compact without resetting existing choices', async () => {
  const { doc, detail, dispose } = setup();
  detail.open = true;
  const next = doc.createElement('details');
  next.setAttribute('data-mobile-disclosure', ''); next.open = true;
  next.innerHTML = '<summary>Metrics</summary><p>CPU</p>';
  doc.body.append(next);
  await Promise.resolve();
  assert.equal(next.open, false);
  assert.equal(detail.open, true);
  dispose();
});

test('Manage follows available destinations when permissions change', async () => {
  const { doc, dispose } = setup();
  const manage = doc.createElement('details');
  manage.setAttribute('data-mobile-disclosure', ''); manage.setAttribute('data-hide-empty', '');
  manage.innerHTML = '<summary>Manage</summary><a href="/identity" hidden>Identity</a>';
  doc.body.append(manage);
  await Promise.resolve();
  assert.equal(manage.hidden, true);
  manage.querySelector('a').hidden = false;
  await Promise.resolve();
  assert.equal(manage.hidden, false);
  dispose();
});

test('outside activation closes More, and disposal releases media listeners', () => {
  const { doc, detail, resize, dispose } = setup();
  detail.open = true;
  doc.getElementById('outside').click();
  assert.equal(detail.open, false);
  dispose(); resize(false);
  assert.equal(detail.open, false);
});
