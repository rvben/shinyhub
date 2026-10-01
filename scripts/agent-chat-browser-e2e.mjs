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

async function sizing(tab, native, action) {
  return tab.evaluate(({ native, action }) => {
    const root = native ? navRoot : document;
    const surface = root.querySelector(native ? '.chat-panel' : '.sh-agent-panel');
    const button = root.querySelector(native ? '.chat-panel-expand' : '.sh-agent-expand');
    const grip = root.querySelector(native ? '.chat-resize' : '.sh-agent-resize');
    if (action === 'expand') button.click();
    if (action === 'focus') grip.focus();
    const rect = surface.getBoundingClientRect(), edge = grip.getBoundingClientRect();
    return { width: rect.width, right: rect.right, left: rect.left,
      hidden: grip.hidden && button.hidden, pressed: button.getAttribute('aria-pressed'),
      x: edge.left + edge.width / 2, y: edge.top + 60,
      saved: JSON.parse(sessionStorage.getItem('shinyhub:chat:size')) };
  }, { native, action });
}

try {
  browser = await chromium.launch({ channel: process.env.SHINYHUB_E2E_BROWSER_CHANNEL || 'chrome', headless: true });
  const context = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] });
  context.setDefaultTimeout(5000);
  for (const native of [false, true]) {
    for (const width of [1280, 800, 375]) {
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
      const small = '| Business line | Cost (USD) | Share |\n| --- | ---: | ---: |\n| North American customer support and business services | $442.91K | 65% |\n';
      await delta(tab, small); await done(tab);
      assert.ok(await tab.locator('.sh-agent-table-scroll').evaluate((w) => w.scrollWidth - w.clientWidth <= 1), 'ordinary tables should fit by wrapping descriptions');
      assert.equal(await tab.locator('td').first().evaluate((cell) => getComputedStyle(cell).whiteSpace), 'normal');
      assert.equal(await tab.locator('td').nth(1).evaluate((cell) => getComputedStyle(cell).whiteSpace), 'nowrap');
      const desktop = width > (native ? 949 : 680);
      const initial = await sizing(tab, native);
      assert.equal(initial.hidden, !desktop);
      if (desktop) {
        assert.equal(initial.width, 440);
        const roomy = await sizing(tab, native, 'expand');
        assert.equal(roomy.width, Math.min(960, width - (native ? 24 : 48)));
        assert.equal(roomy.pressed, 'true');
        assert.ok(roomy.left >= 12 && roomy.right <= width - 12);
        assert.equal((await sizing(tab, native, 'expand')).width, 440, 'restore returns to the compact width');
        // Genuine pointer capture: moving outside the grip still resizes.
        await tab.mouse.move(initial.x, initial.y); await tab.mouse.down();
        await tab.mouse.move(initial.x - 180, initial.y, { steps: 8 }); await tab.mouse.up();
        const dragged = await sizing(tab, native);
        assert.equal(dragged.width, 620); assert.equal(dragged.right, initial.right);
        assert.equal(dragged.saved.width, 620);
        await sizing(tab, native, 'focus'); await tab.keyboard.press('Shift+ArrowLeft');
        assert.equal((await sizing(tab, native)).width, 684);
        await tab.keyboard.press('Home'); assert.equal((await sizing(tab, native)).width, 360);
        await tab.keyboard.press('End');
        assert.equal((await sizing(tab, native)).width, Math.min(960, width - (native ? 24 : 48)));
        await tab.keyboard.press('Home');
        const edge = await sizing(tab, native);
        await tab.mouse.move(edge.x, edge.y); await tab.mouse.down();
        await tab.mouse.move(edge.x - 100, edge.y); await tab.keyboard.press('Escape'); await tab.mouse.up();
        assert.equal((await sizing(tab, native)).width, 360, 'Escape cancels a drag without closing the panel');
        assert.equal(await tab.locator('.sh-agent-panel').isVisible(), true);
        await sizing(tab, native, 'expand'); await sizing(tab, native, 'expand');
        assert.equal((await sizing(tab, native)).width, 360);
        // Restore the original width for the streaming checks.
        for (let i = 0; i < 5; i++) await tab.keyboard.press('ArrowLeft');
        assert.equal((await sizing(tab, native)).width, 440);
      } else assert.equal(initial.width, width, 'small screens keep the full-screen layout');
      if (screenshots) await tab.screenshot({ path: resolve(screenshots, `${native ? 'native' : 'fallback'}-${width}-compact.png`) });
      await tab.evaluate(() => chatEvent({ type: 'reset' }));
      await start(tab);
      const intro = '## Weekly comparison\n\n**Total cost:** $442.91K, up *2.1%*.\n\n- North leads the ranking.\n- South follows.\n\n| Business line | Cost (USD) | Share | Prior cost | Forecast | Budget | Variance | Annual cost |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n';
      const row = (i) => `| Business line ${i} with a descriptive name | $${(442910 / (i + 1)).toFixed(2)} | ${(65 / (i + 1)).toFixed(1)}% | $433,970.00 | $450,000.00 | $500,000.00 | -$50,000.00 | $5,200,000.00 |\n`;
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
      assert.ok(await tab.locator('td').first().evaluate((cell) => {
        const style = getComputedStyle(cell);
        return cell.clientHeight <= 4 * parseFloat(style.lineHeight) + parseFloat(style.paddingTop) + parseFloat(style.paddingBottom) + 1;
      }), 'a short description should not be squeezed into a tall stack of single words');
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
      if (desktop) {
        const beforeOverflow = await tab.locator('.sh-agent-table-scroll').evaluate((w) => w.scrollWidth - w.clientWidth);
        await sizing(tab, native, 'expand');
        const afterOverflow = await tab.locator('.sh-agent-table-scroll').evaluate((w) => w.scrollWidth - w.clientWidth);
        assert.ok(afterOverflow < beforeOverflow, 'expanding makes more table columns visible');
        assert.equal(await tab.evaluate(() => document.querySelector('.sh-agent-table-scroll') === tableWrapper), true);
        assert.ok(await tab.locator('.sh-agent-log').evaluate((log) => log.scrollHeight - log.scrollTop - log.clientHeight < 2), 'resizing should preserve follow-scroll');
        await sizing(tab, native, 'expand');
      }
      await tab.locator('.sh-agent-log').evaluate((log) => { log.scrollTop = 0; }); await frame(tab);
      const top = await tab.locator('.sh-agent-log').evaluate((log) => log.scrollTop);
      source += row(25); await delta(tab, row(25));
      assert.equal(await tab.locator('.sh-agent-log').evaluate((log) => log.scrollTop), top, 'stream should respect reading earlier content');
      if (desktop) {
        await sizing(tab, native, 'expand');
        assert.ok(await tab.locator('.sh-agent-log').evaluate((log) => log.scrollTop < log.scrollHeight - log.clientHeight - 90), 'expanding should not jump a reader to the bottom');
        await sizing(tab, native, 'expand');
      }
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
      // Scope to the conversation: native modal chrome makes the dashboard's
      // main/heading inert, so whole-document landmark checks are inapplicable.
      const accessibility = await tab.evaluate(() => axe.run(document.querySelector('.sh-agent-panel'), { rules: { 'region': { enabled: false } } }));
      assert.deepEqual(accessibility.violations.map((v) => `${v.id}: ${v.description}`), []);
      if (native || !desktop) {
        await tab.locator('.sh-agent-input').fill('Next question');
        await tab.evaluate((native) => {
          const first = native ? navRoot.querySelector('.chat-panel-action[aria-label="Start a new conversation"]') :
            document.querySelector('.sh-agent-header button[aria-label="Close assistant"]');
          first.focus();
        }, native);
        await tab.keyboard.press('Shift+Tab');
        assert.equal(await tab.evaluate(() => document.activeElement === document.querySelector('.sh-agent-send')), true, 'focus trap wraps through all conversation controls');
        await tab.keyboard.press('Tab');
        assert.equal(await tab.evaluate((native) => native ?
          navRoot.activeElement === navRoot.querySelector('.chat-panel-action[aria-label="Start a new conversation"]') :
          document.activeElement === document.querySelector('.sh-agent-header button[aria-label="Close assistant"]'), native), true);
        await tab.locator('.sh-agent-input').fill('');
      }
      if (screenshots) {
        // Keep the comparison and first table rows visible for the visual pass.
        await tab.locator('.sh-agent-log').evaluate((log) => { log.scrollTop = 0; });
        await tab.locator('.sh-agent-table-scroll').evaluate((wrapper) => { wrapper.scrollLeft = 0; });
        await tab.screenshot({ path: resolve(screenshots, `${native ? 'native' : 'fallback'}-${width}.png`) });
        if (desktop) {
          await sizing(tab, native, 'expand');
          await tab.screenshot({ path: resolve(screenshots, `${native ? 'native' : 'fallback'}-${width}-expanded.png`) });
          await sizing(tab, native, 'expand');
        }
      }
      if (desktop) {
        await sizing(tab, native, 'focus'); await tab.keyboard.press('End');
        const wanted = (await sizing(tab, native)).saved.width;
        const narrowWidth = native ? 950 : 700;
        await tab.setViewportSize({ width: narrowWidth, height: 900 }); await frame(tab);
        let size = await sizing(tab, native);
        assert.equal(size.width, Math.min(wanted, narrowWidth - (native ? 24 : 48)), 'width is clamped to the viewport');
        assert.ok(size.left >= 12 && size.right <= narrowWidth - 12);
        await tab.setViewportSize({ width: 375, height: 700 }); await frame(tab);
        size = await sizing(tab, native);
        assert.equal(size.width, 375); assert.equal(size.hidden, true);
        assert.equal(await tab.evaluate(() => document.activeElement === document.querySelector('.sh-agent-input')), true, 'hiding desktop controls keeps keyboard focus in the conversation');
        await tab.setViewportSize({ width, height: 900 }); await frame(tab);
        assert.equal((await sizing(tab, native)).width, wanted, 'returning to desktop restores the preferred width');
        await sizing(tab, native, 'expand');
        await tab.reload();
        if (native) await tab.evaluate(() => navRoot.querySelector('.chat-trigger').click());
        else await tab.locator('.sh-agent-launcher').click();
        assert.equal((await sizing(tab, native)).pressed, 'true', 'expanded state survives reload in this tab');
        await sizing(tab, native, 'expand');
        assert.equal((await sizing(tab, native)).width, wanted, 'compact preference survives reload too');
      }
      assert.deepEqual(errors, []); assert.deepEqual(outbound, []);
      console.log(`PASS ${native ? 'native toolbar' : 'fallback'} at ${width}px: layout, streaming, keyboard, clipboard, injection, accessibility`);
      await tab.close();
    }
  }
} finally {
  await browser?.close(); server.close(); await once(server, 'close');
}
