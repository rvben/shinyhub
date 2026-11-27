#!/usr/bin/env node

// Real Shiny session with the installed agent wheel and a local stub model.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createRequire } from 'node:module';
import { createServer } from 'node:net';
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
const server = spawn(python, ['-I', '-B', '-m', 'shiny', 'run', '--host', '127.0.0.1', '--port', String(port),
  'packaging/python-agent/tests/fixtures/chat_app.py'], {
  cwd: root,
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
  assert.equal(view.period, 'year', 'browser dispatch reads the viewer state');

  await period.selectOption('week');
  await selected.filter({ hasText: 'Selected: week' }).waitFor();
  await ask('Show year');
  await page.locator('.sh-agent-apply').click();
  await page.locator('.sh-agent-undo').waitFor();
  await selected.filter({ hasText: 'Selected: year' }).waitFor();
  await period.selectOption('week');
  await page.locator('.sh-agent-undo').waitFor({ state: 'hidden' });

  // An agent-built view is an ordinary live output: it follows the page with no further chat turn.
  const generated = page.locator('#generated'), total = generated.locator('.sh-agent-view-value');
  assert.equal(await generated.locator('article').count(), 0, 'no view before the agent builds one');
  await ask('Build a dashboard');
  await page.locator('.sh-agent-assistant').filter({ hasText: 'View updated on the page.' }).waitFor();
  await generated.locator('h2').filter({ hasText: 'Sales overview' }).waitFor();
  await total.filter({ hasText: /^10$/ }).waitFor();
  await generated.locator('.sh-agent-view-plot svg').waitFor();
  // A tile the agent narrowed shows its scope next to the figure.
  const narrowed = generated.locator('article[data-tile="tile_3"]');
  assert.equal(await narrowed.locator('.sh-agent-view-scope').textContent(), 'Only Region: North');
  assert.equal(await narrowed.locator('.sh-agent-view-value').textContent(), '4');
  assert.equal(await generated.locator('.sh-agent-view-scope').count(), 1, 'unfiltered tiles carry no scope');
  // The chart follows its container, not only the window. The drawn width is read from the
  // SVG itself: CSS can squeeze a stale drawing into a narrower box.
  const drawn = () => generated.locator('.sh-agent-view-plot svg').first().evaluate((svg) => Number(svg.getAttribute('width')));
  const wide = await drawn();
  assert.ok(wide > 320, `the chart fills its container, got ${wide}px`);
  await generated.evaluate((el) => { el.style.width = '300px'; });
  await page.waitForFunction(() => {
    const svg = document.querySelector('#generated .sh-agent-view-plot svg');
    return svg && Number(svg.getAttribute('width')) <= 300;
  });
  await generated.evaluate((el) => { el.style.width = ''; });
  await page.waitForFunction((width) => {
    const svg = document.querySelector('#generated .sh-agent-view-plot svg');
    return svg && Math.abs(Number(svg.getAttribute('width')) - width) < 2;
  }, wide);
  // A chart drawn while its container is hidden (a closed tab) takes its width when revealed.
  await generated.evaluate((el) => { el.style.display = 'none'; });
  await period.selectOption('year');
  await total.filter({ hasText: /^520$/ }).waitFor({ state: 'attached' });
  await generated.locator('.sh-agent-view-plot svg').waitFor({ state: 'attached' });
  await generated.evaluate((el) => { el.style.display = ''; });
  await page.waitForFunction((width) => {
    const svg = document.querySelector('#generated .sh-agent-view-plot svg');
    return svg && Math.abs(Number(svg.getAttribute('width')) - width) < 2;
  }, wide);
  await period.selectOption('week');
  await total.filter({ hasText: /^10$/ }).waitFor();
  await generated.locator('.sh-agent-view-plot svg').waitFor();
  const answers = await page.locator('.sh-agent-assistant').count();
  await period.selectOption('year');
  await total.filter({ hasText: /^520$/ }).waitFor();
  await generated.locator('.sh-agent-view-plot svg').waitFor();
  assert.equal(await page.locator('.sh-agent-assistant').count(), answers, 'the tiles refreshed without a chat turn');
  // The open chat panel overlaps the right of the page; click the label itself.
  await generated.locator('summary').click({ position: { x: 12, y: 12 } });
  await generated.locator('tbody td').first().waitFor();
  assert.deepEqual(await generated.locator('tbody td').allTextContents(), ['North', '200', 'South', '320']);
  // A refresh redraws the tiles; the table the visitor opened stays open with the new rows.
  await period.selectOption('week');
  await total.filter({ hasText: /^10$/ }).waitFor();
  await generated.locator('tbody td').filter({ hasText: /^4$/ }).waitFor();
  assert.deepEqual(await generated.locator('tbody td').allTextContents(), ['North', '4', 'South', '6']);
  await page.evaluate(() => window.shinyhubAgentChat.close());
  await generated.getByRole('button', { name: /Dismiss/ }).click();
  await generated.locator('article').first().waitFor({ state: 'detached' });
  assert.equal(await generated.isHidden(), true, 'a dismissed view leaves no empty frame');
  await page.evaluate(() => window.shinyhubAgentChat.open());
  await period.selectOption('week');
  await selected.filter({ hasText: 'Selected: week' }).waitFor();

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
  assert.ok(!output.includes('ShinyDeprecationWarning'), output);
  console.log('PASS installed agent wheel: tool reads, approval, writes, undo, stale undo withdrawal, live generated view and cancellation');
} finally {
  if (browser) await browser.close();
  if (server.exitCode === null && !startupError) {
    const ended = once(server, 'exit');
    server.kill('SIGTERM');
    const force = setTimeout(() => server.kill('SIGKILL'), 3000);
    try { await ended; } finally { clearTimeout(force); }
  }
}
