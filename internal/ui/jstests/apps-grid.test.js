import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { mountAppsGrid } from '../static/views/apps-grid.js';

function fixture() {
  const dom = new JSDOM(`<!DOCTYPE html><body>
    <section id="apps-view" hidden>
      <div id="app-grid"></div>
      <div id="empty-state"></div>
    </section>
  </body>`);
  global.document = dom.window.document;
  return dom;
}

function baseCtx(overrides) {
  const errors = [];
  const ctx = {
    state: {},
    metrics: { setTargets() {} },
    api: async () => ({ ok: true, status: 200, json: async () => [] }),
    onUnauthorized() {},
    renderGridVerbatim() {},
    showError: (m) => errors.push(m),
    ...overrides,
  };
  return { ctx, errors };
}

test('a network failure surfaces an error instead of a silent empty grid', async () => {
  fixture();
  const { ctx, errors } = baseCtx({
    api: async () => {
      throw new Error('network down');
    },
  });
  await mountAppsGrid(ctx);
  assert.equal(errors.length, 1, 'showError must be called on fetch failure');
  assert.match(errors[0], /load|retry|refresh/i);
});

test('a non-OK response surfaces an error', async () => {
  fixture();
  const { ctx, errors } = baseCtx({
    api: async () => ({ ok: false, status: 500, json: async () => ({}) }),
  });
  await mountAppsGrid(ctx);
  assert.equal(errors.length, 1, 'showError must be called on a non-OK response');
});

test('a successful load clears any prior error', async () => {
  fixture();
  const { ctx, errors } = baseCtx({
    api: async () => ({ ok: true, status: 200, json: async () => [] }),
  });
  await mountAppsGrid(ctx);
  // The last showError call must clear the banner (empty string).
  assert.equal(errors[errors.length - 1], '', 'a successful load must clear the error banner');
});

test('a 401 delegates to onUnauthorized and does not raise an error banner', async () => {
  fixture();
  let unauth = 0;
  const { ctx, errors } = baseCtx({
    api: async () => ({ ok: false, status: 401, json: async () => ({}) }),
    onUnauthorized: () => unauth++,
  });
  await mountAppsGrid(ctx);
  assert.equal(unauth, 1);
  assert.equal(errors.filter((e) => e).length, 0, 'a 401 is an auth redirect, not an error banner');
});
