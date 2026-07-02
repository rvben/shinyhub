import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  THEME_STORAGE_KEY,
  resolveTheme,
  getThemePreference,
  applyTheme,
  setThemePreference,
} from '../static/views/theme.js';

// A minimal window stub: a Map-backed localStorage, a matchMedia keyed on the
// prefers-light query, and a documentElement whose dataset records data-theme.
function fakeWindow({ stored, prefersLight = false, throwStorage = false } = {}) {
  const store = new Map();
  if (stored !== undefined) store.set(THEME_STORAGE_KEY, stored);
  const dataset = {};
  return {
    document: { documentElement: { dataset } },
    localStorage: {
      getItem(k) { if (throwStorage) throw new Error('blocked'); return store.has(k) ? store.get(k) : null; },
      setItem(k, v) { if (throwStorage) throw new Error('blocked'); store.set(k, v); },
    },
    matchMedia(q) { return { matches: q.includes('light') ? prefersLight : false }; },
    _store: store,
    _dataset: dataset,
  };
}

test('resolveTheme: system follows the OS signal', () => {
  assert.equal(resolveTheme('system', true), 'light');
  assert.equal(resolveTheme('system', false), 'dark');
});

test('resolveTheme: explicit light/dark override the OS signal', () => {
  assert.equal(resolveTheme('light', false), 'light');
  assert.equal(resolveTheme('dark', true), 'dark');
});

test('resolveTheme: unknown/absent preference is treated as system', () => {
  assert.equal(resolveTheme('bogus', true), 'light');
  assert.equal(resolveTheme(undefined, false), 'dark');
});

test('getThemePreference defaults to system and rejects stale values', () => {
  assert.equal(getThemePreference(fakeWindow({})), 'system');
  assert.equal(getThemePreference(fakeWindow({ stored: 'light' })), 'light');
  assert.equal(getThemePreference(fakeWindow({ stored: 'weird' })), 'system');
});

test('getThemePreference tolerates unavailable storage', () => {
  assert.equal(getThemePreference(fakeWindow({ throwStorage: true })), 'system');
});

test('applyTheme stamps the effective theme onto documentElement.dataset', () => {
  const w = fakeWindow({ prefersLight: true });
  assert.equal(applyTheme(w, 'system'), 'light');
  assert.equal(w._dataset.theme, 'light');
  assert.equal(applyTheme(w, 'dark'), 'dark');
  assert.equal(w._dataset.theme, 'dark');
});

test('setThemePreference persists and applies immediately', () => {
  const w = fakeWindow({ prefersLight: false });
  const eff = setThemePreference(w, 'light');
  assert.equal(eff, 'light');
  assert.equal(w._store.get(THEME_STORAGE_KEY), 'light');
  assert.equal(w._dataset.theme, 'light');
});

test('setThemePreference applies for the session even if storage write fails', () => {
  const w = fakeWindow({ throwStorage: true, prefersLight: false });
  assert.equal(setThemePreference(w, 'light'), 'light');
  assert.equal(w._dataset.theme, 'light');
});

test('setThemePreference: system resolves against the live OS signal', () => {
  const w = fakeWindow({ prefersLight: true });
  assert.equal(setThemePreference(w, 'system'), 'light');
  assert.equal(w._dataset.theme, 'light');
});
