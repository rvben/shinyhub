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
  browser-onboarding-cdp.mjs logs    <debugger-url> <logs-url> <expected-log-line> [unused] [screenshot]`);
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

async function verifyLogs(client, logsURL, expectedLine) {
  if (!expectedLine) throw new Error('logs verification requires an expected application log line');
  const target = new URL(logsURL);
  if (!/^\/apps\/[a-z0-9-]+\/logs$/.test(target.pathname)) {
    throw new Error(`invalid app logs URL: ${logsURL}`);
  }

  await client.call('Page.navigate', {url: target.href});
  await waitFor(client, `document.body.dataset.auth === 'in'`, 'the authenticated dashboard session');
  await waitFor(client, visibleExpression('#detail-logs-panel'), 'the app-detail logs panel');
  await waitFor(
    client,
    `document.querySelector('#detail-tab-logs')?.getAttribute('aria-selected') === 'true'`,
    'the Logs tab to become active',
  );
  await waitFor(
    client,
    `document.querySelector('.logs-status-text')?.textContent.trim() === 'Live · 1 connected source'`,
    'one live application log stream',
    30_000,
  );
  await waitFor(
    client,
    `Array.from(document.querySelectorAll('.log-entry-message')).some(element => element.textContent === ${JSON.stringify(expectedLine)})`,
    'the deterministic application startup line',
    30_000,
  );

  const details = await evaluate(client, `(() => {
    const matchingLine = Array.from(document.querySelectorAll('.log-entry-message'))
      .find(element => element.textContent === ${JSON.stringify(expectedLine)});
    return {
      pathname: location.pathname,
      activeTab: document.querySelector('#detail-tab-logs')?.getAttribute('aria-selected'),
      status: document.querySelector('.logs-status-text')?.textContent.trim(),
      sourceOptions: Array.from(document.querySelectorAll('#logs-source option')).map(option => option.textContent.trim()),
      lineSource: matchingLine?.closest('.log-entry')?.querySelector('.log-entry-source')?.textContent.trim(),
      outputLabel: document.querySelector('#detail-logs-body')?.getAttribute('aria-label'),
      pauseDisabled: document.querySelector('#logs-pause')?.disabled,
    };
  })()`);
  if (details.pathname !== target.pathname || details.activeTab !== 'true' ||
      details.status !== 'Live · 1 connected source' ||
      details.sourceOptions[0] !== 'All current replicas (1 live, 1 total)' ||
      !details.sourceOptions.some(option => option.startsWith('Replica #0 — Running')) ||
      details.lineSource !== '#0' || details.outputLabel !== 'Application log output' ||
      details.pauseDisabled !== false) {
    throw new Error(`logs workspace contract was incomplete: ${JSON.stringify(details)}`);
  }
  await screenshot(client, screenshotPath);
  console.log('LOGS PASS');
}

let client;
try {
  const target = await pageTarget(debuggerURL.replace(/\/$/, ''));
  client = await CDPClient.connect(target.webSocketDebuggerUrl);
  await client.call('Page.enable');
  await client.call('Runtime.enable');
  if (action === 'approve') await approve(client, value);
  else if (action === 'revoke') await revoke(client, value);
  else await verifyLogs(client, value, username);
} catch (error) {
  console.error(`BROWSER ONBOARDING FAIL: ${error.stack || error.message}`);
  process.exitCode = 1;
} finally {
  client?.close();
}
