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

for (const body of [null, {}, {items:null}]) test(`invalid app list is an error, never an empty fleet: ${JSON.stringify(body)}`, async () => {
  fixture();let rendered=false;
  const {ctx,errors}=baseCtx({state:{apps:[{slug:'retained'}]},api:async()=>({ok:true,status:200,json:async()=>body}),applyGridFilters:()=>{rendered=true;}});
  await mountAppsGrid(ctx);
  assert.equal(rendered,false);assert.equal(ctx.state.apps[0].slug,'retained');assert.match(errors.at(-1),/retry/);
});

test('a JSON decoding failure preserves the existing list and offers a retry', async () => {
  fixture();const {ctx,errors}=baseCtx({state:{apps:[{slug:'retained'}]},api:async()=>({ok:true,status:200,json:async()=>{throw new SyntaxError('truncated');}})});
  await mountAppsGrid(ctx);assert.match(errors.at(-1),/Refresh/);assert.equal(ctx.state.apps[0].slug,'retained');
});
