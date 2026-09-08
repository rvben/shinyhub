import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  appendBoundedLogEntry,
  createLogsViewer,
  DEFAULT_LOG_TAIL,
  externalLogsCommand,
  filterLogEntries,
  formatLogSourceLabel,
  isFollowableLogSource,
  isExternalLogSource,
  isLiveLogSource,
  logsAreTruncated,
  logsEmptyMessage,
  MAX_LOG_TAIL,
  nextLoadMoreTail,
  normalizeLogSources,
  retainedLogDownloadURL,
  safeExternalLogsURL,
  serializeLogEntries,
  unseenLogSuffix,
} from '../static/views/logs-ui.js';

const tick = () => new Promise((resolve) => setTimeout(resolve, 20));
const waitUntil = async (predicate, timeoutMs = 10_000) => {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('timed out waiting for UI state');
    await tick();
  }
};

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
  assert.equal(formatLogSourceLabel(source), 'Replica #2: Running · burst');
  assert.equal(isLiveLogSource(source), true);
  assert.equal(isFollowableLogSource(source), true);
  assert.equal(isFollowableLogSource({ ...source, stream_available: false }), false);
  assert.equal(isExternalLogSource({ ...source, stream_available: false }), true);
  assert.equal(isLiveLogSource({ status: 'stopped' }), false);
});

test('external AWS handoffs validate links and quote copyable commands', () => {
  const source = {
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: "team's apps",
      resource: "arn:aws:ecs:eu-west-1:123:task/team/task'42",
      console_url: 'https://console.aws.amazon.com/ecs/v2/clusters/team/tasks/task-42/logs?region=eu-west-1',
    },
  };
  assert.equal(
    externalLogsCommand(source),
    `aws ecs describe-tasks --cluster 'team'"'"'s apps' --tasks 'arn:aws:ecs:eu-west-1:123:task/team/task'"'"'42' --region 'eu-west-1'`,
  );
  assert.equal(safeExternalLogsURL(source), source.external_logs.console_url);
  assert.equal(safeExternalLogsURL({
    external_logs: { console_url: 'https://console.aws.amazon.com.evil.test/steal' },
  }), '');
  assert.equal(safeExternalLogsURL({
    external_logs: { console_url: 'https://console.aws.amazon.com:444/ecs' },
  }), '');
  assert.equal(safeExternalLogsURL({
    external_logs: {
      resource: 'arn:aws:ecs:eu-west-1:123:task/team/task-42', region: 'eu-west-1',
      log_url: 'https://eu-west-1.console.amazonaws.cn/cloudwatch/home',
    },
  }), '', 'a commercial ARN cannot hand off to another AWS partition');
  assert.equal(externalLogsCommand({
    external_logs: {
      provider: 'aws_ecs', resource: source.external_logs.resource, region: 'eu-west-1',
      log_group: '/shinyhub/apps', log_stream: 'app/app/task-42',
    },
  }), "aws logs get-log-events --log-group-name '/shinyhub/apps' --log-stream-name 'app/app/task-42' --region 'eu-west-1'");
  assert.equal(externalLogsCommand({ external_logs: { provider: 'aws_ecs' } }), '');
});

test('external live and stopped runs provide an actionable permission-safe AWS handoff', async (t) => {
  const details = (task) => ({
    provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
    resource: `arn:aws:ecs:eu-west-1:123:task/analytics/${task}`,
    console_url: `https://console.aws.amazon.com/ecs/v2/clusters/analytics/tasks/${task}/logs?region=eu-west-1`,
  });
  const current = {
    source_id: 'task-live', run_id: 'task-live', replica: 2, current: true,
    status: 'running', provider: 'fargate', tier: 'burst', stream_available: false,
    external_logs: details('task-live'),
  };
  const stopped = {
    source_id: 'task-old', run_id: 'task-old', replica: 2, current: false,
    status: 'stopped', provider: 'fargate', tier: 'burst', stream_available: false,
    started_at: '2026-08-14T08:00:00Z', external_logs: details('task-old'),
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  const copied = [];
  Object.defineProperty(dom.window.navigator, 'clipboard', {
    configurable: true, value: { writeText: async (value) => copied.push(value) },
  });
  class UnexpectedEventSource {
    constructor() { throw new Error('external sources must not open EventSource'); }
  }
  const panel = dom.window.document.querySelector('#panel');
  const api = async () => ({ ok: true, json: async () => ({ sources: [current, stopped] }) });
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: UnexpectedEventSource, refreshEveryMs: 60_000,
  });
  t.after(() => { cleanup(); dom.window.close(); });
  await waitUntil(() => panel.querySelectorAll('.logs-external-row').length === 1 &&
    /retained by its provider/i.test(panel.querySelector('#detail-logs-body').textContent));

  assert.equal(panel.querySelector('.logs-status-text').textContent, '1 live source · application logs external');
  assert.match(panel.querySelector('#detail-logs-body').textContent, /retained by its provider/i);
  assert.equal(panel.querySelectorAll('.logs-external-row').length, 1);
  assert.equal(panel.querySelector('.logs-external-actions a').textContent, 'Open task logs');
  assert.equal(panel.querySelector('.logs-external-actions a').rel, 'noopener noreferrer');

  const select = panel.querySelector('#logs-source');
  select.value = 'task-old';
  select.dispatchEvent(new dom.window.Event('change'));
  await tick();
  assert.equal(panel.querySelector('.logs-status-text').textContent, 'Stopped · logs retained in AWS');
  assert.equal(panel.querySelector('#logs-download').disabled, true);
  assert.equal(panel.querySelector('#logs-download').textContent, 'External logs');
  assert.match(panel.querySelector('.logs-external-identity').textContent, /Task task-old.*eu-west-1.*analytics/);
  panel.querySelector('.logs-external-actions button').click();
  // The clipboard write resolves as a microtask, but the confirmation text is
  // set via announce() (logs-ui.js), which defers to its own setTimeout(0) -
  // an extra macrotask hop a single fixed-delay tick does not reliably cover
  // under a loaded test runner. Poll for the announcement's final text.
  await waitUntil(() => /command copied.*replica 2/i.test(panel.querySelector('#logs-announcement').textContent));
  assert.equal(copied.at(-1), "aws ecs describe-tasks --cluster 'analytics' --tasks 'arn:aws:ecs:eu-west-1:123:task/analytics/task-old' --region 'eu-west-1'");
  assert.match(panel.querySelector('#logs-announcement').textContent, /command copied.*replica 2/i);
});

test('CloudWatch-backed Fargate runs render inline and resume with an opaque cursor', async (t) => {
  const source = {
    source_id: 'run-live', run_id: 'run-live', replica: 2, current: true,
    status: 'running', provider: 'fargate', tier: 'burst', stream_available: false,
    inline_available: true,
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
      resource: 'arn:aws:ecs:eu-west-1:123:task/analytics/task-live',
      log_group: '/shinyhub/apps', log_stream: 'app/app/task-live',
      console_url: 'https://console.aws.amazon.com/ecs/v2/clusters/analytics/tasks/task-live/logs?region=eu-west-1',
    },
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  class UnexpectedEventSource {
    constructor() { throw new Error('CloudWatch polling must not open EventSource'); }
  }
  const providerURLs = [];
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) return { ok: true, json: async () => ({ sources: [source] }) };
    providerURLs.push(url);
    const cursor = new URL(url, dom.window.location.href).searchParams.get('cursor');
    return {
      ok: true,
      json: async () => cursor
        ? { events: [{ message: 'ready', timestamp: '2026-08-15T14:00:01Z' }], next_cursor: 'cursor-2' }
        : { events: [{ message: 'booting', timestamp: '2026-08-15T14:00:00Z' }], next_cursor: 'cursor-1' },
    };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: UnexpectedEventSource, refreshEveryMs: 30,
  });
  t.after(() => { cleanup(); dom.window.close(); });

  await waitUntil(() => /ready/.test(panel.querySelector('#detail-logs-body').textContent));
  assert.match(providerURLs[0], /provider=true/);
  assert.match(providerURLs[0], /run=run-live/);
  assert.match(providerURLs[1], /cursor=cursor-1/);
  assert.deepEqual(
    [...panel.querySelectorAll('.log-entry-message')].slice(0, 2).map((el) => el.textContent),
    ['booting', 'ready'],
  );
  assert.deepEqual(
    [...panel.querySelectorAll('.log-entry-source')].slice(0, 2).map((el) => el.textContent),
    ['#2', '#2'],
  );
  assert.equal(panel.querySelector('.logs-status-text').textContent, 'Live · 1 CloudWatch source connected');
  assert.equal(panel.querySelectorAll('.logs-external-row').length, 1, 'AWS fallback remains visible');
});

test('a Fargate run stopping appends its final CloudWatch page before the terminal event', async (t) => {
  const running = {
    source_id: 'run-0', run_id: 'run-0', replica: 0, current: true,
    status: 'running', provider: 'fargate', stream_available: false, inline_available: true,
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
      resource: 'arn:aws:ecs:eu-west-1:123:task/analytics/task-0',
      log_group: '/shinyhub/apps', log_stream: 'app/app/task-0',
    },
  };
  const stopped = { ...running, status: 'stopped' };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  let discoveries = 0;
  let pages = 0;
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) {
      discoveries++;
      return { ok: true, json: async () => ({ sources: [discoveries === 1 ? running : stopped] }) };
    }
    pages++;
    return {
      ok: true,
      json: async () => pages === 1
        ? { events: [{ message: 'boot', timestamp: '2026-08-15T14:00:00Z' }], next_cursor: 'cursor-1' }
        : { events: [{ message: 'final line', timestamp: '2026-08-15T14:00:01Z' }], next_cursor: 'cursor-2' },
    };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: class { constructor() { throw new Error('unexpected SSE'); } },
    refreshEveryMs: 35,
  });
  t.after(() => { cleanup(); dom.window.close(); });

  await waitUntil(() => /stopped/i.test(panel.querySelector('#detail-logs-body').textContent));
  const output = panel.querySelector('#detail-logs-body').textContent;
  assert.equal((output.match(/boot/g) || []).length, 1);
  assert.equal((output.match(/final line/g) || []).length, 1);
  assert.doesNotMatch(output, /could not be reconciled/i);
  assert.ok(output.indexOf('final line') < output.indexOf('stopped'), 'terminal event follows final provider output');
});

test('a terminal CloudWatch read waits for an in-flight page before fetching the final page', async (t) => {
  const running = {
    source_id: 'run-race', run_id: 'run-race', replica: 1, current: true,
    status: 'running', provider: 'fargate', stream_available: false, inline_available: true,
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
      resource: 'arn:aws:ecs:eu-west-1:123:task/analytics/task-race',
      log_group: '/shinyhub/apps', log_stream: 'app/app/task-race',
    },
  };
  const stopped = { ...running, status: 'stopped' };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  let discoveries = 0;
  let providerCalls = 0;
  let releaseFirst;
  const firstPage = new Promise((resolve) => { releaseFirst = resolve; });
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) {
      discoveries++;
      return { ok: true, json: async () => ({ sources: [discoveries === 1 ? running : stopped] }) };
    }
    providerCalls++;
    if (providerCalls === 1) {
      await firstPage;
      return {
        ok: true,
        json: async () => ({
          events: [{ message: 'in-flight line', timestamp: '2026-08-15T14:00:00Z' }], next_cursor: 'race-1',
        }),
      };
    }
    return {
      ok: true,
      json: async () => ({
        events: [{ message: 'final raced line', timestamp: '2026-08-15T14:00:01Z' }], next_cursor: 'race-2',
      }),
    };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({ panel, app: { slug: 'demo' }, api, refreshEveryMs: 25 });
  t.after(() => { cleanup(); dom.window.close(); });

  await waitUntil(() => discoveries >= 2 && providerCalls === 1);
  releaseFirst();
  await waitUntil(() => /final raced line/.test(panel.querySelector('#detail-logs-body').textContent));
  const output = panel.querySelector('#detail-logs-body').textContent;
  assert.equal(providerCalls, 2);
  assert.ok(output.indexOf('in-flight line') < output.indexOf('final raced line'));
  assert.ok(output.indexOf('final raced line') < output.indexOf('stopped'));
});

test('CloudWatch permission failures degrade to the direct AWS handoff', async (t) => {
  const source = {
    source_id: 'run-2', run_id: 'run-2', replica: 2, current: true,
    status: 'running', provider: 'fargate', stream_available: false, inline_available: true,
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
      resource: 'arn:aws:ecs:eu-west-1:123:task/analytics/task-2',
      log_group: '/shinyhub/apps', log_stream: 'app/app/task-2',
      console_url: 'https://console.aws.amazon.com/ecs/v2/clusters/analytics/tasks/task-2/logs?region=eu-west-1',
    },
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  const api = async (url) => url.endsWith('/logs/sources')
    ? { ok: true, json: async () => ({ sources: [source] }) }
    : { ok: false, status: 502 };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({ panel, app: { slug: 'demo' }, api, refreshEveryMs: 60_000 });
  t.after(() => { cleanup(); dom.window.close(); });

  await waitUntil(() => /CloudWatch output is unavailable/i.test(panel.querySelector('#detail-logs-body').textContent));
  assert.match(panel.querySelector('.logs-status-text').textContent, /Delayed/i);
  // announce() defers its own textContent write via a separate setTimeout(0),
  // independent of the detail-body render awaited above, so it needs its own
  // poll rather than an assumption that both settle together.
  await waitUntil(() => /Direct AWS access remains available/i.test(panel.querySelector('#logs-announcement').textContent));
  assert.match(panel.querySelector('#logs-announcement').textContent, /Direct AWS access remains available/i);
  assert.equal(panel.querySelector('.logs-external-actions a').textContent, 'Open task logs');
});

test('CloudWatch throttling honors Retry-After without alarming the operator', async (t) => {
  const source = {
    source_id: 'run-throttled', run_id: 'run-throttled', replica: 3, current: true,
    status: 'running', provider: 'fargate', stream_available: false, inline_available: true,
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
      resource: 'arn:aws:ecs:eu-west-1:123:task/analytics/task-throttled',
      log_group: '/shinyhub/apps', log_stream: 'app/app/task-throttled',
      console_url: 'https://console.aws.amazon.com/ecs/v2/clusters/analytics/tasks/task-throttled/logs?region=eu-west-1',
    },
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  let providerCalls = 0;
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) return { ok: true, json: async () => ({ sources: [source] }) };
    providerCalls++;
    return { ok: false, status: 429, headers: { get: () => '1' } };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({ panel, app: { slug: 'demo' }, api, refreshEveryMs: 30 });
  t.after(() => { cleanup(); dom.window.close(); });

  await waitUntil(() => /Delayed/i.test(panel.querySelector('.logs-status-text').textContent));
  await new Promise((resolve) => setTimeout(resolve, 180));
  assert.equal(providerCalls, 1, 'Retry-After must override the ordinary failure backoff');
  assert.doesNotMatch(panel.querySelector('#detail-logs-body').textContent, /CloudWatch output is unavailable/i);
  assert.equal(panel.querySelector('.logs-external-actions a').textContent, 'Open task logs');
});

test('a stopped CloudWatch run is fetched once and then remains quiescent', async (t) => {
  const source = {
    source_id: 'run-stopped', run_id: 'run-stopped', replica: 4, current: false,
    status: 'stopped', provider: 'fargate', stream_available: false, inline_available: true,
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
      resource: 'arn:aws:ecs:eu-west-1:123:task/analytics/task-stopped',
      log_group: '/shinyhub/apps', log_stream: 'app/app/task-stopped',
    },
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs?log_source=run-stopped', pretendToBeVisual: true,
  });
  let providerCalls = 0;
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) return { ok: true, json: async () => ({ sources: [source] }) };
    providerCalls++;
    return {
      ok: true,
      json: async () => ({
        events: [{ message: 'final retained line', timestamp: '2026-08-15T14:00:00Z' }],
        next_cursor: 'stopped-forward-token',
      }),
    };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({ panel, app: { slug: 'demo' }, api, refreshEveryMs: 20 });
  t.after(() => { cleanup(); dom.window.close(); });

  await waitUntil(() => /final retained line/.test(panel.querySelector('#detail-logs-body').textContent));
  await new Promise((resolve) => setTimeout(resolve, 140));
  assert.equal(providerCalls, 1, 'completed runs must not keep spending CloudWatch reads');
});

test('a caught-up live CloudWatch source backs off while source discovery stays fresh', async (t) => {
  const source = {
    source_id: 'run-live-idle', run_id: 'run-live-idle', replica: 5, current: true,
    status: 'running', provider: 'fargate', stream_available: false, inline_available: true,
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
      resource: 'arn:aws:ecs:eu-west-1:123:task/analytics/task-idle',
      log_group: '/shinyhub/apps', log_stream: 'app/app/task-idle',
    },
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  let discoveries = 0;
  let providerCalls = 0;
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) {
      discoveries++;
      return { ok: true, json: async () => ({ sources: [source] }) };
    }
    providerCalls++;
    return {
      ok: true,
      json: async () => providerCalls === 1
        ? { events: [{ message: 'latest line', timestamp: '2026-08-15T14:00:00Z' }], next_cursor: 'idle-token' }
        : { events: [], next_cursor: 'idle-token' },
    };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({ panel, app: { slug: 'demo' }, api, refreshEveryMs: 80 });
  t.after(() => { cleanup(); dom.window.close(); });

  await waitUntil(() => /latest line/.test(panel.querySelector('#detail-logs-body').textContent));
  await new Promise((resolve) => setTimeout(resolve, 850));
  assert.ok(discoveries >= 7, `source discovery became stale (${discoveries} requests)`);
  assert.ok(providerCalls >= 3 && providerCalls <= 4, `caught-up source made ${providerCalls} provider reads`);
});

test('hidden tabs abort CloudWatch reads and resume immediately when visible', async (t) => {
  const source = {
    source_id: 'run-visible', run_id: 'run-visible', replica: 6, current: true,
    status: 'running', provider: 'fargate', stream_available: false, inline_available: true,
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
      resource: 'arn:aws:ecs:eu-west-1:123:task/analytics/task-visible',
      log_group: '/shinyhub/apps', log_stream: 'app/app/task-visible',
    },
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  let hidden = false;
  Object.defineProperty(dom.window.document, 'hidden', { configurable: true, get: () => hidden });
  let providerCalls = 0;
  let aborted = 0;
  const api = async (url, options = {}) => {
    if (url.endsWith('/logs/sources')) return { ok: true, json: async () => ({ sources: [source] }) };
    providerCalls++;
    if (providerCalls === 1) {
      return new Promise((resolve, reject) => {
        options.signal?.addEventListener('abort', () => {
          aborted++;
          reject(new dom.window.DOMException('Aborted', 'AbortError'));
        }, { once: true });
      });
    }
    return {
      ok: true,
      json: async () => ({
        events: [{ message: 'visible again', timestamp: '2026-08-15T14:00:00Z' }],
        next_cursor: 'visible-token',
      }),
    };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({ panel, app: { slug: 'demo' }, api, refreshEveryMs: 30 });
  t.after(() => { cleanup(); dom.window.close(); });

  await waitUntil(() => providerCalls === 1);
  hidden = true;
  dom.window.document.dispatchEvent(new dom.window.Event('visibilitychange'));
  await waitUntil(() => aborted === 1);
  await new Promise((resolve) => setTimeout(resolve, 120));
  assert.equal(providerCalls, 1, 'hidden tabs must remain quiescent');

  hidden = false;
  dom.window.document.dispatchEvent(new dom.window.Event('visibilitychange'));
  await waitUntil(() => /visible again/.test(panel.querySelector('#detail-logs-body').textContent));
  assert.ok(providerCalls >= 2 && providerCalls <= 3, `visibility recovery made ${providerCalls} reads`);
  assert.doesNotMatch(panel.querySelector('#detail-logs-body').textContent, /unavailable in ShinyHub/i,
    'intentional suspension must not look like a provider failure');
});

test('one viewer bounds concurrent CloudWatch reads across many replicas', async (t) => {
  const sources = Array.from({ length: 7 }, (_, replica) => ({
    source_id: `run-${replica}`, run_id: `run-${replica}`, replica, current: true,
    status: 'running', provider: 'fargate', stream_available: false, inline_available: true,
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
      resource: `arn:aws:ecs:eu-west-1:123:task/analytics/task-${replica}`,
      log_group: '/shinyhub/apps', log_stream: `app/app/task-${replica}`,
    },
  }));
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  let active = 0;
  let maxActive = 0;
  let completed = 0;
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) return { ok: true, json: async () => ({ sources }) };
    active++;
    maxActive = Math.max(maxActive, active);
    await new Promise((resolve) => setTimeout(resolve, 35));
    active--;
    completed++;
    return { ok: true, json: async () => ({ events: [], next_cursor: `token-${completed}` }) };
  };
  const cleanup = createLogsViewer({
    panel: dom.window.document.querySelector('#panel'), app: { slug: 'demo' }, api, refreshEveryMs: 60_000,
  });
  t.after(() => { cleanup(); dom.window.close(); });

  await waitUntil(() => completed === sources.length);
  assert.ok(maxActive <= 3, `browser issued ${maxActive} concurrent provider reads`);
});

test('external log command remains available when clipboard access fails', async (t) => {
  const source = {
    source_id: 'task-old', run_id: 'task-old', replica: 2, current: false,
    status: 'stopped', provider: 'fargate', tier: 'burst', stream_available: false,
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics',
      resource: 'arn:aws:ecs:eu-west-1:123:task/analytics/task-old',
    },
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  Object.defineProperty(dom.window.navigator, 'clipboard', {
    configurable: true, value: { writeText: async () => { throw new Error('denied'); } },
  });
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' },
    api: async () => ({ ok: true, json: async () => ({ sources: [source] }) }),
    refreshEveryMs: 60_000,
  });
  t.after(() => { cleanup(); dom.window.close(); });
  await waitUntil(() => panel.querySelector('.logs-external-actions button'));

  assert.equal(panel.querySelector('#logs-source').value, 'task-old');
  assert.equal(new URL(dom.window.location.href).searchParams.get('log_source'), 'task-old');
  panel.querySelector('.logs-external-actions button').click();
  // The rejected clipboard write is itself a promise, and the fallback text
  // it triggers is announced via announce() (logs-ui.js), which defers the
  // actual textContent write to its own setTimeout(0) on top of that. Two
  // stacked macrotask hops is not reliably covered by one fixed-delay tick
  // under a loaded test runner, so poll for the announcement's final text.
  await waitUntil(() => /shown for manual copying/i.test(panel.querySelector('#logs-announcement').textContent));
  const fallback = panel.querySelector('.logs-external-command');
  assert.equal(fallback.textContent, externalLogsCommand(source));
  assert.equal(fallback.tabIndex, 0);
  assert.match(panel.querySelector('#logs-announcement').textContent, /shown for manual copying/i);
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

// F6: the Logs tab silently capped static/history sources at 200 lines with
// no way to see further back, and Search over that truncated buffer returned
// a false "no match" for content that was simply never loaded. These pure
// helpers back the "Load older lines" affordance and its truncation caveats.
test('nextLoadMoreTail grows the requested window five-fold, capped at the server maximum', () => {
  assert.equal(nextLoadMoreTail(DEFAULT_LOG_TAIL), 1000);
  assert.equal(nextLoadMoreTail(1000), 5000);
  assert.equal(nextLoadMoreTail(5000), MAX_LOG_TAIL);
  assert.equal(nextLoadMoreTail(MAX_LOG_TAIL), MAX_LOG_TAIL);
  assert.equal(nextLoadMoreTail(1), 1000, 'the current window is floored at the default before multiplying, so the first grown request is never smaller than 5x the default');
});

test('logsAreTruncated flags a session that dropped rendered entries or never loaded a source in full', () => {
  const source = { source_id: 'a', external_logs: undefined };
  assert.equal(logsAreTruncated([source], { trimmed: 3, fullyLoadedSourceIDs: new Set(['a']) }), true,
    'client-side buffer eviction alone is enough to call the session truncated');
  assert.equal(logsAreTruncated([source], { trimmed: 0, fullyLoadedSourceIDs: new Set() }), true,
    'a source never proven complete is truncated even with nothing trimmed');
  assert.equal(logsAreTruncated([source], { trimmed: 0, fullyLoadedSourceIDs: new Set(['a']) }), false);
  const external = { source_id: 'b', stream_available: false, external_logs: { provider: 'aws_ecs' } };
  assert.equal(logsAreTruncated([external], { trimmed: 0, fullyLoadedSourceIDs: new Set() }), false,
    'an externally retained source has no ShinyHub-side window to be incomplete about');
  assert.equal(logsAreTruncated([], { trimmed: 0, fullyLoadedSourceIDs: new Set() }), false);
});

test('logsEmptyMessage names the reason no lines are visible and offers Load older lines when it would help', () => {
  assert.equal(
    logsEmptyMessage({ hasSearch: false, truncated: false, canLoadMore: false, hasScopedSources: true, allExternal: false, hasAnySources: true }),
    'Waiting for application output…',
  );
  assert.equal(
    logsEmptyMessage({ hasSearch: false, truncated: false, canLoadMore: false, hasScopedSources: false, allExternal: false, hasAnySources: false }),
    'No retained log sources were found.',
  );
  assert.equal(
    logsEmptyMessage({ hasSearch: false, truncated: false, canLoadMore: false, hasScopedSources: true, allExternal: true, hasAnySources: true }),
    'Application output is retained by its provider. Use the access details above.',
  );
  assert.equal(
    logsEmptyMessage({ hasSearch: true, truncated: false, canLoadMore: false, hasScopedSources: true, allExternal: false, hasAnySources: true }),
    'No visible log lines match this search.',
  );
  assert.equal(
    logsEmptyMessage({ hasSearch: true, truncated: true, canLoadMore: true, hasScopedSources: true, allExternal: false, hasAnySources: true }),
    'No matches in the log history loaded so far. Older output may not be loaded yet, so try Load older lines.',
  );
  assert.equal(
    logsEmptyMessage({ hasSearch: true, truncated: true, canLoadMore: false, hasScopedSources: true, allExternal: false, hasAnySources: true }),
    'No matches in the log history loaded so far. Older output may not be loaded yet.',
    'still names the real reason for zero matches even when there is nothing left to load on demand',
  );
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
  // The download completes as a microtask, but the confirmation text is set
  // via announce()'s own deferred setTimeout(0), so poll for it rather than
  // gambling on a single fixed-delay tick.
  await waitUntil(() => /complete retained run/i.test(panel.querySelector('#logs-announcement').textContent));
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
  const panel = dom.window.document.querySelector('#panel');
  // Entries land via scheduleRender's requestAnimationFrame hop, not
  // synchronously with onmessage, so poll for the settled row count instead
  // of gambling on a single fixed-delay tick.
  await waitUntil(() => panel.querySelectorAll('.log-entry-source').length === 4);

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
  // Resuming flushes the buffered line via scheduleRender's deferred
  // requestAnimationFrame hop, not synchronously with the click, so poll for
  // the rendered text instead of gambling on a single fixed-delay tick.
  await waitUntil(() => /buffered while paused/.test(panel.querySelector('#detail-logs-body').textContent));
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

  // The stale-source fallback runs on the interval poll (refreshEveryMs) and
  // then reconnects and rerenders asynchronously, so poll for each settled
  // condition instead of gambling on one fixed-delay wait.
  await waitUntil(() => panel.querySelector('#logs-source').value === 'all');
  assert.equal(dom.window.location.search, '');
  assert.equal(streams.length, 1, 'falling back to all must open the current stream');
  await waitUntil(() => /no longer retained/i.test(panel.querySelector('#detail-logs-body').textContent));
  assert.match(panel.querySelector('#detail-logs-body').textContent, /no longer retained/i);
  await waitUntil(() => /no longer retained/i.test(panel.querySelector('#logs-announcement').textContent));
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
  // Terminal reconciliation runs on the discovery interval poll and then
  // fetches and merges the final retained lines asynchronously, so poll for
  // the fully merged text instead of gambling on fixed-delay waits.
  await waitUntil(() => panel.querySelector('#detail-logs-body').textContent.includes('final crash detail'));

  const output = panel.querySelector('#detail-logs-body').textContent;
  assert.equal((output.match(/boot/g) || []).length, 1);
  assert.equal((output.match(/ready/g) || []).length, 1);
  assert.equal((output.match(/final crash detail/g) || []).length, 1);
  assert.ok(output.indexOf('final crash detail') < output.indexOf('stopped'), 'terminal status follows final retained output');
  assert.equal(terminalFetches, 1);
  assert.equal(stream.closed, true);
});

test('retention gaps are surfaced as source-specific operational events', async (t) => {
  const source = {
    source_id: 'run-2', run_id: 'run-2', replica: 2, status: 'running',
    current: true, has_log: true,
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  let stream;
  class FakeEventSource {
    constructor() { this.listeners = new Map(); stream = this; }
    addEventListener(type, listener) { this.listeners.set(type, listener); }
    close() {}
  }
  const api = async () => ({ ok: true, json: async () => ({ sources: [source] }) });
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: FakeEventSource, refreshEveryMs: 60_000,
  });
  t.after(() => { cleanup(); dom.window.close(); });
  await tick();
  stream.listeners.get('retention-gap')({ data: 'Output before this point is no longer retained' });
  // Both the detail body and the announcement update via scheduleRender's/
  // announce()'s own deferred hops, not synchronously with the event, so
  // poll for each settled condition instead of one fixed-delay tick.
  await waitUntil(() => /Replica #2 reconnected.*no longer retained/i.test(panel.querySelector('#detail-logs-body').textContent));
  assert.match(panel.querySelector('#detail-logs-body').textContent, /Replica #2 reconnected.*no longer retained/i);
  await waitUntil(() => /earlier output from replica 2.*no longer retained/i.test(panel.querySelector('#logs-announcement').textContent));
  assert.match(panel.querySelector('#logs-announcement').textContent, /earlier output from replica 2.*no longer retained/i);
});

test('shared-log delivery degradation stays connected and reports recovery', async (t) => {
  const source = {
    source_id: 'run-2', run_id: 'run-2', replica: 2, status: 'running',
    current: true, has_log: true,
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs', pretendToBeVisual: true,
  });
  let stream;
  class FakeEventSource {
    constructor() { this.listeners = new Map(); stream = this; }
    addEventListener(type, listener) { this.listeners.set(type, listener); }
    close() {}
  }
  const api = async () => ({ ok: true, json: async () => ({ sources: [source] }) });
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: FakeEventSource, refreshEveryMs: 60_000,
  });
  t.after(() => { cleanup(); dom.window.close(); });
  await tick();
  stream.onopen();
  stream.listeners.get('stream-degraded')({ data: 'temporarily delayed' });
  // The status text/class update with the event, but the detail body and
  // announcement render via their own deferred hops (scheduleRender /
  // announce), so poll for those instead of gambling on one fixed-delay tick.
  await waitUntil(() => /Replica #2 live output delayed/i.test(panel.querySelector('#detail-logs-body').textContent));

  assert.equal(panel.querySelector('.logs-status-text').textContent, 'Delayed · 1 live source waiting for log storage');
  assert.ok(panel.querySelector('.logs-stream-status').classList.contains('is-degraded'));
  assert.match(panel.querySelector('#detail-logs-body').textContent, /Replica #2 live output delayed/i);
  await waitUntil(() => /temporarily delayed.*catch up automatically/i.test(panel.querySelector('#logs-announcement').textContent));
  assert.match(panel.querySelector('#logs-announcement').textContent, /temporarily delayed.*catch up automatically/i);

  stream.listeners.get('stream-recovered')({ data: 'recovered' });
  await waitUntil(() => /Replica #2 live output delivery recovered/i.test(panel.querySelector('#detail-logs-body').textContent));
  assert.equal(panel.querySelector('.logs-status-text').textContent, 'Live · 1 connected source');
  assert.ok(panel.querySelector('.logs-stream-status').classList.contains('is-connected'));
  assert.match(panel.querySelector('#detail-logs-body').textContent, /Replica #2 live output delivery recovered/i);
  await waitUntil(() => /recovered and is catching up/i.test(panel.querySelector('#logs-announcement').textContent));
  assert.match(panel.querySelector('#logs-announcement').textContent, /recovered and is catching up/i);
});

test('Load older lines grows a static source past the default cap and retires itself once the run is fully loaded', async (t) => {
  const source = { source_id: 'run-9', run_id: 'run-9', replica: 9, status: 'stopped', current: false, has_log: true };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs?log_source=run-9',
    pretendToBeVisual: true,
  });
  const requested = [];
  const api = async (url) => {
    requested.push(url);
    if (url.endsWith('/logs/sources')) return { ok: true, json: async () => ({ sources: [source] }) };
    const tail = Number((url.match(/tail=(\d+)/) || [])[1]);
    // The first fetch fills the default window exactly (nothing yet proves the
    // run is shorter than that); the grown fetch returns fewer lines than it
    // asked for, which is what proves the run is now fully loaded.
    const count = tail === DEFAULT_LOG_TAIL ? DEFAULT_LOG_TAIL : 300;
    const text = Array.from({ length: count }, (_, i) => `line ${i}`).join('\n') + '\n';
    return { ok: true, text: async () => text };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({ panel, app: { slug: 'demo' }, api, refreshEveryMs: 60_000 });
  t.after(() => { cleanup(); dom.window.close(); });
  // The initial fetch resolves as a microtask, but the render it triggers is
  // deferred to a requestAnimationFrame callback (scheduleRender in
  // logs-ui.js), so a single fixed-delay tick is not reliably long enough
  // under a loaded test runner (jsdom's rAF shim can land past 20ms when the
  // process is busy with other concurrent test files). Poll for the rendered
  // count instead of guessing a delay.
  await waitUntil(() => panel.querySelectorAll('.log-entry').length === DEFAULT_LOG_TAIL);

  const loadMore = panel.querySelector('#logs-load-more');
  assert.equal(loadMore.hidden, false, 'a fetch that exactly filled the requested window has not proven the run is shorter');
  assert.equal(loadMore.textContent, 'Load older lines (up to 1,000)');
  assert.equal(panel.querySelectorAll('.log-entry').length, DEFAULT_LOG_TAIL);

  loadMore.click();
  await waitUntil(() => panel.querySelectorAll('.log-entry').length === 300);

  assert.match(requested.at(-1), /replica=9&run=run-9&tail=1000&follow=false/);
  assert.equal(loadMore.hidden, true, 'a fetch returning fewer lines than the 1,000 requested proves the run is now fully loaded');
  assert.equal(panel.querySelectorAll('.log-entry').length, 300, 'the grown fetch replaces the buffer rather than appending onto it');
  assert.match(panel.querySelector('#detail-logs-body').textContent, /line 299/);
  assert.match(panel.querySelector('#logs-announcement').textContent, /Loaded 300 lines for replica 9/);
});

test('Load older lines targets only a single selected static source, never live, external, or the aggregated view', async (t) => {
  const live = { source_id: 'live-1', replica: 1, status: 'running', current: true, has_log: true };
  const stopped = { source_id: 'run-9', run_id: 'run-9', replica: 9, status: 'stopped', current: false, has_log: true };
  const external = {
    source_id: 'ext-2', replica: 2, status: 'stopped', current: false, has_log: true, stream_available: false,
    external_logs: { provider: 'aws_ecs', region: 'eu-west-1', resource: 'arn:aws:ecs:eu-west-1:1:task/x', cluster: 'x' },
  };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs',
    pretendToBeVisual: true,
  });
  class FakeEventSource { close() {} }
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) return { ok: true, json: async () => ({ sources: [live, stopped, external] }) };
    const count = DEFAULT_LOG_TAIL;
    return { ok: true, text: async () => Array.from({ length: count }, (_, i) => `line ${i}`).join('\n') + '\n' };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({
    panel, app: { slug: 'demo' }, api, EventSourceClass: FakeEventSource, refreshEveryMs: 60_000,
  });
  t.after(() => { cleanup(); dom.window.close(); });
  await tick();

  const loadMore = panel.querySelector('#logs-load-more');
  const select = panel.querySelector('#logs-source');
  assert.equal(select.value, 'all');
  assert.equal(loadMore.hidden, true, 'the aggregated view has no single source to grow');

  select.value = 'live-1';
  select.dispatchEvent(new dom.window.Event('change'));
  await tick();
  assert.equal(loadMore.hidden, true, 'a live source keeps growing on its own via the stream');

  select.value = 'ext-2';
  select.dispatchEvent(new dom.window.Event('change'));
  await tick();
  assert.equal(loadMore.hidden, true, 'an externally retained source has no ShinyHub tail parameter to grow');

  select.value = 'run-9';
  select.dispatchEvent(new dom.window.Event('change'));
  await tick();
  assert.equal(loadMore.hidden, false, 'a single selected static source is exactly the case Load older lines targets');
});

test('a search that misses names unloaded history as the reason rather than reporting a flat no-match', async (t) => {
  const source = { source_id: 'run-9', run_id: 'run-9', replica: 9, status: 'stopped', current: false, has_log: true };
  const dom = new JSDOM('<section id="panel"></section>', {
    url: 'https://shinyhub.test/apps/demo/logs?log_source=run-9',
    pretendToBeVisual: true,
  });
  const api = async (url) => {
    if (url.endsWith('/logs/sources')) return { ok: true, json: async () => ({ sources: [source] }) };
    const text = Array.from({ length: DEFAULT_LOG_TAIL }, (_, i) => `line ${i}`).join('\n') + '\n';
    return { ok: true, text: async () => text };
  };
  const panel = dom.window.document.querySelector('#panel');
  const cleanup = createLogsViewer({ panel, app: { slug: 'demo' }, api, refreshEveryMs: 60_000 });
  t.after(() => { cleanup(); dom.window.close(); });
  await tick();

  const search = panel.querySelector('#logs-search');
  search.value = 'needle not in the loaded window';
  search.dispatchEvent(new dom.window.Event('input'));
  // Filtering re-renders via scheduleRender's deferred hop, not synchronously
  // with the input event, so poll for the settled message instead of
  // gambling on a single fixed-delay tick.
  await waitUntil(() => /Older output may not be loaded yet/.test(panel.querySelector('#detail-logs-body').textContent));

  assert.match(
    panel.querySelector('#detail-logs-body').textContent,
    /Older output may not be loaded yet, so try Load older lines\./,
    'an empty search result over a truncated buffer must not read as a plain no-match',
  );
});
