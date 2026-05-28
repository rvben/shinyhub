import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import {
  summariseAutoscale,
  formatRejectsByReason,
  renderAutoscaleSummary,
  renderRejectsByReason,
} from '../static/views/autoscale.js';

// summariseAutoscale normalises the {app, effective_autoscale_target} slice of
// the GET /api/apps/:slug envelope into a flat shape ready for rendering, so
// the view layer never has to re-derive the inherited-target fallback.
test('summariseAutoscale reads the app + effective fields and flags inheritance', () => {
  const got = summariseAutoscale(
    {
      autoscale_enabled: true,
      autoscale_min_replicas: 2,
      autoscale_max_replicas: 8,
      autoscale_target: 0.75,
    },
    { effective_autoscale_target: 0.75 },
  );
  assert.deepEqual(got, {
    enabled: true,
    min: 2,
    max: 8,
    target: 0.75,
    effectiveTarget: 0.75,
    inheritsTarget: false,
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
    },
    { effective_autoscale_target: 0.8 },
  );
  assert.equal(got.target, 0);
  assert.equal(got.effectiveTarget, 0.8);
  assert.equal(got.inheritsTarget, true);
});

test('summariseAutoscale tolerates missing fields with safe defaults', () => {
  // A legacy payload missing the autoscale_* keys (or a fetch error envelope)
  // must not throw; the controls then start at sensible defaults.
  const got = summariseAutoscale({}, {});
  assert.deepEqual(got, {
    enabled: false, min: 1, max: 1, target: 0,
    effectiveTarget: 0, inheritsTarget: true,
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
// fact (enabled, bounds, target). DOM refs are passed in so the helper is
// reusable from a jsdom test without leaking globals; the same module is
// reused by app-detail.js in production.
test('renderAutoscaleSummary fills the rows and clears prior content', () => {
  const dom = new JSDOM('<dl id="autoscale-summary"><dt>stale</dt><dd>x</dd></dl>');
  const dl = dom.window.document.getElementById('autoscale-summary');

  renderAutoscaleSummary(dl, {
    enabled: true, min: 2, max: 8,
    target: 0.75, effectiveTarget: 0.75, inheritsTarget: false,
  });

  const rows = dl.querySelectorAll('dt');
  assert.ok(rows.length >= 3, `want at least 3 summary rows; got ${rows.length}`);
  // Stale content must be replaced, not appended.
  assert.equal(dl.textContent.includes('stale'), false);
  // The summary surfaces each fact verbatim.
  assert.match(dl.textContent, /enabled/i);
  assert.ok(dl.textContent.includes('2'));
  assert.ok(dl.textContent.includes('8'));
  assert.ok(dl.textContent.includes('0.75'));
});

test('renderAutoscaleSummary marks the target as inherited when target == 0', () => {
  const dom = new JSDOM('<dl id="autoscale-summary"></dl>');
  const dl = dom.window.document.getElementById('autoscale-summary');

  renderAutoscaleSummary(dl, {
    enabled: false, min: 1, max: 4,
    target: 0, effectiveTarget: 0.8, inheritsTarget: true,
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

