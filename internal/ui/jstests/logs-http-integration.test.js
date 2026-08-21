import { once } from 'node:events';
import { createServer } from 'node:http';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import axe from 'axe-core';

import { createLogsViewer } from '../static/views/logs-ui.js';

const waitFor = async (description, predicate, timeoutMs = 5000) => {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.fail(`timed out waiting for ${description}`);
};

// jsdom does not implement EventSource. This small browser-faithful adapter
// deliberately uses fetch so the viewer still crosses a real HTTP boundary.
// It preserves the last SSE id across reconnects, matching the native browser
// behavior on which the production endpoint's durable-cursor resume relies.
function fetchEventSourceClass(origin, reconnectDelayMs = 100) {
  return class FetchEventSource {
    constructor(path) {
      this.path = path;
      this.listeners = new Map();
      this.lastEventID = '';
      this.closed = false;
      this.controller = null;
      this.run();
    }

    addEventListener(type, listener) {
      const listeners = this.listeners.get(type) || [];
      listeners.push(listener);
      this.listeners.set(type, listeners);
    }

    close() {
      this.closed = true;
      this.controller?.abort();
    }

    dispatchFrame(frame) {
      let type = 'message';
      const data = [];
      for (const line of frame.split('\n')) {
        if (!line || line.startsWith(':')) continue;
        const separator = line.indexOf(':');
        const field = separator < 0 ? line : line.slice(0, separator);
        const value = separator < 0 ? '' : line.slice(separator + 1).replace(/^ /, '');
        if (field === 'event') type = value || 'message';
        else if (field === 'data') data.push(value);
        else if (field === 'id' && !value.includes('\0')) this.lastEventID = value;
      }
      if (data.length === 0) return;
      const event = { type, data: data.join('\n'), lastEventId: this.lastEventID };
      if (type === 'message') this.onmessage?.(event);
      for (const listener of this.listeners.get(type) || []) listener(event);
    }

    async consume(body) {
      const reader = body.getReader();
      const decoder = new TextDecoder();
      let buffered = '';
      while (!this.closed) {
        const { value, done } = await reader.read();
        buffered += decoder.decode(value || new Uint8Array(), { stream: !done }).replaceAll('\r\n', '\n');
        let boundary;
        while ((boundary = buffered.indexOf('\n\n')) >= 0) {
          this.dispatchFrame(buffered.slice(0, boundary));
          buffered = buffered.slice(boundary + 2);
        }
        if (done) return;
      }
    }

    async run() {
      while (!this.closed) {
        this.controller = new AbortController();
        try {
          const headers = { Accept: 'text/event-stream' };
          if (this.lastEventID) headers['Last-Event-ID'] = this.lastEventID;
          const response = await fetch(new URL(this.path, origin), {
            headers,
            signal: this.controller.signal,
          });
          if (!response.ok || !response.body) throw new Error(`SSE HTTP ${response.status}`);
          if (this.closed) return;
          this.onopen?.({ type: 'open' });
          await this.consume(response.body);
          if (this.closed) return;
          this.onerror?.({ type: 'error' });
        } catch (error) {
          if (this.closed) return;
          this.onerror?.({ type: 'error', error });
        }
        await new Promise((resolve) => setTimeout(resolve, reconnectDelayMs));
      }
    }
  };
}

test('app-detail logs cross HTTP for replicas, history, switching, and cursor-safe reconnects', async (t) => {
  const sources = [
    {
      source_id: 'live-0', run_id: 'live-0', replica: 0, status: 'running',
      current: true, has_log: true, tier: 'local',
    },
    {
      source_id: 'live-1', run_id: 'live-1', replica: 1, status: 'running',
      current: true, has_log: true, tier: 'worker',
    },
    {
      source_id: 'old-run', run_id: 'old-run', replica: 0, status: 'stopped',
      current: false, has_log: true, tier: 'local', started_at: '2026-08-14T07:00:00Z',
    },
  ];
  const requests = [];
  const openStreams = new Set();
  let replicaZeroConnections = 0;
  let replicaOneConnections = 0;
  // Replica zero's first stream is dropped by the test body, not by a timer, so
  // the reconnect transition always starts from a UI that has finished painting
  // its fully-connected state.
  let dropReplicaZero = () => assert.fail('replica zero never connected');

  const server = createServer((request, response) => {
    const url = new URL(request.url, 'http://shinyhub.test');
    requests.push({
      path: url.pathname + url.search,
      replica: url.searchParams.get('replica'),
      lastEventID: request.headers['last-event-id'] || '',
    });

    if (url.pathname === '/api/apps/demo/logs/sources') {
      response.writeHead(200, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify({ sources }));
      return;
    }
    if (url.pathname !== '/api/apps/demo/logs') {
      response.writeHead(404).end();
      return;
    }
    if (url.searchParams.get('follow') === 'false') {
      response.writeHead(200, { 'Content-Type': 'text/plain; charset=utf-8' });
      response.end(url.searchParams.get('run') === 'old-run'
        ? 'old retained start\nold retained final\n'
        : 'unexpected static source\n');
      return;
    }

    response.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
    });
    response.flushHeaders();
    openStreams.add(response);
    request.on('close', () => openStreams.delete(response));

    const replica = url.searchParams.get('replica');
    if (replica === '0') {
      replicaZeroConnections++;
      if (replicaZeroConnections === 1) {
        response.write('id: 7\ndata: zero before drop\n\n');
        dropReplicaZero = () => response.end();
      } else {
        response.write('id: 28\ndata: zero after reconnect\n\n');
      }
      return;
    }
    replicaOneConnections++;
    response.write(`id: ${replicaOneConnections * 10}\ndata: one live ${replicaOneConnections}\n\n`);
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const address = server.address();
  const origin = `http://127.0.0.1:${address.port}`;

  let dom;
  let cleanupViewer = () => {};
  t.after(async () => {
    cleanupViewer();
    for (const stream of openStreams) stream.end();
    server.closeAllConnections?.();
    await new Promise((resolve) => server.close(resolve));
    dom?.window.close();
  });

  dom = new JSDOM('<main><section id="panel" aria-label="Logs"></section></main>', {
    url: `${origin}/apps/demo/logs`,
    pretendToBeVisual: true,
    runScripts: 'dangerously',
  });
  const panel = dom.window.document.querySelector('#panel');
  cleanupViewer = createLogsViewer({
    panel,
    app: { slug: 'demo' },
    api: (path) => fetch(new URL(path, origin)),
    EventSourceClass: fetchEventSourceClass(origin),
    refreshEveryMs: 60_000,
  });

  const output = () => panel.querySelector('#detail-logs-body').textContent;
  const status = () => panel.querySelector('.logs-status-text').textContent;
  await waitFor('both live replica lines', () =>
    output().includes('zero before drop') && output().includes('one live 1'));
  assert.equal(panel.querySelector('#logs-source').firstElementChild.textContent,
    'All current replicas (2 live, 2 total)');
  await waitFor('fully connected status', () => status() === 'Live · 2 connected sources');

  dropReplicaZero();
  await waitFor('visible reconnecting state', () => status().startsWith('Reconnecting · 1 of 2'));
  await waitFor('cursor-resumed replica zero output', () => output().includes('zero after reconnect'));
  assert.equal(status(), 'Live · 2 connected sources');
  assert.equal((output().match(/zero before drop/g) || []).length, 1, 'acknowledged output must not replay');
  assert.equal((output().match(/zero after reconnect/g) || []).length, 1);
  const replicaZeroRequests = requests.filter((request) =>
    request.path.startsWith('/api/apps/demo/logs?') && request.replica === '0' && !request.path.includes('follow=false'));
  assert.equal(replicaZeroRequests.length, 2);
  assert.equal(replicaZeroRequests[0].lastEventID, '');
  assert.equal(replicaZeroRequests[1].lastEventID, '7');

  const sourceSelect = panel.querySelector('#logs-source');
  const liveRequestCount = requests.filter((request) => request.replica != null && !request.path.includes('follow=false')).length;
  sourceSelect.value = 'old-run';
  sourceSelect.dispatchEvent(new dom.window.Event('change'));
  await waitFor('ended run output', () => output().includes('old retained final'));
  assert.equal(status(), '1 retained source · no live instances');
  assert.equal(panel.querySelector('#logs-pause').disabled, true);
  assert.match(dom.window.location.search, /log_source=old-run/);
  assert.match(panel.querySelector('#logs-announcement').textContent, /Showing Replica #0.*Stopped/i);
  assert.match(requests.at(-1).path, /replica=0&run=old-run&tail=200&follow=false/);
  assert.equal(
    requests.filter((request) => request.replica != null && !request.path.includes('follow=false')).length,
    liveRequestCount,
    'selecting history must not open a live stream',
  );

  const requestIndex = requests.length;
  sourceSelect.value = 'live-1';
  sourceSelect.dispatchEvent(new dom.window.Event('change'));
  await waitFor('single selected live replica', () => output().includes('one live 2'));
  assert.equal(status(), 'Live · 1 connected source');
  assert.doesNotMatch(output(), /zero (before drop|after reconnect)/);
  assert.match(dom.window.location.search, /log_source=live-1/);
  assert.match(panel.querySelector('#logs-announcement').textContent, /Showing Replica #1.*Running/i);
  const switchedRequests = requests.slice(requestIndex).filter((request) =>
    request.replica != null && !request.path.includes('follow=false'));
  assert.deepEqual(switchedRequests.map((request) => request.replica), ['1']);

  const axeScript = dom.window.document.createElement('script');
  axeScript.textContent = axe.source;
  dom.window.document.head.appendChild(axeScript);
  const accessibility = await dom.window.axe.run(panel, {
    runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'] },
    rules: { 'color-contrast': { enabled: false } },
    resultTypes: ['violations'],
  });
  assert.equal(
    accessibility.violations.length,
    0,
    `runtime log viewer accessibility violations: ${accessibility.violations.map((violation) => violation.id).join(', ')}`,
  );
});
