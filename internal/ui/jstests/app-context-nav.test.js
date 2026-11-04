import test from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { installAppContextNavigation } from '../static/views/app-context-nav.js';

const forecast = { id: 1, slug: 'forecast', name: 'Regional forecast' };
const revenue = { id: 2, slug: 'revenue', name: 'Revenue explorer' };

function identityText(summary) {
  const copy = summary.cloneNode(true);
  copy.querySelectorAll('[aria-hidden="true"]').forEach(node => node.remove());
  return copy.textContent;
}

function setup() {
  const dom = new JSDOM('<aside id="sidebar"><div class="sidebar-brand">ShinyHub</div><nav id="primary-nav">Platform sections</nav><div id="app-context-nav" hidden></div></aside>', { url: 'https://hub.example/apps/forecast' });
  const document = dom.window.document;
  const destinations = [];
  const controller = installAppContextNavigation({ document, navigate: path => destinations.push(path) });
  const update = overrides => controller.update({ app: forecast, apps: [forecast, revenue], canManage: true, path: '/apps/forecast', ...overrides });
  return { dom, document, destinations, controller, update, host: document.getElementById('app-context-nav') };
}

test('contextual sidebar exposes ordinary operational and settings links with an app switcher', () => {
  const h = setup();
  try {
    h.update();
    assert.equal(h.host.hidden, false);
    assert.equal(h.document.getElementById('primary-nav').hidden, true);
    assert.equal(h.document.getElementById('sidebar').dataset.context, 'app');
    assert.equal(h.host.querySelector('.app-context-back').getAttribute('href'), '/apps');
    assert.equal(h.host.querySelector('.app-context-back').textContent, 'Back to apps');
    assert.equal(h.host.querySelector('details > summary .app-context-label').textContent, forecast.name);
    assert.equal(h.host.querySelector('summary .app-card-avatar').textContent, 'RF');
    assert.equal(h.host.querySelector('.app-context-label').title, forecast.name);
    assert.equal(identityText(h.host.querySelector('summary')), `Switch app, ${forecast.name}`);
    assert.deepEqual([...h.host.querySelectorAll('.app-context-links > a')].map(link => [link.textContent, link.getAttribute('href')]), [
      ['Overview', '/apps/forecast'], ['Deployments', '/apps/forecast/deployments'], ['Logs', '/apps/forecast/logs'],
      ['Traces', '/apps/forecast/traces'], ['Usage', '/apps/forecast/usage'], ['Schedules', '/apps/forecast/schedules'], ['Data', '/apps/forecast/data'],
    ]);
    assert.deepEqual([...h.host.querySelectorAll('.app-context-settings-links > a')].map(link => [link.dataset.settingsPage, link.getAttribute('href')]),
      ['general', 'environment', 'resources', 'scaling', 'access', 'privacy'].map(page => [page, `/apps/forecast/settings/${page}`]));
    assert.equal(h.host.querySelector('.app-context-settings').hidden, false);
    assert.equal(h.host.querySelector('[role="tab"], [role="tablist"]'), null);
    assert.equal(h.host.querySelectorAll('[aria-current="page"]').length, 1);
    assert.equal(h.host.querySelector('[aria-current="page"]').textContent, 'Overview');
    assert.equal(h.host.querySelector('.app-context-apps [aria-current="true"]').textContent, forecast.name);
  } finally { h.dom.window.close(); }
});

test('non-managers retain operational destinations while manager-only links and settings stay hidden', () => {
  const h = setup();
  try {
    h.update({ canManage: false, path: '/apps/forecast/traces' });
    assert.equal(h.host.querySelector('[data-app-section="logs"]').hidden, true);
    assert.equal(h.host.querySelector('[data-app-section="usage"]').hidden, true);
    assert.equal(h.host.querySelector('[data-app-section="data"]').hidden, true);
    assert.equal(h.host.querySelector('.app-context-settings').hidden, true);
    assert.equal(h.host.querySelector('[data-app-section="schedules"]').hidden, false);
    assert.equal(h.host.querySelector('[aria-current="page"]').textContent, 'Traces');
  } finally { h.dom.window.close(); }
});

test('active destinations follow accepted paths including settings, trailing slashes, and nested deployments', () => {
  const h = setup();
  try {
    for (const page of ['general', 'environment', 'resources', 'scaling', 'access', 'privacy']) {
      h.update({ path: `/apps/forecast/settings/${page}/?ignored=yes#section` });
      assert.equal(h.host.querySelectorAll('[aria-current="page"]').length, 1);
      assert.equal(h.host.querySelector('[aria-current="page"]').dataset.settingsPage, page);
    }
    h.update({ path: '/apps/forecast/deployments/42' });
    assert.equal(h.host.querySelector('[aria-current="page"]').textContent, 'Deployments');
    assert.equal(h.host.querySelector('[data-app-section="overview"]').hasAttribute('aria-current'), false);
  } finally { h.dom.window.close(); }
});

test('legacy and fallback destinations select the page that the renderer actually shows', () => {
  const h = setup();
  try {
    for (const [path, expected] of [
      ['/apps/forecast/overview', 'Overview'],
      ['/apps/forecast/configuration', 'General'],
      ['/apps/forecast/access', 'Access'],
      ['/apps/forecast/settings/unknown', 'General'],
      ['/apps/forecast/settings/res%6furces', 'Resources'],
    ]) {
      h.update({ path });
      assert.equal(h.host.querySelectorAll('[aria-current="page"]').length, 1);
      assert.equal(h.host.querySelector('[aria-current="page"]').textContent, expected);
    }
    h.update({ path: '/apps/forecast/settings/resources/nested' });
    assert.equal(h.host.querySelector('[aria-current="page"]'), null, 'a non-matching nested route is not a General page');
  } finally { h.dom.window.close(); }
});

test('same-app updates preserve DOM, focused links, disclosure state, and dirty badges', () => {
  const h = setup();
  try {
    h.update({ path: '/apps/forecast/settings/resources' });
    const resource = h.host.querySelector('[data-settings-page="resources"]');
    const switcher = h.host.querySelector('details');
    const other = h.host.querySelector('.app-context-apps a[href="/apps/revenue"]');
    resource.setAttribute('data-dirty', '');
    resource.setAttribute('aria-label', 'Resources (unsaved)');
    switcher.open = true;
    other.focus();
    h.update({ app: { ...forecast, name: 'Renamed forecast' }, apps: [revenue, forecast] });
    assert.equal(h.host.querySelector('[data-settings-page="resources"]'), resource);
    assert.equal(resource.hasAttribute('data-dirty'), true);
    assert.equal(resource.getAttribute('aria-label'), 'Resources (unsaved)');
    assert.equal(h.host.querySelector('details'), switcher);
    assert.equal(switcher.open, true);
    assert.equal(h.document.activeElement, other);
    assert.equal(h.host.querySelector('summary .app-context-label').textContent, 'Renamed forecast');
    resource.focus();
    h.update({ app: { slug: forecast.slug, name: forecast.name } });
    assert.equal(h.document.activeElement, resource);
    assert.equal(resource.hasAttribute('data-dirty'), true, 'an abbreviated app refresh is still the same app');
  } finally { h.dom.window.close(); }
});

test('app identity stays stable during refreshes and updates when the icon changes', () => {
  const h = setup();
  try {
    h.update();
    const summary = h.host.querySelector('summary');
    const monogram = summary.querySelector('.app-card-avatar');
    assert.equal(monogram.textContent, 'RF');
    assert.equal(monogram.getAttribute('aria-hidden'), 'true');
    assert.equal(summary.firstElementChild, monogram);
    h.update({ app: { ...forecast }, path: '/apps/forecast/logs' });
    assert.equal(summary.querySelector('.app-card-avatar'), monogram, 'unchanged identity must not replace its node');

    const uploaded = { ...forecast, icon_mime: 'image/png', updated_at: '2026-11-02T10:00:00Z' };
    h.update({ app: uploaded });
    const image = summary.querySelector('img.app-card-avatar');
    assert.ok(image);
    assert.equal(image.getAttribute('src'), '/api/apps/forecast/icon?v=2026-11-02T10%3A00%3A00Z');
    h.update({ app: { ...uploaded }, path: '/apps/forecast/traces' });
    assert.equal(summary.querySelector('.app-card-avatar'), image, 'background refresh preserves a loaded icon');
    image.dispatchEvent(new h.dom.window.Event('error'));
    const fallback = summary.querySelector('.app-card-avatar');
    assert.equal(fallback.textContent, 'RF');
    h.update({ app: { ...uploaded } });
    assert.equal(summary.querySelector('.app-card-avatar'), fallback, 'unchanged metadata does not repeatedly retry a failed image');

    h.update({ app: { ...uploaded, updated_at: '2026-11-02T11:00:00Z' } });
    assert.equal(summary.querySelector('img').getAttribute('src'), '/api/apps/forecast/icon?v=2026-11-02T11%3A00%3A00Z');
    h.update({ app: { ...forecast, icon_emoji: '🌦️' } });
    assert.equal(summary.querySelector('.app-card-avatar--emoji').textContent, '🌦️');
    h.update({ app: { ...forecast, name: 'Capacity planning' } });
    assert.equal(summary.querySelector('.app-card-avatar').textContent, 'CP');
    assert.equal(summary.querySelector('.app-context-label').textContent, 'Capacity planning');
    assert.equal(identityText(summary), 'Switch app, Capacity planning');
  } finally { h.dom.window.close(); }
});

test('switching apps changes destinations and clears the previous app’s dirty indicators', () => {
  const h = setup();
  try {
    h.update();
    const resource = h.host.querySelector('[data-settings-page="resources"]');
    resource.setAttribute('data-dirty', '');
    resource.setAttribute('aria-label', 'Resources (unsaved)');
    h.host.querySelector('details').open = true;
    h.update({ app: revenue, path: '/apps/revenue/settings/access' });
    assert.equal(resource.hasAttribute('data-dirty'), false);
    assert.equal(resource.getAttribute('aria-label'), 'Resources');
    assert.equal(resource.getAttribute('href'), '/apps/revenue/settings/resources');
    assert.equal(h.host.querySelector('summary .app-context-label').textContent, revenue.name);
    assert.equal(h.host.querySelector('details').open, false);
    assert.equal(h.host.querySelector('[aria-current="page"]').textContent, 'Access');
    resource.setAttribute('data-dirty', '');
    h.update({ app: { ...revenue, id: 99 } });
    assert.equal(resource.hasAttribute('data-dirty'), false, 'a replaced app cannot inherit draft badges from the reused slug');
  } finally { h.dom.window.close(); }
});

test('plain link activation calls the router once and leaves active state unchanged if navigation is vetoed', () => {
  const h = setup();
  try {
    h.update({ path: '/apps/forecast/settings/resources' });
    const logs = h.host.querySelector('[data-app-section="logs"]');
    const event = new h.dom.window.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 });
    logs.firstElementChild.dispatchEvent(event);
    assert.equal(event.defaultPrevented, true);
    assert.deepEqual(h.destinations, ['/apps/forecast/logs']);
    assert.equal(h.host.querySelector('[aria-current="page"]').dataset.settingsPage, 'resources');
    assert.equal(logs.hasAttribute('data-nav'), false, 'the shell router cannot handle the same click a second time');
  } finally { h.dom.window.close(); }
});

test('modified clicks and explicit new-tab targets retain normal link behavior', () => {
  const h = setup();
  try {
    h.update();
    const link = h.host.querySelector('[data-app-section="logs"]');
    // Cancel the native default after observing it, avoiding jsdom navigation.
    for (const options of [{ metaKey: true }, { ctrlKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }]) {
      let handled;
      const observe = event => { handled = event.defaultPrevented; event.preventDefault(); };
      h.document.addEventListener('click', observe, { once: true });
      link.dispatchEvent(new h.dom.window.MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...options }));
      assert.equal(handled, false);
    }
    link.target = '_blank';
    let handled;
    h.document.addEventListener('click', event => { handled = event.defaultPrevented; event.preventDefault(); }, { once: true });
    link.click();
    assert.equal(handled, false);
    assert.deepEqual(h.destinations, []);
  } finally { h.dom.window.close(); }
});

test('app names are rendered as text and slugs remain a single encoded URL segment', () => {
  const h = setup();
  try {
    const unusual = { slug: 'a/b ?#', name: '<img src=x onerror=alert(1)>' };
    h.update({ app: unusual, apps: [unusual] });
    assert.equal(h.host.querySelector('summary .app-context-label').textContent, unusual.name);
    assert.equal(h.host.querySelector('.app-context-label').title, unusual.name);
    assert.equal(h.host.querySelector('.app-context-apps a').title, unusual.name);
    assert.equal(h.host.querySelector('img'), null);
    assert.equal(h.host.querySelector('[data-app-section="logs"]').getAttribute('href'), '/apps/a%2Fb%20%3F%23/logs');
    assert.equal(h.host.querySelectorAll('.app-context-apps a').length, 1, 'the selected app is not duplicated');
  } finally { h.dom.window.close(); }
});

test('session clear removes private app identity, restores platform context, and can start fresh', () => {
  const h = setup();
  try {
    h.update();
    const resource = h.host.querySelector('[data-settings-page="resources"]');
    resource.setAttribute('data-dirty', '');
    resource.setAttribute('aria-label', 'Resources (unsaved)');
    h.controller.clear();
    assert.equal(h.host.hidden, true);
    assert.equal(h.document.getElementById('primary-nav').hidden, false);
    assert.equal(h.document.getElementById('sidebar').hasAttribute('data-context'), false);
    assert.equal(h.host.querySelector('summary').textContent, 'Switch app, ');
    assert.equal(h.host.querySelector('summary .app-card-avatar'), null);
    assert.equal(h.host.querySelector('.app-context-label').hasAttribute('title'), false);
    assert.equal(h.host.querySelector('summary').hasAttribute('aria-label'), false);
    assert.equal(resource.getAttribute('aria-label'), 'Resources');
    assert.equal(h.host.querySelectorAll('.app-context-apps a').length, 0);
    assert.equal(h.host.querySelectorAll('[aria-current]').length, 0);
    assert.equal(h.host.querySelector('[data-settings-page="resources"]').hasAttribute('href'), false);
    h.update({ app: revenue, apps: [revenue] });
    assert.equal(h.host.hidden, false);
    assert.equal(h.host.querySelector('summary .app-context-label').textContent, revenue.name);
    h.controller.update({ app: null });
    assert.equal(h.host.hidden, true);
  } finally { h.dom.window.close(); }
});

test('permission loss does not strand keyboard focus in hidden settings', () => {
  const h = setup();
  try {
    h.update();
    h.host.querySelector('[data-settings-page="privacy"]').focus();
    h.update({ canManage: false });
    assert.equal(h.document.activeElement, h.host.querySelector('[data-app-section="overview"]'));
  } finally { h.dom.window.close(); }
});
