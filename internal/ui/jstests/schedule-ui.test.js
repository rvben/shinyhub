import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  dstAdvisoryText,
  dstAdvisoryMarkup,
  DST_ADVISORY_LABEL,
} from '../static/views/schedule-ui.js';

test('dstAdvisoryText: returns the advisory string when present', () => {
  assert.equal(
    dstAdvisoryText({ dst_advisory: 'Schedule fires twice on 2025-10-26.' }),
    'Schedule fires twice on 2025-10-26.',
  );
});

test('dstAdvisoryText: null when missing / empty / blank / non-object', () => {
  assert.equal(dstAdvisoryText({}), null);
  assert.equal(dstAdvisoryText({ dst_advisory: '' }), null);
  assert.equal(dstAdvisoryText({ dst_advisory: '   ' }), null);
  assert.equal(dstAdvisoryText({ dst_advisory: null }), null);
  assert.equal(dstAdvisoryText(null), null);
  assert.equal(dstAdvisoryText(undefined), null);
});

test('dstAdvisoryMarkup: empty string when no advisory', () => {
  assert.equal(dstAdvisoryMarkup({}), '');
  assert.equal(dstAdvisoryMarkup({ dst_advisory: '' }), '');
  assert.equal(dstAdvisoryMarkup(null), '');
});

test('dstAdvisoryMarkup: visible badge carries the full advisory in title + aria-label', () => {
  const html = dstAdvisoryMarkup({ dst_advisory: 'Schedule fires twice on 2025-10-26.' });
  assert.match(html, /dst-advisory/);
  assert.ok(html.includes(DST_ADVISORY_LABEL));
  assert.match(html, /title="Schedule fires twice on 2025-10-26\."/);
  assert.match(html, /aria-label="Schedule fires twice on 2025-10-26\."/);
});

test('dstAdvisoryMarkup: escapes HTML metacharacters in the advisory text', () => {
  const html = dstAdvisoryMarkup({ dst_advisory: 'fires <b>twice</b> & "again"' });
  assert.doesNotMatch(html, /<b>twice<\/b>/);
  assert.match(html, /&lt;b&gt;twice&lt;\/b&gt;/);
  assert.match(html, /&amp;/);
  assert.match(html, /&quot;again&quot;/);
});
