#!/usr/bin/env node

// Actual Shiny session, local stub agent, no provider requests. This catches
// context, graph-lock and flush errors that a fake browser protocol cannot.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createRequire } from 'node:module';
import { createServer } from 'node:net';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(new URL('../loadtest/render/driver/package.json', import.meta.url));
const { chromium } = require('playwright');
const root = fileURLToPath(new URL('..', import.meta.url));
const reservation = createServer();
reservation.listen(0, '127.0.0.1');
await once(reservation, 'listening');
const port = reservation.address().port;
await new Promise((done, reject) => reservation.close((error) => error ? reject(error) : done()));

const python = process.env.SHINYHUB_AGENT_TEST_PYTHON || 'python3';
const server = spawn(python, ['-m', 'shiny', 'run', '--host', '127.0.0.1', '--port', String(port),
  'packaging/python-agent/tests/fixtures/chat_app.py'], {
  cwd: root,
  env: { ...process.env, PYTHONPATH: resolve(root, 'packaging/python-agent/src'), PYTHONDONTWRITEBYTECODE: '1' },
  stdio: ['ignore', 'pipe', 'pipe'],
});
let output = '', startupError;
server.stdout.on('data', (data) => { output += data; });
server.stderr.on('data', (data) => { output += data; });
server.on('error', (error) => { startupError = error; });
const origin = `http://127.0.0.1:${port}`;
let browser;
try {
  const deadline = Date.now() + 15_000;
  while (true) {
    if (startupError || server.exitCode !== null) throw new Error(`Shiny fixture failed: ${startupError || output}`);
    try {
      const response = await fetch(origin, { signal: AbortSignal.timeout(1000),
        headers: { 'User-Agent': 'shinyhub-agent-tests' } });
      if (response.ok) break;
    } catch { /* server is starting */ }
    if (Date.now() >= deadline) throw new Error(`Shiny fixture did not start: ${output}`);
    await new Promise((done) => setTimeout(done, 100));
  }
  browser = await chromium.launch({ channel: process.env.SHINYHUB_E2E_BROWSER_CHANNEL || 'chrome', headless: true });
  const page = await browser.newPage();
  page.setDefaultTimeout(5000);
  const errors = [], outbound = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('request', (request) => { if (!request.url().startsWith(origin)) outbound.push(request.url()); });
  await page.goto(origin);
  await page.waitForFunction(() => window.shinyhubAgentChat?.open());
  const selected = page.locator('#selected'), period = page.locator('#period');
  await selected.filter({ hasText: 'Selected: week' }).waitFor();

  async function ask(message) {
    await page.locator('.sh-agent-input').fill(message);
    await page.locator('.sh-agent-send').click();
  }
  await ask('What period?');
  await page.locator('.sh-agent-assistant').filter({ hasText: 'Current period: week.' }).waitFor();
  await ask('Show year');
  await page.locator('.sh-agent-approval-description').filter({ hasText: 'Reporting period = year' }).waitFor();
  assert.equal(await period.inputValue(), 'week', 'preflight must not change inputs');
  assert.equal(await selected.textContent(), 'Selected: week');
  await page.locator('.sh-agent-apply').click();
  await page.locator('.sh-agent-assistant').filter({ hasText: 'Showing year.' }).waitFor();
  await page.waitForFunction(() => document.querySelector('#period').value === 'year');
  await selected.filter({ hasText: 'Selected: year' }).waitFor();
  await page.locator('.sh-agent-undo').click();
  await page.waitForFunction(() => document.querySelector('#period').value === 'week');
  await selected.filter({ hasText: 'Selected: week' }).waitFor();

  await period.selectOption('year');
  await selected.filter({ hasText: 'Selected: year' }).waitFor();
  const view = await page.evaluate(() => window.shinyhubAgentTools.invoke('get_period', {}));
  assert.equal(view.period, 'year', 'browser dispatch must not deadlock while acquiring the graph lock');

  await ask('wait');
  await page.getByRole('button', { name: 'Stop', exact: true }).waitFor({ state: 'visible' });
  await period.selectOption('week');
  await selected.filter({ hasText: 'Selected: week' }).waitFor();
  await page.getByRole('button', { name: 'Stop', exact: true }).click();
  await page.locator('.sh-agent-assistant').filter({ hasText: 'Stopped. The incomplete answer was discarded.' }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Stop', exact: true }).isVisible(), false);
  assert.deepEqual(errors, []);
  assert.deepEqual(outbound, [], 'the fixture must not contact an external model');
  assert.ok(!output.includes('Traceback'), output);
  console.log('PASS real Shiny: read, preflight, approval, update, flush, undo, browser dispatch, responsive model wait and cancellation');
} finally {
  if (browser) await browser.close();
  if (server.exitCode === null && !startupError) {
    const ended = once(server, 'exit');
    server.kill('SIGTERM');
    const force = setTimeout(() => server.kill('SIGKILL'), 3000);
    try { await ended; } finally { clearTimeout(force); }
  }
}
