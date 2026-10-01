#!/usr/bin/env node

// Production dashboard assets with a simulated authentication gateway. A wake
// check receives 401; reconnect must request a new document, not an SPA route.
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';

const driverRequire = createRequire(new URL('../loadtest/render/driver/package.json', import.meta.url));
const { chromium } = driverRequire('playwright');
const assets = new URL('../internal/ui/static/', import.meta.url);
const html = (await readFile(new URL('index.html', assets), 'utf8'))
  .replace(/<script id="shinyhub-announcements-loader"[^>]*><\/script>/, '');
let signedIn = true;
let providers = {};
let documents = 0;
const server = createServer(async (request, response) => {
  const path = new URL(request.url, 'http://localhost').pathname;
  if (path.startsWith('/static/')) {
    const file = new URL(path.slice('/static/'.length), assets);
    if (!file.href.startsWith(assets.href)) { response.writeHead(404).end(); return; }
    try {
      const content = await readFile(file);
      const ext = path.split('.').at(-1);
      response.setHeader('content-type', ({ js: 'text/javascript', css: 'text/css', svg: 'image/svg+xml', woff2: 'font/woff2' })[ext] || 'application/octet-stream');
      response.end(content);
    } catch { response.writeHead(404).end(); }
    return;
  }
  if (path.startsWith('/api/')) {
    response.setHeader('content-type', 'application/json');
    if (path === '/api/auth/providers') { response.end(JSON.stringify(providers)); return; }
    if (!signedIn) { response.writeHead(401).end('{"error":"Session ended"}'); return; }
    response.end(JSON.stringify(path === '/api/auth/me'
      ? { user: { id: 1, username: 'fixture', role: 'viewer', can_manage_self: false } }
      : path === '/api/server/info' ? { version: 'fixture' } : []));
    return;
  }
  // The gateway accepts an already renewed session on the next navigation.
  signedIn = true;
  documents++;
  response.setHeader('content-type', 'text/html; charset=utf-8');
  response.end(html);
});
server.listen(0, '127.0.0.1');
await once(server, 'listening');
const origin = `http://127.0.0.1:${server.address().port}`;
const browser = await chromium.launch({ channel: process.env.SHINYHUB_E2E_BROWSER_CHANNEL || undefined, headless: true });
try {
  for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
    for (const legacy of [false, true]) {
      providers = { local: false, github: false, google: false, oidc: { enabled: false }, ...(!legacy && { forward_auth: true }) };
      const tab = await browser.newPage({ viewport, reducedMotion: 'reduce' });
      const errors = [];
      tab.on('pageerror', error => errors.push(error.message));
      await tab.goto(`${origin}/apps?view=all#catalog`);
      await tab.waitForFunction(() => document.body.dataset.auth === 'in');
      await tab.waitForFunction(() => !document.querySelector('.login-recovery').hidden);
      signedIn = false;
      await tab.evaluate(() => window.dispatchEvent(new Event('pageshow')));
      await tab.waitForFunction(() => document.body.dataset.auth === 'out');
      const reconnect = tab.getByRole('button', { name: 'Reconnect to dashboard' });
      assert.equal(await reconnect.isVisible(), true);
      assert.equal(await tab.locator('#login-form').isVisible(), false);
      assert.match(await tab.locator('#login-error').innerText(), /session ended/i);
      assert.equal(await tab.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
      await reconnect.focus();
      assert.equal(await reconnect.evaluate(element => element === document.activeElement), true);
      await tab.screenshot({ path: `/tmp/shinyhub-dashboard-recovery-${viewport.width}-${legacy ? 'legacy' : 'gateway'}.png` });
      const before = documents;
      await Promise.all([tab.waitForEvent('domcontentloaded'), reconnect.press('Enter')]);
      await tab.waitForFunction(() => document.body.dataset.auth === 'in');
      assert.equal(documents, before + 1, 'recovery must navigate through the gateway');
      assert.equal(tab.url(), `${origin}/apps?view=all#catalog`);
      assert.deepEqual(errors, []);
      await tab.close();
      console.log(`passed: ${viewport.width}px ${legacy ? 'legacy providers' : 'forward auth'} wake/reconnect`);
    }
  }
} finally {
  await browser.close();
  server.close();
}
