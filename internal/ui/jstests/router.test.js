import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { createRouter } from '../static/router.js';

// router.js uses the window/document/history/location globals directly, so the
// test installs a JSDOM environment onto the globals before driving the router.
function withDom(path = '/') {
  const dom = new JSDOM('<!DOCTYPE html><body><main></main></body>', {
    url: 'http://localhost' + path,
  });
  global.window = dom.window;
  global.document = dom.window.document;
  global.history = dom.window.history;
  global.location = dom.window.location;
  return dom;
}

test('a throwing mount is caught, reported to onError, and does not reject', async () => {
  withDom('/boom');
  const errors = [];
  const router = createRouter({ onError: (err) => errors.push(err) });
  router.register('/boom', () => {
    throw new Error('kaboom');
  });
  await assert.doesNotReject(router.start());
  assert.equal(errors.length, 1);
  assert.match(String(errors[0]), /kaboom/);
});

test('an async-rejecting mount is caught and reported to onError', async () => {
  withDom('/boom');
  const errors = [];
  const router = createRouter({ onError: (err) => errors.push(err) });
  router.register('/boom', async () => {
    throw new Error('async-boom');
  });
  await assert.doesNotReject(router.start());
  assert.equal(errors.length, 1);
  assert.match(String(errors[0]), /async-boom/);
});

test('a healthy route still mounts normally when onError is provided', async () => {
  withDom('/ok');
  let mounted = false;
  const router = createRouter({ onError: () => {} });
  router.register('/ok', () => {
    mounted = true;
    return { title: 'OK' };
  });
  await router.start();
  assert.ok(mounted, 'healthy mount function must run');
});

test('onMounted fires on a successful mount so a prior error state can be cleared', async () => {
  withDom('/ok');
  let mountedCalls = 0;
  const router = createRouter({ onError: () => {}, onMounted: () => mountedCalls++ });
  router.register('/ok', () => ({ title: 'OK' }));
  await router.start();
  assert.equal(mountedCalls, 1, 'onMounted must be called after a successful mount');
});

test('onMounted is NOT called when the mount throws', async () => {
  withDom('/boom');
  let mountedCalls = 0;
  const router = createRouter({ onError: () => {}, onMounted: () => mountedCalls++ });
  router.register('/boom', () => {
    throw new Error('x');
  });
  await router.start();
  assert.equal(mountedCalls, 0, 'onMounted must not fire on a failed mount');
});

// ---------------------------------------------------------------------------
// Same-view updates. Routes that share a key (the app-detail tab routes) must
// hand a navigation to the mounted view instead of tearing it down: a tab is
// one region of a page, not a page.
// ---------------------------------------------------------------------------

// A stand-in for the app-detail view: unmount() hides the section, exactly as
// the real one does, so a test can assert on the symptom (a blank frame) and
// not merely on the call count.
function fakeDetailView(record, title = 'demo') {
  return {
    title,
    unmount() {
      record.unmounts++;
      record.hidden = true;
    },
    update(params) {
      record.updates.push(params);
    },
  };
}

function detailRouter(record, routerOpts = {}) {
  const router = createRouter(routerOpts);
  const key = (p) => 'app-detail:' + p.slug;
  const mountFn = (params) => {
    record.mounts++;
    record.mountParams.push(params);
    record.hidden = false;
    return record.view || fakeDetailView(record);
  };
  router.register('/apps/:slug', mountFn, { key, params: (p) => ({ ...p, tab: 'overview' }) });
  router.register('/apps/:slug/:tab', mountFn, { key });
  return router;
}

function newRecord() {
  return { mounts: 0, unmounts: 0, updates: [], mountParams: [], hidden: true, view: null };
}

// Poll until cond() holds, so a test never depends on a guessed delay.
async function waitFor(cond, what, timeoutMs = 2000) {
  const deadline = Date.now() + timeoutMs;
  while (!cond()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
}

test('a tab switch within the same app updates the view instead of remounting it', async () => {
  withDom('/apps/demo');
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();
  await router.navigate('/apps/demo/logs');

  assert.equal(rec.mounts, 1, 'the view must be mounted exactly once');
  assert.equal(rec.unmounts, 0, 'a tab switch must not unmount the view');
  assert.equal(rec.hidden, false, 'the view must never be hidden during a tab switch');
  assert.deepEqual(rec.updates, [{ slug: 'demo', tab: 'logs' }]);
});

test('a tab switch does not move focus to the section heading', async () => {
  const dom = withDom('/apps/demo');
  dom.window.document.querySelector('main').innerHTML = '<section><h1>demo</h1></section>';
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();
  const h1 = dom.window.document.querySelector('h1');
  assert.equal(dom.window.document.activeElement, h1, 'a fresh mount focuses the heading');

  h1.blur();
  await router.navigate('/apps/demo/logs');
  assert.notEqual(
    dom.window.document.activeElement,
    h1,
    'a tab switch must leave focus where the visitor put it, not re-announce the page',
  );
});

test('navigating to a different app remounts rather than updating', async () => {
  withDom('/apps/demo');
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();
  await router.navigate('/apps/other/logs');

  assert.equal(rec.mounts, 2, 'a different app is a different page and must remount');
  assert.equal(rec.unmounts, 1, 'the previous app view must be unmounted');
  assert.deepEqual(rec.updates, [], 'update() is only for navigations within the same view');
});

test('back/forward between tabs takes the update path too', async () => {
  withDom('/apps/demo');
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();
  await router.navigate('/apps/demo/logs');
  await router.navigate('/apps/demo/access');
  assert.equal(rec.updates.length, 2);

  // popstate is delivered asynchronously by jsdom. Wait for the update it
  // produces rather than for a fixed delay, which turns a slow CI box into a
  // spurious failure.
  global.history.back();
  await waitFor(() => rec.updates.length === 3, 'the popstate update to arrive');
  assert.equal(rec.mounts, 1, 'going back to a sibling tab must not remount');
  assert.equal(rec.unmounts, 0);
  assert.equal(rec.updates.at(-1).tab, 'logs');
});

test('the params normalizer feeds the mount and the update alike', async () => {
  withDom('/apps/demo/logs');
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();
  assert.deepEqual(rec.mountParams, [{ slug: 'demo', tab: 'logs' }]);

  await router.navigate('/apps/demo');
  assert.deepEqual(
    rec.updates,
    [{ slug: 'demo', tab: 'overview' }],
    'the bare app route must resolve to the overview tab for update() as it does for mount()',
  );
});

test('a route without a key keeps the remount-every-navigation behavior', async () => {
  withDom('/a');
  let mounts = 0;
  let unmounts = 0;
  const router = createRouter({});
  const mountFn = () => {
    mounts++;
    return { title: 'x', unmount: () => unmounts++, update: () => {} };
  };
  router.register('/a', mountFn);
  router.register('/b', mountFn);
  await router.start();
  await router.navigate('/b');
  assert.equal(mounts, 2, 'without a key the router must not use the update path');
  assert.equal(unmounts, 1);
});

test('a failing update is reported and the next navigation remounts a clean view', async () => {
  withDom('/apps/demo');
  const errors = [];
  const rec = newRecord();
  rec.view = {
    title: 'demo',
    unmount() {
      rec.unmounts++;
      rec.hidden = true;
    },
    update() {
      throw new Error('update-boom');
    },
  };
  const router = detailRouter(rec, { onError: (err) => errors.push(err) });
  await router.start();

  await assert.doesNotReject(router.navigate('/apps/demo/logs'));
  assert.equal(errors.length, 1);
  assert.match(String(errors[0]), /update-boom/);

  // showRouteError hides every page section, so a view that failed to update
  // can no longer be trusted to be on screen. The next navigation must take the
  // full mount path, which unmounts it and rebuilds a visible one.
  await router.navigate('/apps/demo/access');
  assert.equal(rec.mounts, 2, 'after a failed update the next navigation must remount');
  assert.equal(rec.unmounts, 1, 'the stale view must be unmounted so its cleanup runs');
});

test('an update superseded by a later navigation does not clobber the newer view', async () => {
  withDom('/apps/demo');
  const rec = newRecord();
  let release;
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  let mountedCalls = 0;
  rec.view = {
    title: 'demo',
    unmount() {
      rec.unmounts++;
    },
    async update() {
      await gate;
    },
  };
  const router = detailRouter(rec, { onMounted: () => mountedCalls++ });
  await router.start();
  mountedCalls = 0;

  const slow = router.navigate('/apps/demo/logs');
  await router.navigate('/apps/other');
  release();
  await slow;

  assert.equal(mountedCalls, 1, 'only the winning navigation may report a mount');
  assert.equal(global.location.pathname, '/apps/other');
});

// ---------------------------------------------------------------------------
// Navigating to the URL already on screen. A nav item for the current page is
// the one link a visitor clicks by accident, and it must cost nothing: no
// teardown, no refetch, no focus grab, and no duplicate history entry.
// ---------------------------------------------------------------------------

test('navigating to the current URL neither remounts nor updates the view', async () => {
  withDom('/apps/demo/logs');
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();

  await router.navigate('/apps/demo/logs');

  assert.equal(rec.mounts, 1, 'the page is already on screen; nothing to mount');
  assert.equal(rec.unmounts, 0, 'nothing to tear down either');
  assert.deepEqual(rec.updates, [], 'and nothing changed for the view to update to');
});

test('navigating to the current URL pushes no history entry', async () => {
  withDom('/apps/demo');
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();
  await router.navigate('/apps/demo/logs');
  const depth = global.history.length;

  await router.navigate('/apps/demo/logs');
  assert.equal(global.history.length, depth, 'a no-op navigation must not stack a duplicate entry');

  // The proof that it is not stacked: one Back returns to the previous tab
  // rather than to the same page again.
  global.history.back();
  await waitFor(() => rec.updates.length === 2, 'the popstate update to arrive');
  assert.equal(global.location.pathname, '/apps/demo');
});

test('a route on the current URL still remounts after its mount failed', async () => {
  // The mount threw, so onError blanked the sections: the page named by the URL
  // is not on screen and clicking its nav item again is a retry, not a no-op.
  withDom('/flaky');
  let attempts = 0;
  const router = createRouter({ onError: () => {} });
  router.register('/flaky', () => {
    attempts++;
    if (attempts === 1) throw new Error('first mount fails');
    return { title: 'flaky' };
  });
  await router.start();
  assert.equal(attempts, 1);

  await router.navigate('/flaky');
  assert.equal(attempts, 2, 'the retry must reach the mount function');
});

test('a same-URL navigation does not consult the unsaved-changes guard', async () => {
  // The guard exists to ask about work in progress before leaving a page.
  // Nothing is being left, so nothing may be asked.
  withDom('/apps/demo');
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();
  let asked = 0;
  router.setNavGuard(() => {
    asked++;
    return true;
  });

  await router.navigate('/apps/demo');
  assert.equal(asked, 0, 'staying put must not prompt about unsaved changes');

  await router.navigate('/apps/demo/logs');
  assert.equal(asked, 1, 'an actual navigation still consults the guard');
});

test('a replace navigation to the current URL is a no-op too', async () => {
  // Internal redirects land on the URL already showing (a bounced tab that
  // resolves back to where the visitor is). Remounting would blank the page
  // they are looking at.
  withDom('/apps/demo/logs');
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();

  await router.navigate('/apps/demo/logs', { replace: true });
  assert.equal(rec.mounts, 1);
  assert.deepEqual(rec.updates, []);
});

test('the search string is part of the comparison', async () => {
  withDom('/apps/demo');
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();

  await router.navigate('/apps/demo', { search: '?from=grid' });
  assert.equal(global.location.search, '?from=grid', 'a different query is a different URL');
  assert.equal(rec.updates.length, 1, 'and must reach the view');
});

test('the document title survives a tab switch', async () => {
  withDom('/apps/demo');
  const rec = newRecord();
  const router = detailRouter(rec);
  await router.start();
  const afterMount = global.document.title;
  await router.navigate('/apps/demo/logs');
  assert.equal(global.document.title, afterMount, 'a tab switch must keep the page title');
  assert.match(afterMount, /^demo · /);
});
