#!/usr/bin/env node
// Called by TestBrowserSessionRealBrowser: actual production Go handlers and
// cookies, genuine WebSockets and both shared/isolated application origins.
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
const driverRequire = createRequire(new URL('../loadtest/render/driver/package.json', import.meta.url));
const { chromium } = driverRequire('playwright');
const [control, app] = process.argv.slice(2);
assert.ok(control && app, 'fixture origins required');
const browser = await chromium.launch({ channel: process.env.SHINYHUB_E2E_BROWSER_CHANNEL || 'chrome', headless: true });
const errors = [];
const contexts = [];
async function context() {
  const ctx = await browser.newContext({ userAgent: 'shinyhub-session-browser-test' });
  contexts.push(ctx);
  const response = await ctx.request.post(`${control}/api/auth/session`, { data: { username: 'browser-fixture', password: 'browser-fixture-password' } });
  assert.equal(response.status(), 200);
  return ctx;
}
async function openApp(ctx, viewport = { width: 1280, height: 800 }) {
  const page = await ctx.newPage();
  const diagnostics = [];
  page.on('console', message => { if (message.type() === 'error') diagnostics.push(message.text()); });
  await page.setViewportSize(viewport);
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => {
    const original = Element.prototype.attachShadow;
    Element.prototype.attachShadow = function (options) {
      const root = original.call(this, options);
      if (this.id === 'shinyhub-session-notice') window.noticeRoot = root;
      return root;
    };
  });
  await page.goto(`${control}/app/session-app/`);
  assert.equal(new URL(page.url()).origin, app);
  try {
    await page.waitForFunction(() => window.socket?.readyState === WebSocket.OPEN, null, { timeout: 6000 });
  } catch (error) {
    console.error({ diagnostics, page: await page.evaluate(() => ({ title: document.title, text: document.body.innerText.slice(0, 400), socket: window.socket?.readyState, loader: !!document.getElementById('shinyhub-browser-session') })) });
    throw error;
  }
  await page.locator('#work').fill('Unsaved work stays here');
  return page;
}
async function cookieClaims(ctx, origin) {
  const cookie = (await ctx.cookies(origin)).find(c => c.name === 'shiny_session');
  assert.ok(cookie, 'session cookie present');
  assert.equal(cookie.httpOnly, true);
  return JSON.parse(Buffer.from(cookie.value.split('.')[1], 'base64url').toString());
}
async function ended(page) {
  await page.waitForFunction(() => window.socket?.readyState === WebSocket.CLOSED, null, { timeout: 15000 });
  await page.waitForFunction(() => !!window.noticeRoot, null, { timeout: 6000 });
  assert.equal(await page.locator('#work').inputValue(), 'Unsaved work stays here');
  assert.match(await page.evaluate(() => window.noticeRoot.textContent), /sign-in session ended/);
}
try {
  console.log('app-only renewal, background/resume and offline recovery');
  const ctx = await context();
  const before = await cookieClaims(ctx, control);
  const page = await openApp(ctx);
  const after = await cookieClaims(ctx, app);
  assert.equal(after.auth_time, before.auth_time, 'app launch preserves original login');
  assert.equal(after.jti, before.jti, 'app launch preserves logout identity');
  await page.waitForTimeout(4800); // beyond the initial four-second JWT expiry
  const renewed = await cookieClaims(ctx, app);
  assert.ok(renewed.exp > after.exp, 'app alone renewed its cookie');
  assert.equal(renewed.auth_time, before.auth_time);
  assert.equal(await page.evaluate(() => window.socket.readyState), 1, 'renewal preserves the existing WebSocket');
  let checks = 0;
  page.on('request', request => { if (request.url().endsWith('/.shinyhub/session.json')) checks++; });
  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, get: () => true }); document.dispatchEvent(new Event('visibilitychange')); });
  await page.waitForTimeout(1600);
  const hiddenChecks = checks;
  await page.waitForTimeout(300);
  assert.equal(checks, hiddenChecks, 'hidden tab pauses renewal');
  await page.evaluate(() => { delete document.hidden; document.dispatchEvent(new Event('visibilitychange')); });
  await page.waitForFunction(() => !document.hidden);
  await page.waitForTimeout(200);
  assert.ok(checks > hiddenChecks, 'resume immediately checks identity');
  await ctx.setOffline(true);
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await page.waitForTimeout(300);
  assert.equal(await page.locator('#shinyhub-session-notice').count(), 0, 'offline is not logout');
  assert.equal(await page.locator('#work').inputValue(), 'Unsaved work stays here');
  await ctx.setOffline(false);
  const offlineChecks = checks;
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await page.waitForTimeout(200);
  assert.ok(checks > offlineChecks, 'reconnection verifies identity');
  assert.equal(await page.locator('#shinyhub-session-notice').count(), 0);
  await ctx.close();

  console.log('dashboard logout revokes app cookies and live WebSockets across tabs/origins');
  const logoutCtx = await context();
  const appPage = await openApp(logoutCtx);
  const dashboard = await logoutCtx.newPage();
  await dashboard.goto(control);
  await dashboard.locator('#logout').click();
  await ended(appPage);
  if (process.env.SHINYHUB_SESSION_SCREENSHOTS) await appPage.screenshot({ path: `/tmp/shinyhub-session-${control === app ? 'shared' : 'isolated'}-desktop.png` });
  assert.equal(await dashboard.locator('#status').textContent(), 'Signed out');
  const verdict = await logoutCtx.request.get(`${app}/app/session-app/.shinyhub/session.json`);
  assert.equal(verdict.status(), 401, 'revoked isolated app cookie cannot renew');
  await logoutCtx.close();

  console.log('absolute deadline closes existing sockets and preserves mobile recovery UI');
  const deadlineCtx = await context();
  const deadlinePage = await openApp(deadlineCtx, { width: 390, height: 844 });
  await ended(deadlinePage);
  if (process.env.SHINYHUB_SESSION_SCREENSHOTS) await deadlinePage.screenshot({ path: `/tmp/shinyhub-session-${control === app ? 'shared' : 'isolated'}-mobile.png` });
  const ui = await deadlinePage.evaluate(() => {
    const host = document.getElementById('shinyhub-session-notice');
    const link = window.noticeRoot.querySelector('a');
    const bounds = host.getBoundingClientRect();
    link.focus();
    return { left: bounds.left, right: bounds.right, width: innerWidth, href: link.href, target: link.target, focused: window.noticeRoot.activeElement === link, role: window.noticeRoot.querySelector('.notice').getAttribute('role') };
  });
  assert.ok(ui.left >= 0 && ui.right <= ui.width, 'recovery notice fits mobile viewport');
  assert.equal(ui.target, '_blank');
  assert.equal(ui.href, `${control}/app/session-app/`);
  assert.equal(ui.focused, true, 'recovery link is keyboard focusable');
  assert.equal(ui.role, 'status');
  assert.equal(new URL(deadlinePage.url()).origin, app, 'expiry did not discard the current app page');
  assert.deepEqual(errors, [], 'no uncaught browser errors');
  console.log('browser session E2E passed');
} finally {
  await Promise.allSettled(contexts.map(ctx => ctx.close()));
  await browser.close();
}
