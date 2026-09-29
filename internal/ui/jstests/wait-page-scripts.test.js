import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';

// The proxy's wait pages (starting / deploying / at-capacity) are HTML built in
// Go, with their behaviour in an inline <script> that no Go test executes. That
// script is where the user-visible waiting contract lives: how long the page
// keeps retrying, when it gives up, and whether the next wait in the same tab
// starts from zero. A defect there is invisible to `go test` and visible to
// every user of a busy or slow-starting app.
//
// So these tests run the REAL script text, lifted verbatim out of proxy.go, in a
// real DOM. Extraction is a deliberate choice over a copy: a copy would drift,
// and a drifted copy passes while shipping the old behaviour.

const PROXY_GO = new URL('../../proxy/proxy.go', import.meta.url);

// extractScript pulls a `const <name> = ` + "`...`" raw string constant out of
// the Go source. It throws rather than returning empty on a miss, because a
// silently-empty script would make every test below pass vacuously.
function extractScript(name) {
  const src = readFileSync(PROXY_GO, 'utf8');
  const m = new RegExp('const ' + name + ' = `([^`]*)`').exec(src);
  if (!m) throw new Error(`could not extract ${name} from ${PROXY_GO.pathname}`);
  const body = m[1];
  if (!body.includes('sessionStorage')) {
    throw new Error(`extracted ${name} does not look like a wait script: ${body.slice(0, 80)}`);
  }
  return body;
}

const loadingScript = extractScript('loadingScript');
const waitingScript = extractScript('waitingScript');

// The wait-page shell, mirroring waitPage() in proxy.go. The ids are the
// contract between the HTML and the script; TestWaitPages_ExposeEveryIdTheir
// ScriptsUse in internal/proxy asserts the shipped HTML still carries the ids
// these scripts reference, so this fixture cannot drift away from the page.
const SHELL = `<!DOCTYPE html><html><body>
<div class="box" id="shinyhub-box">
  <div class="spinner"></div>
  <h1 id="shinyhub-title">Waiting…</h1>
  <p id="shinyhub-msg">message</p>
  <button id="shinyhub-retry" style="display:none">Try again</button>
</div>
</body></html>`;

// run evaluates a wait script against a fresh DOM and returns a handle for
// asserting on it and for driving further loads in the same "tab".
//
// The four globals the script uses to reach outside the document - window
// (location only), setTimeout, Date and Math - are shadowed as function
// parameters rather than patched onto the window, so time, jitter, the reload
// and the scheduled timer are all controllable while the DOM, sessionStorage
// and everything else stay genuinely real.
function run(script, { path = '/app/demo/', navType = 'navigate', now = 1_000_000, random = 0, storage = null, requestStart, perfNow } = {}) {
  const dom = new JSDOM(SHELL, { runScripts: 'outside-only', url: 'http://host' + path });
  const w = dom.window;
  if (storage) for (const [k, v] of storage) w.sessionStorage.setItem(k, v);

  const reloads = [];
  const timers = [];
  w.__harness = {
    win: { location: { pathname: path, reload() { reloads.push(now); } } },
    setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    Date: { now: () => now },
    Math: { random: () => random, floor: Math.floor, min: Math.min, max: Math.max },
    performance: {
      getEntriesByType: () => [{ type: navType, requestStart }],
      now: perfNow === undefined ? undefined : () => perfNow,
    },
  };
  w.eval(
    '(function(window, setTimeout, Date, Math, performance){' + script + '})(' +
    '__harness.win, __harness.setTimeout, __harness.Date, __harness.Math, __harness.performance)'
  );

  const el = (id) => w.document.getElementById(id);
  return {
    window: w,
    reloads,
    timers,
    gaveUp: el('shinyhub-box').classList.contains('error'),
    title: el('shinyhub-title').textContent,
    message: el('shinyhub-msg').textContent,
    retryVisible: el('shinyhub-retry').style.display !== 'none',
    clickRetry() { el('shinyhub-retry').dispatchEvent(new w.MouseEvent('click', { bubbles: true })); },
    storage() { return new Map(Object.keys(w.sessionStorage).map((k) => [k, w.sessionStorage.getItem(k)])); },
  };
}

const CAPACITY_KEY = 'shinyhub-capacity:/app/demo/';
const RETRY_KEY = 'shinyhub-retry:/app/demo/';

test('waiting page: a fresh visit waits rather than giving up', () => {
  const r = run(waitingScript);
  assert.equal(r.gaveUp, false, 'a first gated load must show the spinner, not the give-up state');
  assert.equal(r.timers.length, 1, 'it must schedule exactly one reload');
  assert.equal(r.storage().get(CAPACITY_KEY), String(1_000_000 + 60_000), 'it must record a 60s deadline');
});

test('waiting page: retries are jittered across the interval, never below it', () => {
  // Paced clients are the overflow of a single burst, so a fixed interval would
  // re-converge them into a synchronized retry every cycle - the burst the
  // pacer just absorbed, rebuilt on a timer.
  const delays = [0, 0.5, 0.999].map((random) => run(waitingScript, { random }).timers[0].ms);
  assert.deepEqual(delays, [1500, 1750, 1999]);
  assert.ok(new Set(delays).size === 3, 'different random draws must produce different delays');
});

test('waiting page: reloads within the budget keep the original deadline', () => {
  // The deadline is what makes the wait 60 seconds long rather than 60 seconds
  // per reload; a reload that reset it would wait forever.
  const started = run(waitingScript);
  const deadline = started.storage().get(CAPACITY_KEY);
  const later = run(waitingScript, { navType: 'reload', now: 1_030_000, storage: started.storage() });
  assert.equal(later.gaveUp, false);
  assert.equal(later.storage().get(CAPACITY_KEY), deadline, 'a mid-wait reload must not extend the deadline');
});

test('waiting page: a fresh navigation restarts the budget', () => {
  // Navigating away and back is a new attempt by the user, not a continuation,
  // so inheriting a half-spent budget would cut their wait short.
  const started = run(waitingScript);
  const revisit = run(waitingScript, { navType: 'navigate', now: 1_030_000, storage: started.storage() });
  assert.equal(revisit.storage().get(CAPACITY_KEY), String(1_030_000 + 60_000));
});

test('waiting page: it gives up once the budget is spent', () => {
  const started = run(waitingScript);
  const done = run(waitingScript, { navType: 'reload', now: 1_060_001, storage: started.storage() });
  assert.equal(done.gaveUp, true);
  assert.equal(done.title, 'Still at capacity');
  assert.match(done.message, /60 seconds/);
  assert.equal(done.retryVisible, true, 'give-up must offer a manual retry');
  assert.equal(done.timers.length, 0, 'give-up is terminal: it must not schedule another reload');
});

test('waiting page: giving up does not poison the next wait in the same tab', () => {
  // The regression this file exists for. The give-up branch is terminal, so an
  // expired deadline left in sessionStorage outlives the wait it measured. The
  // next gated load in that tab - a reload, so the fresh-navigation reset does
  // not fire - would then read a deadline already in the past and give up on
  // arrival, telling the user no capacity freed up in 60 seconds after waiting
  // none of them.
  const started = run(waitingScript);
  const done = run(waitingScript, { navType: 'reload', now: 1_060_001, storage: started.storage() });
  assert.equal(done.gaveUp, true, 'precondition: this load is the give-up');
  assert.equal(done.storage().has(CAPACITY_KEY), false, 'give-up must not leave its spent deadline behind');

  const nextTime = 9_000_000;
  const again = run(waitingScript, { navType: 'reload', now: nextTime, storage: done.storage() });
  assert.equal(again.gaveUp, false, 'a later wait must start over, not inherit the previous give-up');
  assert.equal(again.storage().get(CAPACITY_KEY), String(nextTime + 60_000));
});

test('waiting page: Try again reloads into a fresh wait', () => {
  const started = run(waitingScript);
  const done = run(waitingScript, { navType: 'reload', now: 1_060_001, storage: started.storage() });
  done.clickRetry();
  assert.equal(done.reloads.length, 1, 'Try again must reload the page');
  const retried = run(waitingScript, { navType: 'reload', now: 1_060_500, storage: done.storage() });
  assert.equal(retried.gaveUp, false, 'the reload Try again triggers must land on a fresh wait');
});

test('waiting page: it clears the starting budget, not the other way round', () => {
  // Being served this page proves the app is up, so any cold-start retries
  // counted against it are stale. The two waits are unrelated and must not
  // spend each other's budget.
  const r = run(waitingScript, { storage: new Map([[RETRY_KEY, '7']]) });
  assert.equal(r.storage().has(RETRY_KEY), false, 'the at-capacity page must reset the cold-start count');
  assert.equal(r.storage().has(CAPACITY_KEY), true);
});

test('starting page: with no server hold, requests are a flat 3 s apart', () => {
  // The page rendered 40 ms after its request went out: nothing held it, so the
  // whole spacing is still ahead and the next reload waits the full interval.
  const first = run(loadingScript, { requestStart: 10, perfNow: 50 });
  assert.equal(first.gaveUp, false);
  assert.equal(first.timers.length, 1, 'it must schedule exactly one reload');
  assert.equal(first.timers[0].ms, 2960, 'the next request starts 3 s after this one did');
  assert.equal(first.storage().get(RETRY_KEY), String(1_000_000 + 60_000), 'it must record a 60s deadline');
});

test('starting page: a request the server held is followed at once, not after 3 s', () => {
  // The proxy held this request for 5 s during a wake. The spacing is spent,
  // so the page asks again almost immediately: the held requests chain into a
  // continuous long poll and the app is served the moment it is routable. A
  // flat 3 s pause here is up to 3 s of spinner after the app is already up.
  const held = run(loadingScript, { requestStart: 10, perfNow: 5_060 });
  assert.equal(held.timers[0].ms, 250, 'a held request is followed after only the floor delay');
  const partly = run(loadingScript, { requestStart: 10, perfNow: 2_010 });
  assert.equal(partly.timers[0].ms, 1000, 'a partly held request waits out only the rest of the spacing');
});

test('starting page: without navigation timing it falls back to the flat interval', () => {
  // Older engines without performance.now or a navigation entry must still
  // poll, at the conservative rate, never in a tight loop.
  assert.equal(run(loadingScript).timers[0].ms, 3000);
  assert.equal(run(loadingScript, { requestStart: 0, perfNow: 9_000 }).timers[0].ms, 3000,
    'a zero requestStart (timing unavailable) must not read as a long hold');
});

test('starting page: reloads within the budget keep the original deadline', () => {
  const started = run(loadingScript);
  const deadline = started.storage().get(RETRY_KEY);
  const later = run(loadingScript, { navType: 'reload', now: 1_045_000, storage: started.storage() });
  assert.equal(later.gaveUp, false);
  assert.equal(later.storage().get(RETRY_KEY), deadline, 'a mid-wait reload must not extend the deadline');
});

test('starting page: it gives up once the budget is spent and does not poison the next start', () => {
  const started = run(loadingScript);
  const done = run(loadingScript, { navType: 'reload', now: 1_060_001, storage: started.storage() });
  assert.equal(done.gaveUp, true);
  assert.equal(done.title, 'App did not start');
  assert.match(done.message, /60 seconds/);
  assert.equal(done.retryVisible, true, 'give-up must offer a manual retry');
  assert.equal(done.timers.length, 0, 'give-up is terminal');
  assert.equal(done.storage().has(RETRY_KEY), false, 'give-up must not leave its spent deadline behind');

  const again = run(loadingScript, { navType: 'reload', now: 9_000_000, storage: done.storage() });
  assert.equal(again.gaveUp, false, 'a later start must wait again, not give up on arrival');
});
