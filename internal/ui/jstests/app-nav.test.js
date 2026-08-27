import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM, VirtualConsole } from 'jsdom';
import { GROUP_ORDER_FIXTURE, GROUP_ORDER_EXPECTED } from './group-order-fixture.js';

// The app switcher is JavaScript ShinyHub splices into pages it did not write.
// No Go test executes it: the Go side proves the tag is in the body, and stops
// there. Everything the visitor actually sees - the bar, the panel, the
// grouping, the dismissal - happens after that tag runs, which makes this file
// the only place those are checked at all.
//
// These tests run the REAL shipped bytes, the same file inject.go embeds, in a
// real DOM, driving the same contract the server provides: a data-nav-url that
// answers with the caller's app list.

const NAV_JS = new URL('../../appnav/assets/nav.js', import.meta.url);
const source = readFileSync(NAV_JS, 'utf8');
// A renamed or emptied source would make every assertion below pass on an
// empty document, which is the one outcome worse than failing.
if (!source.includes('shinyhub-app-nav')) {
  throw new Error(`${NAV_JS.pathname} does not look like the app switcher script`);
}

const TAG_ID = 'shinyhub-app-nav';
const HOST_ID = 'shinyhub-app-nav-host';
const NAV_URL = '/app/demo/.shinyhub/nav.json';
const HOME_URL = 'https://hub.example.com/';
const DISMISS_KEY = 'shinyhub-app-nav:dismissed';
const POSITION_KEY = 'shinyhub-app-nav:position';
const LEGACY_POSITION_KEY = 'shinyhub-app-nav:position:demo';
const SESSION_EVENT = 'shinyhub:session-status';
const SESSION_OWNER_EVENT = 'shinyhub:session-status-owner';
const BOOKMARK_CAPABILITIES_EVENT = 'shinyhub:bookmark:capabilities';
const BOOKMARK_CREATE_EVENT = 'shinyhub:bookmark:create';
const BOOKMARK_RESULT_EVENT = 'shinyhub:bookmark:result';

const flush = () => new Promise((r) => setImmediate(r));

// mount evaluates the switcher the way a browser does: a real <script> carrying
// the data-* attributes nav.go writes, appended to a real document, so
// document.currentScript is genuinely set.
//
// The shadow root is closed, which is the point of it and also means a test
// cannot reach the chrome through host.shadowRoot. attachShadow is wrapped to
// keep the root it returns. That is the harness observing, not the script
// behaving differently: the script still asks for, and gets, a closed root.
function mount({
  payload = { apps: [] },
  status = 200,
  fail = false,
  attrs = {},
  html = '<!DOCTYPE html><html><head></head><body><div id="app-root">app</div></body></html>',
  url = 'http://apps.example.com/app/demo/',
  dismissed = false,
  position = null,
  legacyPosition = null,
  compactImmediately = false,
  deferred = false,
} = {}) {
  const jsdomErrors = [];
  const virtualConsole = new VirtualConsole();
  virtualConsole.on('jsdomError', (e) => jsdomErrors.push(e.message));

  const dom = new JSDOM(html, { runScripts: 'dangerously', url, virtualConsole });
  const w = dom.window;
  const d = w.document;

  if (compactImmediately) {
    w.matchMedia = (query) => ({ matches: query.includes('max-width: 520px') });
    const nativeSetTimeout = w.setTimeout.bind(w);
    w.setTimeout = (fn, delay, ...args) => nativeSetTimeout(fn, delay === 8000 ? 0 : delay, ...args);
  }

  // jsdom computes no layout, so it answers offsetParent with null for every
  // element, always. The switcher's focusable() filters on exactly that, so
  // without this it comes back empty no matter what the panel contains - and
  // every focus assertion in this file would pass whatever the script did.
  //
  // This gives offsetParent back the one meaning the script depends on: an
  // element is laid out unless it, or something above it, is hidden. The
  // switcher hides things by the hidden attribute (the filter) and by inline
  // display (nothing yet), which is what this reads. It does NOT model
  // visibility or opacity, and it is a stand-in for layout, not layout.
  Object.defineProperty(w.HTMLElement.prototype, 'offsetParent', {
    configurable: true,
    get() {
      for (let n = this; n; n = n.parentNode || n.host) {
        if (n.nodeType !== 1) continue;
        if (n.hasAttribute('hidden')) return null;
        if (n.style && n.style.display === 'none') return null;
      }
      return d.body;
    },
  });

  if (dismissed) w.sessionStorage.setItem(DISMISS_KEY, '1');
  if (position) w.localStorage.setItem(POSITION_KEY, position);
  if (legacyPosition) w.localStorage.setItem(LEGACY_POSITION_KEY, legacyPosition);

  const fetches = [];
  let releaseFetch = null;
  w.fetch = (u, opts) => {
    fetches.push({ url: u, opts });
    if (fail) return Promise.reject(new Error('network down'));
    const answer = {
      ok: status >= 200 && status < 300,
      status,
      json: () => Promise.resolve(payload),
    };
    if (!deferred) return Promise.resolve(answer);
    // A real fetch does not answer within the click that started it. Holding it
    // open is the only way to see the panel in the state a first-time visitor
    // actually meets: open, empty, still loading.
    return new Promise((resolve) => {
      releaseFetch = () => resolve(answer);
    });
  };

  let shadow = null;
  const attachShadow = w.Element.prototype.attachShadow;
  w.Element.prototype.attachShadow = function (init) {
    const root = attachShadow.call(this, init);
    shadow = root;
    return root;
  };

  const tag = d.createElement('script');
  tag.id = TAG_ID;
  tag.setAttribute('data-nav-url', NAV_URL);
  tag.setAttribute('data-current-slug', 'demo');
  tag.setAttribute('data-home-url', HOME_URL);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null) tag.removeAttribute(k);
    else tag.setAttribute(k, v);
  }
  tag.textContent = source;
  d.body.appendChild(tag);

  const q = (sel) => (shadow ? shadow.querySelector(sel) : null);
  const qa = (sel) => (shadow ? Array.from(shadow.querySelectorAll(sel)) : []);
  const openBtn = () => q('button.switch');

  return {
    dom,
    window: w,
    document: d,
    fetches,
    jsdomErrors,
    shadow: () => shadow,
    host: () => d.getElementById(HOST_ID),
    q,
    qa,
    openBtn,
    moveBtn: () => q('button.move'),
    closeBtn: () => q('button.close'),
    restoreBtn: () => q('button.restore'),
    panel: () => q('nav.panel'),
    root: () => q('.root'),
    items: () => qa('a.item'),
    labels: () => qa('a.item .label').map((n) => n.textContent),
    headings: () => qa('.grouphead').map((n) => n.textContent.trim()),
    focused: () => (shadow ? shadow.activeElement : null),
    async open() {
      openBtn().click();
      await flush();
      await flush();
    },
    // Opens without waiting for the list: with deferred: true the fetch is still
    // in flight when this returns.
    async openPending() {
      openBtn().click();
      await flush();
    },
    async settle() {
      if (!releaseFetch) throw new Error('settle() called on a mount that is not deferred');
      releaseFetch();
      await flush();
      await flush();
    },
  };
}

const app = (slug, extra = {}) => ({ slug, name: slug, openable: true, ...extra });

test('mounts a recognizable app bar into the page without fetching anything', async () => {
  const m = mount();
  assert.ok(m.host(), 'no switcher host was added to the page');
  assert.ok(m.openBtn(), 'the bar has no control to open the panel');
  assert.equal(m.q('.current-label').textContent, 'Demo');
  assert.match(m.q('.current-action').textContent, /Switch app/);
  assert.equal(m.root().getAttribute('data-position'), 'top-right');
  assert.equal(m.fetches.length, 0);
  assert.equal(m.jsdomErrors.length, 0, `script errored: ${m.jsdomErrors.join('; ')}`);
});

test('bookmarking stays absent until an app publishes supported fields', () => {
  const m = mount();
  const trigger = m.q('button.bookmark-trigger');

  assert.equal(m.root().classList.contains('bookmark-ready'), false);
  assert.equal(trigger.getAttribute('aria-expanded'), 'false');

  m.window.dispatchEvent(new m.window.CustomEvent(BOOKMARK_CAPABILITIES_EVENT, {
    detail: {
      version: 1,
      store: 'url',
      fields: [{ id: 'region', label: 'Region', value: 'Europe' }],
    },
  }));

  assert.ok(m.root().classList.contains('bookmark-ready'));
  assert.equal(trigger.getAttribute('aria-label'), 'Bookmark this view');
});

test('the exact-view receipt names every registered filter before copying', () => {
  const m = mount();
  m.window.dispatchEvent(new m.window.CustomEvent(BOOKMARK_CAPABILITIES_EVENT, {
    detail: {
      version: 1,
      store: 'url',
      fields: [
        { id: 'region', label: 'Region', value: 'Europe' },
        { id: 'year', label: 'Reporting year', value: '2026' },
      ],
    },
  }));

  m.q('button.bookmark-trigger').click();

  assert.ok(m.root().classList.contains('bookmark-open'));
  assert.equal(m.q('.bookmark-badge').textContent, 'Exact view');
  assert.equal(m.q('.bookmark-count').textContent, '2 filters');
  assert.deepEqual(m.qa('.bookmark-field-label').map((node) => node.textContent), ['Region', 'Reporting year']);
  assert.deepEqual(m.qa('.bookmark-field-value').map((node) => node.textContent), ['Europe', '2026']);
  assert.equal(m.qa('.bookmark-check').length, 0);
  assert.match(m.q('.bookmark-intro').textContent, /exactly as shown/);
});

test('a stale bookmark becomes an explicit adjusted-view receipt with a fresh-link action', () => {
  const m = mount();
  m.window.dispatchEvent(new m.window.CustomEvent(BOOKMARK_CAPABILITIES_EVENT, {
    detail: {
      version: 1,
      store: 'url',
      schemaVersion: 2,
      fields: [
        { id: 'region', label: 'Region', value: 'Americas' },
        { id: 'product', label: 'Product', value: 'Planning' },
      ],
      adjustments: [
        {
          kind: 'migrated',
          label: 'Product',
          previous: 'Legacy planning',
          current: 'Planning',
        },
        {
          kind: 'renamed',
          label: 'Region',
          sourceLabel: 'Territory',
          previous: 'Americas',
          current: 'Americas',
        },
        {
          kind: 'removed',
          label: 'Market segment',
          previous: 'Enterprise',
          current: 'Ignored',
        },
      ],
    },
  }));

  const trigger = m.q('button.bookmark-trigger');
  assert.ok(m.root().classList.contains('bookmark-adjusted'));
  assert.match(trigger.getAttribute('aria-label'), /saved filters adjusted/);
  trigger.click();

  assert.equal(m.q('.bookmark-notice').hidden, false);
  assert.equal(m.q('.bookmark-notice-title').textContent, 'View adjusted');
  assert.deepEqual(
    m.qa('.bookmark-adjustment-label').map((node) => node.textContent),
    ['Product', 'Territory is now Region', 'Market segment'],
  );
  assert.deepEqual(
    m.qa('.bookmark-adjustment-kind').map((node) => node.textContent),
    ['Updated', 'Renamed', 'Removed'],
  );
  assert.deepEqual(
    m.qa('.bookmark-adjustment-value').map((node) => node.textContent),
    ['Legacy planning → Planning', 'Restored as Americas', 'Enterprise is no longer used'],
  );
  assert.match(m.q('.bookmark-panel').getAttribute('aria-describedby'), /bookmark-notice/);
  assert.equal(m.q('.bookmark-badge').textContent, 'Updated view');
  assert.equal(m.q('button.bookmark-primary').textContent, 'Copy updated link');
  assert.match(m.q('.bookmark-intro').textContent, /copy a fresh link/);
});

test('malformed adjustment records are ignored without hiding bookmarking', () => {
  const m = mount();
  m.window.dispatchEvent(new m.window.CustomEvent(BOOKMARK_CAPABILITIES_EVENT, {
    detail: {
      version: 1,
      store: 'url',
      fields: [{ id: 'region', label: 'Region', value: 'Europe' }],
      adjustments: [
        { kind: 'future-kind', label: 'Unsafe', previous: '<b>old</b>' },
        null,
      ],
    },
  }));

  assert.ok(m.root().classList.contains('bookmark-ready'));
  assert.equal(m.root().classList.contains('bookmark-adjusted'), false);
  m.q('button.bookmark-trigger').click();
  assert.equal(m.q('.bookmark-notice').hidden, true);
  assert.equal(m.q('button.bookmark-primary').textContent, 'Copy link');
});

test('an unknown bookmark setting shows its safely-rendered name and saved value', () => {
  const m = mount();
  m.window.dispatchEvent(new m.window.CustomEvent(BOOKMARK_CAPABILITIES_EVENT, {
    detail: {
      version: 1,
      store: 'url',
      fields: [{ id: 'region', label: 'Region', value: 'Europe' }],
      adjustments: [
        {
          kind: 'unknown',
          label: '<img src=x onerror=alert(1)>',
          previous: '<script>saved value</script>',
          current: 'Ignored',
        },
      ],
    },
  }));

  m.q('button.bookmark-trigger').click();

  assert.equal(m.q('.bookmark-notice').hidden, false);
  assert.equal(m.q('.bookmark-adjustment-label').textContent, '<img src=x onerror=alert(1)>');
  assert.equal(m.q('.bookmark-adjustment-value').textContent, 'Saved value: <script>saved value</script>');
  assert.equal(m.q('.bookmark-adjustment-kind').textContent, 'Unknown');
  assert.equal(m.qa('.bookmark-adjustments img').length, 0);
  assert.equal(m.qa('.bookmark-adjustments script').length, 0);
  assert.equal(m.q('.bookmark-badge').textContent, 'Updated view');
  assert.equal(m.q('button.bookmark-primary').textContent, 'Copy updated link');
});

test('additional unknown settings collapse into an honest overflow row', () => {
  const m = mount();
  m.window.dispatchEvent(new m.window.CustomEvent(BOOKMARK_CAPABILITIES_EVENT, {
    detail: {
      version: 1,
      store: 'url',
      fields: [{ id: 'region', label: 'Region', value: 'Europe' }],
      adjustments: [
        { kind: 'unknown', label: 'mystery', previous: 'saved', current: 'Ignored' },
        {
          kind: 'unknown_summary',
          label: '3 more unrecognized bookmark settings',
          current: 'Ignored for safety',
        },
      ],
    },
  }));

  m.q('button.bookmark-trigger').click();

  assert.deepEqual(
    m.qa('.bookmark-adjustment-value').map((node) => node.textContent),
    ['Saved value: saved', 'Ignored for safety'],
  );
  assert.deepEqual(
    m.qa('.bookmark-adjustment-kind').map((node) => node.textContent),
    ['Unknown', 'Ignored'],
  );
});

test('custom bookmarks begin with everything selected and never submit an empty selection', () => {
  const m = mount();
  const requests = [];
  m.window.addEventListener(BOOKMARK_CREATE_EVENT, (event) => requests.push(event.detail));
  m.window.dispatchEvent(new m.window.CustomEvent(BOOKMARK_CAPABILITIES_EVENT, {
    detail: {
      version: 1,
      store: 'url',
      fields: [
        { id: 'region', label: 'Region', value: 'Europe' },
        { id: 'year', label: 'Year', value: '2026' },
      ],
    },
  }));
  m.q('button.bookmark-trigger').click();
  m.q('button.bookmark-secondary').click();

  const checks = m.qa('.bookmark-check');
  assert.deepEqual(checks.map((node) => node.checked), [true, true]);
  checks[1].click();
  assert.equal(m.q('.bookmark-count').textContent, '1 of 2 filters');
  m.q('button.bookmark-primary').click();
  assert.equal(requests.length, 1);
  assert.deepEqual(Array.from(requests[0].include), ['region']);

  m.window.dispatchEvent(new m.window.CustomEvent(BOOKMARK_RESULT_EVENT, {
    detail: { version: 1, requestId: requests[0].requestId, url: 'https://hub.test/app/demo/?_inputs_=ok' },
  }));
});

test('deselecting every custom field disables the copy action', () => {
  const m = mount();
  m.window.dispatchEvent(new m.window.CustomEvent(BOOKMARK_CAPABILITIES_EVENT, {
    detail: {
      version: 1,
      store: 'url',
      fields: [{ id: 'region', label: 'Region', value: 'Europe' }],
    },
  }));
  m.q('button.bookmark-trigger').click();
  m.q('button.bookmark-secondary').click();
  m.q('.bookmark-check').click();

  assert.equal(m.q('.bookmark-count').textContent, '0 of 1 filters');
  assert.equal(m.q('button.bookmark-primary').disabled, true);
});

test('an offline snapshot becomes a compact switcher status with explicit recovery actions', async () => {
  const m = mount();
  const status = new m.window.CustomEvent(SESSION_EVENT, {
    cancelable: true,
    detail: { state: 'snapshot', url: m.window.location.href, focus: true },
  });

  assert.equal(m.window.dispatchEvent(status), false, 'the visible switcher did not claim the snapshot');
  assert.equal(status.defaultPrevented, true);
  assert.ok(m.root().classList.contains('session-snapshot'));
  assert.equal(m.q('.current-action').textContent, 'Offline snapshot');
  assert.equal(m.q('button.session-trigger').getAttribute('aria-expanded'), 'false');
  assert.equal(m.focused(), m.q('button.session-trigger'), 'focus did not follow the recovered snapshot');

  m.q('button.session-trigger').click();
  assert.ok(m.root().classList.contains('session-open'));
  assert.equal(m.q('.session-panel').getAttribute('aria-hidden'), 'false');
  assert.equal(m.q('.session-title').textContent, 'Offline snapshot');
  assert.match(m.q('.session-copy').textContent, /out of date/);
  assert.equal(m.q('a.session-primary').href, m.window.location.href);
  assert.equal(m.q('a.session-primary').target, '_blank');
  assert.match(m.q('a.session-primary').rel, /noopener/);
  assert.equal(m.q('a.session-primary').textContent, 'Start in a new tab');
  assert.equal(m.q('button.session-restart').textContent, 'Start in this tab');
  assert.equal(m.focused(), m.q('a.session-primary'));
});

test('snapshot status closes with Escape and clears when the session reconnects', async () => {
  const m = mount();
  m.window.dispatchEvent(new m.window.CustomEvent(SESSION_EVENT, {
    cancelable: true,
    detail: { state: 'snapshot', url: m.window.location.href },
  }));
  const trigger = m.q('button.session-trigger');
  trigger.click();

  m.root().dispatchEvent(
    new m.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true })
  );
  assert.equal(m.root().classList.contains('session-open'), false);
  assert.equal(trigger.getAttribute('aria-expanded'), 'false');
  assert.equal(m.focused(), trigger);

  m.window.dispatchEvent(new m.window.CustomEvent(SESSION_EVENT, {
    detail: { state: 'connected' },
  }));
  assert.equal(m.root().classList.contains('session-snapshot'), false);
  assert.equal(m.q('.current-action').textContent, 'Switch app');
});

test('a dismissed switcher releases snapshot ownership and reclaims it when restored', () => {
  const m = mount({ dismissed: true });
  const owners = [];
  m.window.addEventListener(SESSION_OWNER_EVENT, (event) => owners.push(event.detail.owner));
  const status = new m.window.CustomEvent(SESSION_EVENT, {
    cancelable: true,
    detail: { state: 'snapshot', url: m.window.location.href },
  });

  assert.equal(m.window.dispatchEvent(status), true, 'a hidden switcher incorrectly suppressed the fallback');
  assert.equal(status.defaultPrevented, false);
  m.restoreBtn().click();
  assert.deepEqual(owners, ['switcher']);
  assert.ok(m.root().classList.contains('session-snapshot'));

  m.closeBtn().click();
  assert.deepEqual(owners, ['switcher', 'overlay']);
});

test('opening keeps the current label stable when the friendly name differs from the slug', async () => {
  const m = mount({ payload: { apps: [app('demo', { name: 'Revenue forecast' })] } });
  const initialLabel = m.q('.current-label').textContent;
  const initialAccessibleName = m.openBtn().getAttribute('aria-label');
  await m.open();

  assert.equal(m.q('.current-label').textContent, initialLabel);
  assert.equal(m.openBtn().getAttribute('aria-label'), initialAccessibleName);
  assert.match(initialAccessibleName, /current app Demo/);
});

test('the placement menu offers four deliberate anchors and persists one choice across apps', async () => {
  const m = mount();
  m.moveBtn().click();

  assert.ok(m.root().classList.contains('placing'));
  assert.equal(m.moveBtn().getAttribute('aria-expanded'), 'true');
  const choices = m.qa('button.place-option');
  assert.deepEqual(choices.map((button) => button.getAttribute('data-position')), [
    'top-center', 'top-right', 'left-center', 'right-center',
  ]);

  m.q('button.place-option[data-position="right-center"]').click();
  await flush();
  assert.equal(m.root().getAttribute('data-position'), 'right-center');
  assert.equal(m.window.localStorage.getItem(POSITION_KEY), 'right-center');
  assert.equal(m.root().classList.contains('placing'), false);

  const next = mount({
    position: 'right-center',
    attrs: { 'data-current-slug': 'another-app' },
  });
  assert.equal(next.root().getAttribute('data-position'), 'right-center');
});

test('an old per-app placement is migrated to the hub-wide preference', () => {
  const m = mount({ legacyPosition: 'left-center' });

  assert.equal(m.root().getAttribute('data-position'), 'left-center');
  assert.equal(m.window.localStorage.getItem(POSITION_KEY), 'left-center');
});

test('dragging the handle previews anchors and snaps to the nearest one', async () => {
  const m = mount();
  const pointer = (type, x, y) => new m.window.MouseEvent(type, {
    bubbles: true, clientX: x, clientY: y, button: 0,
  });

  m.moveBtn().dispatchEvent(pointer('pointerdown', 512, 20));
  m.moveBtn().dispatchEvent(pointer('pointermove', 1000, 380));
  assert.ok(m.root().classList.contains('dragging'));
  assert.equal(m.q('.guide.nearest').getAttribute('data-position'), 'right-center');

  m.moveBtn().dispatchEvent(pointer('pointerup', 1000, 380));
  assert.equal(m.root().classList.contains('dragging'), false);
  assert.equal(m.root().getAttribute('data-position'), 'right-center');
  assert.equal(m.window.localStorage.getItem(POSITION_KEY), 'right-center');
});

test('Escape closes the placement menu and returns focus to its trigger', async () => {
  const m = mount();
  m.moveBtn().click();
  m.root().dispatchEvent(
    new m.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true })
  );
  await flush();

  assert.equal(m.root().classList.contains('placing'), false);
  assert.equal(m.moveBtn().getAttribute('aria-expanded'), 'false');
  assert.equal(m.focused(), m.moveBtn());
});

test('the placement menu uses arrow-key roving focus', async () => {
  const m = mount();
  m.moveBtn().click();
  const choices = m.qa('button.place-option');
  assert.equal(m.focused(), choices[1]);
  assert.deepEqual(choices.map((choice) => choice.tabIndex), [-1, 0, -1, -1]);

  choices[1].dispatchEvent(
    new m.window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, composed: true })
  );
  assert.equal(m.focused(), choices[2]);
  assert.deepEqual(choices.map((choice) => choice.tabIndex), [-1, -1, 0, -1]);

  choices[2].dispatchEvent(
    new m.window.KeyboardEvent('keydown', { key: 'End', bubbles: true, composed: true })
  );
  assert.equal(m.focused(), choices[3]);
});

test('on a narrow screen the taught bar compacts to an Apps pill and expands on use', async () => {
  const m = mount({ compactImmediately: true, payload: { apps: [app('demo')] } });
  await new Promise((resolve) => m.window.setTimeout(resolve, 5));
  assert.ok(m.root().classList.contains('compact'));
  assert.equal(m.q('.compact-label').textContent, 'Apps');

  await m.open();
  assert.equal(m.root().classList.contains('compact'), false);
  assert.ok(m.root().classList.contains('open'));
});

test('renders inside a CLOSED shadow root, so the app cannot reach the chrome', async () => {
  const m = mount();
  // The app's own scripts run in this document. If the root were open, any of
  // them could read or restyle ShinyHub's chrome through host.shadowRoot - and
  // the app's stylesheet would reach it whether or not anyone meant it to.
  assert.equal(m.host().shadowRoot, null, 'the shadow root is reachable from the page');
  assert.equal(m.shadow().mode, 'closed');
});

test('the app list is fetched only when the visitor opens the panel', async () => {
  // Every app page load would otherwise carry a request nobody asked for, on a
  // switcher most visitors never touch.
  const m = mount({ payload: { apps: [app('one'), app('two')] } });
  await flush();
  assert.equal(m.fetches.length, 0, 'the switcher fetched the app list before it was opened');

  await m.open();
  assert.equal(m.fetches.length, 1);
  assert.equal(m.fetches[0].url, NAV_URL);
  // Same-origin credentials are what make the answer the caller's own list;
  // no-store is what stops a tab from pinning a list it can no longer open.
  assert.equal(m.fetches[0].opts.credentials, 'same-origin');
  assert.equal(m.fetches[0].opts.cache, 'no-store');
});

test('lists the apps the endpoint returned, linking each to its own page', async () => {
  const m = mount({ payload: { apps: [app('alpha', { name: 'Alpha' }), app('bravo', { name: 'Bravo' })] } });
  await m.open();

  assert.deepEqual(m.labels(), ['Alpha', 'Bravo']);
  assert.deepEqual(
    m.items().map((a) => a.getAttribute('href')),
    ['/app/alpha/', '/app/bravo/']
  );
});

test('uses the dashboard sidebar order when the current app is not in the result', async () => {
  // The visitor came from that sidebar. A switcher that orders the same apps
  // differently is not a second view of one fleet, it is a contradiction they
  // have to resolve themselves. The rule is copied into nav.js because an
  // injected inline script cannot import an ES module, and a copy is exactly
  // the thing that drifts, so both are pinned against one shared fixture.
  const { groupApps } = await import('../static/views/project-groups.js');
  const m = mount({ payload: { apps: GROUP_ORDER_FIXTURE.map((a) => ({ ...a, openable: true })) } });
  await m.open();

  const sidebar = groupApps(GROUP_ORDER_FIXTURE).map((g) => g.project);
  assert.deepEqual(sidebar, GROUP_ORDER_EXPECTED, 'precondition: the sidebar rule changed');

  // The switcher draws headings, not keys: the ungrouped bucket is titled "Other
  // apps" once real projects exist, and named projects by their display name.
  assert.deepEqual(m.headings(), ['Other apps', 'Aaa', 'Bbb']);
  assert.deepEqual(m.labels(), ['Mike', 'Zulu', 'Alpha']);
});

test('puts the current group first and the current app first within it', async () => {
  const m = mount({
    payload: {
      apps: [
        app('loose', { name: 'Loose' }),
        app('sibling', { name: 'Alpha', project_slug: 'analytics', project_name: 'Analytics' }),
        app('demo', { name: 'Zulu', project_slug: 'analytics', project_name: 'Analytics' }),
        app('other', { name: 'Beta', project_slug: 'briefing', project_name: 'Briefing' }),
      ],
    },
  });
  await m.open();

  assert.deepEqual(m.headings(), ['Analytics', 'Other apps', 'Briefing']);
  assert.deepEqual(m.labels(), ['Zulu', 'Alpha', 'Loose', 'Beta']);
  assert.ok(m.items()[0].classList.contains('current'));
  assert.equal(m.items()[0].getAttribute('aria-current'), 'page');
});

test('keeps matching siblings from the current group first while filtering', async () => {
  const apps = [
    app('loose-match', { name: 'Match loose' }),
    app('sibling', { name: 'Match sibling', project_slug: 'analytics', project_name: 'Analytics' }),
    app('demo', { name: 'Current', project_slug: 'analytics', project_name: 'Analytics' }),
    app('other', { name: 'Match other', project_slug: 'briefing', project_name: 'Briefing' }),
    ...Array.from({ length: 6 }, (_, i) => app(`extra-${i}`, { name: `Extra ${i}` })),
  ];
  const m = mount({ payload: { apps } });
  await m.open();

  const filter = m.q('input.filter');
  filter.value = 'match';
  filter.dispatchEvent(new m.window.Event('input'));

  assert.deepEqual(m.headings(), ['Analytics', 'Other apps', 'Briefing']);
  assert.deepEqual(m.labels(), ['Match sibling', 'Match loose', 'Match other']);
});

test('a lone ungrouped bucket gets no heading', async () => {
  // A single "Other apps" heading over the entire list names nothing.
  const m = mount({ payload: { apps: [app('one'), app('two')] } });
  await m.open();
  assert.deepEqual(m.headings(), []);
});

test('the app the visitor is already in is marked, not offered as a destination', async () => {
  const m = mount({ payload: { apps: [app('demo', { name: 'Demo' }), app('other', { name: 'Other' })] } });
  await m.open();

  const [demo, other] = m.items();
  assert.ok(demo.classList.contains('current'));
  assert.equal(demo.getAttribute('aria-current'), 'page');
  // The marker is a word as well as a colour, and it is folded into the
  // accessible name so a screen reader announces it with the app.
  assert.match(demo.getAttribute('aria-label'), /current app/);
  assert.equal(other.getAttribute('aria-current'), null);
});

test('switching apps immediately names the destination and shows a busy row', async () => {
  const m = mount({
    payload: {
      apps: [
        app('demo', { name: 'Current app' }),
        app('forecast', { name: 'Revenue forecast' }),
        app('briefing', { name: 'Morning briefing' }),
      ],
    },
  });
  await m.open();

  const destination = m.q('.item[data-slug="forecast"]');
  // Keep jsdom on this document after the switcher's own handler has observed
  // the click. The production anchor remains native and is not prevented.
  destination.addEventListener('click', (event) => event.preventDefault(), { once: true });
  destination.dispatchEvent(new m.window.MouseEvent('click', {
    bubbles: true, cancelable: true, button: 0,
  }));

  assert.ok(m.root().classList.contains('navigating'));
  assert.equal(m.panel().getAttribute('aria-busy'), 'true');
  assert.equal(m.q('.title').textContent, 'Opening Revenue forecast…');
  assert.equal(m.q('.announcer').textContent, 'Opening Revenue forecast');
  assert.equal(m.q('.announcer').parentElement, m.root(), 'the live message sits inside the busy panel');
  assert.ok(destination.classList.contains('opening-item'));
  assert.equal(destination.querySelector('.opening-state').textContent, 'Opening');
  assert.equal(destination.getAttribute('aria-label'), 'Revenue forecast, opening');
  assert.ok(
    m.items().every((item) => item.getAttribute('aria-disabled') === 'true' && item.tabIndex === -1),
    'the old app still offered another switch while navigation was pending',
  );
});

test('a pending switch ignores a second destination and resets after back-forward restore', async () => {
  const m = mount({
    payload: { apps: [app('demo'), app('alpha'), app('bravo')] },
  });
  await m.open();

  const alpha = m.q('.item[data-slug="alpha"]');
  alpha.addEventListener('click', (event) => event.preventDefault(), { once: true });
  alpha.dispatchEvent(new m.window.MouseEvent('click', {
    bubbles: true, cancelable: true, button: 0,
  }));

  const bravo = m.q('.item[data-slug="bravo"]');
  const duplicate = new m.window.MouseEvent('click', {
    bubbles: true, cancelable: true, button: 0,
  });
  assert.equal(bravo.dispatchEvent(duplicate), false, 'the duplicate navigation was not cancelled');
  assert.equal(duplicate.defaultPrevented, true);
  assert.ok(alpha.classList.contains('opening-item'));
  assert.equal(bravo.classList.contains('opening-item'), false);

  m.window.dispatchEvent(new m.window.PageTransitionEvent('pageshow', { persisted: true }));
  assert.equal(m.root().classList.contains('navigating'), false);
  assert.equal(m.panel().getAttribute('aria-busy'), null);
  assert.equal(m.q('.title').textContent, 'Switch app');
  assert.ok(m.items().every((item) => item.getAttribute('aria-disabled') === null));
  assert.equal(m.q('.opening-state'), null);
});

test('modified app clicks keep native new-tab behavior without making this tab busy', async () => {
  const m = mount({ payload: { apps: [app('demo'), app('other', { name: 'Other app' })] } });
  await m.open();

  const destination = m.q('.item[data-slug="other"]');
  destination.addEventListener('click', (event) => event.preventDefault(), { once: true });
  destination.dispatchEvent(new m.window.MouseEvent('click', {
    bubbles: true, cancelable: true, button: 0, metaKey: true,
  }));

  assert.equal(m.root().classList.contains('navigating'), false);
  assert.equal(m.panel().getAttribute('aria-busy'), null);
  assert.equal(m.q('.title').textContent, 'Switch app');
  assert.equal(destination.querySelector('.opening-state'), null);
});

test('an app that cannot be opened says so in words, not only in colour', async () => {
  const m = mount({
    payload: { apps: [app('up', { name: 'Up' }), app('offline', { name: 'Offline', openable: false })] },
  });
  await m.open();

  const down = m.q('.item[data-slug="offline"]');
  assert.ok(down.classList.contains('down'), 'the unavailable app is not dimmed');
  // The word itself, in the row's own text. There is no aria-label to check:
  // the row is not interactive, so its name is what it says.
  assert.match(down.textContent, /Unavailable/);
});

test('an unavailable app is not a link, so it cannot cost the visitor the app they are in', async () => {
  // Dimming a row and labelling it Unavailable while leaving it navigable is
  // the worst of both: the visitor is told they cannot go there, goes anyway,
  // and lands on an error page having lost the working app they came from.
  const m = mount({
    payload: { apps: [app('up', { name: 'Up' }), app('offline', { name: 'Offline', openable: false })] },
  });
  await m.open();

  const down = m.q('.item[data-slug="offline"]');
  assert.equal(down.tagName, 'DIV', 'the unavailable app is still an anchor');
  assert.equal(down.getAttribute('href'), null, 'the unavailable app still carries a destination');

  // The available one beside it must still work, or this test would pass on a
  // switcher that had stopped linking anything at all.
  const up = m.q('.item[data-slug="up"]');
  assert.equal(up.tagName, 'A');
  assert.equal(up.getAttribute('href'), '/app/up/');

  // Not reachable by keyboard either: an unfocusable row that Tab still stops
  // on is the same broken promise, just quieter.
  assert.equal(
    m.qa('a[href], button, input').some((n) => n.getAttribute('data-slug') === 'offline'),
    false,
    'the unavailable app is still in the tab order',
  );
});

test('the filter appears only once the list is long enough to need it', async () => {
  const short = mount({ payload: { apps: Array.from({ length: 8 }, (_, i) => app(`a${i}`)) } });
  await short.open();
  assert.equal(short.q('.filterwrap').hidden, true, 'a short list was given a filter box');

  const long = mount({ payload: { apps: Array.from({ length: 9 }, (_, i) => app(`a${i}`)) } });
  await long.open();
  assert.equal(long.q('.filterwrap').hidden, false, 'a long list was left without a filter box');
});

test('filtering narrows the list by name and by slug, and says when nothing matches', async () => {
  const apps = Array.from({ length: 9 }, (_, i) => app(`slug-${i}`, { name: `App ${i}` }));
  const m = mount({ payload: { apps } });
  await m.open();

  const filter = m.q('input.filter');
  filter.value = 'slug-3';
  filter.dispatchEvent(new m.window.Event('input'));
  assert.deepEqual(m.labels(), ['App 3'], 'filtering by slug found the wrong apps');

  filter.value = 'App 4';
  filter.dispatchEvent(new m.window.Event('input'));
  assert.deepEqual(m.labels(), ['App 4']);

  filter.value = 'nothing like this';
  filter.dispatchEvent(new m.window.Event('input'));
  assert.equal(m.items().length, 0);
  assert.match(m.q('.note').textContent, /No apps match/);
});

test('a clipped list says it is clipped, in the list and in the count', async () => {
  // The server caps the answer. A switcher that renders the first page with nothing
  // to mark the edge is telling the visitor this is their whole fleet, and the
  // app they are looking for has been taken away from them.
  const apps = Array.from({ length: 12 }, (_, i) => app(`a${i}`, { name: `App ${i}` }));
  const m = mount({ payload: { apps, truncated: true } });
  await m.open();

  assert.match(m.q('.more').textContent, /Showing the first 12 apps/);
  assert.equal(m.q('.count').textContent, '12+', 'the count claims to be the whole fleet');
});

test('a filter over a clipped list does not claim to have searched everything', async () => {
  // "No apps match that filter" is a statement about the whole fleet. Over a
  // clipped list it is false, and it is exactly when a visitor is hunting for a
  // missing app that they will read it.
  const apps = Array.from({ length: 12 }, (_, i) => app(`a${i}`, { name: `App ${i}` }));
  const m = mount({ payload: { apps, truncated: true } });
  await m.open();

  const filter = m.q('input.filter');
  filter.value = 'nothing like this';
  filter.dispatchEvent(new m.window.Event('input'));

  assert.equal(m.items().length, 0);
  assert.match(m.q('.more').textContent, /Only the first 12 apps are searchable/);
});

test('a complete list carries no clipped-list note', async () => {
  // The positive control for the two tests above: the note must be a fact about
  // the answer, not decoration on every list.
  const m = mount({ payload: { apps: [app('one'), app('two')] } });
  await m.open();
  assert.equal(m.q('.more'), null, 'a complete list was reported as clipped');
  assert.equal(m.q('.count').textContent, '2');
});

test('a caller with no apps is told so, rather than shown an empty panel', async () => {
  const m = mount({ payload: { apps: [] } });
  await m.open();
  assert.match(m.q('.note').textContent, /No apps are available to you yet/);
});

test('a failed load offers a retry, and the retry actually re-fetches', async () => {
  const m = mount({ fail: true });
  await m.open();

  assert.match(m.q('.note').textContent, /Could not load the app list/);
  const retry = m.q('button.retry');
  assert.ok(retry, 'a failed load left the visitor with nothing to do');

  retry.click();
  await flush();
  await flush();
  assert.equal(m.fetches.length, 2, 'Try again did not re-request the list');
});

test('a non-200 answer is a failure, not an empty fleet', async () => {
  // Rendering "no apps" for a 500 would tell the visitor their access was
  // revoked, when in fact the server is broken.
  const m = mount({ status: 503, payload: { apps: [app('one')] } });
  await m.open();
  assert.match(m.q('.note').textContent, /Could not load the app list/);
});

test('the footer names the signed-in visitor, and stays quiet for an anonymous one', async () => {
  const named = mount({ payload: { apps: [app('one')], username: 'ruben' } });
  await named.open();
  assert.match(named.q('.who').textContent, /ruben/);

  const anon = mount({ payload: { apps: [app('one')] } });
  await anon.open();
  assert.equal(anon.q('.who').textContent, '', 'an anonymous visitor was given a name');
});

test('the dashboard link points at the configured home, not the app origin root', async () => {
  // On a deployment with a separate app origin, a relative "/" here addresses
  // the app origin, which serves nothing. That origin exists precisely so app
  // pages cannot reach the control plane by a relative path.
  const m = mount();
  assert.equal(m.q('a.home').getAttribute('href'), HOME_URL);
});

test('dismissing reduces the chrome to a restore tab, and the dismissal follows the tab', async () => {
  const m = mount({ payload: { apps: [app('one')] } });
  m.closeBtn().click();
  await flush();

  assert.ok(m.host(), 'dismissing removed the only route that can restore the switcher');
  assert.ok(m.root().classList.contains('dismissed'));
  assert.ok(m.restoreBtn(), 'dismissing left no restore control');
  assert.equal(m.window.sessionStorage.getItem(DISMISS_KEY), '1');

  // A dismissal that did not survive the app's own navigations would be no
  // dismissal at all: a Shiny app reloads its page for plenty of reasons.
  const next = mount({ dismissed: true });
  assert.ok(next.root().classList.contains('dismissed'), 'the full bar came back in a tab that dismissed it');
  assert.ok(next.restoreBtn(), 'a returning dismissed page has no recovery path');
  assert.equal(next.fetches.length, 0);
});

test('the restore tab brings the full switcher back without fetching the list', async () => {
  const m = mount({ dismissed: true });
  m.restoreBtn().click();
  await flush();

  assert.equal(m.root().classList.contains('dismissed'), false);
  assert.equal(m.window.sessionStorage.getItem(DISMISS_KEY), null);
  assert.equal(m.focused(), m.openBtn());
  assert.equal(m.fetches.length, 0, 'restoring the bar fetched a list the visitor did not open');
});

test('the dismiss control does not promise that reloading brings the switcher back', async () => {
  // The dismissal lives in sessionStorage, so a reload keeps it hidden and only
  // closing the tab clears it. Copy offering a reload as the way back sends the
  // visitor to do the one thing that cannot work.
  const m = mount({ payload: { apps: [app('one')] } });
  const title = m.closeBtn().title;
  assert.ok(title, 'the dismiss control has no tooltip at all');
  assert.doesNotMatch(title, /reload/i, `dismiss tooltip offers a reload as the way back: ${title}`);
  assert.match(title, /tab/i, `dismiss tooltip does not say the scope is this tab: ${title}`);
});

test('the panel carries its own close control', async () => {
  // The panel has to offer a way
  // out that is not the scrim or the keyboard.
  const m = mount({ payload: { apps: [app('one'), app('two')] } });
  await m.open();

  const headClose = m.q('button.headclose');
  assert.ok(headClose, 'the open panel has no close control of its own');

  headClose.click();
  await flush();
  assert.equal(m.root().classList.contains('open'), false, 'the panel close control did not close it');
  assert.equal(m.openBtn().getAttribute('aria-expanded'), 'false');
});

test('opening moves focus into the panel instead of leaving it on the bar', async () => {
  const m = mount({ payload: { apps: [app('one'), app('two')] } });
  await m.open();

  const landed = m.focused();
  assert.ok(landed, 'nothing inside the panel has focus after opening');
  assert.notEqual(landed, m.openBtn(), 'focus stayed on the bar button after the panel opened');
  assert.ok(m.panel().contains(landed), `focus landed outside the panel: ${landed.outerHTML}`);
  assert.notEqual(landed, m.q('button.headclose'),
    'focus landed on the close button, so the first Enter closes what was just opened');
  assert.equal(landed, m.items()[0], 'focus did not land on the first app in the list');
});

test('a long list opens with focus in the filter, ready to type', async () => {
  // Past the threshold the filter is the point of the panel: a visitor with
  // dozens of apps opens it to type, not to scroll.
  const apps = Array.from({ length: 12 }, (_, i) => app(`app-${i}`));
  const m = mount({ payload: { apps } });
  await m.open();

  assert.equal(m.focused(), m.q('input'), 'focus did not land in the filter of a list long enough to have one');
});

test('focus enters the panel while the list is still loading, and moves on when it arrives', async () => {
  // The list is fetched on the first open, so for the first visitor the panel is
  // empty at the moment it opens: there is nothing to focus yet. Waiting for the
  // fetch would leave focus on the bar for the length of a network round
  // trip, so the panel takes focus itself and hands it over when the list lands.
  const m = mount({ payload: { apps: [app('one'), app('two')] }, deferred: true });
  await m.openPending();

  assert.equal(m.items().length, 0, 'the list resolved early; this test is not exercising the loading state');
  assert.equal(m.focused(), m.panel(), 'focus is not in the panel while the list loads');

  await m.settle();
  assert.equal(m.focused(), m.items()[0], 'focus stayed on the panel after the list arrived');
});

test('a list that arrives after the visitor gave up does not pull focus back', async () => {
  // Closing while the fetch is still open must be final. Yanking focus into a
  // panel the visitor has already dismissed takes their cursor out of the app
  // they went back to.
  const m = mount({ payload: { apps: [app('one')] }, deferred: true });
  await m.openPending();

  m.q('.scrim').click();
  await flush();
  const afterClose = m.focused();

  await m.settle();
  assert.equal(m.root().classList.contains('open'), false, 'the panel reopened on its own');
  assert.equal(m.focused(), afterClose, 'the late list pulled focus back into a closed panel');
});

test('closing returns focus to the bar button a keyboard visitor opened it from', async () => {
  // The trap here is that document.activeElement retargets to the shadow host,
  // which is a plain div and cannot take focus. A keyboard visitor is always on
  // the bar button when they open, so reading the document instead of the root
  // records the host every time, and closing drops them to the top of the app's
  // document instead of back where they were.
  const m = mount({ payload: { apps: [app('one')] } });

  m.openBtn().focus();
  assert.equal(m.focused(), m.openBtn(), 'the bar button did not take focus');
  // Proof the trap is live in this environment rather than assumed: the
  // document really does answer with the host, so a fix reading it would look
  // correct here and fail in a browser for the same reason.
  assert.equal(m.document.activeElement, m.host(), 'jsdom did not retarget to the host');

  await m.open();
  assert.notEqual(m.focused(), m.openBtn(), 'focus never left the bar button');

  m.q('button.headclose').click();
  await flush();
  assert.equal(m.focused(), m.openBtn(), 'closing did not return focus to the bar button');
});

test('closing returns focus into the app when the visitor opened the panel from there', async () => {
  // The other half of the same line: a visitor who clicked the bar while their
  // cursor was in the app has focus outside the root, where the root reports
  // nothing. Falling through to the document is what puts them back.
  const m = mount({ payload: { apps: [app('one')] } });
  const inApp = m.document.getElementById('app-root');
  inApp.setAttribute('tabindex', '-1');
  inApp.focus();

  await m.open();
  m.q('.scrim').click();
  await flush();

  assert.equal(m.document.activeElement, inApp, 'the visitor was not put back where they were in the app');
});

test('opening and closing tracks aria state, so assistive tech is not told a hidden panel is open', async () => {
  const m = mount({ payload: { apps: [app('one')] } });
  assert.equal(m.openBtn().getAttribute('aria-expanded'), 'false');
  assert.equal(m.panel().getAttribute('aria-hidden'), 'true');

  await m.open();
  assert.equal(m.openBtn().getAttribute('aria-expanded'), 'true');
  assert.equal(m.panel().getAttribute('aria-hidden'), 'false');
  assert.ok(m.root().classList.contains('open'));

  m.q('.scrim').click();
  await flush();
  assert.equal(m.openBtn().getAttribute('aria-expanded'), 'false');
  assert.equal(m.panel().getAttribute('aria-hidden'), 'true');
  assert.equal(m.root().classList.contains('open'), false);
});

test('Escape closes the panel and does not reach the app underneath', async () => {
  // Apps bind their own shortcuts on document. A visitor pressing Escape to
  // close our panel must not also trigger whatever the app does with it.
  const m = mount({ payload: { apps: [app('one')] } });
  await m.open();

  let leaked = 0;
  m.document.addEventListener('keydown', () => { leaked += 1; });

  m.q('.root').dispatchEvent(
    new m.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true })
  );
  await flush();

  assert.equal(m.root().classList.contains('open'), false, 'Escape did not close the panel');
  assert.equal(leaked, 0, 'the Escape keypress reached the application document');
});

test('nothing is injected into a framed page', async () => {
  // A framed app is furniture inside someone else's layout, not a destination.
  const m = mount({
    html: '<!DOCTYPE html><html><body><iframe></iframe></body></html>',
  });
  const frame = m.document.querySelector('iframe').contentWindow;
  const fd = frame.document;
  fd.open();
  fd.write('<!DOCTYPE html><html><body><div>embedded app</div></body></html>');
  fd.close();

  const tag = fd.createElement('script');
  tag.id = TAG_ID;
  tag.setAttribute('data-nav-url', NAV_URL);
  tag.setAttribute('data-home-url', HOME_URL);
  tag.textContent = source;
  fd.body.appendChild(tag);

  assert.equal(fd.getElementById(HOST_ID), null, 'the switcher mounted inside a frame');
});

test('a tag with no data-nav-url mounts nothing at all', async () => {
  // Half-injected chrome that can never populate is worse than none: it is a
  // control that does nothing, on someone else's page.
  const m = mount({ attrs: { 'data-nav-url': null } });
  assert.equal(m.host(), null);
});
