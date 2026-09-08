import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { createRouter } from '../static/router.js';
import { startAuthenticatedRouter } from '../static/auth-navigation.js';

function setup(path) {
  const dom = new JSDOM('<main></main>', { url: 'http://localhost' + path });
  for (const key of ['window', 'document', 'history', 'location']) global[key] = key === 'window' ? dom.window : dom.window[key];
  const destinations = [];
  const win = { history, location: {
    get search() { return location.search; }, get pathname() { return location.pathname; },
    get hash() { return location.hash; }, origin: location.origin,
    replace: path => destinations.push(path),
  } };
  const mounted = [];
  const router = createRouter();
  router.register('/', () => { mounted.push('/'); });
  router.register('/apps/:slug', p => { mounted.push(p.slug); });
  return { dom, win, router, destinations, mounted };
}

for (const entry of ['/login', '/']) {
  test(`authenticated ${entry} returns directly to the requested proxy document`, async () => {
    const path = '/app/demo/nested?filter=a%20b#plot';
    const t = setup(entry + '?next=' + encodeURIComponent(path));
    try {
      assert.equal(await startAuthenticatedRouter(t.router, t.win), true);
      assert.deepEqual(t.destinations, [path]);
      assert.deepEqual(t.mounted, []);
      assert.equal(location.search, '');
    } finally { t.dom.window.close(); }
  });
}

test('a SPA return path mounts once with query and fragment preserved', async () => {
  const t = setup('/login?next=' + encodeURIComponent('/apps/demo?tab=logs#latest'));
  try {
    assert.equal(await startAuthenticatedRouter(t.router, t.win), false);
    assert.deepEqual(t.mounted, ['demo']);
    assert.equal(location.pathname + location.search + location.hash, '/apps/demo?tab=logs#latest');
    assert.deepEqual(t.destinations, []);
  } finally { t.dom.window.close(); }
});

for (const next of ['', '/', '/login', '/login?next=/app/demo/', '//example.com/', '/\\example.com/', '/\n/example.com/', 'https://example.com/']) {
  test(`invalid or looping return path ${JSON.stringify(next)} falls back safely`, async () => {
    const t = setup('/login?next=' + encodeURIComponent(next));
    try {
      assert.equal(await startAuthenticatedRouter(t.router, t.win), false);
      assert.deepEqual(t.destinations, []);
      assert.deepEqual(t.mounted, ['/']);
      assert.equal(location.pathname + location.search, '/');
    } finally { t.dom.window.close(); }
  });
}

test('normal startup preserves deploy intent and unrelated query parameters', async () => {
  const t = setup('/?source=cli#deploy=demo');
  try {
    await startAuthenticatedRouter(t.router, t.win);
    assert.deepEqual(t.mounted, ['/']);
    assert.equal(location.search + location.hash, '?source=cli#deploy=demo');
  } finally { t.dom.window.close(); }
});
