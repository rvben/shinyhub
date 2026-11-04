import test from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import { directSettings, populateDirectSetting, installDirectSetting, refreshDirectSetting, readDirectSetting } from '../static/views/direct-setting.js';
const html = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');
function fixture(id, value, defaultValue = '75') {
  const doc = new JSDOM(html).window.document;
  const input = doc.getElementById(id);
  input.dataset.defaultValue = defaultValue;
  populateDirectSetting(doc, id, value, true);
  installDirectSetting(doc, id, () => {});
  return { doc, input, mode: doc.getElementById(`${id}-mode`),
    override: doc.querySelector(`[data-setting="${id}"][data-policy="override"]`),
    off: doc.querySelector(`[data-setting="${id}"][data-policy="off"]`),
    type(value) { input.value = value; input.dispatchEvent(new doc.defaultView.Event('input')); },
  };
}
for (const id of ['scaling-cap', 'hibernate-custom-minutes', 'autoscale-target']) {
  test(`${id}: native policies preserve exact values and custom drafts`, () => {
    const def = directSettings[id];
    const {doc, input, mode, type} = fixture(id, def.inherit, '20');
    const choose = value => { mode.value = value; mode.dispatchEvent(new doc.defaultView.Event('change')); };
    assert.equal(mode.tagName, 'SELECT');
    assert.equal(input.closest('.setting-number').hidden, true);
    assert.equal(readDirectSetting(doc, id), def.inherit);
    choose('custom'); type('40');
    assert.equal(input.closest('.setting-number').hidden, false);
    assert.equal(readDirectSetting(doc, id), 40 / (def.factor || 1));
    if (Object.hasOwn(def, 'off')) {
      choose('off');
      assert.equal(readDirectSetting(doc, id), def.off);
      assert.match(doc.getElementById(`${id}-hint`).textContent, /Idle replicas stay running/);
      assert.equal(input.closest('.setting-number').hidden, true);
    }
    choose('inherit'); choose('custom');
    assert.equal(input.value, '40');
    type(''); assert.throws(() => readDirectSetting(doc, id), /enter/);
    type('0'); assert.throws(() => readDirectSetting(doc, id), /enter/);
    populateDirectSetting(doc, id, def.inherit, false);
    assert.equal(mode.disabled, true);
    choose('custom'); assert.equal(input.readOnly, true);
  });
}
test('render cost remains directly editable because it has no inherited policy', () => {
  const { doc, input, off, type } = fixture('render-seconds', 0);
  assert.equal(input.readOnly, false);
  type('1.5'); assert.equal(readDirectSetting(doc, 'render-seconds'), 1.5);
  off.click(); assert.equal(readDirectSetting(doc, 'render-seconds'), 0);
  type('2'); assert.equal(readDirectSetting(doc, 'render-seconds'), 2);
});
for (const id of ['resources-memory', 'resources-cpu']) {
  test(`${id}: native policies preserve custom drafts, inheritance and no-limit values`, () => {
    const {doc, input, mode, type} = fixture(id, null, '512');
    const choose = value => { mode.value = value; mode.dispatchEvent(new doc.defaultView.Event('change')); };
    assert.equal(mode.tagName, 'SELECT');
    assert.equal(input.closest('.setting-number').hidden, true);
    assert.equal(readDirectSetting(doc, id), null);
    choose('custom');
    assert.equal(input.closest('.setting-number').hidden, false);
    assert.equal(input.value, '512');
    type(id === 'resources-cpu' ? '1.5' : '1024');
    const expected = id === 'resources-cpu' ? 150 : 1024;
    choose('off');
    assert.equal(readDirectSetting(doc, id), 0);
    assert.equal(input.closest('.setting-number').hidden, true);
    assert.match(doc.getElementById(`${id}-hint`).textContent, /Available host resources are shared/);
    choose('inherit');
    input.dataset.defaultValue = '256'; refreshDirectSetting(doc, id);
    assert.equal(readDirectSetting(doc, id), null);
    choose('custom');
    assert.equal(readDirectSetting(doc, id), expected, 'custom draft survives both other policies');
    type(''); assert.throws(() => readDirectSetting(doc, id), /enter/);
    type('0'); assert.throws(() => readDirectSetting(doc, id), /enter/);
    populateDirectSetting(doc, id, null, false);
    assert.equal(mode.disabled, true);
    choose('custom');
    assert.equal(input.readOnly, true, 'a disabled policy cannot unlock editing');
  });
}

test('unknown defaults stay unknown and do not invent a custom limit', () => {
  const {doc, input, mode} = fixture('resources-memory', null, '');
  assert.equal(input.value, '');
  assert.match(doc.getElementById('resources-memory-hint').textContent, /not reported/);
  mode.value = 'custom'; mode.dispatchEvent(new doc.defaultView.Event('change'));
  assert.throws(() => readDirectSetting(doc, 'resources-memory'), /enter/);
});

test('an unlimited server default remains distinct from an explicit no-limit choice', () => {
  const {doc, input, mode} = fixture('resources-memory', null, '0');
  assert.equal(readDirectSetting(doc, 'resources-memory'), null);
  assert.match(doc.getElementById('resources-memory-hint').textContent, /No limit/);
  mode.value = 'off'; mode.dispatchEvent(new doc.defaultView.Event('change'));
  assert.equal(readDirectSetting(doc, 'resources-memory'), 0);
  assert.equal(input.closest('.setting-number').hidden, true);
});

test('default updates preserve custom limits and discard resets remembered drafts', () => {
  const {doc, input, mode, type} = fixture('resources-memory', null, '512');
  const choose = value => { mode.value = value; mode.dispatchEvent(new doc.defaultView.Event('change')); };
  choose('custom'); type('999'); choose('inherit');
  input.dataset.defaultValue = '1024'; refreshDirectSetting(doc, 'resources-memory');
  assert.equal(input.value, '1024');
  assert.equal(readDirectSetting(doc, 'resources-memory'), null);
  refreshDirectSetting(doc, 'resources-memory', {resetDraft:true});
  choose('custom');
  assert.equal(input.value, '1024', 'discard clears the previous custom draft');
  input.dataset.defaultValue = '2048'; refreshDirectSetting(doc, 'resources-memory');
  assert.equal(input.value, '1024', 'new defaults cannot replace a custom limit');
  populateDirectSetting(doc, 'resources-memory', 0, true);
  choose('custom');
  assert.equal(input.value, '2048', 'app navigation clears remembered values');
});

test('CPU cores round-trip exactly to integer percentage quotas', () => {
  const id = 'resources-cpu';
  const {doc,input,type,mode} = fixture(id,150,'1');
  assert.equal(input.value,'1.5');
  for (const [cores,quota] of [['0.01',1],['0.25',25],['0.5',50],['0.29',29],['1',100],['1.5',150],['64',6400]]) {
    type(cores); assert.equal(readDirectSetting(doc,id),quota);
    populateDirectSetting(doc,id,quota,true); assert.equal(input.value,cores);
  }
  type('0.125'); assert.throws(() => readDirectSetting(doc,id),/increments of 0.01 core/);
  type('64.01'); assert.throws(() => readDirectSetting(doc,id),/0.01 to 64 cores/);
  mode.value='off'; mode.dispatchEvent(new doc.defaultView.Event('change'));
  assert.equal(readDirectSetting(doc,id),0);
  mode.value='inherit'; mode.dispatchEvent(new doc.defaultView.Event('change'));
  assert.equal(readDirectSetting(doc,id),null);
});
