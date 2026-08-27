import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM, VirtualConsole } from 'jsdom';

// The status overlay is JavaScript the proxy splices into every app's HTML. No
// Go test executes it, and it runs inside pages ShinyHub did not write, so a
// defect here is invisible to `go test` and visible to every visitor of every
// app. These tests run the REAL shipped bytes - the same file inject.go embeds -
// in a real DOM, driving the actual contract it depends on: Shiny appending
// #shiny-disconnected-overlay to <body> and removing it again.

const OVERLAY_JS = new URL('../../proxy/assets/overlay.js', import.meta.url);
const source = readFileSync(OVERLAY_JS, 'utf8');
// A silently-empty or renamed source would make every test below pass
// vacuously, which is the one outcome worse than failing.
if (!source.includes('shiny-disconnected-overlay')) {
  throw new Error(`${OVERLAY_JS.pathname} does not look like the overlay script`);
}

const SHINY_ID = 'shiny-disconnected-overlay';
const OWN_ID = 'shinyhub-status-overlay';
const OPEN_ID = 'shinyhub-status-open';
const RELOAD_ID = 'shinyhub-status-reload';
const RESTART_ID = 'shinyhub-status-restart';
const DOT_ID = 'shinyhub-status-dot';
const READY_URL = '/app/demo/.shinyhub/ready';
const SESSION_EVENT = 'shinyhub:session-status';
const SESSION_OWNER_EVENT = 'shinyhub:session-status-owner';

const flush = () => new Promise((r) => setImmediate(r));

// mount evaluates the overlay exactly as a browser would: a real <script> with
// the data-* attributes inject.go writes, appended to a real document, so
// document.currentScript is genuinely set and the attribute-reading path is the
// one under test rather than a stand-in.
//
// fetch and the timers are the only things replaced. They are the script's two
// windows onto the outside world, and controlling them is what lets a 60-second
// retry budget be spent in a millisecond.
function mount({
  statuses = [], pollMs = 3000, maxPolls = 20, attrs = {}, preMarker = false,
  claimSnapshot = false,
} = {}) {
  const jsdomErrors = [];
  const virtualConsole = new VirtualConsole();
  virtualConsole.on('jsdomError', (e) => jsdomErrors.push(e.message));

  const dom = new JSDOM('<!DOCTYPE html><html><head></head><body><div id="app-root">app</div></body></html>', {
    runScripts: 'dangerously',
    url: 'http://host/app/demo/',
    virtualConsole,
  });
  const w = dom.window;
  const d = w.document;
  const sessionEvents = [];
  w.addEventListener(SESSION_EVENT, (event) => {
    sessionEvents.push(event.detail);
    if (claimSnapshot && event.detail && event.detail.state === 'snapshot') {
      event.preventDefault();
    }
  });

  const fetches = [];
  const queue = statuses.slice();
  w.fetch = (url, opts) => {
    fetches.push({ url, opts });
    const next = queue.length ? queue.shift() : 503;
    if (next === 'error') return Promise.reject(new Error('network down'));
    return Promise.resolve({ status: next });
  };

  const timers = [];
  let nextTimer = 1;
  w.setTimeout = (fn, ms) => {
    const id = nextTimer++;
    timers.push({ id, fn, ms });
    return id;
  };
  w.clearTimeout = (id) => {
    const i = timers.findIndex((t) => t.id === id);
    if (i >= 0) timers.splice(i, 1);
  };

  // preMarker puts Shiny's marker in the document BEFORE the script runs, which
  // no mutation record will ever report.
  if (preMarker) {
    const early = d.createElement('div');
    early.id = SHINY_ID;
    if (preMarker === 'reloading') early.className = 'reloading';
    d.body.appendChild(early);
  }

  const tag = d.createElement('script');
  tag.id = 'shinyhub-status-overlay-loader';
  tag.setAttribute('data-ready-url', READY_URL);
  tag.setAttribute('data-poll-ms', String(pollMs));
  tag.setAttribute('data-max-polls', String(maxPolls));
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null) tag.removeAttribute(k);
    else tag.setAttribute(k, v);
  }
  tag.textContent = source;
  d.body.appendChild(tag);

  const overlay = () => d.getElementById(OWN_ID);
  const el = (sel) => (overlay() ? overlay().querySelector(sel) : null);

  return {
    window: w,
    document: d,
    fetches,
    timers,
    jsdomErrors,
    sessionEvents,
    overlay,
    // disconnect reproduces what Shiny's client does on a socket close.
    async disconnect({ reloading = false } = {}) {
      const div = d.createElement('div');
      div.id = SHINY_ID;
      if (reloading) div.className = 'reloading';
      d.body.appendChild(div);
      await flush();
      await flush();
    },
    async reconnect() {
      const div = d.getElementById(SHINY_ID);
      if (div) div.parentNode.removeChild(div);
      await flush();
      await flush();
    },
    // fireTimer runs the pending poll, the way the clock would.
    async fireTimer() {
      const t = timers.shift();
      assert.ok(t, 'expected a scheduled poll');
      t.fn();
      await flush();
      await flush();
      return t.ms;
    },
    title: () => (overlay() ? overlay().querySelector('h1').textContent : null),
    message: () => (overlay() ? overlay().querySelector('p').textContent : null),
    button: () => el(`#${RELOAD_ID}`),
    openLink: () => el(`#${OPEN_ID}`),
    restartButton: () => el(`#${RESTART_ID}`),
    actionVisible: (id) => {
      const action = el(`#${id}`);
      return !!action && action.style.display !== 'none';
    },
    buttonVisible: () => {
      const b = el(`#${RELOAD_ID}`);
      return !!b && b.style.display !== 'none';
    },
    spinnerVisible: () => {
      const s = el('#shinyhub-status-spinner');
      return !!s && s.style.display !== 'none';
    },
    statusDot: () => el(`#${DOT_ID}`),
  };
}

test('it is inert until the app disconnects', async () => {
  const h = mount();
  await flush();
  assert.equal(h.overlay(), null, 'a working app must never see this script paint anything');
  assert.equal(h.fetches.length, 0, 'a working app must never see it call the server');
  assert.equal(h.document.getElementById('app-root').textContent, 'app', 'the app page is untouched');
});

test('a disconnect raises the overlay and starts polling the ready probe', async () => {
  const h = mount({ statuses: [503] });
  await h.disconnect();

  assert.ok(h.overlay(), 'the disconnect must be explained');
  assert.match(h.title(), /Connection interrupted/);
  assert.equal(h.spinnerVisible(), true, 'a wait in progress shows the spinner');
  assert.equal(h.fetches.length, 1, 'it must ask the server what happened');
  assert.equal(h.fetches[0].url, READY_URL, 'it must poll the URL the proxy gave it, not a guessed one');
  assert.equal(h.fetches[0].opts.cache, 'no-store', 'a cached readiness answer is a wrong answer');
  assert.equal(h.overlay().getAttribute('aria-live'), 'polite', 'the state change is announced');
  assert.equal(h.overlay().getAttribute('role'), 'dialog', 'the blocking state uses dialog semantics');
  assert.equal(h.overlay().getAttribute('aria-modal'), 'true', 'the obscured page is exposed as modal');
  assert.equal(h.document.activeElement, h.button(), 'focus starts on the recovery action');

  // Restart is offered immediately: the ready probe never wakes a hibernated
  // app, so for the commonest cause of a drop, waiting cannot fix it and
  // reloading can. Holding the button back would be a minute of theatre.
  assert.equal(h.buttonVisible(), true, 'the fix must be offered at once, not after the budget');
  assert.equal(h.button().textContent, 'Restart now');
  assert.equal(h.button().style.minHeight, '44px', 'the primary action must remain a touch target');
});

test('a deliberate reload is not a fault', async () => {
  // Shiny sets the "reloading" class when it is tearing the page down on
  // purpose (dev autoreload, a session reload). Treating that as a disconnect
  // would flash an error over every hot reload in development.
  const h = mount();
  await h.disconnect({ reloading: true });
  assert.equal(h.overlay(), null, 'a reload-marked overlay must not raise ours');
  assert.equal(h.fetches.length, 0, 'and must not poll');
});

test('unrelated DOM churn is ignored', async () => {
  const h = mount();
  const div = h.document.createElement('div');
  div.id = 'some-app-modal';
  h.document.body.appendChild(div);
  await flush();
  await flush();
  assert.equal(h.overlay(), null);
  assert.equal(h.fetches.length, 0);
});

test('a reconnect takes the overlay away again', async () => {
  const h = mount({ statuses: [503] });
  await h.disconnect();
  assert.ok(h.overlay(), 'precondition: the overlay is up');
  const pending = h.timers.length;
  assert.equal(pending, 1, 'precondition: a poll is scheduled');

  await h.reconnect();
  assert.equal(h.overlay(), null, 'Shiny recovered on its own; nothing to explain');
  assert.equal(h.timers.length, 0, 'and the poll must stop, not keep hitting the server');
});

test('a recovered app offers a new session while preserving the old results', async () => {
  // The dead page still shows the visitor's last results. Reloading replaces
  // them with a blank app, so that is their call to make, not ours.
  const h = mount({ statuses: [503, 200] });
  await h.disconnect();
  assert.match(h.title(), /Connection interrupted/);

  await h.fireTimer();
  assert.equal(h.title(), 'This session was interrupted');
  assert.equal(h.spinnerVisible(), false, 'the wait is over');
  assert.equal(h.statusDot().style.background, 'rgb(251, 191, 36)', 'interruption uses the amber warning state');
  assert.match(h.statusDot().style.animation, /shinyhub-ready-pulse/, 'the decision state announces its arrival');
  assert.doesNotMatch(h.statusDot().style.animation, /infinite/, 'the arrival pulse must settle');
  assert.equal(h.buttonVisible(), false, 'the ambiguous generic reload action is gone');
  assert.match(h.message(), /Use a new tab to keep these results available/, 'the safe action explains its consequence');
  assert.equal(h.actionVisible(OPEN_ID), true, 'a safe new-session path is primary');
  assert.equal(h.openLink().textContent, 'Start in a new tab');
  assert.equal(h.openLink().getAttribute('aria-label'), 'Start a new app session in a new tab');
  assert.equal(h.openLink().target, '_blank', 'the new session preserves this tab');
  assert.match(h.openLink().rel, /noopener/, 'the new tab cannot retain an opener');
  assert.equal(h.actionVisible(RESTART_ID), true, 'same-tab restart remains available');
  assert.equal(h.restartButton().textContent, 'Start in this tab');
  assert.equal(h.document.activeElement, h.openLink(), 'focus moves to the safest recovery action');
  assert.equal(h.timers.length, 0, 'recovery is terminal: no further polling');
});

test('previous results become an explicit non-blocking offline snapshot', async () => {
  const h = mount({ statuses: [200] });
  await h.disconnect();

  h.overlay().dispatchEvent(new h.window.MouseEvent('click', {
    bubbles: true, cancelable: true,
  }));
  await flush();

  assert.match(h.title(), /Previous results/);
  assert.match(h.message(), /may be out of date/);
  assert.equal(h.overlay().getAttribute('role'), 'status', 'the snapshot banner is no longer modal');
  assert.equal(h.overlay().hasAttribute('aria-modal'), false, 'snapshot mode releases the app beneath it');
  assert.match(h.overlay().className, /is-snapshot/, 'the full-screen blocker becomes a banner');
  assert.equal(h.document.getElementById(SHINY_ID).style.display, 'none', 'Shiny’s blocker no longer hides the results');
  assert.equal(h.document.getElementById(SHINY_ID).getAttribute('aria-hidden'), 'true');
  assert.equal(h.document.getElementById('app-root').textContent, 'app', 'the prior results remain in place');
  assert.equal(h.actionVisible(OPEN_ID), true, 'continuation stays available from snapshot mode');
});

test('the app switcher can claim the snapshot without duplicating the fallback card', async () => {
  const h = mount({ statuses: [200], claimSnapshot: true });
  await h.disconnect();

  h.overlay().dispatchEvent(new h.window.MouseEvent('click', {
    bubbles: true, cancelable: true,
  }));
  await flush();

  assert.equal(h.sessionEvents.length, 1);
  assert.equal(h.sessionEvents[0].state, 'snapshot');
  assert.equal(h.sessionEvents[0].url, 'http://host/app/demo/');
  assert.equal(h.sessionEvents[0].focus, true);
  assert.equal(h.overlay().style.display, 'none', 'the claimed snapshot still rendered a second status card');
  assert.equal(h.document.getElementById(SHINY_ID).style.display, 'none', 'the old results stayed obscured');

  h.window.dispatchEvent(new h.window.CustomEvent(SESSION_OWNER_EVENT, {
    detail: { owner: 'overlay' },
  }));
  assert.equal(h.overlay().style.display, 'block', 'dismissing the switcher did not restore the fallback');

  h.window.dispatchEvent(new h.window.CustomEvent(SESSION_OWNER_EVENT, {
    detail: { owner: 'switcher' },
  }));
  assert.equal(h.overlay().style.display, 'none', 'restoring the switcher left the fallback visible');
  assert.equal(h.sessionEvents.length, 2, 'the switcher was not offered the snapshot again');
  assert.equal(h.sessionEvents[1].focus, false, 'restoring chrome unexpectedly moved focus');
});

test('the recovered decision traps focus and Escape reveals the snapshot', async () => {
  const h = mount({ statuses: [200] });
  await h.disconnect();

  h.openLink().dispatchEvent(new h.window.KeyboardEvent('keydown', {
    key: 'Tab', shiftKey: true, bubbles: true, cancelable: true,
  }));
  assert.equal(h.document.activeElement, h.restartButton(), 'Shift+Tab wraps to the final recovery action');

  h.restartButton().dispatchEvent(new h.window.KeyboardEvent('keydown', {
    key: 'Tab', bubbles: true, cancelable: true,
  }));
  assert.equal(h.document.activeElement, h.openLink(), 'Tab wraps back to the safest action');

  h.openLink().dispatchEvent(new h.window.KeyboardEvent('keydown', {
    key: 'Escape', bubbles: true, cancelable: true,
  }));
  assert.match(h.overlay().className, /is-snapshot/, 'Escape chooses the non-destructive exit');
  assert.equal(h.document.activeElement, h.overlay(), 'focus follows the newly revealed snapshot status');
});

test('clicking the recovered backdrop reveals the snapshot without discarding status', async () => {
  const h = mount({ statuses: [200] });
  await h.disconnect();

  const card = h.overlay().firstElementChild;
  card.dispatchEvent(new h.window.MouseEvent('click', {
    bubbles: true, cancelable: true,
  }));
  assert.equal(h.overlay().classList.contains('is-snapshot'), false, 'clicking the decision card must not dismiss it');

  h.overlay().dispatchEvent(new h.window.MouseEvent('click', {
    bubbles: true, cancelable: true,
  }));
  await flush();

  assert.match(h.overlay().className, /is-snapshot/, 'the backdrop chooses the non-destructive exit');
  assert.match(h.title(), /Previous results/, 'the dismissed decision remains visible as status');
  assert.equal(h.document.getElementById(SHINY_ID).style.display, 'none', 'the prior results become inspectable');
  assert.equal(h.statusDot().style.animation, '', 'the arrival pulse settles in snapshot mode');
});

test('waiting and error overlays ignore backdrop clicks', async () => {
  const waiting = mount({ statuses: [503] });
  await waiting.disconnect();
  waiting.overlay().dispatchEvent(new waiting.window.MouseEvent('click', {
    bubbles: true, cancelable: true,
  }));
  assert.match(waiting.title(), /Connection interrupted/);
  assert.equal(waiting.overlay().getAttribute('aria-modal'), 'true');

  const error = mount({ statuses: [404] });
  await error.disconnect();
  error.overlay().dispatchEvent(new error.window.MouseEvent('click', {
    bubbles: true, cancelable: true,
  }));
  assert.match(error.title(), /no longer available/);
  assert.equal(error.overlay().getAttribute('aria-modal'), 'true');
});

test('a genuine reconnect also clears an already-revealed snapshot', async () => {
  const h = mount({ statuses: [200] });
  await h.disconnect();
  h.overlay().dispatchEvent(new h.window.MouseEvent('click', {
    bubbles: true, cancelable: true,
  }));
  assert.ok(h.overlay(), 'precondition: snapshot banner is visible');

  await h.reconnect();
  assert.equal(h.overlay(), null, 'a live reconnection removes the stale-state banner');
  assert.equal(h.sessionEvents.at(-1).state, 'connected', 'the switcher was left showing a stale status');
});

test('a deleted app is terminal, not a slow restart', async () => {
  // 404 and 503 are distinct answers from the ready probe. Conflating them
  // would leave a visitor waiting out the full budget for an app that is never
  // coming back, and then offering them a reload that cannot work.
  const h = mount({ statuses: [404] });
  await h.disconnect();

  assert.match(h.title(), /no longer available/);
  assert.equal(h.timers.length, 0, 'no further polling for an app that is gone');
  assert.equal(h.buttonVisible(), false, 'no reload button: there is nothing to reload into');
});

test('it gives up after the budget and says how long it waited', async () => {
  const h = mount({ statuses: [503, 503, 503], pollMs: 1000, maxPolls: 3 });
  await h.disconnect();
  assert.match(h.title(), /Connection interrupted/, 'precondition: waiting');

  assert.equal(await h.fireTimer(), 1000, 'it must wait the configured interval');
  assert.match(h.title(), /Connection interrupted/, 'still within the budget');
  await h.fireTimer();

  assert.match(h.title(), /did not come back/);
  assert.match(h.message(), /3 seconds/, 'the budget it actually spent, derived from the attributes');
  assert.equal(h.timers.length, 0, 'give-up is terminal');
  assert.equal(h.buttonVisible(), true, 'a manual retry is still offered');
});

test('a failed fetch counts as a miss, not as a crash', async () => {
  // From inside a disconnected page, "the network refused me" and "the server
  // is down" are the same observation. Either way the app is not back yet.
  const h = mount({ statuses: ['error', 503, 200], pollMs: 500, maxPolls: 5 });
  await h.disconnect();
  assert.match(h.title(), /Connection interrupted/, 'a rejected fetch must not blank the overlay');
  assert.equal(h.timers.length, 1, 'and must schedule the next attempt');

  await h.fireTimer();
  await h.fireTimer();
  assert.equal(h.title(), 'This session was interrupted', 'it recovers once the probe answers');
});

test('a page that already carries the marker is handled on arrival', async () => {
  // A restored page (bfcache, back button) can come back with Shiny's overlay
  // already in the DOM. No mutation ever fires for it, so only the initial
  // check catches it, and without that check the visitor is left staring at
  // Shiny's grey box with no explanation and no way out.
  const h = mount({ statuses: [503], preMarker: true });
  await flush();
  await flush();
  assert.ok(h.overlay(), 'a marker already in the document must still be explained');
  assert.equal(h.fetches.length, 1, 'and must start the same poll');
});

test('a marker already present but reloading is still not a fault', async () => {
  const h = mount({ statuses: [503], preMarker: 'reloading' });
  await flush();
  await flush();
  assert.equal(h.overlay(), null);
});

test('it declines to run when it cannot know where to poll', async () => {
  // Without a ready URL it could only guess at an endpoint, and a guessed
  // endpoint on someone else's app is a request this proxy has no business
  // making.
  const h = mount({ attrs: { 'data-ready-url': null } });
  await h.disconnect();
  assert.equal(h.overlay(), null);
  assert.equal(h.fetches.length, 0);
});

test('bad attribute values fall back rather than break', async () => {
  // The attributes are written by inject.go, so garbage means a bug upstream.
  // Failing closed there would leave the visitor with no explanation at all.
  const h = mount({ statuses: [503], attrs: { 'data-poll-ms': 'soon', 'data-max-polls': '-3' } });
  await h.disconnect();
  assert.ok(h.overlay(), 'a malformed attribute must not disable the overlay');
  assert.equal(h.timers[0].ms, 3000, 'it falls back to the documented default interval');
});

test('the script never throws into the host page', async () => {
  const h = mount({ statuses: ['error'] });
  await h.disconnect();
  await h.reconnect();
  await h.disconnect();
  assert.deepEqual(h.jsdomErrors, [], 'an uncaught throw here would break the app it is trying to explain');
});

test('motion and keyboard focus have explicit accessible fallbacks', () => {
  assert.match(source, /prefers-reduced-motion:\s*reduce/, 'the spinner must yield to reduced-motion preferences');
  assert.match(source, /shinyhub-status-spinner, #" \+ DOT_ID/, 'the status pulse must also yield to reduced-motion preferences');
  assert.match(source, /:focus-visible/, 'actions need a visible keyboard focus treatment');
});
