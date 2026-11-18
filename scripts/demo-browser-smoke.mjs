import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const driverRequire = createRequire(new URL('../loadtest/render/driver/package.json', import.meta.url));
const { chromium } = driverRequire('playwright');

const demoURL = process.env.SHINYHUB_DEMO_URL || 'https://demo.shinyhub.dev/';
const browser = await chromium.launch({ headless: true, executablePath: process.env.SHINYHUB_E2E_CHROMIUM || undefined });
try {
  const page = await browser.newPage();
  const failed = new Promise((_, reject) => page.once('pageerror', reject));
  await Promise.race([failed, (async () => {
    await page.goto(demoURL, { waitUntil: 'domcontentloaded' });
    await page.getByRole('button', { name: 'Continue as Demo Viewer', exact: true }).click({ timeout: 120_000 });
    await page.getByRole('button', { name: 'Account menu for Demo Viewer Viewer', exact: true }).waitFor();
    const fontLoaded = await page.evaluate(async () => {
      const faces = await document.fonts.load('400 14px Manrope', 'ShinyHub');
      return faces.some(face => face.status === 'loaded');
    });
    assert.ok(fontLoaded, 'The browser did not load the Manrope dashboard font');
  })()]);
  console.log('Chromium loaded the demo dashboard, viewer session, and dashboard font');
} finally {
  await browser.close();
}
