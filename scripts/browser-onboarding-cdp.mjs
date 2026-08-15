#!/usr/bin/env node

// Tiny Chrome DevTools Protocol driver for the release-like onboarding E2E.
// It deliberately uses only Node's built-in fetch/WebSocket APIs so contributors
// do not need Playwright, a downloaded browser, or another dependency tree.

import fs from 'node:fs/promises';

const [action, debuggerURL, value, username, password, screenshotPath] = process.argv.slice(2);

if (!['approve', 'revoke', 'logs'].includes(action) || !debuggerURL || !value) {
  console.error(`usage:
  browser-onboarding-cdp.mjs approve <debugger-url> <pairing-url> [username] [password] [screenshot]
  browser-onboarding-cdp.mjs revoke  <debugger-url> <token-name> [username] [password] [screenshot]
  browser-onboarding-cdp.mjs logs    <debugger-url> <logs-url> <expected-log-line> <live|reconnected|history> [screenshot]`);
  process.exit(2);
}

class CDPClient {
  constructor(socket) {
    this.socket = socket;
    this.nextID = 1;
    this.pending = new Map();
  }

  static async connect(url) {
    const socket = new WebSocket(url);
    await new Promise((resolve, reject) => {
      socket.addEventListener('open', resolve, {once: true});
      socket.addEventListener('error', () => reject(new Error(`could not connect to Chrome at ${url}`)), {once: true});
    });
    const client = new CDPClient(socket);
    socket.addEventListener('message', event => client.receive(event.data));
    socket.addEventListener('close', () => {
      for (const {reject} of client.pending.values()) reject(new Error('Chrome DevTools connection closed'));
      client.pending.clear();
    });
    return client;
  }

  receive(data) {
    const message = JSON.parse(data);
    if (message.method === 'Page.javascriptDialogOpening') {
      void this.call('Page.handleJavaScriptDialog', {accept: true});
      return;
    }
    if (!message.id) return;
    const pending = this.pending.get(message.id);
    if (!pending) return;
    this.pending.delete(message.id);
    clearTimeout(pending.timer);
    if (message.error) pending.reject(new Error(`${pending.method}: ${message.error.message}`));
    else pending.resolve(message.result || {});
  }

  call(method, params = {}) {
    const id = this.nextID++;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`${method} timed out`));
      }, 15_000);
      this.pending.set(id, {resolve, reject, timer, method});
      this.socket.send(JSON.stringify({id, method, params}));
    });
  }

  close() {
    this.socket.close();
  }
}

async function pageTarget(baseURL) {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      const targets = await fetch(`${baseURL}/json/list`).then(response => response.json());
      const page = targets.find(target => target.type === 'page');
      if (page?.webSocketDebuggerUrl) return page;
    } catch {
      // Chrome may have written DevToolsActivePort just before the endpoint is ready.
    }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error(`Chrome exposed no page target at ${baseURL}`);
}

const visibleExpression = selector => `(() => {
  const element = document.querySelector(${JSON.stringify(selector)});
  if (!element || element.hidden) return false;
  const style = getComputedStyle(element);
  return style.display !== 'none' && style.visibility !== 'hidden';
})()`;

async function evaluate(client, expression) {
  const result = await client.call('Runtime.evaluate', {
    expression,
    awaitPromise: true,
    returnByValue: true,
  });
  if (result.exceptionDetails) {
    const detail = result.exceptionDetails.exception?.description || result.exceptionDetails.text;
    throw new Error(`browser evaluation failed: ${detail}`);
  }
  return result.result?.value;
}

async function waitFor(client, expression, label, timeout = 20_000) {
  const deadline = Date.now() + timeout;
  let lastError = '';
  while (Date.now() < deadline) {
    try {
      if (await evaluate(client, expression)) return;
    } catch (error) {
      lastError = error.message;
    }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error(`timed out waiting for ${label}${lastError ? ` (${lastError})` : ''}`);
}

async function setInput(client, selector, inputValue) {
  const changed = await evaluate(client, `(() => {
    const element = document.querySelector(${JSON.stringify(selector)});
    if (!element) return false;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    setter.call(element, ${JSON.stringify(inputValue)});
    element.dispatchEvent(new Event('input', {bubbles: true}));
    element.dispatchEvent(new Event('change', {bubbles: true}));
    return true;
  })()`);
  if (!changed) throw new Error(`input not found: ${selector}`);
}

async function click(client, selector) {
  const clicked = await evaluate(client, `(() => {
    const element = document.querySelector(${JSON.stringify(selector)});
    if (!element || element.hidden || element.disabled) return false;
    element.focus();
    element.click();
    return true;
  })()`);
  if (!clicked) throw new Error(`button was unavailable: ${selector}`);
}

async function screenshot(client, path) {
  if (!path) return;
  const result = await client.call('Page.captureScreenshot', {format: 'png', fromSurface: true});
  await fs.writeFile(path, Buffer.from(result.data, 'base64'));
}

async function approve(client, pairingURL) {
  const pairing = new URL(pairingURL);
  const tokenName = pairing.searchParams.get('connect_name');
  const expectedCode = pairing.searchParams.get('connect_code');
  if (!tokenName || !expectedCode || pairingURL.includes('shk_')) {
    throw new Error('pairing URL is missing bounded public context or contains a raw credential');
  }

  await client.call('Page.navigate', {url: pairingURL});
  // Decide from the SPA's completed auth probe, not the static login shell.
  // On a returning visit that shell can be visible for a few milliseconds
  // before /api/auth/me restores the existing session and hides it.
  await waitFor(client, `['in', 'out'].includes(document.body.dataset.auth)`, 'the browser session check');

  if (await evaluate(client, `document.body.dataset.auth === 'out'`)) {
    if (!username || !password) throw new Error('the fresh browser requested sign-in credentials');
    await setInput(client, '#login-username', username);
    await setInput(client, '#login-password', password);
    await click(client, '#login-form button[type="submit"]');
  }

  await waitFor(client, visibleExpression('#cli-connect-panel'), 'the restored CLI approval request');
  const details = await evaluate(client, `(() => ({
    heading: document.querySelector('#cli-connect-heading')?.textContent.trim(),
    user: document.querySelector('#cli-connect-user')?.textContent.trim(),
    device: document.querySelector('#cli-connect-device')?.textContent.trim(),
    code: document.querySelector('#cli-connect-code')?.textContent.trim(),
    labelledBy: document.querySelector('#cli-connect-panel')?.getAttribute('aria-labelledby'),
  }))()`);
  if (details.heading !== 'Connect this terminal?' || details.code !== expectedCode ||
      !details.user || !details.device || details.labelledBy !== 'cli-connect-heading') {
    throw new Error(`approval context was incomplete: ${JSON.stringify(details)}`);
  }

  await click(client, '#cli-connect-approve');
  await waitFor(client, visibleExpression('#cli-connect-success'), 'the connected confirmation');
  await waitFor(
    client,
    `Array.from(document.querySelectorAll('[data-token-row] .token-name')).some(element => element.textContent === ${JSON.stringify(tokenName)})`,
    'the newly created CLI token in the token list',
  );
  const success = await evaluate(client, `document.querySelector('#cli-connect-success')?.textContent.trim()`);
  if (!success?.includes('return to your terminal')) throw new Error(`unclear success copy: ${JSON.stringify(success)}`);
  await screenshot(client, screenshotPath);
  console.log(tokenName);
}

async function revoke(client, tokenName) {
  // The approval action leaves this isolated, authenticated browser on
  // /tokens. A separate driver process reconnects to that same real page here,
  // proving the session and token list survive beyond the approval automation.
  await waitFor(
    client,
    `Array.from(document.querySelectorAll('[data-token-row] .token-name')).some(element => element.textContent === ${JSON.stringify(tokenName)})`,
    `token ${tokenName}`,
  );
  const clicked = await evaluate(client, `(() => {
    const rows = Array.from(document.querySelectorAll('[data-token-row]'));
    const row = rows.find(candidate => candidate.querySelector('.token-name')?.textContent === ${JSON.stringify(tokenName)});
    const button = row?.querySelector('button[data-token-id]');
    if (!button) return false;
    button.focus();
    button.click();
    return true;
  })()`);
  if (!clicked) throw new Error(`revoke button unavailable for ${tokenName}`);
  await waitFor(
    client,
    `!Array.from(document.querySelectorAll('[data-token-row] .token-name')).some(element => element.textContent === ${JSON.stringify(tokenName)})`,
    `token ${tokenName} to disappear after revocation`,
  );
  await screenshot(client, screenshotPath);
}

const logLineDetailsExpression = expectedLine => `(() =>
  Array.from(document.querySelectorAll('.log-entry-message'))
    .filter(element => element.textContent === ${JSON.stringify(expectedLine)})
    .map(element => ({
      line: element.textContent,
      source: element.closest('.log-entry')?.querySelector('.log-entry-source')?.textContent.trim(),
    }))
)()`;

async function waitForExactReplicaLines(client, expectedLine, expectedSources) {
  await waitFor(
    client,
    `JSON.stringify(${logLineDetailsExpression(expectedLine)}.map(item => item.source).sort()) === ${JSON.stringify(JSON.stringify([...expectedSources].sort()))}`,
    `application startup output from ${expectedSources.join(' and ')}`,
    30_000,
  );
}

async function assertExactReplicaLines(client, expectedLine, expectedSources, label) {
  const details = await evaluate(client, logLineDetailsExpression(expectedLine));
  const actual = details.map(item => item.source).sort();
  const expected = [...expectedSources].sort();
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`${label}: got ${JSON.stringify(details)}, want one line from ${expected.join(' and ')}`);
  }
}

async function selectLogSource(client, labelPrefix) {
  const selected = await evaluate(client, `(() => {
    const select = document.querySelector('#logs-source');
    const option = Array.from(select?.options || []).find(candidate => candidate.textContent.trim().startsWith(${JSON.stringify(labelPrefix)}));
    if (!select || !option) return false;
    select.value = option.value;
    select.dispatchEvent(new Event('change', {bubbles: true}));
    return true;
  })()`);
  if (!selected) throw new Error(`log source unavailable: ${labelPrefix}`);
}

async function verifyMobileLogsLayout(client, screenshotPath) {
  await client.call('Emulation.setDeviceMetricsOverride', {
    width: 390,
    height: 844,
    deviceScaleFactor: 1,
    mobile: false,
  });
  try {
    const layout = await evaluate(client, `(() => {
      const selectors = ['#logs-source', '#logs-search', '#logs-pause', '#logs-copy', '#logs-download', '#detail-logs-body'];
      return {
        viewportWidth: innerWidth,
        documentWidth: document.documentElement.scrollWidth,
        controls: selectors.map(selector => {
          const rect = document.querySelector(selector)?.getBoundingClientRect();
          return {selector, left: rect?.left, right: rect?.right, width: rect?.width};
        }),
      };
    })()`);
    const invalidControl = layout.controls.find(control =>
      !(control.width > 0) || control.left < 0 || control.right > layout.viewportWidth + 0.5);
    if (layout.documentWidth !== layout.viewportWidth || invalidControl) {
      throw new Error(`mobile logs layout overflowed: ${JSON.stringify({layout, invalidControl})}`);
    }
    await screenshot(client, screenshotPath);
  } finally {
    await client.call('Emulation.clearDeviceMetricsOverride');
  }
}

async function verifyLiveLogs(client, target, expectedLine) {
  await client.call('Page.navigate', {url: target.href});
  await waitFor(client, `location.pathname === ${JSON.stringify(target.pathname)}`, 'the app-detail logs route');
  await waitFor(client, `document.body.dataset.auth === 'in'`, 'the authenticated dashboard session');
  await waitFor(client, visibleExpression('#detail-logs-panel'), 'the app-detail logs panel');
  await waitFor(
    client,
    `document.querySelector('#detail-tab-logs')?.getAttribute('aria-selected') === 'true'`,
    'the Logs tab to become active',
  );
  await waitFor(
    client,
    `document.querySelector('.logs-status-text')?.textContent.trim() === 'Live · 2 connected sources'`,
    'two live application log streams',
    30_000,
  );
  await waitForExactReplicaLines(client, expectedLine, ['#0', '#1']);

  const initial = await evaluate(client, `(() => ({
    sourceOptions: Array.from(document.querySelectorAll('#logs-source option')).map(option => option.textContent.trim()),
    outputLabel: document.querySelector('#detail-logs-body')?.getAttribute('aria-label'),
    pauseDisabled: document.querySelector('#logs-pause')?.disabled,
  }))()`);
  if (initial.sourceOptions[0] !== 'All current replicas (2 live, 2 total)' ||
      !initial.sourceOptions.some(option => option.startsWith('Replica #0 — Running')) ||
      !initial.sourceOptions.some(option => option.startsWith('Replica #1 — Running')) ||
      initial.outputLabel !== 'Application log output' || initial.pauseDisabled !== false) {
    throw new Error(`two-replica logs workspace contract was incomplete: ${JSON.stringify(initial)}`);
  }

  await verifyMobileLogsLayout(client, screenshotPath);
  await selectLogSource(client, 'Replica #1 — Running');
  await waitFor(
    client,
    `new URL(location.href).searchParams.has('log_source') && document.querySelector('.logs-status-text')?.textContent.trim() === 'Live · 1 connected source'`,
    'the isolated Replica #1 stream and persistent URL selection',
  );
  await waitForExactReplicaLines(client, expectedLine, ['#1']);

  const beforeReload = await evaluate(client, `performance.timeOrigin`);
  await client.call('Page.reload');
  await waitFor(client, `performance.timeOrigin !== ${JSON.stringify(beforeReload)}`, 'the refreshed logs document');
  await waitFor(client, `document.body.dataset.auth === 'in'`, 'the restored authenticated session');
  await waitFor(
    client,
    `document.querySelector('#logs-source')?.selectedOptions[0]?.textContent.trim().startsWith('Replica #1 — Running') && document.querySelector('.logs-status-text')?.textContent.trim() === 'Live · 1 connected source'`,
    'Replica #1 selection to survive refresh',
    30_000,
  );
  await waitForExactReplicaLines(client, expectedLine, ['#1']);
  console.log('LOGS LIVE PASS');
}

async function verifyReconnectedLogs(client, target, expectedLine) {
  await waitFor(client, `location.pathname === ${JSON.stringify(target.pathname)}`, 'the existing app-detail logs route');
  await waitFor(
    client,
    `document.querySelector('.logs-status-text')?.textContent.trim().startsWith('Reconnecting ·')`,
    'the selected native stream to notice the crashed control plane',
  );
  // The shell waits for this sentinel before restarting the control plane, so
  // observing the reconnecting state is deterministic rather than a race.
  if (!screenshotPath) throw new Error('reconnect verification requires a sentinel path');
  await fs.writeFile(`${screenshotPath}.ready`, 'ready\n');
  await waitFor(
    client,
    `document.querySelector('#logs-source')?.selectedOptions[0]?.textContent.trim().startsWith('Replica #1 — Running') && document.querySelector('.logs-status-text')?.textContent.trim() === 'Live · 1 connected source'`,
    'Replica #1 to reconnect through the restarted control plane',
    45_000,
  );
  // onopen precedes any replayed message events. Give those events one turn to
  // arrive, then assert instead of accepting the pre-disconnect DOM before an
  // erroneous duplicate is appended.
  await new Promise(resolve => setTimeout(resolve, 750));
  await assertExactReplicaLines(client, expectedLine, ['#1'], 'reconnect duplicated application output');
  const restored = await evaluate(client, `(() => ({
    hasPersistentSelection: new URL(location.href).searchParams.has('log_source'),
    activeTab: document.querySelector('#detail-tab-logs')?.getAttribute('aria-selected'),
  }))()`);
  if (!restored.hasPersistentSelection || restored.activeTab !== 'true') {
    throw new Error(`reconnected logs lost navigation state: ${JSON.stringify(restored)}`);
  }
  await screenshot(client, screenshotPath);
  console.log('LOGS RECONNECTED PASS');
}

async function verifyHistoricalLogs(client, target, expectedLine) {
  await waitFor(client, `location.pathname === ${JSON.stringify(target.pathname)}`, 'the existing app-detail logs route');
  await waitFor(client, `document.body.dataset.auth === 'in'`, 'the authenticated dashboard session');
  try {
    await waitFor(
      client,
      `document.querySelector('#logs-source')?.selectedOptions[0]?.textContent.trim().startsWith('Replica #1 — Stopped')`,
      'the selected Replica #1 run to become retained and stopped',
      30_000,
    );
  } catch (error) {
    const diagnostics = await evaluate(client, `(async () => {
      const response = await fetch('/api/apps/app/logs/sources');
      return {
        selected: document.querySelector('#logs-source')?.selectedOptions[0]?.textContent.trim(),
        status: document.querySelector('.logs-status-text')?.textContent.trim(),
        groups: Array.from(document.querySelectorAll('#logs-source optgroup')).map(group => ({
          label: group.label,
          options: Array.from(group.querySelectorAll('option')).map(option => option.textContent.trim()),
        })),
        sources: response.ok ? (await response.json()).sources : {httpStatus: response.status},
      };
    })()`);
    throw new Error(`${error.message}: ${JSON.stringify(diagnostics)}`);
  }
  await waitFor(
    client,
    `document.querySelector('.logs-status-text')?.textContent.trim() === '1 retained source · no live instances'`,
    'the retained-run status',
  );
  await waitForExactReplicaLines(client, expectedLine, ['#1']);

  const history = await evaluate(client, `(() => ({
    hasPersistentSelection: new URL(location.href).searchParams.has('log_source'),
    sourceOptions: Array.from(document.querySelectorAll('#logs-source option')).map(option => option.textContent.trim()),
    historyGroups: Array.from(document.querySelectorAll('#logs-source optgroup')).map(group => group.label),
    pauseDisabled: document.querySelector('#logs-pause')?.disabled,
  }))()`);
  if (!history.hasPersistentSelection || history.sourceOptions[0] !== 'All current replicas (1 live, 2 total)' ||
      !history.sourceOptions.some(option => option.startsWith('Replica #0 — Running')) ||
      !history.sourceOptions.some(option => option.startsWith('Replica #1 —') && option.includes('Stopped')) ||
      !history.historyGroups.includes('Current runs') || history.pauseDisabled !== true) {
    throw new Error(`scale-down history contract was incomplete: ${JSON.stringify(history)}`);
  }
  await screenshot(client, screenshotPath);

  await selectLogSource(client, 'All current replicas');
  await waitFor(
    client,
    `!new URL(location.href).searchParams.has('log_source') && document.querySelector('.logs-status-text')?.textContent.trim() === 'Live · 1 connected source'`,
    'the merged live-and-retained view after leaving the stopped run',
    30_000,
  );
  await waitForExactReplicaLines(client, expectedLine, ['#0', '#1']);
  console.log('LOGS HISTORY PASS');
}

async function verifyLogs(client, logsURL, expectedLine, phase) {
  if (!expectedLine) throw new Error('logs verification requires an expected application log line');
  const target = new URL(logsURL);
  if (!/^\/apps\/[a-z0-9-]+\/logs$/.test(target.pathname)) {
    throw new Error(`invalid app logs URL: ${logsURL}`);
  }
  if (phase === 'live') await verifyLiveLogs(client, target, expectedLine);
  else if (phase === 'reconnected') await verifyReconnectedLogs(client, target, expectedLine);
  else if (phase === 'history') await verifyHistoricalLogs(client, target, expectedLine);
  else throw new Error(`unknown logs verification phase: ${phase}`);
}

let client;
try {
  const target = await pageTarget(debuggerURL.replace(/\/$/, ''));
  client = await CDPClient.connect(target.webSocketDebuggerUrl);
  await client.call('Page.enable');
  await client.call('Runtime.enable');
  if (action === 'approve') await approve(client, value);
  else if (action === 'revoke') await revoke(client, value);
  else await verifyLogs(client, value, username, password);
} catch (error) {
  console.error(`BROWSER ONBOARDING FAIL: ${error.stack || error.message}`);
  process.exitCode = 1;
} finally {
  client?.close();
}
