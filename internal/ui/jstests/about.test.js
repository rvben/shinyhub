import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { serverIdentity, runtimeRows, renderAbout, createServerInfoLoader } from '../static/views/about.js';

// The About dialog is the only place a signed-in operator can read which
// ShinyHub they run and what the host can start. It has to stay honest when the
// server does not answer, and it must not be swapped out by operator branding.

function aboutDoc() {
  return new JSDOM(`<!DOCTYPE html><body><div id="about-modal">
    <p class="about-wordmark">ShinyHub</p>
    <p id="about-version"></p>
    <p id="about-build" hidden></p>
    <dl id="about-runtimes"></dl>
  </div></body>`).window.document;
}

function response(body, ok = true) {
  return { ok, json: async () => body };
}

const states = (doc) => [...doc.querySelectorAll('.about-runtime-state')].map((el) => el.textContent);

// --- serverIdentity (pure) ---

test('a semver gets the conventional v prefix', () => {
  assert.equal(serverIdentity({ version: '0.11.2' }).versionText, 'v0.11.2');
});

test('an already-prefixed version is not double-prefixed', () => {
  assert.equal(serverIdentity({ version: 'v0.11.2' }).versionText, 'v0.11.2');
});

test('an unstamped build reads as dev, not vdev', () => {
  assert.equal(serverIdentity({ version: 'dev' }).versionText, 'dev');
});

test('a missing version says so rather than rendering an empty line', () => {
  // A blank would read as "this build has no version", which is a different and
  // false claim from "we could not find out".
  for (const input of [undefined, null, {}, 'nonsense', { version: '' }, { version: '   ' }, { version: 7 }]) {
    const id = serverIdentity(input);
    assert.equal(id.versionText, 'Version unavailable', `versionText for ${JSON.stringify(input)}`);
    assert.equal(id.known, false, `known for ${JSON.stringify(input)}`);
    assert.equal(id.version, '');
  }
});

test('a real version is reported as known', () => {
  assert.equal(serverIdentity({ version: '0.11.2' }).known, true);
});

test('whitespace around the version is trimmed', () => {
  assert.equal(serverIdentity({ version: '  0.11.2  ' }).versionText, 'v0.11.2');
});

test('the build line appears only when the binary carries VCS stamping', () => {
  assert.equal(serverIdentity({ version: '0.11.2', commit: 'a1b2c3d4e5f6' }).buildText, 'Build a1b2c3d4e5f6');
  assert.equal(serverIdentity({ version: '0.11.2' }).buildText, '');
});

// --- runtimeRows (pure) ---

test('a reported runtime maps to ready or not installed', () => {
  const rows = runtimeRows({ runtimes: { python: true, r: false } });
  assert.deepEqual(rows.map((r) => r.key), ['python', 'r']);
  assert.deepEqual(rows.map((r) => r.state), ['ready', 'missing']);
  assert.deepEqual(rows.map((r) => r.stateText), ['Ready', 'Host tool unavailable']);
});

test('an unreported runtime is unknown, never "not installed"', () => {
  // Absent data must not become a plausible value: "the server never said" and
  // "the server said no" are different facts, and rendering the second would
  // tell a developer their R app cannot deploy when nobody ever checked.
  for (const input of [null, undefined, {}, { runtimes: null }, { runtimes: {} }, { runtimes: 'yes' }, { version: '0.11.2' }]) {
    const rows = runtimeRows(input);
    assert.deepEqual(rows.map((r) => r.state), ['unknown', 'unknown'], `state for ${JSON.stringify(input)}`);
    assert.deepEqual(rows.map((r) => r.stateText), ['Unknown', 'Unknown']);
  }
});

test('a non-boolean runtime value is unknown rather than coerced', () => {
  // A truthy string would read as "Ready" under a loose check.
  assert.equal(runtimeRows({ runtimes: { python: 'true' } })[0].state, 'unknown');
  assert.equal(runtimeRows({ runtimes: { python: 1 } })[0].state, 'unknown');
});

test('each runtime names the executable the server looked for', () => {
  const rows = runtimeRows({ runtimes: { python: false, r: false } });
  assert.deepEqual(rows.map((r) => r.launcher), ['uv', 'Rscript']);
});

// --- renderAbout (DOM) ---

test('the version lands in the dialog', () => {
  const doc = aboutDoc();
  renderAbout(doc, { version: '0.11.2' });
  assert.equal(doc.getElementById('about-version').textContent, 'v0.11.2');
});

test('the build line is shown when stamped and hidden when not', () => {
  const doc = aboutDoc();
  renderAbout(doc, { version: '0.11.2', commit: 'a1b2c3d4e5f6' });
  const build = doc.getElementById('about-build');
  assert.equal(build.textContent, 'Build a1b2c3d4e5f6');
  assert.equal(build.hidden, false);

  renderAbout(doc, { version: '0.11.3' });
  assert.equal(build.hidden, true, 'a stale build line must not survive a re-render');
  assert.equal(build.textContent, '');
});

test('an unknown version is marked so it can be de-emphasized', () => {
  const doc = aboutDoc();
  renderAbout(doc, null);
  const el = doc.getElementById('about-version');
  assert.equal(el.textContent, 'Version unavailable');
  assert.equal(el.classList.contains('is-unknown'), true);

  renderAbout(doc, { version: '0.11.2' });
  assert.equal(el.classList.contains('is-unknown'), false, 'the marker must clear once the version is known');
});

test('runtimes render as name plus launcher plus state', () => {
  const doc = aboutDoc();
  renderAbout(doc, { version: '0.11.2', runtimes: { python: true, r: false } });
  const names = [...doc.querySelectorAll('.about-runtime-name')].map((el) => el.textContent);
  assert.deepEqual(names, ['Pythonuv', 'RRscript']);
  assert.deepEqual(states(doc), ['Ready', 'Host tool unavailable']);
  assert.deepEqual(
    [...doc.querySelectorAll('.about-runtime-state')].map((el) => el.className),
    ['about-runtime-state is-ready', 'about-runtime-state is-missing'],
  );
});

test('a re-render replaces the runtime rows instead of appending', () => {
  const doc = aboutDoc();
  renderAbout(doc, { runtimes: { python: true, r: true } });
  renderAbout(doc, { runtimes: { python: true, r: false } });
  assert.deepEqual(states(doc), ['Ready', 'Host tool unavailable']);
});

test('an unreachable server leaves runtimes unknown, not missing', () => {
  const doc = aboutDoc();
  renderAbout(doc, null);
  assert.deepEqual(states(doc), ['Unknown', 'Unknown']);
});

test('a shell without the dialog does not throw', () => {
  const doc = new JSDOM('<!DOCTYPE html><body></body>').window.document;
  assert.equal(renderAbout(doc, { version: '0.11.2' }), null);
});

test('nothing in the dialog is a brand slot, so operator branding cannot swap it', () => {
  // views/branding.js rewrites every `.brand` node. This dialog names the
  // software for a bug report and has to survive white-labeling.
  const doc = aboutDoc();
  renderAbout(doc, { version: '0.11.2', runtimes: { python: true, r: true } });
  const modal = doc.getElementById('about-modal');
  assert.equal(modal.closest('.brand'), null, 'must not sit inside a brand slot');
  assert.equal(modal.querySelector('.brand'), null, 'must not contain a brand slot');
});

// --- createServerInfoLoader ---

test('the server is asked once per session, however often the dialog opens', async () => {
  let calls = 0;
  const load = createServerInfoLoader(async () => {
    calls += 1;
    return response({ version: '0.11.2' });
  });
  assert.deepEqual(await load(), { version: '0.11.2' });
  await load();
  await load();
  assert.equal(calls, 1);
});

test('concurrent opens share one in-flight request', async () => {
  let calls = 0;
  const load = createServerInfoLoader(async () => {
    calls += 1;
    return response({ version: '0.11.2' });
  });
  await Promise.all([load(), load(), load()]);
  assert.equal(calls, 1);
});

test('a failure is not cached, so the next open can still get the answer', async () => {
  // Caching the failure would pin "version unavailable" for the whole session
  // over one blip.
  let calls = 0;
  const load = createServerInfoLoader(async () => {
    calls += 1;
    if (calls === 1) throw new Error('network down');
    return response({ version: '0.11.2' });
  });
  assert.equal(await load(), null);
  assert.deepEqual(await load(), { version: '0.11.2' });
  assert.equal(calls, 2);
});

test('a non-ok response reads as unavailable, never as a version', async () => {
  // An older server without /api/server-info answers 404; that must not be
  // parsed into something that renders as a real version.
  const load = createServerInfoLoader(async () => response({ error: 'not found' }, false));
  assert.equal(await load(), null);
  assert.equal(serverIdentity(await load()).known, false);
});
