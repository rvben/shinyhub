import { afterEach, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { mountLaunchpad } from '../static/views/launchpad.js';
let dom, mounted, poll, originalSetInterval, originalClearInterval;
const flush = () => new Promise(r => setImmediate(r));
beforeEach(() => {
  dom = new JSDOM('<main id="launchpad-view"><div id="launchpad-body"></div></main>', { url: 'http://localhost/apps', pretendToBeVisual: true });
  for (const key of ['window', 'document', 'location', 'localStorage']) globalThis[key] = key === 'window' ? dom.window : dom.window[key];
  originalSetInterval = globalThis.setInterval; originalClearInterval = globalThis.clearInterval;
  globalThis.setInterval = fn => { poll = fn; return 1; }; globalThis.clearInterval = () => {};
});
afterEach(() => {
  mounted?.unmount(); mounted = null; dom.window.close();
  globalThis.setInterval = originalSetInterval; globalThis.clearInterval = originalClearInterval;
  for (const key of ['window', 'document', 'location', 'localStorage']) delete globalThis[key];
});
function mount(getApps) {
  mounted = mountLaunchpad({ state: { user: { id: 1 } }, updateActiveNav() {}, onUnauthorized() {},
    api: async () => ({ ok: true, json: async () => ({ items: await getApps() }) }) });
}
const apps = () => [{ slug: 'alpha', name: 'Alpha', status: 'running', description: 'Forecasts' }, { slug: 'beta', name: 'Beta', status: 'running' }];
test('unchanged polls preserve the focused app node', async () => {
  mount(apps); await flush(); const tile = document.querySelector('a.lp-tile'); tile.focus(); poll(); await flush();
  assert.equal(document.activeElement, tile); assert.equal(tile.isConnected, true);
});
test('changed polls preserve the selected app and search query', async () => {
  let data = apps(); mount(() => data); await flush();
  const search = document.querySelector('.lp-search'); search.value = 'Alpha'; search.dispatchEvent(new window.Event('input'));
  document.querySelector('a.lp-tile').focus(); data = [{ ...data[0], description: 'Updated forecasts' }, data[1]]; poll(); await flush();
  assert.match(document.activeElement.href, /alpha/); assert.equal(search.value, 'Alpha');
  assert.match(document.querySelector('[role=status][data-results-status]').textContent, /1 app matches/);
});
test('removing a focused app moves focus to another available app', async () => {
  let data = apps(); mount(() => data); await flush(); document.querySelector('a.lp-tile').focus();
  data = [data[1]]; poll(); await flush(); assert.match(document.activeElement.href, /beta/);
  assert.match(document.querySelector('[data-results-status]').textContent, /previous selection/);
});
test('failed polls retain apps, show a retry and recover without losing focus', async () => {
  let fail = false; mount(() => { if (fail) throw Error('offline'); return apps(); }); await flush();
  fail = true; poll(); await flush(); assert.equal(document.querySelectorAll('a.lp-tile').length, 2);
  const notice = document.querySelector('.lp-stale'); assert.equal(notice.hidden, false);
  fail = false; notice.querySelector('button').focus(); notice.querySelector('button').click(); await flush();
  assert.equal(notice.hidden, true); assert.equal(document.activeElement, document.querySelector('.lp-search'));
});
test('empty list retry recovery focuses the empty-state heading', async () => {
  let fail = false; mount(() => { if (fail) throw Error('offline'); return []; }); await flush();
  fail = true; poll(); await flush(); const retry = document.querySelector('.lp-stale button'); retry.focus();
  fail = false; retry.click(); await flush(); assert.equal(document.activeElement, document.querySelector('.lp-empty-title'));
});
test('search announces zero results without moving input focus', async () => {
  mount(apps); await flush(); const search = document.querySelector('.lp-search'); search.focus(); search.value = 'Nothing'; search.dispatchEvent(new window.Event('input'));
  assert.match(document.querySelector('[data-results-status]').textContent, /0 apps match/); assert.equal(document.activeElement, search);
});
