import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { windowCount, showMoreLabel, createShowMore, GRID_PAGE, SIDEBAR_PAGE } from '../static/views/render-window.js';
import { renderSidebarApps, highlightSidebarApp } from '../static/views/sidebar-nav.js';
import { appCardBadge } from '../static/views/app-card-badge.js';

const badgeFor = (a) => appCardBadge(a, (s) => s);

function fleet(n) {
  return Array.from({ length: n }, (_, i) => ({
    slug: `app-${String(i).padStart(4, '0')}`,
    name: `App ${String(i).padStart(4, '0')}`,
    deploy_count: 1,
    status: 'stopped',
    project_slug: 'default',
  }));
}

function doc() {
  return new JSDOM('<!DOCTYPE html><div id="c"></div>').window.document;
}

test('windowCount: a group that fits in one page renders whole', () => {
  assert.equal(windowCount(0, undefined, 48), 0);
  assert.equal(windowCount(12, undefined, 48), 12);
  assert.equal(windowCount(48, undefined, 48), 48);
});

test('windowCount: a long group renders one page until asked for more, never past the end', () => {
  assert.equal(windowCount(2000, undefined, 48), 48);
  assert.equal(windowCount(2000, 96, 48), 96);
  assert.equal(windowCount(100, 144, 48), 100);
});

test('windowCount: a pinned index beyond the window widens it to the page that holds it', () => {
  assert.equal(windowCount(2000, undefined, 50, 49), 50);
  assert.equal(windowCount(2000, undefined, 50, 50), 100);
  assert.equal(windowCount(2000, undefined, 50, 999), 1000);
  assert.equal(windowCount(2000, undefined, 50, 1999), 2000);
});

test('showMoreLabel names the next step and the remainder', () => {
  assert.equal(showMoreLabel(1952, 48), 'Show 48 more (1,952 not shown)');
  assert.equal(showMoreLabel(3, 48), 'Show 3 more (3 not shown)');
  assert.equal(showMoreLabel(1952, 48, true), 'Show 48 more');
});

test('createShowMore returns null when nothing is hidden', () => {
  const d = doc();
  assert.equal(createShowMore(d, { hidden: 0, page: 48, className: 'x', onMore() {} }), null);
  const b = createShowMore(d, { hidden: 5, page: 48, className: 'x', focusKey: 'k', onMore() {} });
  assert.equal(b.tagName, 'BUTTON');
  assert.equal(b.type, 'button');
  assert.equal(b.dataset.focusKey, 'k');
});

test('sidebar: a 2000-app group renders one page of rows plus a Show more button', () => {
  const d = doc();
  const c = d.getElementById('c');
  renderSidebarApps(c, fleet(2000), '/apps', badgeFor, d);
  assert.equal(c.querySelectorAll('a.sidebar-app').length, SIDEBAR_PAGE);
  const more = c.querySelectorAll('button.sidebar-show-more');
  assert.equal(more.length, 1);
  assert.equal(more[0].textContent, `Show ${SIDEBAR_PAGE} more`);
  assert.equal(more[0].getAttribute('aria-label'), `Show ${SIDEBAR_PAGE} more (1,950 not shown)`);
  // The group heading still counts the whole group, not the rendered page.
  assert.match(c.querySelector('.sidebar-project-group-toggle').getAttribute('aria-label'), /2000 apps/);
});

test('sidebar: a small fleet renders exactly as before, with no button', () => {
  const d = doc();
  const c = d.getElementById('c');
  renderSidebarApps(c, fleet(SIDEBAR_PAGE), '/apps', badgeFor, d);
  assert.equal(c.querySelectorAll('a.sidebar-app').length, SIDEBAR_PAGE);
  assert.equal(c.querySelector('button.sidebar-show-more'), null);
});

test('sidebar: Show more reveals the next page, focuses its first row, and survives a rebuild', () => {
  const d = doc();
  const c = d.getElementById('c');
  const apps = fleet(2000);
  renderSidebarApps(c, apps, '/apps', badgeFor, d);
  c.querySelector('button.sidebar-show-more').click();
  assert.equal(c.querySelectorAll('a.sidebar-app').length, SIDEBAR_PAGE * 2);
  assert.equal(d.activeElement.getAttribute('data-app-slug'), apps[SIDEBAR_PAGE].slug);
  renderSidebarApps(c, apps, '/apps', badgeFor, d);
  assert.equal(c.querySelectorAll('a.sidebar-app').length, SIDEBAR_PAGE * 2);
});

test('sidebar: a deep link to an app beyond the first page still renders and highlights its row', () => {
  const d = doc();
  const c = d.getElementById('c');
  const apps = fleet(2000);
  renderSidebarApps(c, apps, `/apps/${apps[1234].slug}/logs`, badgeFor, d);
  const active = c.querySelectorAll('a.sidebar-app.active');
  assert.equal(active.length, 1);
  assert.equal(active[0].getAttribute('data-app-slug'), apps[1234].slug);
});

test('highlightSidebarApp reports whether the route matched a rendered row', () => {
  const d = doc();
  const c = d.getElementById('c');
  const apps = fleet(2000);
  renderSidebarApps(c, apps, '/apps', badgeFor, d);
  assert.equal(highlightSidebarApp(c, `/apps/${apps[3].slug}`), true);
  assert.equal(highlightSidebarApp(c, `/apps/${apps[1500].slug}`), false);
});

test('GRID_PAGE fills whole rows at 2, 3 and 4 columns', () => {
  for (const cols of [2, 3, 4]) assert.equal(GRID_PAGE % cols, 0);
});
