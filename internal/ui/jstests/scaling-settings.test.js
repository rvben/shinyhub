import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { scalingSettingsSnapshot, scalingSettingsPatch } from '../static/views/scaling-settings.js';

function form() {
  return new JSDOM(`<input id="scaling-replicas" value="2">
    <input id="scaling-cap" value="10">
    <select id="worker-isolation"><option value="multiplex">Multiplex</option><option value="grouped">Grouped</option></select>
    <input id="worker-grouped-size" value="1">
    <input id="worker-max-workers" value="0">
    <input id="worker-warm-spares" value="0">`).window.document;
}

test('cap-only save omits stale replica count and untouched displayed worker defaults', () => {
  const doc = form();
  const baseline = scalingSettingsSnapshot(doc);
  // Autoscaling may have changed the server count since this form loaded.
  doc.getElementById('scaling-cap').value = '20';
  assert.deepEqual(scalingSettingsPatch(scalingSettingsSnapshot(doc), baseline), {
    max_sessions_per_replica: 20,
  });
});

test('replica increase submits just the requested count', () => {
  const doc = form();
  const baseline = scalingSettingsSnapshot(doc);
  doc.getElementById('scaling-replicas').value = '4';
  assert.deepEqual(scalingSettingsPatch(scalingSettingsSnapshot(doc), baseline), { replicas: 4 });
});

test('worker changes submit only edited fields and subsequent save uses new baseline', () => {
  const doc = form();
  const baseline = scalingSettingsSnapshot(doc);
  doc.getElementById('worker-isolation').value = 'grouped';
  doc.getElementById('worker-grouped-size').value = '5';
  const current = scalingSettingsSnapshot(doc);
  assert.deepEqual(scalingSettingsPatch(current, baseline), {
    worker_isolation: 'grouped', worker_grouped_size: 5,
  });
  assert.deepEqual(scalingSettingsPatch(scalingSettingsSnapshot(doc), current), {});
});
