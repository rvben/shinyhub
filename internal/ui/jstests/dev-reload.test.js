import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

const script = readFileSync(new URL('../../proxy/assets/devreload.js', import.meta.url), 'utf8');
function browser() {
  const timers = new Map();
  const events = {};
  let next = 0;
  let reloads = 0;
  let response = {ok: true, text: async () => 'session:1'};
  const requests = [];
  const context = {
    document: {currentScript: {dataset: {url: '/app/demo/__shinyhub_dev_revision', revision: 'session:1'}}},
    window: {
      location: {reload() { reloads++; }},
      addEventListener(name, handler) { events[name] = handler; },
    },
    AbortSignal: {timeout() { return 'signal'; }},
    async fetch(url, options) {
      requests.push({url, options});
      if (response instanceof Error) throw response;
      return response;
    },
    setTimeout(handler) { timers.set(++next, handler); return next; },
    clearTimeout(id) { timers.delete(id); },
  };
  vm.runInNewContext(script, context);
  return {
    events, requests,
    get reloads() { return reloads; },
    get pending() { return timers.size; },
    respond(value) { response = value; },
    async tick() {
      const [id, handler] = timers.entries().next().value;
      timers.delete(id);
      await handler();
    },
  };
}

test('refresh only on a changed healthy revision; retry failures without reloading', async () => {
  const b = browser();
  await b.tick();
  assert.equal(b.reloads, 0);
  assert.equal(b.requests[0].options.cache, 'no-store');
  b.respond(new Error('offline'));
  await b.tick();
  b.respond({ok: false, text: async () => 'error page'});
  await b.tick();
  b.respond({ok: true, text: async () => ''});
  await b.tick();
  assert.equal(b.reloads, 0);
  b.respond({ok: true, text: async () => 'session:2'});
  await b.tick();
  assert.equal(b.reloads, 1);
  assert.equal(b.pending, 0);
});

test('stop polling on navigation and check a restored document against its original revision', async () => {
  const b = browser();
  b.events.pagehide();
  assert.equal(b.pending, 0);
  b.respond({ok: true, text: async () => 'new-session:1'});
  b.events.pageshow({persisted: true});
  // Flush fetch and response.text promises from the event handler.
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(b.reloads, 1);
});
