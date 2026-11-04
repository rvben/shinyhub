import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const require = createRequire(new URL('../../loadtest/render/driver/package.json', import.meta.url));
const { chromium } = require('playwright');
const rootRequire = createRequire(new URL('../../package.json', import.meta.url));

// Geometric assertions catch broken layout without machine/font-sensitive PNG
// goldens. In particular, overflow alone misses an empty implicit grid column.
export async function checkOverviewLayout(page, width) {
  const boxes = await page.locator('#detail-overview-panel .overview-grid').evaluate(grid => {
    const rect = selector => {
      const r = (selector ? grid.querySelector(selector) : grid).getBoundingClientRect();
      return { x: r.x, y: r.y, width: r.width, right: r.right };
    };
    return { grid: rect(), trends: rect('.overview-trends'), activity: rect('.overview-recent-activity') };
  });
  if (width <= 1180) {
    assert.ok(Math.abs(boxes.grid.width - boxes.trends.width) < 2, `${width}px: graphs must fill their row`);
    assert.ok(boxes.activity.y > boxes.trends.y, `${width}px: activity follows graphs`);
  } else {
    assert.ok(boxes.activity.x >= boxes.trends.right, `${width}px: activity sits beside graphs`);
    assert.ok(Math.abs(boxes.trends.y - boxes.activity.y) < 2, `${width}px: top row aligns`);
  }
  return boxes;
}

export async function runBrowserJourneys(f) {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.SHINYHUB_E2E_CHROMIUM || undefined });
  const closeOnAbort = () => { browser.close().catch(() => {}); };
  f.signal.addEventListener('abort', closeOnAbort, { once: true });
  let context, page, axeSource;
  const errors = [];
  function observe(target) {
    target.on('pageerror', error => errors.push(error.message));
    target.on('response', response => {
      const path = new URL(response.url()).pathname;
      if (response.status() >= 500 || (path.startsWith('/static/') && !response.ok())) errors.push(`${response.status()} ${path}`);
    });
  }
  const screenshot = async name => page.screenshot({ path: join(f.output, name + '.png'), animations: 'disabled',
    mask: [page.locator('input[type="password"]'), page.locator('#token-reveal-value'), page.locator('#new-user-snippet')] });
  async function navigate(path, ready) {
    const response = await page.goto(f.host + path, { waitUntil: 'domcontentloaded' });
    assert.equal(response.status(), 200, `direct link ${path}`);
    await page.locator('body[data-auth="in"]').waitFor();
    if (ready) await page.locator(ready).first().waitFor();
    await page.waitForFunction(() => ![...document.querySelectorAll(
      '[aria-busy="true"], #people-loading, #workers-loading, #audit-loading, #tokens-loading, #announcements-view [data-loading]'
    )].some(el => el.getClientRects().length));
  }
  async function signIn(target, role) {
    await target.goto(f.host + '/login');
    await target.getByRole('textbox', { name: 'Username', exact: true }).fill(f.accounts[role].username);
    await target.getByLabel('Password', { exact: true }).fill(f.accounts[role].password);
    await target.getByRole('button', { name: 'Sign in', exact: true }).click();
    await target.locator('body[data-auth="in"]').waitFor();
  }
  async function setTheme(theme) {
    await page.locator('#identity-card').click();
    await page.getByRole('button', { name: 'Your profile', exact: true }).click();
    await page.locator(`input[name="theme-pref"][value="${theme}"]`).check();
    await page.locator('#profile-cancel').click();
    assert.equal(await page.locator('html').getAttribute('data-theme'), theme);
  }
  async function accessible(label) {
    await page.evaluate(axeSource);
    const violations = await page.evaluate(async () => (await window.axe.run(document, {
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'] },
    })).violations.map(v => ({ id: v.id, impact: v.impact, nodes: v.nodes.map(n => n.target) })));
    // Finish the matrix so one run reports every affected page, then fail the
    // gate below. Never suppress a rule or downgrade a violation to a warning.
    f.report.accessibility.push({ label, violations });
  }
  try {
    context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, userAgent: 'shinyhub-acceptance-test' });
    context.setDefaultTimeout(15_000);
    page = await context.newPage();
    observe(page);
    axeSource = await readFile(rootRequire.resolve('axe-core/axe.min.js'), 'utf8');
    await f.check('browser sign-in and real reactive Shiny session', async () => {
      await signIn(page, 'admin');
      await page.goto(f.host + '/app/forecast/');
      await page.getByRole('heading', { name: 'Acceptance forecast', exact: true }).waitFor();
      await page.waitForFunction(() => document.getElementById('forecast')?.textContent === 'Forecast: 35');
      await page.getByRole('spinbutton', { name: 'Demand', exact: true }).fill('42');
      await page.waitForFunction(() => document.getElementById('forecast')?.textContent === 'Forecast: 42');
    });
    await f.check('settings save, discard, and navigation confirmation preserve the right values', async () => {
      await navigate('/apps/forecast/settings/general', '#general-description');
      const description = page.getByRole('textbox', { name: 'App description', exact: true });
      const original = await description.inputValue();
      await description.fill('Unsaved acceptance draft');
      page.once('dialog', dialog => dialog.dismiss());
      await page.getByRole('link', { name: 'Logs', exact: true }).click();
      assert.equal(new URL(page.url()).pathname, '/apps/forecast/settings/general');
      assert.equal(await description.inputValue(), 'Unsaved acceptance draft');
      page.once('dialog', dialog => dialog.accept());
      await page.getByRole('link', { name: 'Logs', exact: true }).click();
      await page.locator('#detail-logs-panel').waitFor();
      await navigate('/apps/forecast/settings/general', '#general-description');
      assert.equal(await description.inputValue(), original);
      await description.fill('Discard this draft');
      await page.getByRole('button', { name: 'Discard changes', exact: true }).click();
      assert.equal(await description.inputValue(), original);
      await description.fill('Saved from the browser · 東京');
      await page.getByRole('button', { name: 'Save general settings', exact: true }).click();
      await page.getByText('Saved.', { exact: true }).waitFor();
      await page.reload();
      await description.waitFor();
      assert.equal(await description.inputValue(), 'Saved from the browser · 東京');
      const { app } = await f.json('/api/apps/forecast');
      assert.equal(app.description, 'Saved from the browser · 東京');
      assert.equal(app.project_slug, 'acceptance', 'editing a description preserves the project');
    });
    await f.check('new-app upload recovers from an invalid bundle and reaches a ready app', async () => {
      await navigate('/apps', '#new-app-button');
      await page.getByRole('button', { name: 'New app', exact: true }).click();
      await page.getByRole('textbox', { name: 'Display name', exact: true }).fill('Browser acceptance');
      await page.getByRole('textbox', { name: 'Slug', exact: true }).fill('browser-created');
      await page.getByRole('button', { name: 'Create app', exact: true }).click();
      await page.getByText('Browser upload Fallback', { exact: true }).click();
      await page.locator('#deploy-file').setInputFiles(f.invalid);
      await page.getByRole('button', { name: 'Deploy', exact: true }).click();
      await page.getByText(/This ZIP bundle is invalid or damaged/).waitFor();
      assert.equal(await page.locator('#deploy-error-logs').isVisible(), false);
      const choice = page.waitForEvent('filechooser');
      await page.getByRole('button', { name: 'Choose another bundle', exact: true }).click();
      await (await choice).setFiles(f.bundle);
      let release, received, failed;
      const responseGate = new Promise(resolve => { release = resolve; });
      const serverFinished = new Promise((resolve, reject) => { received = resolve; failed = reject; });
      const deploymentURL = '**/api/apps/browser-created/deploy';
      await page.route(deploymentURL, async route => {
        try {
          const response = await route.fetch({ timeout: 120_000 });
          received();
          await responseGate;
          await route.fulfill({ response });
        } catch (error) { failed(error); }
      });
      await page.getByRole('button', { name: 'Deploy', exact: true }).click();
      await page.getByRole('button', { name: 'Hide progress', exact: true }).click();
      await page.locator('#deploy-monitor').waitFor();
      assert.equal(await page.locator('#deploy-modal').isVisible(), false);
      await serverFinished;
      await page.getByRole('button', { name: 'View deployment', exact: true }).click();
      release();
      await page.getByRole('button', { name: 'View app', exact: true }).waitFor({ timeout: 120_000 });
      await page.getByRole('button', { name: 'View app', exact: true }).click();
      await page.getByRole('heading', { name: 'Browser acceptance', exact: true }).waitFor();
      assert.equal((await f.json('/api/apps/browser-created')).app.status, 'running');
      await page.unroute(deploymentURL);
    });
    await f.check('access refresh retains backend grants through a lost connection and recovers', async () => {
      await f.json('/api/apps/forecast/members', { method: 'POST', body: { username: f.accounts.viewer.username } }, 204);
      await navigate('/apps/forecast/settings/access', '#members-list select');
      const role = page.getByRole('combobox', { name: `Role for ${f.accounts.viewer.username}`, exact: true });
      await role.waitFor();
      const previous = await role.inputValue();
      const memberURL = '**/api/apps/forecast/members';
      await page.route(memberURL, route => route.abort('connectionfailed'));
      await page.getByRole('button', { name: 'Refresh members', exact: true }).click();
      await page.locator('#members-list').getByText(/Showing the last known entries/).waitFor();
      assert.equal(await role.inputValue(), previous);
      await page.unroute(memberURL);
      await page.locator('#members-list').getByRole('button', { name: 'Try again', exact: true }).click();
      await page.waitForFunction(() => !document.querySelector('#members-list .access-list-state'));
      assert.equal(await role.inputValue(), previous);
      const current = await f.json('/api/apps/forecast/members');
      assert.ok(current.items.some(member => member.username === f.accounts.viewer.username));
    });
    await f.check('one-time token reveal, duplicate-name correction, and inline revocation', async () => {
      await navigate('/tokens', '#new-token-button');
      await page.getByRole('button', { name: 'New token', exact: true }).click();
      await page.getByRole('textbox', { name: 'Token name', exact: true }).fill('browser-token');
      await page.locator('#new-token-form button[type="submit"]').click();
      await page.locator('#token-reveal').waitFor();
      const raw = await page.locator('#token-reveal-value').textContent();
      assert.ok(raw?.startsWith('shk_'), 'a one-time token was issued');
      await page.getByRole('button', { name: 'I’ve saved my token', exact: true }).click();
      assert.ok(await page.locator('#token-reveal-value').textContent() === '', 'dismissal clears the raw token');
      await page.getByRole('button', { name: 'New token', exact: true }).click();
      await page.getByRole('textbox', { name: 'Token name', exact: true }).fill('browser-token');
      await page.locator('#new-token-form button[type="submit"]').click();
      await page.locator('#new-token-error').getByText(/already have/).waitFor();
      await page.locator('#new-token-close').click();
      await page.getByRole('button', { name: 'Revoke token browser-token', exact: true }).click();
      await page.getByRole('button', { name: 'Revoke token', exact: true }).click();
      await page.getByRole('button', { name: 'Revoke token browser-token', exact: true }).waitFor({ state: 'hidden' });
      await f.json('/api/auth/me', { auth: null, headers: { Authorization: `token ${raw}` } }, 401);
    });
    await f.check('profile save and announcement conflict recovery retain persisted values', async () => {
      await page.locator('#identity-card').click();
      await page.getByRole('button', { name: 'Your profile', exact: true }).click();
      await page.locator('#profile-display-name').fill('Acceptance operator');
      await page.locator('#profile-submit').click();
      await page.getByText('Changes saved', { exact: true }).waitFor();
      assert.equal((await f.json('/api/auth/me')).user.display_name, 'Acceptance operator');
      await page.locator('#profile-cancel').click();
      await navigate('/announcements', '[data-announcement-id]');
      await page.getByRole('button', { name: 'Acceptance maintenance', exact: true }).click();
      await page.getByRole('heading', { name: 'Edit announcement', exact: true }).waitFor();
      const path = '/api/announcements/' + f.announcementID;
      const saved = await f.json(path);
      await f.json(path, { method: 'PATCH', body: { expected_revision: saved.revision, message: 'Concurrent saved version' } });
      await page.getByRole('textbox', { name: 'Message', exact: true }).fill('Preserve my unsaved correction');
      await page.getByRole('button', { name: 'Save changes', exact: true }).click();
      await page.locator('[data-editor-error]').getByText(/another administrator/i).waitFor();
      assert.equal(await page.getByRole('textbox', { name: 'Message', exact: true }).inputValue(), 'Preserve my unsaved correction');
      page.once('dialog', dialog => dialog.accept());
      await page.getByRole('button', { name: 'Reload saved version', exact: true }).click();
      await page.waitForFunction(() => document.getElementById('announcement-message')?.value === 'Concurrent saved version');
      await page.getByRole('textbox', { name: 'Message', exact: true }).fill('Browser-confirmed announcement');
      await page.getByRole('button', { name: 'Save changes', exact: true }).click();
      await page.locator('[data-success]').waitFor();
      assert.equal((await f.json(path)).message, 'Browser-confirmed announcement');
      await page.getByRole('button', { name: 'Close editor', exact: true }).click();
      await page.getByRole('heading', { name: 'Announcements', exact: true }).waitFor();
    });
    await f.check('audit deep link exposes changed fields and inline copy confirmation', async () => {
      const event = (await f.json('/api/audit?q=forecast&action=update_app')).events[0];
      await navigate('/audit-log?event=' + event.id, '#audit-detail-' + event.id);
      assert.match(await page.locator('#audit-event-' + event.id + ' .audit-change').textContent(), /1 field changed.*Description/);
      await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: f.host });
      await page.locator('#audit-detail-' + event.id + ' button').click();
      await page.locator('#audit-detail-' + event.id + ' .audit-copy-status').getByText('Link copied.', { exact: true }).waitFor();
      assert.equal(await page.evaluate(() => navigator.clipboard.readText()), f.host + '/audit-log?event=' + event.id);
    });
    await f.check('live app diagnostics and keyboard app switching work', async () => {
      await navigate('/apps/forecast/logs', '#detail-logs-body');
      await page.locator('#detail-logs-body').getByText(/Acceptance session connected/).first().waitFor();
      await f.poll(async () => {
        const traces = await f.json('/api/apps/forecast/traces');
        assert.equal(traces.enabled, true);
        return traces.spans.length > 0;
      }, 'real proxy span recorded');
      await navigate('/apps/forecast/traces', '#traces-tbody tr');
      await navigate('/apps/forecast', '.overview-trends');
      const switcher = page.locator('.app-context-identity');
      assert.match(await switcher.ariaSnapshot(), /Switch app, Regional forecast · 東京/);
      await switcher.focus();
      await switcher.press('Enter');
      await page.locator('.app-context-apps').getByRole('link', { name: 'Browser acceptance', exact: true }).waitFor();
      await switcher.press('Enter');
      await page.locator('.app-context-apps').getByRole('link', { name: 'Browser acceptance', exact: true }).waitFor({ state: 'hidden' });
      await page.locator('.overview-trends [role="slider"]').first().waitFor();
    });
    const routes = [
      ['/', '#overview-view'], ['/apps', '#apps-view'], ['/identity', '#users-view'], ['/workers', '#workers-view'],
      ['/audit-log', '#audit-view'], ['/tokens', '#tokens-view'], ['/announcements', '#announcements-view'],
      ['/projects/acceptance', '#project-detail-heading'],
      ...['overview', 'deployments', 'logs', 'traces', 'usage', 'schedules', 'data'].map(tab => [
        '/apps/forecast' + (tab === 'overview' ? '' : '/' + tab), '#detail-' + tab + '-panel']),
      ...['general', 'environment', 'resources', 'scaling', 'access', 'privacy'].map(tab => [
        '/apps/forecast/settings/' + tab, tab === 'access' ? '#detail-access-panel' : '#detail-configuration-panel']),
      [`/apps/forecast/schedules/${f.scheduleID}`, '#schedule-detail-title'],
    ];
    for (const theme of ['light', 'dark']) {
      await page.setViewportSize({ width: 1440, height: 1000 });
      await navigate('/apps', '#apps-view');
      await setTheme(theme);
      for (const width of [390, 1113, 1440]) {
        await f.check(`all ${routes.length} pages: ${theme}, ${width}px`, async () => {
          await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 });
          let sharedHeader;
          for (const [path, ready] of routes) {
            await navigate(path, ready);
            await page.evaluate(() => document.fonts.ready);
            const layout = await page.evaluate(() => ({
              width: innerWidth, contentWidth: document.documentElement.scrollWidth,
              headings: [...document.querySelectorAll('h1')].filter(e => e.getClientRects().length).map(e => e.textContent.trim()),
            }));
            assert.ok(layout.contentWidth <= width, `${path}/${theme}/${width}: horizontal overflow`);
            assert.equal(layout.headings.length, 1, `${path}: one visible page heading`);
            assert.ok(!layout.headings[0].includes('Something went wrong'), `${path}: route rendered`);
            f.report.layouts.push({ path, theme, ...layout });
            if (path.startsWith('/apps/forecast')) {
              const header = await page.locator('.app-detail-bar').boundingBox();
              if (sharedHeader) for (const axis of ['x', 'y', 'width', 'height']) {
                assert.ok(Math.abs(header[axis] - sharedHeader[axis]) < 2, `${path}/${width}: shared header ${axis}`);
              }
              sharedHeader = header;
            }
            if (path === '/apps/forecast') {
              await page.locator('.overview-trends').waitFor();
              await checkOverviewLayout(page, width);
              await screenshot(`overview-${theme}-${width}`);
            }
            if ((theme === 'light' && width === 1440) || (theme === 'dark' && width === 390)) await accessible(`${path}/${theme}/${width}`);
          }
        });
      }
    }
    await f.check('overview layout at both sides of its breakpoint', async () => {
      await navigate('/apps/forecast', '.overview-trends');
      for (const width of [1000, 1001, 1180, 1181, 1280]) {
        await page.setViewportSize({ width, height: 1000 });
        await checkOverviewLayout(page, width);
      }
    });
    await f.check('phone forms and announcement editor fit the viewport and remain accessible', async () => {
      await page.setViewportSize({ width: 390, height: 844 });
      async function fits(selector, label) {
        const box = await page.locator(selector).boundingBox();
        assert.ok(box && box.x >= 0 && box.x + box.width <= 391, `${label}: fits phone viewport`);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${label}: no overflow`);
        await accessible(label);
      }
      await navigate('/apps', '#new-app-button');
      await page.getByRole('button', { name: 'New app', exact: true }).click();
      await fits('#new-app-modal .modal-card', 'phone new-app form');
      await page.locator('#new-app-close').click();
      await navigate('/tokens', '#new-token-button');
      await page.getByRole('button', { name: 'New token', exact: true }).click();
      await fits('#new-token-modal .modal-card', 'phone token form');
      await page.locator('#new-token-close').click();
      await page.getByRole('button', { name: 'Open navigation', exact: true }).click();
      await page.locator('#identity-card').click();
      await page.getByRole('button', { name: 'Your profile', exact: true }).click();
      await fits('#profile-modal .modal-card', 'phone profile form');
      await page.locator('#profile-cancel').click();
      await navigate('/announcements', '[data-announcement-id]');
      await page.getByRole('button', { name: 'Acceptance maintenance', exact: true }).click();
      await page.getByRole('heading', { name: 'Edit announcement', exact: true }).waitFor();
      await fits('#announcement-message', 'phone announcement editor');
      await page.getByRole('button', { name: 'Close editor', exact: true }).click();
    });
    await f.check('phone navigation, viewer restrictions, and protected deep-link recovery', async () => {
      const viewerContext = await browser.newContext({ viewport: { width: 390, height: 844 }, userAgent: 'shinyhub-acceptance-test' });
      try {
        const viewer = await viewerContext.newPage();
        observe(viewer);
        await signIn(viewer, 'viewer');
        await viewer.getByRole('button', { name: 'Open navigation', exact: true }).click();
        assert.equal(await viewer.locator('#administration-nav').isVisible(), false);
        await viewer.goto(f.host + '/workers');
        await viewer.locator('#launchpad-view').waitFor();
        assert.equal(new URL(viewer.url()).pathname, '/');
        await viewer.goto(f.host + '/apps/forecast/settings/general');
        await viewer.locator('#launchpad-view').waitFor();
        assert.equal(new URL(viewer.url()).pathname, '/apps');
        await viewer.getByRole('button', { name: 'Open navigation', exact: true }).click();
        assert.equal(await viewer.getByRole('link', { name: 'General', exact: true }).isVisible(), false);
      } finally { await viewerContext.close(); }
    });
    assert.deepEqual(f.report.accessibility.filter(result => result.violations.length), [], 'browser accessibility');
    assert.deepEqual(errors, [], 'no script errors, failed assets, or server errors');
    f.report.browser = browser.version();
  } catch (error) {
    await screenshot('failure').catch(() => {});
    await writeFile(join(f.output, 'browser-errors.json'), JSON.stringify(errors, null, 2));
    throw error;
  } finally {
    f.signal.removeEventListener('abort', closeOnAbort);
    await context?.close();
    await browser.close();
  }
}
