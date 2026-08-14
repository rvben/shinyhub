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
  serializeLogEntries,
} from '../static/views/logs-ui.js';

const tick = () => new Promise((resolve) => setTimeout(resolve, 20));

test('normalizeLogSources accepts API and replica-status shapes, deduplicates, and sorts', () => {
  const got = normalizeLogSources([
    { index: 3, status: 'STOPPED', tier: 'worker' },
    { replica: 0, status: 'running', provider: 'native' },
    { replica: 3, status: 'lost', has_log: true },
    { replica: -1, status: 'running' },
  ]);
  assert.deepEqual(got.map((s) => s.replica), [0, 3]);
  assert.equal(got[0].status, 'running');
  assert.equal(got[1].status, 'lost');
  assert.equal(got[1].tier, 'worker');
  assert.equal(got[1].has_log, true);
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
  assert.match(panel.querySelector('#logs-source').textContent, /All replicas \(2 live, 3 retained\)/);
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

test('pause buffers incoming lines and resume renders them', async () => {
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
  cleanup();
  dom.window.close();
});
