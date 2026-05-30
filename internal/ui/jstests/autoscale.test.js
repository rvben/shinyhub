import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  summariseAutoscale,
  formatRejectsByReason,
  renderAutoscaleSummary,
  renderRejectsByReason,
  readAutoscaleForm,
  parseReplicaBound,
  formatRelative,
  formatCountdown,
} from '../static/views/autoscale.js';

// summariseAutoscale normalises the {app, effective_autoscale_target} slice of
// the GET /api/apps/:slug envelope into a flat shape ready for rendering, so
// the view layer never has to re-derive the inherited-target fallback. It also
// carries the live pool size (app.replicas) and a drift flag so the view can
// call out when the live pool sits outside the configured autoscale bounds -
// the controller will reconverge on its next tick, but the operator should see
// the transient gap explicitly.
test('summariseAutoscale reads the app + effective fields and flags inheritance', () => {
  const got = summariseAutoscale(
    {
      autoscale_enabled: true,
      autoscale_min_replicas: 2,
      autoscale_max_replicas: 8,
      autoscale_target: 0.75,
      replicas: 3,
    },
    { effective_autoscale_target: 0.75 },
  );
  assert.deepEqual(got, {
    enabled: true,
    current: 3,
    min: 2,
    max: 8,
    target: 0.75,
    effectiveTarget: 0.75,
    inheritsTarget: false,
    drift: false,
    lastActionAt: null,
    lastAction: '',
    inCooldown: false,
    cooldownUntil: null,
    globalEnabled: true,
  });
});

test('summariseAutoscale flags an inherited target when app.autoscale_target is 0', () => {
  // The API stores 0 to mean "inherit the runtime default"; the envelope
  // resolves it server-side. The summary keeps both values so the view can show
  // "0.80 (inherited)" honestly without re-implementing the resolution.
  const got = summariseAutoscale(
    {
      autoscale_enabled: false,
      autoscale_min_replicas: 1,
      autoscale_max_replicas: 4,
      autoscale_target: 0,
      replicas: 1,
    },
    { effective_autoscale_target: 0.8 },
  );
  assert.equal(got.target, 0);
  assert.equal(got.effectiveTarget, 0.8);
  assert.equal(got.inheritsTarget, true);
});

test('summariseAutoscale flags drift when the live pool is above max with autoscale enabled', () => {
  // A manual scale-out (or a lowered max while pool was higher) leaves the
  // pool outside the band; the controller will scale down on its next tick,
  // but the view should call this out so the operator knows what is about
  // to happen.
  const got = summariseAutoscale(
    {
      autoscale_enabled: true,
      autoscale_min_replicas: 2,
      autoscale_max_replicas: 4,
      autoscale_target: 0.75,
      replicas: 6,
    },
    { effective_autoscale_target: 0.75 },
  );
  assert.equal(got.current, 6);
  assert.equal(got.drift, true);
});

test('summariseAutoscale flags drift when the live pool is below min with autoscale enabled', () => {
  const got = summariseAutoscale(
    {
      autoscale_enabled: true,
      autoscale_min_replicas: 3,
      autoscale_max_replicas: 8,
      autoscale_target: 0.75,
      replicas: 1,
    },
    { effective_autoscale_target: 0.75 },
  );
  assert.equal(got.current, 1);
  assert.equal(got.drift, true);
});

test('summariseAutoscale never flags drift when autoscale is disabled', () => {
  // With autoscale disabled the min/max bounds are persisted (so a re-enable
  // restores them) but do not govern the live pool; the operator owns the
  // replica count directly and a "drift" call-out would be misleading.
  const got = summariseAutoscale(
    {
      autoscale_enabled: false,
      autoscale_min_replicas: 2,
      autoscale_max_replicas: 4,
      autoscale_target: 0,
      replicas: 7,
    },
    { effective_autoscale_target: 0.8 },
  );
  assert.equal(got.drift, false);
});

test('summariseAutoscale tolerates missing fields with safe defaults', () => {
  // A legacy payload missing the autoscale_* keys (or a fetch error envelope)
  // must not throw; the controls then start at sensible defaults.
  const got = summariseAutoscale({}, {});
  assert.deepEqual(got, {
    enabled: false, current: 1, min: 1, max: 1, target: 0,
    effectiveTarget: 0, inheritsTarget: true, drift: false,
    lastActionAt: null, lastAction: '', inCooldown: false,
    cooldownUntil: null, globalEnabled: true,
  });
});

// formatRejectsByReason turns the optional rejects_by_reason envelope into a
// stable list of lines for rendering. The server emits an unordered map; the
// view must render deterministically so a repeating refresh does not flicker
// the row order.
test('formatRejectsByReason returns [] for an absent or empty rollup', () => {
  assert.deepEqual(formatRejectsByReason(undefined), []);
  assert.deepEqual(formatRejectsByReason(null), []);
  assert.deepEqual(formatRejectsByReason({}), []);
  assert.deepEqual(formatRejectsByReason({ window_seconds: 600, counts: {} }), []);
});

test('formatRejectsByReason sorts by count desc, then reason asc', () => {
  const got = formatRejectsByReason({
    window_seconds: 600,
    counts: { pool_saturated: 7, degraded: 7, unknown_slug: 12 },
  });
  // Deterministic: highest count first; ties broken by reason name ascending
  // so refreshes never reorder a steady-state set.
  assert.deepEqual(got, [
    { reason: 'unknown_slug', count: 12 },
    { reason: 'degraded', count: 7 },
    { reason: 'pool_saturated', count: 7 },
  ]);
});

// renderAutoscaleSummary populates the read-only summary block with one row per
// fact (enabled, replica count vs bounds, target). DOM refs are passed in so
// the helper is reusable from a jsdom test without leaking globals; the same
// module is reused by app-detail.js in production.
test('renderAutoscaleSummary fills the rows and clears prior content', () => {
  const dom = new JSDOM('<dl id="autoscale-summary"><dt>stale</dt><dd>x</dd></dl>');
  const dl = dom.window.document.getElementById('autoscale-summary');

  renderAutoscaleSummary(dl, {
    enabled: true, current: 3, min: 2, max: 8,
    target: 0.75, effectiveTarget: 0.75, inheritsTarget: false, drift: false,
  });

  const rows = dl.querySelectorAll('dt');
  assert.ok(rows.length >= 3, `want at least 3 summary rows; got ${rows.length}`);
  // Stale content must be replaced, not appended.
  assert.equal(dl.textContent.includes('stale'), false);
  // The summary surfaces each fact verbatim.
  assert.match(dl.textContent, /enabled/i);
  assert.ok(dl.textContent.includes('3'), `want current pool 3 rendered: ${dl.textContent}`);
  assert.ok(dl.textContent.includes('2'));
  assert.ok(dl.textContent.includes('8'));
  assert.ok(dl.textContent.includes('0.75'));
});

test('renderAutoscaleSummary shows current pool alongside bounds when enabled', () => {
  // With autoscale enabled the bounds govern the live pool, so the operator
  // wants to see both ("3 / [2-8]") in a single row. The exact glyph between
  // them doesn't matter; the test asserts that current, min, and max all
  // co-occur on the replicas row so a refactor of the formatting does not
  // silently drop one.
  const dom = new JSDOM('<dl id="autoscale-summary"></dl>');
  const dl = dom.window.document.getElementById('autoscale-summary');

  renderAutoscaleSummary(dl, {
    enabled: true, current: 3, min: 2, max: 8,
    target: 0.5, effectiveTarget: 0.5, inheritsTarget: false, drift: false,
  });

  // Find the dd that immediately follows a dt mentioning replicas/pool.
  let replicasDd = null;
  for (const dt of dl.querySelectorAll('dt')) {
    if (/replica|pool/i.test(dt.textContent)) {
      replicasDd = dt.nextElementSibling;
      break;
    }
  }
  assert.ok(replicasDd, 'a replicas/pool row must be present when autoscale is enabled');
  assert.match(replicasDd.textContent, /3/, `want current=3 on replicas row: ${replicasDd.textContent}`);
  assert.match(replicasDd.textContent, /2/, `want min=2 on replicas row: ${replicasDd.textContent}`);
  assert.match(replicasDd.textContent, /8/, `want max=8 on replicas row: ${replicasDd.textContent}`);
});

test('renderAutoscaleSummary calls out drift when the pool is outside bounds', () => {
  // A drift call-out is the operator's hint that the controller will move the
  // pool back into the band on its next tick. The exact phrasing is not
  // pinned; what matters is that the word "drift" (or equivalent) appears so
  // the row is visually distinguishable from a steady-state band.
  const dom = new JSDOM('<dl id="autoscale-summary"></dl>');
  const dl = dom.window.document.getElementById('autoscale-summary');

  renderAutoscaleSummary(dl, {
    enabled: true, current: 6, min: 2, max: 4,
    target: 0.75, effectiveTarget: 0.75, inheritsTarget: false, drift: true,
  });

  assert.match(dl.textContent, /drift|reconverge|outside/i,
    `expected a drift call-out in: ${dl.textContent}`);
});

test('renderAutoscaleSummary shows a bare pool count when autoscale is disabled', () => {
  // Disabled means the bounds do not govern the live pool; rendering "3 /
  // [2-8]" would imply a relationship that does not exist. The summary
  // should surface the count plainly and let the "Autoscale: disabled" row
  // carry the rest of the context.
  const dom = new JSDOM('<dl id="autoscale-summary"></dl>');
  const dl = dom.window.document.getElementById('autoscale-summary');

  renderAutoscaleSummary(dl, {
    enabled: false, current: 3, min: 2, max: 8,
    target: 0, effectiveTarget: 0.5, inheritsTarget: true, drift: false,
  });

  let replicasDd = null;
  for (const dt of dl.querySelectorAll('dt')) {
    if (/replica|pool/i.test(dt.textContent)) {
      replicasDd = dt.nextElementSibling;
      break;
    }
  }
  assert.ok(replicasDd, 'a replicas/pool row must be present');
  assert.match(replicasDd.textContent, /3/, 'current count must render');
  // The bounds are not authoritative while disabled; we don't want the row
  // to imply a band that isn't enforced.
  assert.equal(/\[2.*8\]|2.*–.*8|2.*-.*8/.test(replicasDd.textContent), false,
    `disabled row must not render a bounds band: ${replicasDd.textContent}`);
});

test('renderAutoscaleSummary marks the target as inherited when target == 0', () => {
  const dom = new JSDOM('<dl id="autoscale-summary"></dl>');
  const dl = dom.window.document.getElementById('autoscale-summary');

  renderAutoscaleSummary(dl, {
    enabled: false, current: 1, min: 1, max: 4,
    target: 0, effectiveTarget: 0.8, inheritsTarget: true, drift: false,
  });

  // The effective value must be shown alongside an inheritance hint so the
  // operator can see what the controller will actually use.
  assert.ok(dl.textContent.includes('0.8'), `want 0.8 in: ${dl.textContent}`);
  assert.match(dl.textContent, /inherited/i);
});

test('renderRejectsByReason populates rows and reveals the container', () => {
  const dom = new JSDOM(`
    <section id="rejects" hidden>
      <ul id="rejects-list"></ul>
    </section>
  `);
  const section = dom.window.document.getElementById('rejects');
  const list = dom.window.document.getElementById('rejects-list');

  renderRejectsByReason(section, list, [
    { reason: 'pool_saturated', count: 12 },
    { reason: 'degraded', count: 3 },
  ]);

  assert.equal(section.hidden, false);
  const items = list.querySelectorAll('li');
  assert.equal(items.length, 2);
  assert.match(items[0].textContent, /pool_saturated/);
  assert.match(items[0].textContent, /12/);
  assert.match(items[1].textContent, /degraded/);
});

test('renderRejectsByReason hides the container for an empty rollup', () => {
  const dom = new JSDOM(`
    <section id="rejects">
      <ul id="rejects-list"><li>stale</li></ul>
    </section>
  `);
  const section = dom.window.document.getElementById('rejects');
  const list = dom.window.document.getElementById('rejects-list');

  renderRejectsByReason(section, list, []);

  // No rejections in the window means no rollup block: hiding it (and clearing
  // any stale rows) keeps the panel uncluttered for a healthy app.
  assert.equal(section.hidden, true);
  assert.equal(list.querySelectorAll('li').length, 0);
});

// readAutoscaleForm is the pure validator behind the Configuration tab's
// autoscale fieldset. It reads enabled/min/max/target from a form-like
// document and returns {payload, error}; payload is shaped for the PATCH
// /api/apps/:slug autoscale block (see internal/api/apps.go handlePatchApp)
// so the save wrapper in app.js never has to re-derive the contract. Keeping
// validation in a pure function means the same rules run under jsdom in this
// test and behind the production Save button.

function autoscaleFormDom({
  enabled = false,
  min = '',
  max = '',
  targetMode = '',
  target = '',
} = {}) {
  // Mirrors the fieldset shape in internal/ui/static/index.html so the form
  // helper is exercised against the same selectors the production form uses.
  const dom = new JSDOM(`
    <form>
      <input id="autoscale-enabled" type="checkbox" ${enabled ? 'checked' : ''}>
      <input id="autoscale-min" type="number" value="${min}">
      <input id="autoscale-max" type="number" value="${max}">
      <input type="radio" name="autoscale-target-mode" value="default" ${targetMode === 'default' ? 'checked' : ''}>
      <input type="radio" name="autoscale-target-mode" value="custom" ${targetMode === 'custom' ? 'checked' : ''}>
      <input id="autoscale-target" type="number" value="${target}">
    </form>
  `);
  return dom.window.document;
}

test('readAutoscaleForm builds a clean payload from a valid enabled form', () => {
  const doc = autoscaleFormDom({
    enabled: true, min: '2', max: '8',
    targetMode: 'custom', target: '0.75',
  });
  const got = readAutoscaleForm(doc);
  // The server merges the autoscale block over the current row, so the four
  // fields are sent atomically; "target" carries the explicit override.
  assert.equal(got.error, null);
  assert.deepEqual(got.payload, {
    enabled: true, min_replicas: 2, max_replicas: 8, target: 0.75,
  });
});

test('readAutoscaleForm sends target=0 when the operator picks the runtime default', () => {
  // Picking "Use runtime default" is the operator saying "inherit"; the server
  // stores 0 to mean exactly that. The radio choice must round-trip through
  // the payload so a save after a populate is idempotent.
  const doc = autoscaleFormDom({
    enabled: true, min: '1', max: '4', targetMode: 'default',
  });
  const got = readAutoscaleForm(doc);
  assert.equal(got.error, null);
  assert.deepEqual(got.payload, {
    enabled: true, min_replicas: 1, max_replicas: 4, target: 0,
  });
});

test('readAutoscaleForm allows min=0 / max<min while disabled', () => {
  // While autoscale is disabled the bounds are persisted but do not govern
  // the live pool. The handler still rejects values outside [0,1000], but it
  // does not require min>=1 or max>=min - so neither does the form. Saving a
  // disabled row preserves the last edit verbatim, including a deliberately
  // narrow band the operator parks for a future re-enable.
  const doc = autoscaleFormDom({
    enabled: false, min: '0', max: '0', targetMode: 'default',
  });
  const got = readAutoscaleForm(doc);
  assert.equal(got.error, null);
  assert.deepEqual(got.payload, {
    enabled: false, min_replicas: 0, max_replicas: 0, target: 0,
  });
});

test('readAutoscaleForm rejects min<1 when autoscale is enabled', () => {
  const doc = autoscaleFormDom({
    enabled: true, min: '0', max: '4', targetMode: 'default',
  });
  const got = readAutoscaleForm(doc);
  assert.equal(got.payload, null);
  assert.match(got.error, /min.*1|at least 1/i);
});

test('readAutoscaleForm rejects max<min when autoscale is enabled', () => {
  const doc = autoscaleFormDom({
    enabled: true, min: '5', max: '2', targetMode: 'default',
  });
  const got = readAutoscaleForm(doc);
  assert.equal(got.payload, null);
  assert.match(got.error, /max.*min|greater than or equal/i);
});

test('readAutoscaleForm rejects an out-of-range custom target', () => {
  // The handler caps target at [0,1]; the form mirrors that range so the user
  // sees a clear inline message instead of a generic 400 from the API.
  const doc = autoscaleFormDom({
    enabled: true, min: '1', max: '4', targetMode: 'custom', target: '1.5',
  });
  const got = readAutoscaleForm(doc);
  assert.equal(got.payload, null);
  assert.match(got.error, /target/i);
});

test('readAutoscaleForm rejects fractional replica bounds instead of truncating', () => {
  // <input type="number"> accepts "1.5" because step is "1" by default but
  // user agents do not always enforce step on raw entry; parseInt would
  // silently truncate to 1 and the operator's intent ("between 1 and 1.5")
  // would be lost without an error. We refuse the value so the typo surfaces.
  const doc = autoscaleFormDom({
    enabled: true, min: '1.5', max: '4', targetMode: 'default',
  });
  const got = readAutoscaleForm(doc);
  assert.equal(got.payload, null);
  assert.match(got.error, /whole number|integer|fraction/i);
});

// parseReplicaBound is the shared integer parser the save path and the live
// ceiling preview both consume so the preview never shows a different bound
// than the one Save will send. It returns the integer value when the input
// is a valid bound in [0,1000], or null for blank/fractional/exponent-
// truncated/out-of-range/non-numeric inputs.
test('parseReplicaBound accepts whole numbers in [0,1000]', () => {
  assert.equal(parseReplicaBound('0'), 0);
  assert.equal(parseReplicaBound('1'), 1);
  assert.equal(parseReplicaBound('1000'), 1000);
  // Exponent notation that resolves to an integer in range is accepted at its
  // numeric value, matching what Number() would parse and what the server-side
  // contract would store. The save path will surface a clearer error if the
  // value exceeds runtime.MaxReplicas; the parser does not pretend to know.
  assert.equal(parseReplicaBound('1e2'), 100);
  // Trims surrounding whitespace so a stray space does not invalidate the entry.
  assert.equal(parseReplicaBound(' 4 '), 4);
});

test('parseReplicaBound returns null for blank / fractional / out-of-range / non-numeric input', () => {
  assert.equal(parseReplicaBound(''), null);
  assert.equal(parseReplicaBound('   '), null);
  assert.equal(parseReplicaBound('1.5'), null);   // fractional - not a whole number
  assert.equal(parseReplicaBound('-1'), null);    // below the [0,1000] band
  assert.equal(parseReplicaBound('1001'), null);  // above the [0,1000] band
  assert.equal(parseReplicaBound('abc'), null);
  assert.equal(parseReplicaBound(null), null);
  assert.equal(parseReplicaBound(undefined), null);
});

test('readAutoscaleForm reads exponent-notation bounds as their numeric value, not the truncated prefix', () => {
  // parseInt("1e2", 10) returns 1 (a silent two-order-of-magnitude off-by);
  // Number("1e2") returns 100. The form must use the latter so an operator
  // who types "1e2" either gets the value they wrote (100) or, if that
  // exceeds the runtime cap, sees the server's clear "must be <= N" error.
  // The silent truncation is the bug we will not ship.
  const doc = autoscaleFormDom({
    enabled: true, min: '1', max: '1e2', targetMode: 'default',
  });
  const got = readAutoscaleForm(doc);
  assert.equal(got.error, null);
  assert.equal(got.payload.max_replicas, 100,
    `want max_replicas=100 from "1e2", got ${got.payload && got.payload.max_replicas}`);
});

// ---- formatRelative ----

test('formatRelative returns "" for falsy tsMs', () => {
  assert.equal(formatRelative(Date.now(), null), '');
  assert.equal(formatRelative(Date.now(), 0), '');
  assert.equal(formatRelative(Date.now(), undefined), '');
});

test('formatRelative returns "just now" for ts within 60 s', () => {
  const now = Date.now();
  const got = formatRelative(now, now - 30_000);
  assert.equal(got, 'just now');
});

test('formatRelative returns minutes for ts 2-59 min ago', () => {
  const now = Date.now();
  const got = formatRelative(now, now - 2 * 60_000);
  assert.match(got, /2 minutes? ago/);
});

test('formatRelative returns hours for ts 1-23 h ago', () => {
  const now = Date.now();
  const got = formatRelative(now, now - 90 * 60_000);
  assert.match(got, /1 hour ago/);
});

test('formatRelative returns days for ts 24 h+ ago', () => {
  const now = Date.now();
  const got = formatRelative(now, now - 25 * 3600_000);
  assert.match(got, /1 day ago/);
});

// ---- formatCountdown ----

test('formatCountdown returns "" for falsy untilMs', () => {
  assert.equal(formatCountdown(Date.now(), null), '');
  assert.equal(formatCountdown(Date.now(), 0), '');
});

test('formatCountdown returns "ready" when cooldown has passed', () => {
  const now = Date.now();
  assert.equal(formatCountdown(now, now - 1000), 'ready');
});

test('formatCountdown returns "in N s" for sub-minute remaining', () => {
  const now = Date.now();
  const got = formatCountdown(now, now + 43_000);
  assert.match(got, /in \d+ s/);
});

test('formatCountdown returns "in N m" for sub-hour remaining', () => {
  const now = Date.now();
  const got = formatCountdown(now, now + 3 * 60_000);
  assert.match(got, /in \d+ m/);
});

// ---- summariseAutoscale new fields (safe-default design) ----

test('summariseAutoscale returns safe defaults for new fields when absent', () => {
  // Existing callers pass objects without the new fields; these must not error
  // and must return safe defaults so old tests and edge-case envelopes are stable.
  const got = summariseAutoscale(
    { autoscale_enabled: true, autoscale_min_replicas: 1, autoscale_max_replicas: 4,
      autoscale_target: 0.75, replicas: 2 },
    { effective_autoscale_target: 0.75 },
  );
  assert.equal(got.lastActionAt, null, 'lastActionAt defaults to null');
  assert.equal(got.lastAction, '', 'lastAction defaults to empty string');
  assert.equal(got.inCooldown, false, 'inCooldown defaults to false');
  assert.equal(got.cooldownUntil, null, 'cooldownUntil defaults to null');
  assert.equal(got.globalEnabled, true, 'globalEnabled defaults to true (no spurious kill-switch warning)');
});

test('summariseAutoscale parses autoscale_status and global_autoscale_enabled from envelope', () => {
  const lastAt = new Date('2026-05-30T10:00:00Z');
  const coolUntil = new Date('2026-05-30T10:05:00Z');
  const got = summariseAutoscale(
    { autoscale_enabled: true, autoscale_min_replicas: 1, autoscale_max_replicas: 4,
      autoscale_target: 0.75, replicas: 2 },
    {
      effective_autoscale_target: 0.75,
      autoscale_status: {
        last_action_at: lastAt.toISOString(),
        last_action: 'up',
        in_cooldown: true,
        cooldown_until: coolUntil.toISOString(),
      },
      global_autoscale_enabled: false,
    },
  );
  assert.ok(got.lastActionAt instanceof Date, 'lastActionAt must be a Date');
  assert.equal(got.lastActionAt.getTime(), lastAt.getTime());
  assert.equal(got.lastAction, 'up');
  assert.equal(got.inCooldown, true);
  assert.ok(got.cooldownUntil instanceof Date, 'cooldownUntil must be a Date');
  assert.equal(got.cooldownUntil.getTime(), coolUntil.getTime());
  assert.equal(got.globalEnabled, false);
});

// ---- renderAutoscaleSummary additions ----

test('renderAutoscaleSummary with safe defaults: existing rows only, no errors', () => {
  // When the new fields are absent (old code path), only the existing 3 rows
  // render and no exception is thrown. This is the backward-compat contract.
  const dom = new JSDOM('<dl id="dl"></dl>');
  const dl = dom.window.document.getElementById('dl');
  // Should not throw with minimal s object (no new fields).
  renderAutoscaleSummary(dl, {
    enabled: true, current: 2, min: 1, max: 4,
    target: 0.75, effectiveTarget: 0.75, inheritsTarget: false, drift: false,
    lastActionAt: null, lastAction: '', inCooldown: false, cooldownUntil: null,
    globalEnabled: true,
  });
  const rows = dl.querySelectorAll('dt');
  assert.ok(rows.length >= 3, `want at least 3 rows; got ${rows.length}`);
  // No "Last scaled" row when lastAction is empty.
  const text = dl.textContent;
  assert.ok(!text.includes('Last scaled') || text.includes('never'),
    'when lastAction="" the Last scaled row should either be absent or say "never"');
});

test('renderAutoscaleSummary shows "Last scaled" row with direction when lastAction is set', () => {
  const dom = new JSDOM('<dl id="dl"></dl>');
  const dl = dom.window.document.getElementById('dl');
  const now = Date.now();
  renderAutoscaleSummary(dl, {
    enabled: true, current: 3, min: 1, max: 8,
    target: 0.75, effectiveTarget: 0.75, inheritsTarget: false, drift: false,
    lastActionAt: new Date(now - 90_000),
    lastAction: 'up',
    inCooldown: false, cooldownUntil: null,
    globalEnabled: true,
  });
  assert.match(dl.textContent, /Last scaled/i);
  assert.match(dl.textContent, /up/i);
  assert.match(dl.textContent, /ago/i);
});

test('renderAutoscaleSummary shows "Cooldown" row with "ready" when not in cooldown', () => {
  const dom = new JSDOM('<dl id="dl"></dl>');
  const dl = dom.window.document.getElementById('dl');
  const now = Date.now();
  renderAutoscaleSummary(dl, {
    enabled: true, current: 2, min: 1, max: 4,
    target: 0.5, effectiveTarget: 0.5, inheritsTarget: false, drift: false,
    lastActionAt: new Date(now - 5 * 60_000),
    lastAction: 'down',
    inCooldown: false, cooldownUntil: new Date(now - 1000),
    globalEnabled: true,
  });
  assert.match(dl.textContent, /Cooldown/i);
  assert.match(dl.textContent, /ready/i);
});

test('renderAutoscaleSummary shows in-cooldown countdown when inCooldown is true', () => {
  const dom = new JSDOM('<dl id="dl"></dl>');
  const dl = dom.window.document.getElementById('dl');
  const now = Date.now();
  renderAutoscaleSummary(dl, {
    enabled: true, current: 4, min: 1, max: 8,
    target: 0.5, effectiveTarget: 0.5, inheritsTarget: false, drift: false,
    lastActionAt: new Date(now - 30_000),
    lastAction: 'up',
    inCooldown: true, cooldownUntil: new Date(now + 90_000),
    globalEnabled: true,
  });
  assert.match(dl.textContent, /Cooldown/i);
  // in 1 m or in N s depending on remaining time
  assert.match(dl.textContent, /in \d+/i);
});

test('renderAutoscaleSummary shows kill-switch warning when enabled && !globalEnabled', () => {
  const dom = new JSDOM('<dl id="dl"></dl>');
  const dl = dom.window.document.getElementById('dl');
  renderAutoscaleSummary(dl, {
    enabled: true, current: 2, min: 1, max: 4,
    target: 0.75, effectiveTarget: 0.75, inheritsTarget: false, drift: false,
    lastActionAt: null, lastAction: '', inCooldown: false, cooldownUntil: null,
    globalEnabled: false,
  });
  // The warning must mention the global controller being disabled.
  assert.match(dl.parentNode.textContent || dl.textContent,
    /global controller is disabled|runtime\.autoscale\.enabled=false/i);
});

test('renderAutoscaleSummary does NOT show kill-switch warning when autoscale is disabled', () => {
  // app.autoscale_enabled = false, global = false: no warning needed because
  // autoscale wasn't going to run anyway.
  const dom = new JSDOM('<dl id="dl"></dl>');
  const dl = dom.window.document.getElementById('dl');
  renderAutoscaleSummary(dl, {
    enabled: false, current: 1, min: 1, max: 4,
    target: 0, effectiveTarget: 0.8, inheritsTarget: true, drift: false,
    lastActionAt: null, lastAction: '', inCooldown: false, cooldownUntil: null,
    globalEnabled: false,
  });
  // No warning when app autoscale is disabled regardless of global flag.
  const fullText = (dl.parentNode || dl).textContent;
  assert.ok(
    !fullText.includes('global controller is disabled'),
    'kill-switch warning must not appear when app autoscale is disabled',
  );
});

// ---- Kill-switch warning accumulation regression ----

test('renderAutoscaleSummary: kill-switch warning does not accumulate across repeated calls', () => {
  // Regression: the warning <p> was appended to dl.parentNode on every call but
  // dl.innerHTML='' only clears dl itself, so each 10s poll added another warning.
  const dom = new JSDOM('<section><dl id="dl"></dl></section>');
  const dl = dom.window.document.getElementById('dl');
  const s = {
    enabled: true, current: 2, min: 1, max: 4,
    target: 0.75, effectiveTarget: 0.75, inheritsTarget: false, drift: false,
    lastActionAt: null, lastAction: '', inCooldown: false, cooldownUntil: null,
    globalEnabled: false,
  };

  renderAutoscaleSummary(dl, s);
  renderAutoscaleSummary(dl, s);
  const warnings = dl.parentNode.querySelectorAll('.autoscale-killswitch-warning');
  assert.equal(warnings.length, 1,
    `want exactly 1 kill-switch warning after 2 renders; got ${warnings.length}`);
});

test('renderAutoscaleSummary: kill-switch warning is removed when globalEnabled flips back to true', () => {
  const dom = new JSDOM('<section><dl id="dl"></dl></section>');
  const dl = dom.window.document.getElementById('dl');
  const sKilled = {
    enabled: true, current: 2, min: 1, max: 4,
    target: 0.75, effectiveTarget: 0.75, inheritsTarget: false, drift: false,
    lastActionAt: null, lastAction: '', inCooldown: false, cooldownUntil: null,
    globalEnabled: false,
  };
  const sOk = { ...sKilled, globalEnabled: true };

  renderAutoscaleSummary(dl, sKilled);
  assert.equal(dl.parentNode.querySelectorAll('.autoscale-killswitch-warning').length, 1,
    'warning must appear when globalEnabled=false');

  renderAutoscaleSummary(dl, sOk);
  assert.equal(dl.parentNode.querySelectorAll('.autoscale-killswitch-warning').length, 0,
    'warning must be removed when globalEnabled flips back to true');
});

// ---- formatRelative future timestamp (clock skew) ----

test('formatRelative returns "just now" for a future timestamp (clock skew)', () => {
  // A timestamp in the future (server clock ahead of client) must not produce
  // "-N days ago". The guard clamps any negative diff to "just now".
  const now = Date.now();
  // Small skew: sub-minute; the < 60 branch catches this without the guard.
  assert.equal(formatRelative(now, now + 5_000), 'just now');
  // Large skew: server 25 h ahead; without the guard this falls through to the
  // days branch and produces "-1 days ago".
  assert.equal(formatRelative(now, now + 25 * 3600_000), 'just now');
});
