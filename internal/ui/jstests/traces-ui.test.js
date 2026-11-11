import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  formatTraceWhen,
  formatPollStatus,
  makeTraceRow,
  UNSAMPLED_TITLE,
  createTracesViewer,
  makeTraceDetails,
  traceFailed,
  traceKey,
  selectTraceEntries,
  safeTraceURL,
  formatTraceDuration,
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

const TRACE_ID = 'abcdef0123456789abcdef0123456789';
const LINK_TEMPLATE = 'https://backend.example/trace/{trace_id}';
const span = (overrides = {}) => ({
  trace_id: TRACE_ID, span_id: '1111111111111111', app_slug: 'demo',
  method: 'GET', path: '/slow', status: 200, duration_ms: 1200,
  replica: 0, started_at: '2026-05-22T09:30:00Z', sampled: true,
  ...overrides,
});
const response = (spans = [], extra = {}) => ({
  ok: true, status: 200,
  json: async () => ({ enabled: true, spans, trace_link_template: LINK_TEMPLATE, ...extra }),
});
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const settle = async () => { await new Promise(resolve => setImmediate(resolve)); };

function viewer(t, api, options = {}) {
  const dom = new JSDOM('<main id="panel"></main>', {
    url: options.url || 'https://shinyhub.example/apps/demo/traces', pretendToBeVisual: true,
  });
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createTracesViewer({ panel, app: { slug: 'demo' }, api, refreshEveryMs: 60_000, ...options });
  t.after(() => { cleanup(); dom.window.close(); });
  return {
    panel, cleanup, window: dom.window,
    find: selector => panel.querySelector(selector),
    paths: () => [...panel.querySelectorAll('.trace-inspect code')].map(el => el.textContent),
    automaticTick: () => dom.window.document.dispatchEvent(new dom.window.Event('visibilitychange')),
    change(selector, value) {
      const control = panel.querySelector(selector);
      control.value = value;
      control.dispatchEvent(new dom.window.Event(selector === '#traces-search' ? 'input' : 'change', { bubbles: true }));
    },
  };
}

function detailFields(section) {
  return Object.fromEntries([...section.querySelectorAll('dl > div')].map(item =>
    [item.querySelector('dt').textContent, item.querySelector('dd').textContent]));
}

test('request selection composes case-insensitive search and failure filtering without mutating API order', () => {
  const healthy = span({ path: '/DATA', status: 404, duration_ms: 7000 });
  const failed = span({ path: '/data', status: 200, error: 'socket reset', duration_ms: 20 });
  const server = span({ path: '/submit', status: 503, duration_ms: 20 });
  const records = [healthy, failed, server];
  assert.equal(traceFailed(healthy), false, 'slow 4xx is not a server/proxy failure');
  assert.equal(traceFailed(failed), true, 'proxy error counts even with status 200');
  assert.deepEqual(selectTraceEntries(records, { query: '  DaTa ', failuresOnly: true }), [failed]);
  assert.deepEqual(selectTraceEntries(records, { query: 'RESET' }), [failed]);
  assert.deepEqual(selectTraceEntries(records, { query: TRACE_ID }), records);
  assert.deepEqual(selectTraceEntries(records, { sort: 'slowest' }), [healthy, failed, server]);
  assert.deepEqual(selectTraceEntries(records), records, 'newest preserves capture order, including ties');
  assert.deepEqual(records, [healthy, failed, server]);
  assert.notEqual(traceKey(failed), traceKey({ ...failed, span_id: '2222222222222222' }), 'same trace can contain separate requests');
});

test('duration formatting distinguishes unavailable values and seconds', () => {
  assert.equal(formatTraceDuration(42), '42 ms');
  assert.equal(formatTraceDuration(1200), '1.2 s');
  assert.equal(formatTraceDuration(0), '0 ms');
  for (const value of [undefined, null, '', -1, NaN, Infinity]) assert.equal(formatTraceDuration(value), '—');
});

test('compact row has four native cells and a keyboard-accessible inspection action', () => {
  const { document } = new JSDOM('<body></body>').window;
  const record = span({ path: '<script>alert(1)</script>', status: 502, duration_ms: 42 });
  let inspected;
  const row = makeTraceRow(document, record, LINK_TEMPLATE, new Date('2026-05-22T10:00:00Z'), {
    selected: true, onInspect: value => { inspected = value; },
  });
  const cells = row.querySelectorAll('td');
  assert.equal(cells.length, 4);
  assert.equal(cells[0].querySelector('code').textContent, record.path);
  assert.equal(row.querySelector('script'), null);
  assert.match(cells[1].textContent, /502Server error/);
  assert.equal(cells[2].textContent, '42 ms');
  assert.match(row.className, /is-failed/);
  const button = row.querySelector('button');
  assert.equal(button.type, 'button');
  assert.equal(button.getAttribute('aria-expanded'), 'true');
  assert.equal(button.getAttribute('aria-controls'), 'traces-detail');
  button.click();
  assert.equal(inspected, record);
  assert.equal(row.querySelector('a'), null, 'backend action belongs in full request details');
});

test('details expose complete raw metadata safely, including sampling, replica and error', async () => {
  const { document } = new JSDOM('<body></body>', { url: 'https://shinyhub.example' }).window;
  const record = span({ parent_id: '3333333333333333', deployment_id: 27, replica: 2,
    path: '<img src=x onerror=alert(1)>', error: '<script>bad</script>', duration_ms: 10123 });
  let closed = false;
  let copied;
  let feedback;
  Object.defineProperty(document.defaultView.navigator, 'clipboard', { value: { writeText: async text => { copied = text; } } });
  const section = makeTraceDetails(document, record, LINK_TEMPLATE, {
    onClose: () => { closed = true; }, onFeedback: text => { feedback = text; },
  });
  const fields = detailFields(section);
  assert.equal(fields['Trace ID'], TRACE_ID);
  assert.equal(fields['Span ID'], record.span_id);
  assert.equal(fields['Parent span ID'], record.parent_id);
  assert.equal(fields['Deployment ID'], '27');
  assert.equal(fields.Replica, '#2');
  assert.equal(fields.Duration, '10123 ms', 'inspection preserves exact duration rather than rounded seconds');
  assert.match(fields.Started, /2026-05-22T09:30:00.000Z/);
  assert.equal(section.querySelector('.trace-detail-error').textContent, record.error);
  assert.equal(section.querySelector('.trace-detail-request').textContent, `GET ${record.path}`);
  assert.equal(section.querySelector('img, script'), null);
  assert.match(section.querySelector('.trace-sampling').textContent, /availability depends/);
  const link = section.querySelector('a');
  assert.equal(link.href, `https://backend.example/trace/${TRACE_ID}`);
  assert.equal(link.target, '_blank');
  assert.equal(link.rel, 'noopener noreferrer');
  section.querySelector('.trace-detail-actions button').click();
  await settle();
  assert.deepEqual(JSON.parse(copied), record);
  assert.equal(feedback, 'Request details copied.');
  section.querySelector('header button').click();
  assert.equal(closed, true);
});

test('unsampled and unavailable metadata remain inspectable without backend links', () => {
  const { document } = new JSDOM('<body></body>').window;
  const section = makeTraceDetails(document, span({ sampled: false, replica: -1, status: 0,
    started_at: '', span_id: '', duration_ms: null }), LINK_TEMPLATE);
  assert.equal(section.querySelector('a'), null);
  assert.match(section.querySelector('.trace-sampling').textContent, /not sampled/);
  assert.equal(section.querySelector('.trace-sampling').title, UNSAMPLED_TITLE);
  const fields = detailFields(section);
  assert.equal(fields['Trace ID'], TRACE_ID);
  assert.equal(fields['Span ID'], 'Unavailable');
  assert.equal(fields.Replica, 'Unavailable');
  assert.equal(fields.Started, 'Unavailable');
  assert.equal(fields.Result, 'No response');
  assert.equal(fields.Duration, 'Unavailable');
  assert.equal(fields['Parent span ID'], undefined);
});

test('backend links require explicit sampling, a trace ID and a safe web URL', () => {
  for (const template of [
    'javascript:alert("{trace_id}")', 'data:text/html,{trace_id}', 'file:///tmp/{trace_id}',
    'https://user:secret@backend.example/{trace_id}', 'https://backend.example/no-placeholder', 'http://[/{trace_id}',
  ]) assert.equal(safeTraceURL(span(), template, 'https://shinyhub.example/'), '', template);
  assert.equal(safeTraceURL(span({ sampled: false }), LINK_TEMPLATE), '');
  assert.equal(safeTraceURL(span({ sampled: undefined }), LINK_TEMPLATE), '');
  assert.equal(safeTraceURL(span({ trace_id: 'bad-id' }), LINK_TEMPLATE), '');
  assert.equal(safeTraceURL(span(), 'http://localhost/{trace_id}'), `http://localhost/${TRACE_ID}`);
  assert.equal(safeTraceURL(span(), '/trace/{trace_id}', 'https://backend.example/'), `https://backend.example/trace/${TRACE_ID}`);
});

test('viewer filters, sorts and clears a captured sample without fetching', async t => {
  let calls = 0;
  const v = viewer(t, async () => { calls++; return response([
    span({ path: '/data', duration_ms: 50, status: 500 }),
    span({ path: '/slow', duration_ms: 9000, span_id: '2222222222222222' }),
    span({ path: '/DATA', duration_ms: 300, error: 'transport reset', span_id: '3333333333333333' }),
  ]); });
  await settle();
  assert.deepEqual(v.paths(), ['/data', '/slow', '/DATA']);
  v.change('#traces-sort', 'slowest');
  assert.deepEqual(v.paths(), ['/slow', '/DATA', '/data']);
  v.change('#traces-filter', 'failed');
  v.change('#traces-search', 'data');
  assert.deepEqual(v.paths(), ['/DATA', '/data']);
  assert.equal(v.find('#traces-summary').textContent, '2 of 3 captured requests');
  v.change('#traces-search', 'nothing');
  assert.equal(v.find('#traces-table').hidden, true);
  assert.equal(v.find('#traces-empty h3').textContent, 'No matching requests');
  v.find('#traces-clear').click();
  assert.deepEqual(v.paths(), ['/slow', '/DATA', '/data']);
  assert.equal(v.window.document.activeElement, v.find('#traces-search'));
  assert.equal(calls, 1);
});

test('inspection pauses updates, stays keyed across filtering and sorting, and closing preserves pause', async t => {
  let calls = 0;
  const first = span({ path: '/first', duration_ms: 10 });
  const second = span({ path: '/second', span_id: '2222222222222222', duration_ms: 9000 });
  const v = viewer(t, async () => { calls++; return response([first, second]); });
  await settle();
  v.find('.trace-inspect').click();
  assert.equal(detailFields(v.find('#traces-detail'))['Span ID'], first.span_id);
  assert.equal(v.find('#traces-pause').getAttribute('aria-pressed'), 'true');
  assert.equal(v.find('.trace-detail-row td').colSpan, 4);
  assert.equal(v.window.document.activeElement, v.find('#traces-detail h3'));
  v.automaticTick();
  await settle();
  assert.equal(calls, 1);
  v.change('#traces-sort', 'slowest');
  assert.equal(v.find('.is-selected .trace-inspect').dataset.traceKey, traceKey(first));
  v.change('#traces-search', '/second');
  assert.ok(v.find('#traces-detail-slot #traces-detail'), 'hidden selected row keeps inspection available');
  v.change('#traces-search', '');
  assert.ok(v.find('.trace-detail-row #traces-detail'));
  v.find('#traces-detail header button').click();
  assert.equal(v.find('#traces-detail'), null);
  assert.equal(v.find('#traces-pause').getAttribute('aria-pressed'), 'true');
  assert.equal(v.window.document.activeElement.dataset.traceKey, traceKey(first));
  [...v.panel.querySelectorAll('.trace-inspect')][0].click();
  assert.equal(detailFields(v.find('#traces-detail'))['Span ID'], second.span_id, 'requests sharing a trace remain distinct');
});

test('automatic and manual requests are serialized until response JSON finishes', async t => {
  const body = deferred();
  let calls = 0;
  const v = viewer(t, async () => {
    calls++;
    return calls === 1 ? { ok: true, status: 200, json: () => body.promise } : response([span({ path: '/next' })]);
  });
  await settle();
  v.automaticTick();
  v.find('#traces-refresh').click();
  await settle();
  assert.equal(calls, 1);
  assert.equal(v.find('#traces-refresh').disabled, true);
  body.resolve({ enabled: true, spans: [span()] });
  await settle();
  assert.deepEqual(v.paths(), ['/slow']);
  assert.equal(v.find('#traces-refresh').disabled, false);
  v.automaticTick();
  await settle();
  assert.equal(calls, 2);
  assert.deepEqual(v.paths(), ['/next']);
});

test('pausing aborts and discards an in-flight poll; manual Refresh stays paused', async t => {
  const pending = deferred();
  const calls = [];
  const v = viewer(t, (url, options) => {
    calls.push({ url, options });
    return calls.length === 2 ? pending.promise : Promise.resolve(response([span({ path: calls.length === 1 ? '/original' : '/manual' })]));
  });
  await settle();
  v.automaticTick();
  assert.equal(calls.length, 2);
  v.find('#traces-pause').click();
  assert.equal(calls[1].options.signal.aborted, true);
  pending.resolve(response([span({ path: '/stale' })]));
  await settle();
  assert.deepEqual(v.paths(), ['/original']);
  v.automaticTick();
  assert.equal(calls.length, 2);
  v.find('#traces-refresh').click();
  await settle();
  assert.deepEqual(v.paths(), ['/manual']);
  assert.equal(v.find('#traces-pause').getAttribute('aria-pressed'), 'true');
  v.automaticTick();
  assert.equal(calls.length, 3);
});

test('manual refresh preserves evicted inspection while keeping its snapshot frozen', async t => {
  let calls = 0;
  const v = viewer(t, async () => response([span({ path: ++calls === 1 ? '/inspected' : '/replacement' })]));
  await settle();
  v.find('.trace-inspect').click();
  v.find('#traces-refresh').click();
  await settle();
  assert.deepEqual(v.paths(), ['/replacement']);
  assert.match(v.find('#traces-detail').textContent, /GET \/inspected/);
  assert.match(v.find('#traces-detail').textContent, /no longer in the recent sample/);
  assert.equal(v.find('#traces-pause').getAttribute('aria-pressed'), 'true');
});

test('teardown prevents pending JSON writes, later polling and late unauthorized callbacks', async t => {
  const body = deferred();
  let calls = 0;
  let unauthorized = 0;
  const v = viewer(t, async () => { calls++; return { ok: true, status: 200, json: () => body.promise }; }, {
    onUnauthorized: () => { unauthorized++; },
  });
  await settle();
  v.cleanup();
  const html = v.panel.innerHTML;
  body.resolve({ enabled: true, spans: [span()] });
  await settle();
  assert.equal(v.panel.innerHTML, html);
  v.automaticTick();
  v.find('#traces-refresh').click();
  assert.equal(calls, 1);
  assert.equal(unauthorized, 0);

  const pending = deferred();
  const late = viewer(t, () => pending.promise, { onUnauthorized: () => { unauthorized++; } });
  late.cleanup();
  pending.resolve({ ok: false, status: 401 });
  await settle();
  assert.equal(unauthorized, 0);
});

test('failed refresh preserves last sample and Retry recovers without hiding stale data', async t => {
  let calls = 0;
  const v = viewer(t, async () => {
    if (++calls === 2) throw new Error('offline');
    return response([span({ path: calls === 1 ? '/last-good' : '/recovered' })]);
  });
  await settle();
  v.find('#traces-refresh').click();
  await settle();
  assert.deepEqual(v.paths(), ['/last-good']);
  assert.equal(v.find('#traces-error').hidden, false);
  assert.match(v.find('#traces-error').textContent, /last successful sample is still shown/);
  assert.match(v.find('#traces-status').textContent, /Updates interrupted.*updated/);
  v.find('#traces-retry').click();
  await settle();
  assert.deepEqual(v.paths(), ['/recovered']);
  assert.equal(v.find('#traces-error').hidden, true);
});

test('initial HTTP and invalid JSON failures can retry into distinct empty and disabled states', async t => {
  const responses = [
    { ok: false, status: 503 },
    { ok: true, status: 200, json: async () => { throw new SyntaxError('invalid JSON'); } },
    response([]), response([], { enabled: false }),
  ];
  const v = viewer(t, async () => responses.shift());
  await settle();
  assert.equal(v.find('#traces-empty h3').textContent, 'Traces could not be loaded');
  assert.equal(v.find('#traces-error').hidden, false);
  v.find('#traces-retry').click();
  await settle();
  assert.equal(v.find('#traces-error').hidden, false);
  v.find('#traces-retry').click();
  await settle();
  assert.equal(v.find('#traces-error').hidden, true);
  assert.equal(v.find('#traces-empty h3').textContent, 'No requests captured yet');
  assert.match(v.find('#traces-empty p').textContent, /proxy error/);
  v.find('#traces-pause').click();
  v.find('#traces-refresh').click();
  await settle();
  assert.equal(v.find('#traces-empty h3').textContent, 'Tracing is disabled');
  assert.equal(v.find('#traces-search').disabled, true);
  assert.equal(v.find('#traces-pause').disabled, true);
  assert.match(v.find('#traces-status').textContent, /^Tracing disabled/, 'disabled tracing takes precedence over paused status');
});

test('development-session scope reaches the API and has a clear all-traces escape', async t => {
  const id = 'abcdef0123456789abcdef0123456789';
  const calls = [];
  const v = viewer(t, async (url, options) => { calls.push({ url, options }); return response(); }, {
    url: `https://shinyhub.example/apps/demo/traces?development_session_id=${id}`,
  });
  await settle();
  assert.equal(calls[0].url, `/api/apps/demo/traces?development_session_id=${id}`);
  assert.equal(calls[0].options.cache, 'no-store');
  assert.ok(calls[0].options.signal);
  assert.equal(v.find('#traces-session').hidden, false);
  assert.equal(v.find('#traces-session a').getAttribute('href'), '/apps/demo/traces');
  const invalidCalls = [];
  const invalid = viewer(t, async url => { invalidCalls.push(url); return response(); }, {
    url: 'https://shinyhub.example/apps/demo/traces?development_session_id=invalid',
  });
  await settle();
  assert.equal(invalidCalls[0], '/api/apps/demo/traces');
  assert.equal(invalid.find('#traces-session').hidden, true);
});

test('the one-second status repaint is disarmed while the tab is hidden and repaints on return', async (t) => {
  const dom = new JSDOM('<main id="panel"></main>', {
    url: 'https://shinyhub.example/apps/demo/traces', pretendToBeVisual: true,
  });
  const { window } = dom;
  let hidden = false;
  Object.defineProperty(window.document, 'hidden', { get: () => hidden });
  // Count live 1 s intervals on the injected window, the timer the status
  // repaint must use.
  const live = new Set();
  const realSet = window.setInterval.bind(window);
  const realClear = window.clearInterval.bind(window);
  window.setInterval = (fn, ms, ...rest) => {
    const id = realSet(fn, ms, ...rest);
    if (ms === 1000) live.add(id);
    return id;
  };
  window.clearInterval = (id) => { live.delete(id); realClear(id); };
  const panel = window.document.querySelector('#panel');
  const cleanup = createTracesViewer({
    panel, app: { slug: 'demo' }, api: async () => response([span()]), refreshEveryMs: 60_000,
  });
  t.after(() => { cleanup(); window.close(); });
  await settle();
  assert.equal(live.size, 1, 'visible tab runs the status repaint');

  hidden = true;
  window.document.dispatchEvent(new window.Event('visibilitychange'));
  assert.equal(live.size, 0, 'hidden tab runs no one-second timer');

  hidden = false;
  window.document.dispatchEvent(new window.Event('visibilitychange'));
  assert.equal(live.size, 1, 'returning to the tab re-arms the status repaint');

  cleanup();
  assert.equal(live.size, 0, 'unmount clears the status repaint');
});
