import assert from 'node:assert/strict';
import { readPageHeader, assertPageHeader } from './browser-page-header-checks.mjs';

// Compare routes with each other, not with approved pixel values. A deliberate
// global redesign is allowed; an accidental page-specific redesign is not.
export async function checkShellConsistency(page, origin, { role, width, theme, extraRoutes = [] }) {
  const mobile = width <= 860;
  const modes = mobile ? ['drawer'] : ['expanded', 'collapsed'];
  for (const mode of modes) {
    await page.goto(origin + '/');
    await page.locator('body[data-auth="in"]').waitFor();
    if (!mobile) {
      const collapsed = await page.locator('body').evaluate(el => el.classList.contains('sidebar-collapsed'));
      if (collapsed !== (mode === 'collapsed')) await page.locator('#sidebar-collapse').click();
    }
    const primaryRoutes = await page.locator('#primary-nav .nav-item:not([hidden])').evaluateAll(links => links.map(a => a.getAttribute('href')));
    assert.ok(primaryRoutes.length, 'The shell check must exercise real navigation');
    const routes = [...new Set(['/', ...primaryRoutes, ...extraRoutes])];
    let baseline, activeBaseline, headingBaseline;
    for (const path of routes) {
      // Primary destinations use the SPA navigation path. Detail destinations
      // also exercise direct loads, where shell initialization can diverge.
      if (new URL(page.url()).pathname !== path) {
        const link = page.locator(`#primary-nav .nav-item:not([hidden])[href=${JSON.stringify(path)}]`);
        if (await link.count()) {
          if (mobile && await page.locator('#sidebar-toggle').getAttribute('aria-expanded') !== 'true') {
            await page.locator('#sidebar-toggle').click();
          }
          await link.click();
          await page.waitForURL(origin + path);
        } else {
          await page.goto(origin + path);
        }
      }
      await page.locator('body[data-auth="in"]').waitFor();
      if (role === 'admin') await page.locator('#sidebar-apps .sidebar-app').first().waitFor({ state: 'attached' });
      if (mobile && await page.locator('#sidebar-toggle').getAttribute('aria-expanded') !== 'true') {
        await page.locator('#sidebar-toggle').click();
      }
      await page.mouse.move(width - 1, 999);
      await page.evaluate(async () => {
        await document.fonts.ready;
        await Promise.all(document.querySelector('#sidebar').getAnimations({ subtree: true }).map(a => a.finished.catch(() => {})));
        await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      });
      const actual = await page.evaluate(readShell);
      const label = `${role}/${theme}/${width}/${mode} ${path}`;
      if (role === 'admin') {
        const selection = await page.evaluate(() => {
          const nav = document.querySelector('#primary-nav');
          const app = [...nav.querySelectorAll('.sidebar-app')].find(link =>
            location.pathname === link.pathname || location.pathname.startsWith(link.pathname + '/'));
          return { expected: app?.getAttribute('href'),
            current: [...nav.querySelectorAll('[aria-current="page"]')].map(link => link.getAttribute('href')),
            nested: document.querySelector('#sidebar-apps').previousElementSibling?.id === 'tab-apps' };
        });
        assert.ok(selection.nested, `${label}: apps must be nested directly beneath Apps`);
        if (selection.expected) assert.deepEqual(selection.current, [selection.expected], `${label}: only the current app should be selected`);
      }
      assertPageHeader(await page.evaluate(readPageHeader), label);
      if (!baseline) baseline = actual.shell;
      assert.deepEqual(actual.shell, baseline, `${label}: shared shell changed between routes`);
      if (actual.heading && (path === '/' || primaryRoutes.includes(path))) {
        headingBaseline ||= actual.heading;
        assert.deepEqual(actual.heading, headingBaseline, `${label}: primary page heading changed between routes`);
      }
      if (actual.active) {
        activeBaseline ||= actual.active;
        assert.deepEqual(actual.active, activeBaseline, `${label}: active navigation styling changed between routes`);
      }
      console.log('SHELL PASS', label);
    }
    // Prove this comparison detects the exact defect, rather than merely
    // passing the current implementation. Remove the injected rule afterward.
    const before = await page.evaluate(readShell);
    const mutation = await page.addStyleTag({ content: 'body:has(main section:not([hidden])) #sidebar { padding-top: 123px !important; }' });
    const after = await page.evaluate(readShell);
    assert.throws(() => assert.deepEqual(after.shell, before.shell), { name: 'AssertionError' },
      'The shell comparison must reject the injected route-specific padding regression');
    await mutation.evaluate(el => el.remove());
    const contentMutation = await page.addStyleTag({ content: 'body:has(main section:not([hidden])) main { padding-left: 123px !important; }' });
    const contentAfter = await page.evaluate(readShell);
    assert.throws(() => assert.deepEqual(contentAfter.shell.contentFrame, before.shell.contentFrame), { name: 'AssertionError' },
      'The shell comparison must reject route-specific content padding');
    await contentMutation.evaluate(el => el.remove());
    if (!mobile && mode === 'collapsed') await page.locator('#sidebar-collapse').click();
  }
}

// Pure DOM observation, shared by every route and the regression probe.
export function readShell() {
  const layout = ['display', 'position', 'width', 'height', 'min-height', 'padding', 'margin', 'gap',
    'font-family', 'font-size', 'font-weight', 'line-height', 'letter-spacing', 'border-radius', 'align-items', 'justify-content'];
  const paint = ['color', 'background-color', 'border-color', 'box-shadow', 'backdrop-filter'];
  const read = (el, properties) => {
    const style = getComputedStyle(el);
    return Object.fromEntries(properties.map(key => [key, style.getPropertyValue(key)]));
  };
  const geometry = el => {
    const rect = el.getBoundingClientRect();
    return Object.fromEntries(['x', 'y', 'width', 'height'].map(k => [k, Math.round(rect[k] * 10) / 10]));
  };
  const selectors = ['#sidebar', '.sidebar-brand', '#primary-nav', '#sidebar-apps', '.sidebar-footer',
    '#identity-card', '#about-button', '#sidebar-collapse', '#mobile-topbar', '#sidebar-toggle'];
  const shell = Object.fromEntries(selectors.map(selector => {
    const el = document.querySelector(selector);
    if (!el) throw new Error(`Missing shell element: ${selector}`);
    return [selector, { ...read(el, [...layout, ...paint]), rect: geometry(el) }];
  }));
  // Content height belongs to each page; its frame belongs to the shared shell.
  // Comparing width, insets and typography catches route-specific workspace CSS
  // without forcing dashboards and detail views to have identical contents.
  const main = document.querySelector('main');
  const mainRect = geometry(main);
  shell.contentFrame = {
    ...read(main, ['max-width', 'padding', 'font-family', 'font-size']),
    x: mainRect.x, width: mainRect.width,
  };
  const pageRoot = main.querySelector(':scope > section:not([hidden])');
  if (pageRoot) {
    const pageRect = geometry(pageRoot);
    shell.contentFrame.page = {
      ...read(pageRoot, ['padding']),
      x: pageRect.x, width: pageRect.width,
    };
  }
  shell.canvas = read(document.body, ['background-color']);
  // Selected destination may change color, but cannot move any nav row.
  const links = [...document.querySelectorAll('#primary-nav .nav-item:not([hidden])')];
  shell.links = links.map(el => ({ href: el.getAttribute('href'), ...read(el, layout), rect: geometry(el) }));
  const active = links.find(el => el.classList.contains('tab-active'));
  const heading = [...main.querySelectorAll('.toolbar h1')].find(el => el.getClientRects().length);
  return { shell, active: active ? read(active, paint) : null,
    heading: heading ? read(heading, ['font-family', 'font-size', 'font-weight', 'line-height', 'letter-spacing']) : null };
}
