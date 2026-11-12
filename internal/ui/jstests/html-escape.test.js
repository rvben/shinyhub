import { test } from 'node:test';
import assert from 'node:assert/strict';
import { escapeHtml } from '../static/views/html-escape.js';

test('escapes every character that can leave text or a quoted attribute', () => {
  assert.equal(escapeHtml(`<a href="x" title='y'>&</a>`),
    '&lt;a href=&quot;x&quot; title=&#39;y&#39;&gt;&amp;&lt;/a&gt;');
});

test('escapes an ampersand once, so an existing entity stays literal text', () => {
  assert.equal(escapeHtml('&lt;'), '&amp;lt;');
});

test('stringifies non-string values', () => {
  assert.equal(escapeHtml(42), '42');
  assert.equal(escapeHtml(null), 'null');
});
