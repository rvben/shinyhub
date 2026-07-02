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
