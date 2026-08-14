import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  appendBoundedLogEntry,
  createLogsViewer,
  filterLogEntries,
  formatLogSourceLabel,
  isFollowableLogSource,
  isLiveLogSource,
  normalizeLogSources,
  retainedLogDownloadURL,
  serializeLogEntries,
  unseenLogSuffix,
} from '../static/views/logs-ui.js';

const tick = () => new Promise((resolve) => setTimeout(resolve, 20));

test('normalizeLogSources keeps distinct runs of one replica and sorts current first', () => {
  const got = normalizeLogSources([
    { source_id: 'old-3', index: 3, status: 'STOPPED', tier: 'worker', current: false, started_at: '2026-01-01T00:00:00Z' },
    { replica: 0, status: 'running', provider: 'native' },
    { source_id: 'new-3', replica: 3, status: 'lost', has_log: true, current: true },
    { replica: -1, status: 'running' },
  ]);
  assert.deepEqual(got.map((s) => s.source_id), ['replica-0', 'new-3', 'old-3']);
  assert.equal(got[0].status, 'running');
  assert.equal(got[1].status, 'lost');
  assert.equal(got[1].has_log, true);
  assert.equal(got[2].tier, 'worker');
});

test('source labels and liveness are explicit', () => {
  const source = { replica: 2, status: 'running', tier: 'burst' };
  assert.equal(formatLogSourceLabel(source), 'Replica #2 — Running · burst');
  assert.equal(isLiveLogSource(source), true);
  assert.equal(isFollowableLogSource(source), true);
  assert.equal(isFollowableLogSource({ ...source, stream_available: false }), false);
  assert.equal(isLiveLogSource({ status: 'stopped' }), false);
});

test('filtering and serialization retain replica identity', () => {
  const entries = [
    { kind: 'line', replica: 0, line: 'ready' },
    { kind: 'line', replica: 2, line: 'memory error' },
    { kind: 'event', replica: 2, line: 'Replica #2 stopped' },
  ];
  assert.deepEqual(filterLogEntries(entries, '#2'), entries.slice(1));
  assert.deepEqual(filterLogEntries(entries, 'MEMORY'), [entries[1]]);
  assert.equal(
    serializeLogEntries(entries),
    '#0  ready\n#2  memory error\n--- Replica #2 stopped ---',
  );
});

test('bounded buffer drops the oldest entries', () => {
  const entries = [];
  assert.equal(appendBoundedLogEntry(entries, { line: 'a' }, 2), 0);
  assert.equal(appendBoundedLogEntry(entries, { line: 'b' }, 2), 0);
  assert.equal(appendBoundedLogEntry(entries, { line: 'c' }, 2), 1);
  assert.deepEqual(entries.map((e) => e.line), ['b', 'c']);
});

test('retained downloads target one exact run', () => {
  assert.equal(
    retainedLogDownloadURL('demo app', { replica: 2, run_id: 'run-2', has_log: true }),
    '/api/apps/demo%20app/logs?replica=2&download=true&run=run-2',
  );
  assert.equal(
    retainedLogDownloadURL('demo', { replica: 4, legacy: true, has_log: true }),
    '/api/apps/demo/logs?replica=4&download=true&run=legacy',
  );
  assert.equal(retainedLogDownloadURL('demo', { replica: 0, has_log: false }), '');
});

test('terminal snapshots append only lines not already observed live', () => {
  assert.deepEqual(
    unseenLogSuffix(['boot', 'ready', 'ready'], ['ready', 'ready', 'stopping', 'stopped']),
    ['stopping', 'stopped'],
  );
  assert.deepEqual(unseenLogSuffix(['old'], ['new', 'final']), ['new', 'final']);
  assert.deepEqual(unseenLogSuffix(['same'], ['same']), []);
});

test('selected source downloads its complete retained run while all scope exports visible output', async (t) => {
  const old = {
    source_id: 'old-run', run_id: 'old-run', replica: 4, status: 'stopped',
    current: false, has_log: true,
  };
  const current = {
    source_id: 'current-run', run_id: 'current-run', replica: 0, status: 'running',
    current: true, has_log: true,
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs?log_source=old-run',
    pretendToBeVisual: true,
  });
  class FakeEventSource { close() {} }
  const requested = [];
  const api = async (url) => {
    requested.push(url);
    if (url.endsWith('/logs/sources')) return { ok: true, json: async () => ({ sources: [current, old] }) };
    if (url.includes('download=true')) return { ok: true, blob: async () => new dom.window.Blob(['complete retained log']) };
    return { ok: true, text: async () => 'visible historical line\n' };
  };
  const saved = [];
  dom.window.URL.createObjectURL = (blob) => { saved.push({ blob }); return 'blob:test'; };
  dom.window.URL.revokeObjectURL = () => {};
  dom.window.HTMLAnchorElement.prototype.click = function click() {
    saved[saved.length - 1].filename = this.download;
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: FakeEventSource, refreshEveryMs: 60_000,
  });
  t.after(() => { cleanup(); dom.window.close(); });
  await tick();

  const download = panel.querySelector('#logs-download');
  assert.equal(download.textContent, 'Download retained run');
  download.click();
  await tick();
  assert.match(requested.at(-1), /replica=4&download=true&run=old-run/);
  assert.equal(saved.at(-1).filename, 'demo-replica-4-old-run.log');
  assert.match(panel.querySelector('#logs-announcement').textContent, /complete retained run/i);

  const source = panel.querySelector('#logs-source');
  source.value = 'all';
  source.dispatchEvent(new dom.window.Event('change'));
  assert.equal(download.textContent, 'Download visible');
  const requestsBeforeVisibleExport = requested.length;
  download.click();
  await tick();
  assert.equal(requested.length, requestsBeforeVisibleExport, 'visible export should use the bounded browser buffer');
  assert.equal(saved.at(-1).filename, 'demo-logs-visible.log');
});

test('viewer merges live replicas, identifies every line, and loads ended logs once', async () => {
  const dom = new JSDOM('<main><section id="panel"></section></main>', {
    url: 'https://shinyhub.test/apps/demo/logs',
    pretendToBeVisual: true,
  });
  const streams = [];
  class FakeEventSource {
    constructor(url) { this.url = url; streams.push(this); }
    close() { this.closed = true; }
  }
  const sources = [
    { replica: 0, status: 'running', tier: 'local', has_log: true },
    { replica: 1, status: 'running', tier: 'worker', has_log: true },
    { replica: 4, status: 'stopped', tier: 'worker', has_log: true },
  ];
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) return { ok: true, json: async () => ({ sources }) };
    assert.match(url, /replica=4/);
    assert.match(url, /follow=false/);
    return { ok: true, text: async () => 'old one\nold two\n' };
  };

  const cleanup = createLogsViewer({
    panel: dom.window.document.querySelector('#panel'),
    app: { slug: 'demo' },
    api,
    EventSourceClass: FakeEventSource,
    refreshEveryMs: 60_000,
  });
  await tick();
  assert.equal(streams.length, 2, 'only running replicas should be followed');
  streams[0].onopen();
  streams[1].onopen();
  streams[0].onmessage({ data: 'zero live' });
  streams[1].onmessage({ data: 'one live' });
  await tick();

  const panel = dom.window.document.querySelector('#panel');
  assert.match(panel.querySelector('#logs-source').textContent, /All current replicas \(2 live, 3 total\)/);
  assert.deepEqual(
    [...panel.querySelectorAll('.log-entry-source')].map((el) => el.textContent),
    ['#4', '#4', '#0', '#1'],
  );
  assert.match(panel.querySelector('#detail-logs-body').textContent, /old one/);
  assert.match(panel.querySelector('#detail-logs-body').textContent, /zero live/);
  assert.equal(panel.querySelector('.logs-status-text').textContent, 'Live · 2 connected sources');
  cleanup();
  assert.ok(streams.every((stream) => stream.closed));
  dom.window.close();
});

test('pause buffers incoming lines and resume renders them', async (t) => {
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs',
    pretendToBeVisual: true,
  });
  let stream;
  class FakeEventSource {
    constructor() { stream = this; }
    close() {}
  }
  const source = { replica: 0, status: 'running', has_log: true };
  const api = async () => ({ ok: true, json: async () => ({ sources: [source] }) });
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: FakeEventSource, refreshEveryMs: 60_000,
  });
  t.after(() => { cleanup(); dom.window.close(); });
  await tick();
  const pause = panel.querySelector('#logs-pause');
  pause.click();
  stream.onmessage({ data: 'buffered while paused' });
  await tick();
  assert.doesNotMatch(panel.querySelector('#detail-logs-body').textContent, /buffered/);
  assert.equal(pause.textContent, 'Resume (1)');
  pause.click();
  await tick();
  assert.match(panel.querySelector('#detail-logs-body').textContent, /buffered while paused/);
  assert.equal(pause.getAttribute('aria-pressed'), 'false');
});

test('steady live output appends rows without rebuilding the whole log viewport', async (t) => {
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs',
    pretendToBeVisual: true,
  });
  let stream;
  class FakeEventSource {
    constructor() { stream = this; }
    close() {}
  }
  const source = { source_id: 'live-0', replica: 0, status: 'running', current: true, has_log: true };
  const api = async () => ({ ok: true, json: async () => ({ sources: [source] }) });
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: FakeEventSource,
    refreshEveryMs: 60_000, maxRenderedEntries: 8,
  });
  t.after(() => { cleanup(); dom.window.close(); });
  await tick();

  const output = panel.querySelector('#detail-logs-body');
  const replaceChildren = output.replaceChildren.bind(output);
  let fullRebuilds = 0;
  output.replaceChildren = (...children) => {
    fullRebuilds++;
    return replaceChildren(...children);
  };
  for (let i = 0; i < 20; i++) {
    stream.onmessage({ data: `line ${i}` });
    await tick();
  }

  assert.equal(output.querySelectorAll('.log-entry').length, 8);
  assert.match(output.firstElementChild.textContent, /line 12/);
  assert.match(panel.querySelector('#logs-output-summary').textContent, /12 older lines omitted/);
  assert.ok(fullRebuilds <= 1, `steady append caused ${fullRebuilds} full viewport rebuilds`);

  const search = panel.querySelector('#logs-search');
  search.value = 'line 1';
  search.dispatchEvent(new dom.window.Event('input'));
  await tick();
  fullRebuilds = 0;
  stream.onmessage({ data: 'line 20' });
  await tick();
  stream.onmessage({ data: 'line 100' });
  await tick();
  assert.equal(fullRebuilds, 0, 'steady filtered output should update incrementally');
  assert.match(output.lastElementChild.textContent, /line 100/);
});

test('pruning the selected run resets scope, reconnects current logs, and repairs the URL', async (t) => {
  const old = {
    source_id: 'old-run', run_id: 'old-run', replica: 0, status: 'stopped',
    current: false, has_log: true,
  };
  const current = {
    source_id: 'current-run', run_id: 'current-run', replica: 0, status: 'running',
    current: true, has_log: true,
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs?log_source=old-run',
    pretendToBeVisual: true,
  });
  const streams = [];
  class FakeEventSource {
    constructor(url) { this.url = url; streams.push(this); }
    close() { this.closed = true; }
  }
  let sourceRefreshes = 0;
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) {
      sourceRefreshes++;
      return { ok: true, json: async () => ({ sources: sourceRefreshes === 1 ? [current, old] : [current] }) };
    }
    return { ok: true, text: async () => 'historical line\n' };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: FakeEventSource, refreshEveryMs: 30,
  });
  t.after(() => { cleanup(); dom.window.close(); });
  await tick();
  assert.equal(panel.querySelector('#logs-source').value, 'old-run');
  assert.equal(streams.length, 0, 'historical selection must not open current streams');

  await new Promise((resolve) => setTimeout(resolve, 70));
  assert.equal(panel.querySelector('#logs-source').value, 'all');
  assert.equal(dom.window.location.search, '');
  assert.equal(streams.length, 1, 'falling back to all must open the current stream');
  assert.match(panel.querySelector('#detail-logs-body').textContent, /no longer retained/i);
  assert.match(panel.querySelector('#logs-announcement').textContent, /no longer retained/i);
});

test('a run that stops reconciles its final retained lines without duplicating streamed output', async (t) => {
  const running = {
    source_id: 'run-0', run_id: 'run-0', replica: 0, status: 'running',
    current: true, has_log: true, size_bytes: 10,
  };
  const stopped = { ...running, status: 'stopped', size_bytes: 30 };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs',
    pretendToBeVisual: true,
  });
  let stream;
  class FakeEventSource {
    constructor() { stream = this; }
    close() { this.closed = true; }
  }
  let discoveries = 0;
  let terminalFetches = 0;
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) {
      discoveries++;
      return { ok: true, json: async () => ({ sources: [discoveries === 1 ? running : stopped] }) };
    }
    terminalFetches++;
    return { ok: true, text: async () => 'boot\nready\nfinal crash detail\n' };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: FakeEventSource, refreshEveryMs: 40,
  });
  t.after(() => { cleanup(); dom.window.close(); });
  await tick();
  stream.onmessage({ data: 'boot' });
  stream.onmessage({ data: 'ready' });
  await tick();
  await new Promise((resolve) => setTimeout(resolve, 70));

  const output = panel.querySelector('#detail-logs-body').textContent;
  assert.equal((output.match(/boot/g) || []).length, 1);
  assert.equal((output.match(/ready/g) || []).length, 1);
  assert.equal((output.match(/final crash detail/g) || []).length, 1);
  assert.ok(output.indexOf('final crash detail') < output.indexOf('stopped'), 'terminal status follows final retained output');
  assert.equal(terminalFetches, 1);
  assert.equal(stream.closed, true);
});
