import { test } from 'node:test';
import assert from 'node:assert/strict';
import { resourceSettingsPatch } from '../static/views/resource-settings.js';

test('CPU-only save preserves memory changed on the server since the form loaded', () => {
  assert.deepEqual(resourceSettingsPatch(
    { memory_limit_mb: 256, cpu_quota_percent: 200 },
    { memory_limit_mb: 256, cpu_quota_percent: 100 },
  ), { cpu_quota_percent: 200 });
});

test('clearing memory submits only the edited field and a clean form submits nothing', () => {
  const saved = { memory_limit_mb: null, cpu_quota_percent: 100 };
  assert.deepEqual(resourceSettingsPatch(saved,
    { memory_limit_mb: 256, cpu_quota_percent: 100 }), { memory_limit_mb: null });
  assert.deepEqual(resourceSettingsPatch(saved, saved), {});
});
