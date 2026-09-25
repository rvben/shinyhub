#!/usr/bin/env node

// Real-browser contract for the injected app switcher. jsdom runs no CSS, so
// it cannot see that a panel fading in from `visibility: hidden` refuses focus
// at the moment it opens. This serves the production nav.js to system Chrome
// and asserts that opening each dialog moves keyboard focus inside it and that
// Escape hands focus back to the control that opened it.

import assert from 'node:assert/strict';
import { once } from 'node:events';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';

const driverRequire = createRequire(new URL('../loadtest/render/driver/package.json', import.meta.url));
const { chromium } = driverRequire('playwright');

const navSource = await readFile(new URL('../internal/appnav/assets/nav.js', import.meta.url));
if (!navSource.includes('shinyhub-app-nav')) {
  throw new Error('nav.js does not look like the app switcher script');
}

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
  response.end(page);
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

  console.log('app switcher browser E2E passed');
} finally {
  await browser.close();
  server.close();
}
