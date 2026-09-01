import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  createSupportSessionRecovery,
  supportSessionRemaining,
  supportSessionThreshold,
} from '../static/views/support-session-recovery.js';

function response(status, body = null) {
  return {
    status,
    ok: status >= 200 && status < 300,
    async json() { return body; },
  };
}

function fixture(request, options = {}) {
  const dom = new JSDOM(`<!doctype html><body>
    <section id="support-session-recovery" hidden>
      <h2 data-support-current-heading>Active support session</h2>
      <p data-support-current-details>
      <strong data-support-current-subject></strong>
      <code data-support-current-app></code>
      </p>
      <p data-support-current-unavailable hidden></p>
      <p data-support-current-meta>
      <span data-support-current-phase></span>
      <time data-support-current-deadline><span data-support-current-countdown></span></time>
      </p>
      <p data-support-current-error hidden></p>
      <a data-support-current-resume hidden></a>
      <button data-support-current-retry hidden>Retry status</button>
      <button data-support-current-end>End session</button>
    </section>
    <p id="support-session-recovery-status"></p>
  </body>`, { url: 'https://hub.example.com/' });
  const root = dom.window.document.getElementById('support-session-recovery');
  const intervals = [];
  const recovery = createSupportSessionRecovery({
    root,
    request,
    setIntervalFn(fn) { intervals.push(fn); return intervals.length; },
    clearIntervalFn() {},
    ...options,
  });
  return { dom, root, recovery, intervals };
}

test('support-session countdown is stable and announces only meaningful thresholds', () => {
  assert.deepEqual(supportSessionRemaining('2026-09-01T12:05:00Z', Date.parse('2026-09-01T12:00:00Z')), {
    seconds: 300, label: '5:00',
  });
  assert.equal(supportSessionThreshold(301, 300), 'Five minutes remain in the support session.');
  assert.equal(supportSessionThreshold(300, 299), '');
  assert.equal(supportSessionThreshold(61, 60), 'One minute remains in the support session.');
  assert.equal(supportSessionThreshold(1, 0), 'The support session has expired.');
});

test('active recovery shows redacted context, resumes safely, and ends in place', async () => {
  let currentTime = Date.parse('2026-09-01T12:00:00Z');
  const calls = [];
  const view = fixture(async (path, options = {}) => {
    calls.push([path, options.method || 'GET']);
    if (options.method === 'DELETE') return response(204);
    return response(200, { active: {
      subject_username: 'Alice Example With A Very Long Name',
      app_slug: 'quarterly-sales-analysis',
      app_url: 'https://apps.example.com/app/quarterly-sales-analysis/',
      started_at: '2026-09-01T11:59:00Z',
      expires_at: '2026-09-01T12:06:00Z',
      remaining_seconds: 360,
      resumable: true,
    } });
  }, { now: () => currentTime });

  await view.recovery.load({ announce: true });
  assert.equal(view.root.hidden, false);
  assert.equal(view.root.querySelector('[data-support-current-subject]').textContent, 'Alice Example With A Very Long Name');
  assert.equal(view.root.querySelector('[data-support-current-countdown]').textContent, '6:00');
  assert.equal(view.root.querySelector('[data-support-current-resume]').href, 'https://apps.example.com/app/quarterly-sales-analysis/');
  assert.match(view.dom.window.document.getElementById('support-session-recovery-status').textContent, /Alice Example/);

  currentTime = Date.parse('2026-09-01T12:01:00Z');
  view.intervals[0]();
  assert.equal(view.root.querySelector('[data-support-current-countdown]').textContent, '5:00');
  assert.match(view.dom.window.document.getElementById('support-session-recovery-status').textContent, /Five minutes/);

  view.root.querySelector('[data-support-current-end]').click();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.deepEqual(calls.at(-1), ['/api/support-sessions/current', 'DELETE']);
  assert.equal(view.root.hidden, true);
  assert.match(view.dom.window.document.getElementById('support-session-recovery-status').textContent, /ended/);
  view.dom.window.close();
});

test('pending launch cannot be resumed and a failed end remains recoverable', async () => {
  let failDelete = true;
  const view = fixture(async (_path, options = {}) => {
    if (options.method === 'DELETE') {
      if (failDelete) throw new Error('offline');
      return response(204);
    }
    return response(200, { active: {
      subject_username: 'alice', app_slug: 'sales', expires_at: '2099-01-01T00:00:00Z',
      remaining_seconds: 600, resumable: false,
    } });
  });

  await view.recovery.load();
  assert.equal(view.root.querySelector('[data-support-current-resume]').hidden, true);
  const end = view.root.querySelector('[data-support-current-end]');
  end.click();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(view.root.hidden, false);
  assert.equal(end.disabled, false);
  assert.equal(end.textContent, 'Try ending again');
  assert.match(view.root.querySelector('[data-support-current-error]').textContent, /automatic expiry remains in force/);

  failDelete = false;
  end.click();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(view.root.hidden, true);
  view.dom.window.close();
});

test('another administrator with no current session gets no recovery surface', async () => {
  const view = fixture(async () => response(200, { active: null }));
  await view.recovery.load();
  assert.equal(view.root.hidden, true);
  view.dom.window.close();
});

test('initial status failure stays visible with retry and precautionary termination', async () => {
  let calls = 0;
  const view = fixture(async (_path, options = {}) => {
    calls += 1;
    if (options.method === 'DELETE') return response(204);
    throw new Error('offline');
  });

  await view.recovery.load({ announce: true });
  assert.equal(view.root.hidden, false);
  assert.equal(view.root.querySelector('[data-support-current-heading]').textContent, 'Support session status unavailable');
  assert.equal(view.root.querySelector('[data-support-current-retry]').hidden, false);
  assert.equal(view.root.querySelector('[data-support-current-end]').textContent, 'End current session');
  assert.match(view.dom.window.document.getElementById('support-session-recovery-status').textContent, /status is unavailable/);

  view.root.querySelector('[data-support-current-retry]').click();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(calls, 2);

  view.root.querySelector('[data-support-current-end]').click();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(calls, 3);
  assert.equal(view.root.hidden, true);
  assert.match(view.dom.window.document.getElementById('support-session-recovery-status').textContent, /Any current/);
  view.dom.window.close();
});

test('malformed success payload is uncertainty, not proof that no session exists', async () => {
  const view = fixture(async () => response(200, { active: { subject_username: 'alice' } }));
  await view.recovery.load({ announce: true });
  assert.equal(view.root.hidden, false);
  assert.equal(view.root.querySelector('[data-support-current-heading]').textContent, 'Support session status unavailable');
  assert.equal(view.root.querySelector('[data-support-current-end]').textContent, 'End current session');
  assert.match(view.dom.window.document.getElementById('support-session-recovery-status').textContent, /status is unavailable/);
  view.dom.window.close();
});

test('interactive retry announces a recovered session and moves focus to its heading', async () => {
  let calls = 0;
  const view = fixture(async () => {
    calls += 1;
    if (calls === 1) throw new Error('offline');
    return response(200, { active: {
      subject_username: 'alice', app_slug: 'sales', expires_at: '2099-01-01T00:00:00Z',
      remaining_seconds: 600, resumable: false,
    } });
  });
  await view.recovery.load();

  const retry = view.root.querySelector('[data-support-current-retry]');
  retry.focus();
  retry.click();
  assert.equal(retry.textContent, 'Checking…');
  assert.equal(retry.disabled, true);
  await new Promise(resolve => setTimeout(resolve, 0));

  const heading = view.root.querySelector('[data-support-current-heading]');
  assert.equal(view.dom.window.document.activeElement, heading);
  assert.match(view.dom.window.document.getElementById('support-session-recovery-status').textContent, /Active support session as alice/);
  view.dom.window.close();
});

test('interactive retry announces no session before closing the recovery surface', async () => {
  let calls = 0;
  let cleared = 0;
  const view = fixture(async () => {
    calls += 1;
    if (calls === 1) throw new Error('offline');
    return response(200, { active: null });
  }, { onStatusCleared: () => { cleared += 1; } });
  await view.recovery.load();

  view.root.querySelector('[data-support-current-retry]').click();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(view.root.hidden, true);
  assert.equal(cleared, 1);
  assert.match(view.dom.window.document.getElementById('support-session-recovery-status').textContent, /No active support session/);
  view.dom.window.close();
});

test('server remaining time wins over a fast workstation clock', async () => {
  const view = fixture(async () => response(200, { active: {
    subject_username: 'alice', app_slug: 'sales', expires_at: '2026-09-01T12:10:00Z',
    remaining_seconds: 600, resumable: true, app_url: 'https://apps.example.com/app/sales/',
  } }), { now: () => Date.parse('2036-09-01T12:00:00Z') });

  await view.recovery.load();
  assert.equal(view.root.hidden, false);
  assert.equal(view.root.querySelector('[data-support-current-countdown]').textContent, '10:00');
  view.dom.window.close();
});

test('expiry cannot clear state while termination is in flight', async () => {
  let currentTime = Date.parse('2026-09-01T12:00:00Z');
  let finishDelete;
  let ended = 0;
  const view = fixture(async (_path, options = {}) => {
    if (options.method === 'DELETE') return new Promise(resolve => { finishDelete = resolve; });
    return response(200, { active: {
      subject_username: 'alice', app_slug: 'sales', expires_at: '2026-09-01T12:00:01Z',
      remaining_seconds: 1, resumable: true, app_url: 'https://apps.example.com/app/sales/',
    } });
  }, { now: () => currentTime, onEnded: () => { ended += 1; } });

  await view.recovery.load();
  view.root.querySelector('[data-support-current-end]').click();
  currentTime += 1000;
  view.intervals[0]();
  assert.equal(view.root.hidden, false);
  finishDelete(response(204));
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(view.root.hidden, true);
  assert.equal(ended, 1);
  view.dom.window.close();
});
