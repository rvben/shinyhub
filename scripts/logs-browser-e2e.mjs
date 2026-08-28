#!/usr/bin/env node

// Real-browser contract for the app-detail logs workspace. This deliberately
// serves the production module and stylesheet: jsdom covers the detailed state
// machine, while this test catches browser-only failures in native EventSource,
// keyboard controls, responsive layout, reduced motion, and rendered contrast.

import assert from 'node:assert/strict';
import { once } from 'node:events';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';

const rootRequire = createRequire(new URL('../package.json', import.meta.url));
const driverRequire = createRequire(new URL('../loadtest/render/driver/package.json', import.meta.url));
const axe = rootRequire('axe-core');
const { chromium } = driverRequire('playwright');

const stylesheet = await readFile(new URL('../internal/ui/static/style.css', import.meta.url));
const logsModule = await readFile(new URL('../internal/ui/static/views/logs-ui.js', import.meta.url));

const sources = [
  {
    source_id: 'live-0', run_id: 'live-run-0', replica: 0, current: true,
    status: 'running', has_log: true, stream_available: true, tier: 'local',
    started_at: '2026-08-15T09:00:00Z',
  },
  {
    source_id: 'live-1', run_id: 'live-run-1', replica: 1, current: true,
    status: 'running', has_log: true, stream_available: true, tier: 'local',
    started_at: '2026-08-15T09:00:01Z',
  },
  {
    source_id: 'old-run', run_id: 'old-run', replica: 0, current: false,
    status: 'stopped', has_log: true, stream_available: true, tier: 'local',
    started_at: '2026-08-14T08:00:00Z',
  },
  {
    source_id: 'cloudwatch-run', run_id: 'cloudwatch-run', replica: 6, current: false,
    status: 'stopped', has_log: false, stream_available: false, inline_available: true,
    provider: 'fargate', tier: 'burst', started_at: '2026-08-13T09:00:00Z',
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics-production',
      resource: 'arn:aws:ecs:eu-west-1:111122223333:task/analytics-production/cloudwatch-task-0123456789abcdef',
      log_group: '/shinyhub/apps', log_stream: 'app/shinyhub/cloudwatch-task-0123456789abcdef',
      log_url: 'https://eu-west-1.console.aws.amazon.com/cloudwatch/home?region=eu-west-1#logsV2:log-groups',
      console_url: 'https://console.aws.amazon.com/ecs/v2/clusters/analytics-production/tasks/cloudwatch-task-0123456789abcdef/logs?region=eu-west-1',
    },
  },
  {
    source_id: 'external-run', run_id: 'external-run', replica: 7, current: false,
    status: 'stopped', has_log: false, stream_available: false, provider: 'fargate', tier: 'burst',
    started_at: '2026-08-13T08:00:00Z',
    external_logs: {
      provider: 'aws_ecs', region: 'eu-west-1', cluster: 'analytics-production',
      resource: 'arn:aws:ecs:eu-west-1:111122223333:task/analytics-production/very-long-task-identity-0123456789abcdef',
      console_url: 'https://console.aws.amazon.com/ecs/v2/clusters/analytics-production/tasks/very-long-task-identity-0123456789abcdef/logs?region=eu-west-1',
    },
  },
];

const fixtureHTML = `<!doctype html>
<html lang="en" data-theme="dark">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Application logs browser contract</title>
  <link rel="stylesheet" href="/static/style.css">
</head>
<body data-auth="loading">
  <div class="bg-stars" aria-hidden="true"></div>
  <div id="app-shell">
    <div class="app-content">
      <main>
        <section id="logs-panel" class="settings-tab-panel" aria-label="Application logs"></section>
      </main>
    </div>
  </div>
  <script type="module">
    import { createLogsViewer } from '/static/views/logs-ui.js';
    const panel = document.querySelector('#logs-panel');
    window.__nativeEventSource = EventSource;
    window.__destroyLogsViewer = createLogsViewer({
      panel,
      app: { slug: 'demo' },
      api: (path) => fetch(path),
      EventSourceClass: window.EventSource,
      refreshEveryMs: 60000,
    });
  </script>
</body>
</html>`;

function json(res, status, value) {
  res.writeHead(status, {
    'content-type': 'application/json; charset=utf-8',
    'cache-control': 'no-store',
  });
  res.end(JSON.stringify(value));
}

function text(res, status, value, contentType = 'text/plain; charset=utf-8') {
  res.writeHead(status, { 'content-type': contentType, 'cache-control': 'no-store' });
  res.end(value);
}

function startFixtureServer() {
  const state = {
    liveRequests: [],
    openStreams: new Set(),
    zeroConnections: 0,
    oneConnections: 0,
    providerRequests: [],
  };

  const server = createServer((req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');

    if (url.pathname === '/') {
      text(res, 200, fixtureHTML, 'text/html; charset=utf-8');
      return;
    }
    if (url.pathname === '/favicon.ico') {
      res.writeHead(204, { 'cache-control': 'no-store' });
      res.end();
      return;
    }
    if (url.pathname === '/static/style.css') {
      text(res, 200, stylesheet, 'text/css; charset=utf-8');
      return;
    }
    if (url.pathname === '/static/views/logs-ui.js') {
      text(res, 200, logsModule, 'text/javascript; charset=utf-8');
      return;
    }
    if (url.pathname === '/api/apps/demo/logs/sources') {
      json(res, 200, { sources });
      return;
    }
    if (url.pathname !== '/api/apps/demo/logs') {
      text(res, 404, 'not found');
      return;
    }

    const replica = url.searchParams.get('replica');
    const run = url.searchParams.get('run');
    if (url.searchParams.get('provider') === 'true') {
      state.providerRequests.push(url.href);
      if (replica === '6' && run === 'cloudwatch-run') {
        json(res, 200, {
          events: [{ message: 'historical CloudWatch output', timestamp: '2026-08-13T09:00:01Z' }],
          next_cursor: 'cloudwatch-forward-token',
        });
      } else {
        json(res, 404, { error: 'provider log not found' });
      }
      return;
    }
    if (url.searchParams.get('follow') === 'false') {
      if (replica === '0' && run === 'old-run') {
        text(res, 200, 'retained first line\nretained final line\n');
      } else {
        text(res, 404, 'retained log not found');
      }
      return;
    }

    if (!['0', '1'].includes(replica)) {
      text(res, 400, 'invalid replica');
      return;
    }

    const record = {
      replica,
      run,
      lastEventID: req.headers['last-event-id'] || '',
    };
    state.liveRequests.push(record);
    state.openStreams.add(res);
    res.on('close', () => state.openStreams.delete(res));
    res.writeHead(200, {
      'content-type': 'text/event-stream; charset=utf-8',
      'cache-control': 'no-cache, no-transform',
      connection: 'keep-alive',
      'x-accel-buffering': 'no',
    });
    res.flushHeaders();

    if (replica === '0') {
      state.zeroConnections++;
      if (state.zeroConnections === 1) {
        res.write('retry: 500\nid: 7\ndata: zero before drop\n\n');
        // Leave enough time for the test to observe the fully-connected state
        // before the intentional transport drop. Native EventSource emits an
        // error and then reconnects after the advertised retry interval.
        setTimeout(() => {
          if (!res.destroyed) res.end();
        }, 1500);
      } else {
        res.write('id: 28\ndata: zero after reconnect\n\n');
      }
      return;
    }

    state.oneConnections++;
    res.write(`id: ${100 + state.oneConnections}\ndata: one live ${state.oneConnections}\n\n`);
  });

  return { server, state };
}

async function listen(server) {
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const address = server.address();
  assert(address && typeof address === 'object');
  return `http://127.0.0.1:${address.port}`;
}

async function closeServer(server, state) {
  for (const response of state.openStreams) response.end();
  if (!server.listening) return;
  server.close();
  await once(server, 'close');
}

async function assertContrast(page, theme) {
  await page.evaluate((nextTheme) => {
    document.documentElement.dataset.theme = nextTheme;
  }, theme);
  // Several shared controls animate colour changes for 200ms. Audit the stable
  // rendered palette, not an arbitrary frame in the theme transition.
  await page.waitForTimeout(250);
  await page.evaluate(axe.source);
  const result = await page.evaluate(async () => globalThis.axe.run(document, {
    runOnly: { type: 'rule', values: ['color-contrast'] },
    resultTypes: ['violations', 'incomplete'],
  }));
  const unresolved = result.incomplete.flatMap((incomplete) => incomplete.nodes
    .filter((node) => !node.failureSummary?.includes('background color could not be determined due to a background gradient'))
    .map((node) => ({ id: incomplete.id, target: node.target, summary: node.failureSummary })));
  assert.deepEqual(unresolved, [], `${theme} theme unexpected incomplete contrast checks:\n${JSON.stringify(unresolved, null, 2)}`);
  const failures = result.violations.map((violation) => ({
    id: violation.id,
    impact: violation.impact,
    nodes: violation.nodes.map((node) => ({ target: node.target, summary: node.failureSummary })),
  }));
  assert.deepEqual(failures, [], `${theme} theme contrast failures:\n${JSON.stringify(failures, null, 2)}`);
}

async function assertResponsiveLayout(page) {
  await page.setViewportSize({ width: 390, height: 844 });
  const layout = await page.evaluate(() => {
    const selectors = [
      '#logs-source', '#logs-search', '#logs-pause', '#logs-copy',
      '#logs-download', '#logs-stream-status', '#detail-logs-body',
      '#logs-external',
    ];
    const boxes = selectors.map((selector) => {
      const rect = document.querySelector(selector).getBoundingClientRect();
      return { selector, left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: rect.width };
    });
    const actionBoxes = [...document.querySelectorAll('.logs-toolbar-actions button')]
      .map((element) => element.getBoundingClientRect())
      .map((rect) => ({ left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom }));
    return {
      viewportWidth: innerWidth,
      documentWidth: document.documentElement.scrollWidth,
      boxes,
      actionBoxes,
    };
  });

  assert.equal(layout.documentWidth, layout.viewportWidth, 'mobile logs workspace must not overflow horizontally');
  for (const box of layout.boxes) {
    assert(box.width > 0, `${box.selector} must remain visible on mobile`);
    assert(box.left >= 0 && box.right <= layout.viewportWidth + 0.5, `${box.selector} must remain inside the mobile viewport`);
  }
  for (let i = 0; i < layout.actionBoxes.length; i++) {
    for (let j = i + 1; j < layout.actionBoxes.length; j++) {
      const a = layout.actionBoxes[i];
      const b = layout.actionBoxes[j];
      const overlapWidth = Math.min(a.right, b.right) - Math.max(a.left, b.left);
      const overlapHeight = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top);
      assert(overlapWidth <= 0 || overlapHeight <= 0, `mobile toolbar actions ${i} and ${j} must not overlap`);
    }
  }
}

async function runBrowserContract(origin, state) {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.SHINYHUB_E2E_BROWSER_CHANNEL || 'chrome',
  });
  const context = await browser.newContext({
    viewport: { width: 1280, height: 900 },
    colorScheme: 'dark',
    reducedMotion: 'reduce',
  });
  const page = await context.newPage();
  const browserErrors = [];
  page.on('pageerror', (error) => browserErrors.push(`pageerror: ${error.message}`));
  page.on('console', (message) => {
    if (message.type() === 'error') browserErrors.push(`console: ${message.text()}`);
  });

  try {
    await page.goto(origin, { waitUntil: 'domcontentloaded' });
    assert.equal(await page.evaluate(() => window.__nativeEventSource === window.EventSource), true,
      'fixture must exercise the browser native EventSource implementation');

    const output = page.locator('#detail-logs-body');
    const status = page.locator('.logs-status-text');
    await output.getByText('zero before drop', { exact: true }).waitFor();
    await output.getByText('one live 1', { exact: true }).waitFor();
    await page.waitForFunction(() => document.querySelector('.logs-status-text')?.textContent === 'Live · 2 connected sources');
    await page.waitForFunction(() => document.querySelector('.logs-status-text')?.textContent?.startsWith('Reconnecting · 1 of 2'));
    await output.getByText('zero after reconnect', { exact: true }).waitFor();
    await page.waitForFunction(() => document.querySelector('.logs-status-text')?.textContent === 'Live · 2 connected sources');

    const zeroLines = await output.locator('.log-entry-message').allTextContents();
    assert.equal(zeroLines.filter((line) => line === 'zero before drop').length, 1, 'pre-drop event must render once');
    assert.equal(zeroLines.filter((line) => line === 'zero after reconnect').length, 1, 'reconnected event must render once');
    assert.equal(state.liveRequests.filter((request) => request.replica === '0')[1]?.lastEventID, '7',
      'native EventSource reconnect must resume with Last-Event-ID');

    const source = page.locator('#logs-source');
    await source.focus();
    // Exercise native select typeahead: every source option starts with
    // "Replica", so repeated R keys cycle from all -> live 0 -> live 1 ->
    // retained run. Unlike popup-navigation keys, typeahead behaves identically
    // in macOS and Linux Chrome.
    await source.press('r');
    await source.press('r');
    await source.press('r');
    await page.waitForFunction(() => new URL(location.href).searchParams.get('log_source') === 'old-run');
    await output.getByText('retained first line', { exact: true }).waitFor();
    await output.getByText('retained final line', { exact: true }).waitFor();
    await page.waitForFunction(() => document.querySelector('.logs-status-text')?.textContent === '1 retained source · no live instances');
    assert.equal(await page.locator('#logs-pause').isDisabled(), true, 'ended runs must not expose a live pause action');
    await page.waitForFunction(() => document.querySelector('#logs-announcement')?.textContent?.includes('Replica #0'));

    const focusStyle = await source.evaluate((element) => {
      const style = getComputedStyle(element);
      return { active: document.activeElement === element, boxShadow: style.boxShadow, borderColor: style.borderColor };
    });
    assert.equal(focusStyle.active, true, 'keyboard-selected source control must retain focus');
    assert.notEqual(focusStyle.boxShadow, 'none', 'focused source control must have a visible focus treatment');

    const requestCountWhileHistorical = state.liveRequests.length;
    await page.waitForTimeout(700);
    assert.equal(state.liveRequests.length, requestCountWhileHistorical,
      'selecting an ended run must close live streams without reconnecting them');

    await source.selectOption('live-1');
    await page.waitForFunction(() => new URL(location.href).searchParams.get('log_source') === 'live-1');
    await output.getByText(/^one live \d+$/).waitFor();
    await page.waitForFunction(() => document.querySelector('.logs-status-text')?.textContent === 'Live · 1 connected source');
    assert.equal(await output.getByText('zero before drop', { exact: true }).count(), 0,
      'changing source scope must clear output from other replicas');
    await page.waitForFunction(() => document.querySelector('#logs-announcement')?.textContent?.includes('Replica #1'));

    await source.selectOption('cloudwatch-run');
    await output.getByText('historical CloudWatch output', { exact: true }).waitFor();
    await page.waitForFunction(() => document.querySelector('.logs-status-text')?.textContent === 'Stopped · CloudWatch logs available');
    await page.getByText('Open CloudWatch logs', { exact: true }).waitFor();
    assert.equal(await page.locator('#logs-download').textContent(), 'AWS-retained logs');
    assert.equal(await page.locator('#logs-download').isDisabled(), true, 'provider-retained runs must not offer a false local download');
    assert.match(state.providerRequests[0], /provider=true/);
    assert.match(state.providerRequests[0], /run=cloudwatch-run/);
    assert.match(await page.locator('.logs-external-identity').textContent(), /shinyhub\/apps.*cloudwatch-task.*eu-west-1/);
    assert.equal(await page.getByText('Open CloudWatch logs', { exact: true }).getAttribute('rel'), 'noopener noreferrer');

    await source.selectOption('external-run');
    await page.waitForFunction(() => document.querySelector('.logs-status-text')?.textContent === 'Stopped · logs retained in AWS');
    await page.getByText('Open task logs', { exact: true }).waitFor();
    assert.equal(await page.locator('#logs-external').isVisible(), true, 'external handoff must be visible');
    assert.equal(await page.locator('#logs-download').isDisabled(), true, 'external runs must not offer a false local download');
    assert.equal(await page.getByText('Open task logs', { exact: true }).getAttribute('rel'), 'noopener noreferrer');
    assert.match(await page.locator('.logs-external-identity').textContent(), /very-long-task-identity.*eu-west-1.*analytics-production/);
    assert.match(await output.textContent(), /retained by its provider/i,
      'switching sources must replace the previous run output before exposing the new source identity');

    assert.equal(await page.locator('.logs-status-dot').evaluate((element) => getComputedStyle(element).animationName), 'none',
      'reduced-motion users must not receive the live pulse animation');

    await assertContrast(page, 'dark');
    await assertContrast(page, 'light');
    await assertResponsiveLayout(page);
    assert.deepEqual(browserErrors, [], `browser runtime errors:\n${browserErrors.join('\n')}`);
    assert.equal(await status.textContent(), 'Stopped · logs retained in AWS');
  } finally {
    await page.evaluate(() => window.__destroyLogsViewer?.()).catch(() => {});
    await browser.close();
  }
}

const { server, state } = startFixtureServer();
const origin = await listen(server);

if (process.argv.includes('--serve')) {
  process.stdout.write(`${origin}\n`);
  await Promise.race([once(process, 'SIGINT'), once(process, 'SIGTERM')]);
  await closeServer(server, state);
} else {
  try {
    await runBrowserContract(origin, state);
    process.stdout.write('BROWSER LOGS E2E PASS\n');
  } finally {
    await closeServer(server, state);
  }
}
