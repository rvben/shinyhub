import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createSessionController } from '../static/views/session-controller.js';

function fixture(request, windowOverrides = {}) {
  let clock = 0;
  let sequence = 0;
  const timers = new Map();
  const doc = new EventTarget();
  doc.hidden = false;
  const win = Object.assign(new EventTarget(), windowOverrides);
  const calls = [];
  let expired = 0;
  const sessions = [];
  const controller = createSessionController({
    document: doc, window: win, now: () => clock,
    setTimer: (fn, ms) => { const id = ++sequence; timers.set(id, { fn, at: clock + ms }); return id; },
    clearTimer: id => timers.delete(id),
    request: async (...args) => { calls.push(args); return request(...args); },
    onExpired: () => expired++, onSession: session => sessions.push(session),
  });
  return {
    controller, calls, timers, sessions, doc, win,
    get expired() { return expired; },
    async advance(ms) {
      clock += ms;
      const due = [...timers.entries()].filter(([, item]) => item.at <= clock);
      for (const [id, item] of due) { if (timers.delete(id)) await item.fn(); }
      await Promise.resolve();
    },
    async event(target, type) {
      target.dispatchEvent(new Event(type));
      for (let i = 0; i < 8; i++) await Promise.resolve();
    },
  };
}

const payload = (seconds = 60) => ({ user: { id: 1 }, session: {
  server_time: '2026-01-01T00:00:00Z', expires_at: '2026-01-01T01:00:00Z', refresh_after_seconds: seconds,
} });
const ok = (body = payload()) => ({ status: 200, ok: true, json: async () => body });

test('renews using server timing despite a different browser clock', async () => {
  const f = fixture(async () => ok());
  f.controller.start(payload());
  await f.advance(59999);
  assert.equal(f.calls.length, 0);
  await f.advance(1);
  assert.equal(f.calls.length, 1);
  assert.equal(f.calls[0][0], '/api/auth/me');
  assert.equal(f.calls[0][1].cache, 'no-store');
  assert.equal(f.sessions.length, 1);
  await f.advance(60000);
  assert.equal(f.calls.length, 2);
  f.controller.stop();
  assert.equal(f.timers.size, 0);
});

test('hidden tabs stop renewing and immediately check after sleeping past renewal', async () => {
  const f = fixture(async () => ok());
  f.controller.start(payload());
  f.doc.hidden = true;
  await f.event(f.doc, 'visibilitychange');
  await f.advance(3600000);
  assert.equal(f.calls.length, 0);
  f.doc.hidden = false;
  await f.event(f.doc, 'visibilitychange');
  assert.equal(f.calls.length, 1);
  f.controller.stop();
});

test('even briefly returning to a tab verifies identity immediately', async () => {
  const f = fixture(async () => ok());
  f.controller.start(payload());
  await f.advance(30000);
  f.doc.hidden = true;
  await f.event(f.doc, 'visibilitychange');
  await f.advance(10000);
  f.doc.hidden = false;
  await f.event(f.doc, 'visibilitychange');
  assert.equal(f.calls.length, 1);
  await f.advance(20000);
  assert.equal(f.calls.length, 1);
  f.controller.stop();
});

for (const failure of [async () => { throw new Error('offline'); }, async () => ({ ok: false, status: 503 })]) {
  test('temporary failure retries without logging out or replacing user state', async () => {
    const f = fixture(failure);
    f.controller.start(payload());
    await f.advance(60000);
    assert.equal(f.expired, 0);
    assert.equal(f.sessions.length, 0);
    await f.advance(2000);
    assert.equal(f.calls.length, 2);
    f.controller.stop();
  });
}

test('a 401 ends renewal once and cancels future timers', async () => {
  const f = fixture(async () => ({ status: 401, ok: false }));
  f.controller.start(payload());
  await f.advance(60000);
  assert.equal(f.expired, 1);
  assert.equal(f.timers.size, 0);
  await f.event(f.win, 'online');
  await f.advance(60000);
  assert.equal(f.calls.length, 1);
});

test('renewal does not overlap and a stale response cannot restore a logged-out session', async () => {
  let resolve;
  const f = fixture(() => new Promise(r => { resolve = r; }));
  f.controller.start(payload());
  const work = f.controller.check();
  await f.controller.check();
  await f.event(f.win, 'online');
  assert.equal(f.calls.length, 1);
  f.controller.stop();
  assert.equal(f.calls[0][1].signal.aborted, true);
  resolve(ok());
  await work;
  assert.equal(f.sessions.length, 0);
  assert.equal(f.timers.size, 0);
});

test('restoring a cached page and coming online verify identity immediately', async () => {
  const f = fixture(async () => ok());
  f.controller.start(payload());
  await f.event(f.win, 'pageshow');
  await f.event(f.win, 'online');
  assert.equal(f.calls.length, 2);
  f.controller.stop();
});

test('timeout aborts renewal and retries without claiming the session ended', async () => {
  const f = fixture((_, { signal }) => new Promise((resolve, reject) => {
    signal.addEventListener('abort', () => reject(new Error('aborted')));
  }));
  f.controller.start(payload());
  const work = f.controller.check();
  await f.advance(15000);
  await work;
  assert.equal(f.calls[0][1].signal.aborted, true);
  assert.equal(f.expired, 0);
  assert.equal(f.timers.size, 1);
  f.controller.stop();
});

test('at the strict deadline it checks once instead of repeatedly renewing', async () => {
  const f = fixture(async () => ({ status: 401, ok: false }));
  f.controller.start({ user: { id: 1 }, session: {
    server_time: '2026-01-01T00:00:00Z', expires_at: '2026-01-01T00:00:10Z', refresh_after_seconds: 10,
  } });
  await f.advance(9999);
  assert.equal(f.calls.length, 0);
  await f.advance(1);
  assert.equal(f.expired, 1);
});

function channelFactory() {
  const peers = new Set();
  return class {
    constructor(name) { this.name = name; peers.add(this); }
    postMessage(data) {
      for (const peer of [...peers]) {
        if (peer !== this && peer.name === this.name) peer.onmessage?.({ data });
      }
    }
    close() { peers.delete(this); }
  };
}

for (const reason of ['explicit logout', 'authentication rejection']) {
  test(`${reason} notifies other tabs and stops their renewal`, async () => {
    const BroadcastChannel = channelFactory();
    const a = fixture(async () => ({ status: 401, ok: false }), { BroadcastChannel });
    const b = fixture(async () => ok(), { BroadcastChannel });
    a.controller.start(payload());
    b.controller.start(payload());
    if (reason === 'explicit logout') a.controller.end();
    else await a.controller.check();
    assert.equal(b.expired, 1);
    assert.equal(b.timers.size, 0);
    await b.advance(60000);
    assert.equal(b.calls.length, 0);
  });
}

test('forward-auth sessions are checked without inventing a browser token deadline', async () => {
  const f = fixture(async () => ok({ user: { id: 1 } }));
  f.controller.start({ user: { id: 1 } });
  await f.advance(300000);
  assert.equal(f.calls.length, 1);
  assert.equal(f.expired, 0);
  f.controller.stop();
});

test('an old failed request cannot change a newly started session', async () => {
  let reject;
  const f = fixture(() => new Promise((resolve, r) => { reject = r; }));
  f.controller.start(payload());
  const work = f.controller.check();
  f.controller.start(payload(120));
  reject(new Error('old failure'));
  await work;
  assert.equal(f.timers.size, 1);
  assert.equal([...f.timers.values()][0].at, 120000);
  f.controller.stop();
});

test('a browser that denies BroadcastChannel still renews normally', async () => {
  const f = fixture(async () => ok(), { BroadcastChannel: class { constructor() { throw new Error('privacy mode'); } } });
  f.controller.start(payload());
  await f.advance(60000);
  assert.equal(f.calls.length, 1);
  assert.equal(f.sessions.length, 1);
  f.controller.end();
  assert.equal(f.timers.size, 0);
});
