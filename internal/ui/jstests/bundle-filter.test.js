import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { inspectBundleEntry } from '../static/views/bundle-filter.js';

// The real rules, so a change to the shipped policy is exercised here rather
// than against a hand-written copy that can drift from it. Go's
// TestDefaultRulesMatchBundleRulesJSON pins this file to DefaultRules().
const rules = JSON.parse(
  readFileSync(fileURLToPath(new URL('../static/bundle-rules.json', import.meta.url)), 'utf8'),
);

test('a bare cacheDirs entry matches the first path segment only', () => {
  assert.equal(inspectBundleEntry(rules, '.venv/lib/python3.12/site.py', 10), 'skipCacheDir');
  assert.equal(inspectBundleEntry(rules, 'node_modules', 0), 'skipCacheDir');
  // Not the first segment: a directory that merely contains one of the names
  // is the app's own content and must be bundled.
  assert.equal(inspectBundleEntry(rules, 'src/node_modules/index.js', 10), 'accept');
});

test('a slash-bearing cacheDirs entry matches that whole prefix', () => {
  // renv restores into renv/library as symlinks into a machine-local cache.
  // Matching only the first segment ("renv") would either miss this entirely
  // or wrongly exclude renv/activate.R, which the bundle needs.
  assert.equal(inspectBundleEntry(rules, 'renv/library/R-4.4/x86_64/shiny/DESCRIPTION', 10), 'skipCacheDir');
  assert.equal(inspectBundleEntry(rules, 'renv/library', 0), 'skipCacheDir');
  assert.equal(inspectBundleEntry(rules, 'renv/activate.R', 10), 'accept');
  assert.equal(inspectBundleEntry(rules, 'renv/settings.json', 10), 'accept');
  // The prefix is a path prefix, not a string prefix: a sibling whose name
  // merely starts with it is the app's own content.
  assert.equal(inspectBundleEntry(rules, 'renv/library-notes.md', 10), 'accept');
});

test('data directories are rejected rather than skipped', () => {
  assert.equal(inspectBundleEntry(rules, 'data/rows.csv', 10), 'rejectDataDir');
  assert.equal(inspectBundleEntry(rules, 'datasets/rows.csv', 10), 'rejectDatasetDir');
  assert.equal(inspectBundleEntry(rules, '.shinyhub-data/rows.csv', 10), 'rejectDatasetDir');
});

test('extension and size rules apply to accepted paths', () => {
  assert.equal(inspectBundleEntry(rules, 'model/weights.parquet', 10), 'rejectExtension');
  assert.equal(inspectBundleEntry(rules, 'app.py', rules.maxFileBytes + 1), 'rejectFileSize');
  assert.equal(inspectBundleEntry(rules, 'app.py', rules.maxFileBytes), 'accept');
});

test('a leading slash does not defeat classification', () => {
  assert.equal(inspectBundleEntry(rules, '/.venv/bin/python', 10), 'skipCacheDir');
  assert.equal(inspectBundleEntry(rules, '/renv/library/shiny/DESCRIPTION', 10), 'skipCacheDir');
  assert.equal(inspectBundleEntry(rules, '/data/rows.csv', 10), 'rejectDataDir');
});
