import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  formatTraceWhen,
  formatPollStatus,
  makeTraceRow,
  UNSAMPLED_TITLE,
} from '../static/views/traces-ui.js';

// TRC-3: the "When" column must carry a date for a buffer that can span days.
test('formatTraceWhen: same calendar day shows time only', () => {
  const now = new Date('2026-05-22T14:00:00');
  const started = new Date('2026-05-22T09:30:00');
  const out = formatTraceWhen(started.toISOString(), now);
  // Same day: must NOT contain the date portion, only a time-of-day string.
  assert.equal(out, started.toLocaleTimeString());
});

test('formatTraceWhen: a different day includes the date', () => {
  const now = new Date('2026-05-22T14:00:00');
  const started = new Date('2026-05-20T09:30:00');
  const out = formatTraceWhen(started.toISOString(), now);
  assert.equal(out, started.toLocaleString());
  // Sanity: the cross-day rendering differs from the time-only rendering.
  assert.notEqual(out, started.toLocaleTimeString());
});

test('formatTraceWhen: missing / invalid timestamp is an em-dash', () => {
  assert.equal(formatTraceWhen('', new Date()), '—');
  assert.equal(formatTraceWhen(undefined, new Date()), '—');
  assert.equal(formatTraceWhen('not-a-date', new Date()), '—');
});

// TRC-5: the traces-status element must reflect poll freshness.
test('formatPollStatus: fresh / seconds / minutes buckets', () => {
  const now = new Date('2026-05-22T14:00:00Z');
  assert.equal(formatPollStatus(new Date('2026-05-22T13:59:58Z'), now), 'updated just now');
  assert.equal(formatPollStatus(new Date('2026-05-22T13:59:40Z'), now), 'updated 20s ago');
  assert.equal(formatPollStatus(new Date('2026-05-22T13:57:00Z'), now), 'updated 3m ago');
});

test('formatPollStatus: empty / invalid input yields empty string', () => {
  assert.equal(formatPollStatus(null, new Date()), '');
  assert.equal(formatPollStatus('nope', new Date()), '');
});

// TRC-2: an unsampled span was never exported, so it must render a muted,
// non-linked id tagged "(unsampled)" rather than a dead deep link.
test('makeTraceRow: unsampled span renders no link, tagged (unsampled)', () => {
  const { document } = new JSDOM('<body></body>').window;
  const tr = makeTraceRow(document, {
    trace_id: '0123456789abcdef0123456789abcdef',
    method: 'GET',
    path: '/',
    status: 200,
    duration_ms: 1200,
    replica: 0,
    started_at: '2026-05-22T09:30:00Z',
    sampled: false,
  }, 'https://backend/trace/{trace_id}', new Date('2026-05-22T10:00:00Z'));

  assert.equal(tr.querySelector('a'), null, 'unsampled span must not deep-link');
  assert.match(tr.textContent, /\(unsampled\)/);
  const muted = tr.querySelector('.trace-unsampled');
  assert.ok(muted, 'unsampled id must carry the trace-unsampled class');
  assert.equal(muted.title, UNSAMPLED_TITLE);
});

test('makeTraceRow: sampled span with link template deep-links into the backend', () => {
  const { document } = new JSDOM('<body></body>').window;
  const tr = makeTraceRow(document, {
    trace_id: 'abcdef0123456789abcdef0123456789',
    method: 'POST',
    path: '/submit',
    status: 502,
    duration_ms: 30,
    replica: 1,
    started_at: '2026-05-22T09:30:00Z',
    sampled: true,
  }, 'https://backend/trace/{trace_id}', new Date('2026-05-22T10:00:00Z'));

  const a = tr.querySelector('a');
  assert.ok(a, 'sampled span must deep-link when a template is configured');
  assert.equal(a.getAttribute('href'), 'https://backend/trace/abcdef0123456789abcdef0123456789');
  assert.equal(a.getAttribute('target'), '_blank');
  assert.equal(a.getAttribute('rel'), 'noopener');
  // 5xx must mark the row as an error row.
  assert.match(tr.className, /replica-row-error/);
});

test('makeTraceRow: sampled span without a template shows a plain code id', () => {
  const { document } = new JSDOM('<body></body>').window;
  const tr = makeTraceRow(document, {
    trace_id: 'abcdef0123456789abcdef0123456789',
    method: 'GET',
    path: '/',
    status: 200,
    duration_ms: 10,
    replica: 0,
    started_at: '2026-05-22T09:30:00Z',
    sampled: true,
  }, '', new Date('2026-05-22T10:00:00Z'));

  assert.equal(tr.querySelector('a'), null);
  assert.ok(tr.querySelector('code'));
  assert.doesNotMatch(tr.textContent, /\(unsampled\)/);
});

test('makeTraceRow: builds method/path/status/duration/replica cells safely', () => {
  const { document } = new JSDOM('<body></body>').window;
  const tr = makeTraceRow(document, {
    trace_id: 'abcdef0123456789abcdef0123456789',
    method: 'GET',
    path: '<script>',
    status: 200,
    duration_ms: 42,
    replica: 2,
    started_at: '2026-05-22T09:30:00Z',
    sampled: true,
  }, '', new Date('2026-05-22T10:00:00Z'));

  const cells = tr.querySelectorAll('td');
  assert.equal(cells.length, 7);
  // The path cell uses textContent, so the markup is inert (no child <script>).
  const pathCode = cells[2].querySelector('code');
  assert.equal(pathCode.textContent, '<script>');
  assert.equal(pathCode.querySelector('script'), null);
  assert.equal(cells[3].textContent, '200');
  assert.equal(cells[4].textContent, '42 ms');
  assert.equal(cells[5].textContent, '#2');
});
