#!/usr/bin/env node

// Serve the actual agent assets and actual toolbar. jsdom cannot verify native
// slot layout, horizontal scrolling, keyboard focus, or streamed layout shifts.
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { readFile, mkdir } from 'node:fs/promises';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';

const driverRequire = createRequire(new URL('../loadtest/render/driver/package.json', import.meta.url));
const { chromium } = driverRequire('playwright');
const rootRequire = createRequire(new URL('../package.json', import.meta.url));
const axe = rootRequire('axe-core');
const assets = new Map(await Promise.all([
  ['/chat.js', '../packaging/python-agent/src/shinyhub_agent/www/chat.js'],
  ['/chat.css', '../packaging/python-agent/src/shinyhub_agent/www/chat.css'],
  ['/nav.js', '../internal/appnav/assets/nav.js'],
].map(async ([route, path]) => [route, await readFile(new URL(path, import.meta.url))])));

function fixture(native) {
  return `<!doctype html><html lang="en"><head><meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1"><title>Agent chat</title>
  <style>body{margin:0} main{padding:24px} table{width:1800px;margin:50px;font-size:30px} th,td{color:red;text-align:center;padding:40px} pre{white-space:normal}</style>
  <link rel="stylesheet" href="/chat.css"></head><body><main><h1>Dashboard</h1><button>App action</button></main>
  <script>
    const attach = Element.prototype.attachShadow;
    Element.prototype.attachShadow = function(options) { const root=attach.call(this,options); window.navRoot=root; return root; };
    window.handlers = new Map(); window.request = null;
    window.Shiny = { addCustomMessageHandler(name,fn){ handlers.set(name,fn); },
      setInputValue(name,value){ if(value.message) window.request=value; } };
    window.chatEvent = function(event) { handlers.get('shinyhub-agent-chat-event')({version:1,session:'browser-session',requestId:request.requestId,...event}); };
  </script>
  ${native ? '<script id="shinyhub-app-nav" src="/nav.js" data-nav-url="/nav.json" data-current-slug="demo" data-current-name="Demo" data-home-url="/"></script>' : ''}
  <script src="/chat.js"></script>
  <script>handlers.get('shinyhub-agent-chat-capabilities')({version:1,enabled:true,session:'browser-session'});</script>
  </body></html>`;
}

const server = createServer((req, res) => {
  if (assets.has(req.url)) {
    res.setHeader('Content-Type', req.url.endsWith('.css') ? 'text/css' : 'text/javascript');
    res.end(assets.get(req.url));
  } else if (req.url === '/nav.json') {
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ apps: [{ slug: 'demo', name: 'Demo', url: '/app/demo/' }] }));
  } else {
    res.setHeader('Content-Type', 'text/html'); res.end(fixture(req.url.startsWith('/native')));
  }
});
server.listen(0, '127.0.0.1'); await once(server, 'listening');
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
const screenshots = process.env.SHINYHUB_CHAT_QA_DIR;
if (screenshots) await mkdir(screenshots, { recursive: true });

async function frame(tab) {
  await tab.evaluate(() => Promise.race([
    new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))),
    new Promise((_, reject) => setTimeout(() => reject(new Error('Browser render frame stalled')), 5000)),
  ]));
}
async function delta(tab, text) {
  await tab.evaluate((text) => chatEvent({ type: 'delta', text }), text); await frame(tab);
}
async function done(tab) { await tab.evaluate(() => chatEvent({ type: 'done' })); }
async function start(tab) {
  await tab.locator('.sh-agent-input').fill('Compare the business lines');
  await tab.locator('.sh-agent-send').click();
}

try {
  browser = await chromium.launch({ channel: process.env.SHINYHUB_E2E_BROWSER_CHANNEL || 'chrome', headless: true });
  const context = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] });
  context.setDefaultTimeout(5000);
  for (const native of [false, true]) {
    for (const width of [1280, 375]) {
      console.log(`Checking ${native ? 'native toolbar' : 'fallback'} at ${width}px`);
      const tab = await context.newPage();
      const errors = [], outbound = [];
      tab.on('pageerror', (error) => errors.push(error.message));
      tab.on('request', (req) => { if (!req.url().startsWith(origin)) outbound.push(req.url()); });
      await tab.setViewportSize({ width, height: 900 });
      await tab.goto(`${origin}/${native ? 'native' : 'fallback'}`);
      if (native) {
        await tab.evaluate(() => navRoot.querySelector('.chat-trigger').click());
        assert.equal(await tab.locator('body').evaluate((body) => body.classList.contains('sh-agent-native-chat')), true);
        assert.equal(await tab.locator('.sh-agent-panel').evaluate((panel) => panel.slot), 'shinyhub-chat-content');
      } else await tab.locator('.sh-agent-launcher').click();
      await start(tab);
      const intro = '## Weekly comparison\n\n**Total cost:** $442.91K, up *2.1%*.\n\n- North leads the ranking.\n- South follows.\n\n| Business line | Cost (USD) | Share |\n| --- | ---: | ---: |\n';
      const row = (i) => `| Business line ${i} with a descriptive name | $${(442910 / (i + 1)).toFixed(2)} | ${(65 / (i + 1)).toFixed(1)}% |\n`;
      await delta(tab, intro + row(0));
      assert.equal(await tab.locator('tbody tr').count(), 1);
      assert.equal(await tab.locator('.sh-agent-message-body strong').textContent(), 'Total cost:');
      assert.equal(await tab.locator('.sh-agent-assistant li').count(), 2);
      const geometry = await tab.locator('.sh-agent-table-scroll').evaluate((wrapper) => {
        window.tableWrapper = wrapper; window.firstRow = wrapper.querySelector('tbody tr');
        wrapper.scrollLeft = 0; wrapper.focus();
        const log = document.querySelector('.sh-agent-log'), panel = document.querySelector('.sh-agent-panel');
        return { horizontal: wrapper.scrollWidth > wrapper.clientWidth,
          logOverflow: log.scrollWidth - log.clientWidth, panelOverflow: panel.scrollWidth - panel.clientWidth,
          documentOverflow: document.documentElement.scrollWidth - innerWidth,
          aligned: getComputedStyle(wrapper.querySelector('td:nth-child(2)')).textAlign,
          headerColor: getComputedStyle(wrapper.querySelector('th')).color };
      });
      assert.equal(geometry.horizontal, true);
      assert.ok(geometry.logOverflow <= 1 && geometry.panelOverflow <= 1 && geometry.documentOverflow <= 1, JSON.stringify(geometry));
      assert.equal(geometry.aligned, 'right');
      assert.equal(geometry.headerColor, 'rgb(22, 32, 58)', 'host table rules must not override the panel');
      await tab.keyboard.press('ArrowRight');
      await tab.waitForFunction(() => tableWrapper.scrollLeft > 0);
      await tab.waitForFunction(() => {
        const current = tableWrapper.scrollLeft;
        window.horizontalStable = current === window.horizontalPrevious ? (window.horizontalStable || 0) + 1 : 0;
        window.horizontalPrevious = current;
        return window.horizontalStable >= 4;
      });
      const horizontal = await tab.evaluate(() => tableWrapper.scrollLeft);
      await delta(tab, row(1));
      assert.equal(await tab.evaluate(() => document.querySelector('.sh-agent-table-scroll') === tableWrapper && tableWrapper.querySelector('tbody tr') === firstRow), true);
      assert.ok(Math.abs(await tab.evaluate(() => tableWrapper.scrollLeft) - horizontal) <= 1);
      assert.equal(await tab.evaluate(() => document.activeElement === tableWrapper), true);
      // A visible first row does not move as rows are added; no table-wide reflow.
      const firstRowY = await tab.evaluate(() => firstRow.getBoundingClientRect().top + document.querySelector('.sh-agent-log').scrollTop);
      await delta(tab, row(2));
      assert.ok(Math.abs(await tab.evaluate(() => firstRow.getBoundingClientRect().top + document.querySelector('.sh-agent-log').scrollTop) - firstRowY) <= 1);
      let source = intro + row(0) + row(1) + row(2);
      for (let i = 3; i < 25; i++) { source += row(i); await delta(tab, row(i)); }
      assert.ok(await tab.locator('.sh-agent-log').evaluate((log) => log.scrollHeight - log.scrollTop - log.clientHeight < 2), 'stream should follow the bottom');
      await tab.locator('.sh-agent-log').evaluate((log) => { log.scrollTop = 0; }); await frame(tab);
      const top = await tab.locator('.sh-agent-log').evaluate((log) => log.scrollTop);
      source += row(25); await delta(tab, row(25));
      assert.equal(await tab.locator('.sh-agent-log').evaluate((log) => log.scrollTop), top, 'stream should respect reading earlier content');
      await tab.locator('.sh-agent-jump').click();
      const end = '\n```python\n' + 'long_code_line = '.repeat(30) + '\n```\n\n<img src=x onerror="window.pwned=true"> <script>window.pwned=true</script> [x](javascript:alert(1)) ![x](https://example.test/leak)';
      source += end; await delta(tab, end); await done(tab);
      assert.ok(await tab.locator('.sh-agent-log').evaluate((log) => log.scrollHeight - log.scrollTop - log.clientHeight < 2), 'completion should keep the copy action visible');
      assert.equal(await tab.locator('.sh-agent-assistant img, .sh-agent-assistant script, .sh-agent-assistant a').count(), 0);
      assert.equal(await tab.evaluate(() => window.pwned), undefined);
      assert.equal(await tab.locator('.sh-agent-log').evaluate((log) => log.scrollWidth - log.clientWidth <= 1), true, 'code must not widen the log');
      await tab.locator('.sh-agent-copy').click();
      assert.equal(await tab.evaluate(() => navigator.clipboard.readText()), source);
      await tab.addScriptTag({ content: axe.source });
      const accessibility = await tab.evaluate(() => axe.run(document.querySelector('.sh-agent-panel'), { rules: { 'region': { enabled: false } } }));
      assert.deepEqual(accessibility.violations.map((v) => `${v.id}: ${v.description}`), []);
      if (screenshots) {
        // Keep the comparison and first table rows visible for the visual pass.
        await tab.locator('.sh-agent-log').evaluate((log) => { log.scrollTop = 0; });
        await tab.locator('.sh-agent-table-scroll').evaluate((wrapper) => { wrapper.scrollLeft = 0; });
        await tab.screenshot({ path: resolve(screenshots, `${native ? 'native' : 'fallback'}-${width}.png`) });
      }
      assert.deepEqual(errors, []); assert.deepEqual(outbound, []);
      console.log(`PASS ${native ? 'native toolbar' : 'fallback'} at ${width}px: layout, streaming, keyboard, clipboard, injection, accessibility`);
      await tab.close();
    }
  }
} finally {
  await browser?.close(); server.close(); await once(server, 'close');
}
