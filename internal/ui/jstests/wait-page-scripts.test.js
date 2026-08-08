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

// The wait-page shell, mirroring waitPage() in proxy.go. The ids are the
// contract between the HTML and the script.
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
// The globals the script uses to reach outside the document - window (location
// only), setTimeout and performance - are shadowed as function parameters
// rather than patched onto the window, so the reload and the scheduled timer
// are controllable while the DOM, sessionStorage and everything else stay
// genuinely real.
function run(script, { path = '/app/demo/', navType = 'navigate', storage = null } = {}) {
  const dom = new JSDOM(SHELL, { runScripts: 'outside-only', url: 'http://host' + path });
  const w = dom.window;
  if (storage) for (const [k, v] of storage) w.sessionStorage.setItem(k, v);

  const reloads = [];
  const timers = [];
  w.__harness = {
    win: { location: { pathname: path, reload() { reloads.push(true); } } },
    setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    performance: { getEntriesByType: () => [{ type: navType }] },
  };
  w.eval(
    '(function(window, setTimeout, performance){' + script + '})(' +
    '__harness.win, __harness.setTimeout, __harness.performance)'
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

const RETRY_KEY = 'shinyhub-retry:/app/demo/';

test('starting page: it retries on a flat interval up to the cap', () => {
  const first = run(loadingScript);
  assert.equal(first.gaveUp, false, 'a first load must show the spinner, not the give-up state');
  assert.equal(first.timers.length, 1, 'it must schedule exactly one reload');
  assert.equal(first.timers[0].ms, 3000);
  assert.equal(first.storage().get(RETRY_KEY), '1');
});

test('starting page: reloads within the cap keep counting up', () => {
  // The count is what makes the wait 60 seconds long rather than 3 seconds
  // repeated forever; a reload that reset it would wait indefinitely.
  const first = run(loadingScript);
  const second = run(loadingScript, { navType: 'reload', storage: first.storage() });
  assert.equal(second.storage().get(RETRY_KEY), '2');
  assert.equal(second.gaveUp, false);
});

test('starting page: a fresh navigation restarts the count', () => {
  // Navigating away and back is a new attempt by the user, not a continuation,
  // so inheriting a half-spent count would cut their wait short.
  const spent = new Map([[RETRY_KEY, '15']]);
  const revisit = run(loadingScript, { navType: 'navigate', storage: spent });
  assert.equal(revisit.storage().get(RETRY_KEY), '1');
  assert.equal(revisit.gaveUp, false);
});

test('starting page: it gives up after the cap and does not poison the next start', () => {
  // The regression this test exists for. The give-up branch is terminal, so a
  // spent count left in sessionStorage outlives the wait it measured. The next
  // load in that tab - a reload, so the fresh-navigation reset does not fire -
  // would then read a count already at the cap and give up on arrival, telling
  // the user the app did not start in 60 seconds after waiting none of them.
  const spent = new Map([[RETRY_KEY, '20']]);
  const done = run(loadingScript, { navType: 'reload', storage: spent });
  assert.equal(done.gaveUp, true);
  assert.equal(done.title, 'App did not start');
  assert.equal(done.retryVisible, true, 'give-up must offer a manual retry');
  assert.equal(done.timers.length, 0, 'give-up is terminal: it must not schedule another reload');
  assert.equal(done.storage().has(RETRY_KEY), false, 'give-up must not leave its spent count behind');

  const again = run(loadingScript, { navType: 'reload', storage: done.storage() });
  assert.equal(again.gaveUp, false, 'a later start must wait again, not give up on arrival');
  assert.equal(again.timers.length, 1);
});

test('starting page: Try again reloads into a fresh wait', () => {
  const spent = new Map([[RETRY_KEY, '20']]);
  const done = run(loadingScript, { navType: 'reload', storage: spent });
  done.clickRetry();
  assert.equal(done.reloads.length, 1, 'Try again must reload the page');
  const retried = run(loadingScript, { navType: 'reload', storage: done.storage() });
  assert.equal(retried.gaveUp, false, 'the reload Try again triggers must land on a fresh wait');
});
