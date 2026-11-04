#!/usr/bin/env node

// Real-browser contract for the injected app switcher. jsdom runs no CSS, so
// it cannot see that a panel fading in from `visibility: hidden` refuses focus
// at the moment it opens. This serves the production nav.js to system Chrome
// and asserts that opening each dialog moves keyboard focus inside it and that
// Escape hands focus back to the control that opened it.

import assert from 'node:assert/strict';
import { once } from 'node:events';
import { readFile, mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';

const driverRequire = createRequire(new URL('../loadtest/render/driver/package.json', import.meta.url));
const { chromium } = driverRequire('playwright');

const navSource = await readFile(new URL('../internal/appnav/assets/nav.js', import.meta.url));
if (!navSource.includes('shinyhub-app-nav')) {
  throw new Error('nav.js does not look like the app switcher script');
}

const artifactDir = await mkdtemp(join(tmpdir(), 'shinyhub-app-nav-'));
const bridgeSource = await readFile(new URL('../packaging/python-bookmarks/src/shinyhub_bookmarks/www/bridge.js', import.meta.url));
const overlaySource = await readFile(new URL('../internal/proxy/assets/overlay.js', import.meta.url));
let holdReadiness = false;
let releaseReadiness;
let switched = false;
let switchRequests = 0;
const hiddenPage = () => `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Dashboard</title></head>
<body style="font-family: sans-serif; padding: 24px"><h1>Regional dashboard</h1><label>Region <select id="region"><option>Europe</option><option>Asia</option></select></label>
<script>
 const attach = Element.prototype.attachShadow;
 Element.prototype.attachShadow = function(options) { const root = attach.call(this, options); window.__navShadow = root; return root; };
 const filter = document.getElementById('region');
 filter.value = new URLSearchParams(location.search).get('region') || 'Europe';
 const handlers = {};
 let revision = 0;
 let observedBrowserRevision = 0;
 const capabilities = () => handlers['shinyhub-bookmark-capabilities']?.({version: 1, store: 'url', session: 'fixture-session', pendingSync: true, syncReady: true,
   autoSync: revision > 0, syncRevision: revision, registeredInputs: ['region'], observedBrowserRevision, syncFields: ['region'], fields: [{id: 'region', label: 'Region', value: filter.value}]});
 window.Shiny = {
  addCustomMessageHandler(name, handler) { handlers[name] = handler; },
  setInputValue(name, value) {
   if (name === '.shinyhub_bookmark_discover') capabilities();
   if (name === '.shinyhub_bookmark_browser_revision') { observedBrowserRevision = value; queueMicrotask(capabilities); }
   if (name === '.shinyhub_bookmark_request') {
    const captured = filter.value;
    setTimeout(() => handlers['shinyhub-bookmark-result']({version: 1, requestId: value.requestId, purpose: value.purpose,
      url: '/app/demo/?_inputs_&region=' + encodeURIComponent(captured)}), 20);
   }
  }
 };
 filter.addEventListener('change', () => {
  document.dispatchEvent(new CustomEvent('shiny:inputchanged', {detail: {name: 'region', value: filter.value}}));
  revision += 1; capabilities();
 });
</script>
<script src="/bridge.js"></script>
<script id="shinyhub-app-nav" src="/nav.js" data-nav-url="/app/demo/.shinyhub/nav.json" data-current-slug="demo" data-current-name="Regional dashboard" data-home-url="/"
 data-served-generation="${switched ? 'new' : 'old'}" data-version-switch="when_hidden" data-version-url="/app/demo/.shinyhub/version.json" data-switch-url="/app/demo/.shinyhub/version/switch"></script>
</body></html>`;

// The switcher asks for a closed shadow root. The page keeps a reference to
// the root it is given so the test can read focus inside it; the script still
// receives the closed root it asked for.
const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>App</title></head>
<body><h1>App</h1>
<script>
  const attach = Element.prototype.attachShadow;
  Element.prototype.attachShadow = function (options) {
    const shadow = attach.call(this, options);
    window.__navShadow = shadow;
    return shadow;
  };
</script>
<script id="shinyhub-app-nav" src="/nav.js"
  data-nav-url="/app/demo/.shinyhub/nav.json"
  data-current-slug="demo" data-current-name="Demo" data-home-url="/"></script>
</body></html>`;

const apps = {
  apps: [
    { slug: 'demo', name: 'Demo', url: '/app/demo/' },
    { slug: 'sales', name: 'Sales', url: '/app/sales/' },
  ],
};

const server = createServer((request, response) => {
  if (request.url === '/overlay.js') {
    response.setHeader('content-type', 'text/javascript; charset=utf-8'); response.end(overlaySource); return;
  }
  if (request.url.endsWith('/.shinyhub/ready')) {
    const answer = () => {
      response.setHeader('content-type', 'application/json'); response.end('{"ready":true}');
    };
    if (holdReadiness) releaseReadiness = answer;
    else answer();
    return;
  }
  if (request.url === '/bridge.js') {
    response.setHeader('content-type', 'text/javascript; charset=utf-8'); response.end(bridgeSource); return;
  }
  if (request.url.endsWith('/version.json')) {
    response.setHeader('content-type', 'application/json'); response.end(JSON.stringify({ active_generation: 'new' })); return;
  }
  if (request.url.endsWith('/version/switch')) {
    assert.equal(request.headers['x-shinyhub-version-switch'], '1');
    if (request.method === 'DELETE') { response.writeHead(204); response.end(); return; }
    assert.equal(request.method, 'POST');
    assert.equal(request.headers['x-shinyhub-automatic-switch'], '1');
    switched = true; switchRequests += 1; response.writeHead(204); response.end(); return;
  }
  if (request.url === '/nav.js') {
    response.setHeader('content-type', 'text/javascript; charset=utf-8');
    response.end(navSource);
    return;
  }
  if (request.url.endsWith('/nav.json')) {
    response.setHeader('content-type', 'application/json');
    response.end(JSON.stringify(apps));
    return;
  }
  response.setHeader('content-type', 'text/html; charset=utf-8');
  if (request.url.includes('recovery=1')) {
    response.end(hiddenPage()
      .replace('<script src="/bridge.js">', '<script src="/overlay.js" data-ready-url="/app/demo/.shinyhub/ready"></script><script src="/bridge.js">')
      .replace(' data-version-url="/app/demo/.shinyhub/version.json"', '')
      .replace('</body>', '<script>document.dispatchEvent(new Event("shiny:connected"));</script></body>'));
    return;
  }
  response.end(request.url.startsWith("/app/demo/?") ? hiddenPage() : page);
});
server.listen(0, '127.0.0.1');
await once(server, 'listening');
const origin = `http://127.0.0.1:${server.address().port}`;

const channel = process.env.SHINYHUB_E2E_BROWSER_CHANNEL || undefined;
const browser = await chromium.launch({ channel, headless: true });

async function activeClass(tab) {
  return tab.evaluate(() => {
    const active = window.__navShadow.activeElement;
    return active ? active.className : '';
  });
}

async function openFromKeyboard(tab, triggerSelector) {
  await tab.evaluate((selector) => window.__navShadow.querySelector(selector).focus(), triggerSelector);
  await tab.keyboard.press('Enter');
}

async function check(name, fn) {
  console.log(`==> ${name}`);
  await fn();
}

try {
  const tab = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  await tab.goto(`${origin}/app/demo/`);
  await tab.waitForFunction(() => window.__navShadow && window.__navShadow.querySelector('.bar'));

  await check('app list panel takes focus when opened', async () => {
    await openFromKeyboard(tab, 'button.switch');
    // Until the list arrives the panel holds focus itself, so focus must be
    // inside it straight away and then settle on the first app.
    const early = await activeClass(tab);
    assert.equal(
      await tab.evaluate(() => window.__navShadow.querySelector('.panel').contains(window.__navShadow.activeElement)),
      true,
      `focus is outside the app list panel (on "${early}")`,
    );
    await tab.waitForFunction(() => /\bitem\b/.test(window.__navShadow.activeElement?.className || ''));
    await tab.keyboard.press('Escape');
    assert.match(await activeClass(tab), /\bswitch\b/);
  });

  await check('view-link dialog takes focus when opened', async () => {
    await tab.evaluate(() => window.dispatchEvent(new CustomEvent('shinyhub:bookmark:capabilities', {
      detail: {
        version: 1,
        store: 'url',
        fields: [{ id: 'region', label: 'Region', value: 'Europe' }],
        adjustments: [],
      },
    })));
    await openFromKeyboard(tab, 'button.bookmark-trigger');
    // Asserted in the same task as the keypress on purpose: focus must land
    // immediately, not after the opening transition happens to finish.
    assert.match(await activeClass(tab), /\bbookmark-primary\b/);
    await tab.keyboard.press('Escape');
    assert.match(await activeClass(tab), /\bbookmark-trigger\b/);
  });

  await check('offline snapshot dialog takes focus when opened', async () => {
    await tab.evaluate(() => window.dispatchEvent(new CustomEvent('shinyhub:session-status', {
      cancelable: true,
      detail: { state: 'snapshot' },
    })));
    await openFromKeyboard(tab, 'button.session-trigger');
    const active = await activeClass(tab);
    assert.doesNotMatch(active, /\bsession-trigger\b/, 'focus stayed on the trigger');
    assert.equal(
      await tab.evaluate(() => window.__navShadow.querySelector('.session-panel').contains(window.__navShadow.activeElement)),
      true,
      `focus is outside the session dialog (on "${active}")`,
    );
    await tab.keyboard.press('Escape');
    assert.match(await activeClass(tab), /\bsession-trigger\b/);
  });

  await check('a hidden dashboard reloads once and restores its bookmarked filter URL', async () => {
    const dashboard = await browser.newPage({viewport: {width: 1280, height: 800}});
    const errors = [];
    dashboard.on('pageerror', error => errors.push(error.message));
    await dashboard.clock.install();
    await dashboard.goto(`${origin}/app/demo/?_inputs_&region=Europe#plot`);
    await dashboard.selectOption('#region', 'Asia');
    await dashboard.clock.fastForward(1000);
    await dashboard.waitForFunction(() => location.search.includes('region=Asia'));
    await dashboard.evaluate(() => {
      Object.defineProperty(document, 'hidden', {configurable: true, value: true});
      document.dispatchEvent(new Event('visibilitychange'));
    });
    const loaded = dashboard.waitForEvent('load');
    await dashboard.clock.fastForward(31000);
    await loaded;
    assert.equal(switchRequests, 1);
    assert.equal(await dashboard.inputValue('#region'), 'Asia');
    assert.match(dashboard.url(), /region=Asia#plot$/);
    await dashboard.waitForFunction(() => window.__navShadow?.querySelector('.automatic-update-notice'));
    await dashboard.evaluate(() => window.__navShadow.querySelector('button.switch').click());
    await dashboard.clock.resume();
    await new Promise(resolve => setTimeout(resolve, 250));
    await dashboard.screenshot({path: join(artifactDir, 'desktop.png'), animations: 'disabled'});
    await dashboard.setViewportSize({width: 390, height: 844});
    await new Promise(resolve => setTimeout(resolve, 250));
    await dashboard.screenshot({path: join(artifactDir, 'mobile.png'), animations: 'disabled'});
    const notice = await dashboard.evaluate(() => {
      const line = window.__navShadow.querySelector('.automatic-update-notice');
      const rect = line.getBoundingClientRect();
      return {text: line.textContent, width: rect.width, left: rect.left, right: rect.right, viewport: innerWidth};
    });
    assert.match(notice.text, /Updated to the latest version/);
    assert.ok(notice.width > 0 && notice.left >= 0 && notice.right <= notice.viewport, 'update notice overflowed mobile');
    await dashboard.evaluate(() => window.__navShadow.querySelector('.automatic-update-notice button').click());
    assert.equal(await dashboard.evaluate(() => !!window.__navShadow.querySelector('.automatic-update-notice')), false);
    assert.deepEqual(errors, []);
    await dashboard.close();
  });

  await check('interrupted bookmarked dashboards recover automatically; slow recovery stays quiet', async () => {
    for (const width of [1280, 390]) {
      // Each page gets its own sessionStorage and recovery circuit breaker.
      const dashboard = await browser.newPage({ viewport: { width, height: 844 } });
      const errors = [];
      dashboard.on('pageerror', error => errors.push(error.message));
      await dashboard.addInitScript(() => {
        window.addEventListener('shinyhub:bookmark:sync-status', event => { window.__syncStatus = event.detail; });
        window.__interruptionDialogShown = false;
        new MutationObserver(() => {
          if (document.querySelector('#shinyhub-status-overlay[role="dialog"]')) window.__interruptionDialogShown = true;
        }).observe(document, { childList: true, subtree: true });
      });
      await dashboard.goto(`${origin}/app/demo/?recovery=1&_inputs_&region=Asia#plot`);
      await dashboard.waitForFunction(() => window.__syncStatus?.state === 'saved');
      await dashboard.focus('#region');
      holdReadiness = true;
      const loaded = dashboard.waitForEvent('load');
      await dashboard.evaluate(() => {
        document.dispatchEvent(new Event('shiny:disconnected'));
        const marker = document.createElement('div'); marker.id = 'shiny-disconnected-overlay'; document.body.appendChild(marker);
      });
      const notice = dashboard.locator('#shinyhub-status-overlay');
      await notice.waitFor();
      assert.equal(await notice.getAttribute('role'), 'status');
      assert.equal(await dashboard.evaluate(() => document.activeElement.id), 'region');
      assert.equal(await dashboard.evaluate(() => window.__interruptionDialogShown), false);
      const box = await notice.boundingBox();
      assert.ok(box.x >= 0 && box.x + box.width <= width && box.height < 220);
      await dashboard.screenshot({ path: join(artifactDir, `recovery-${width}.png`), animations: 'disabled' });
      releaseReadiness();
      await loaded;
      assert.equal(await dashboard.inputValue('#region'), 'Asia');
      assert.match(dashboard.url(), /region=Asia#plot$/);
      await dashboard.waitForFunction(() => window.__syncStatus?.state === 'saved');
      // A second immediate disconnect must use manual recovery, not loop.
      holdReadiness = false;
      await dashboard.evaluate(() => {
        document.dispatchEvent(new Event('shiny:disconnected'));
        const marker = document.createElement('div'); marker.id = 'shiny-disconnected-overlay'; document.body.appendChild(marker);
      });
      await dashboard.locator('#shinyhub-status-overlay[role="dialog"]').waitFor();
      assert.deepEqual(errors, []);
      await dashboard.close();
    }
  });

  console.log(`app switcher browser E2E passed; screenshots: ${artifactDir}`);
} finally {
  await browser.close();
  server.close();
}
