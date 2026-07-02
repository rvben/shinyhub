import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';
import axe from 'axe-core';

// Automated accessibility gate for the dashboard's static shell. Loads
// index.html into jsdom, reveals every hidden section/view (the SPA hides all
// but the active one, so a plain scan would only audit the login shell), and
// runs axe-core over the whole markup against the WCAG 2.0/2.1 A + AA rule set.
//
// color-contrast is disabled: jsdom has no layout/paint engine, so it cannot
// compute rendered colours - that rule needs a real browser (kept for a future
// Playwright/CI pass). Everything structural axe CAN evaluate here - form
// labels, ARIA role validity/nesting, accessible names, list structure,
// landmarks, nested-interactive - is enforced.

const AXE_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

// runAxeOn builds a jsdom document from html, applies mutate() (e.g. reveal
// hidden nodes or inject rendered fragments), runs axe inside the jsdom realm,
// and returns the violations array.
async function runAxeOn(html, mutate) {
  const dom = new JSDOM(html, { runScripts: 'dangerously', pretendToBeVisual: true });
  const d = dom.window.document;
  if (mutate) mutate(d);
  const s = d.createElement('script');
  s.textContent = axe.source;
  d.head.appendChild(s);
  const results = await dom.window.axe.run(d, {
    runOnly: { type: 'tag', values: AXE_TAGS },
    rules: { 'color-contrast': { enabled: false } },
    resultTypes: ['violations'],
  });
  return results.violations;
}

// formatViolations renders axe output into an actionable failure message:
// rule, impact, help URL, and the offending element selectors.
function formatViolations(violations) {
  return violations
    .map((v) => {
      const nodes = v.nodes.map((n) => `      - ${n.target.join(' ')}`).join('\n');
      return `  [${v.impact}] ${v.id}: ${v.help}\n    ${v.helpUrl}\n${nodes}`;
    })
    .join('\n\n');
}

const indexHTML = readFileSync(new URL('../static/index.html', import.meta.url), 'utf8');

test('index.html has no WCAG A/AA violations with every view revealed', async () => {
  const violations = await runAxeOn(indexHTML, (d) => {
    // Reveal every hidden section so axe audits the full markup, not just the
    // initially-visible login shell. Each hidden view is shown one-at-a-time by
    // the SPA at runtime; a clean audit requires each to be violation-free.
    d.querySelectorAll('[hidden]').forEach((el) => el.removeAttribute('hidden'));
    d.querySelectorAll('[style*="display: none"],[style*="display:none"]').forEach((el) => {
      el.style.display = '';
    });
  });
  assert.equal(
    violations.length,
    0,
    `axe found ${violations.length} accessibility violation(s):\n\n${formatViolations(violations)}`,
  );
});

test('the axe gate has teeth (a known-bad fragment is flagged)', async () => {
  // Guards against a vacuous "always passes" gate: an unlabelled form control
  // must be reported. If this stops failing, the harness has silently broken and
  // the clean results above mean nothing.
  const bad = '<!DOCTYPE html><html lang="en"><head><title>t</title></head>' +
    '<body><input type="text"></body></html>';
  const violations = await runAxeOn(bad, null);
  assert.ok(
    violations.some((v) => v.id === 'label'),
    `expected the axe gate to flag the unlabelled input; got: ${violations.map((v) => v.id).join(', ') || '(none)'}`,
  );
});

test('the initially-visible shell (nothing revealed) is also clean', async () => {
  // A second, stricter-context pass: the markup a first-paint user actually sees
  // before any JS runs must be clean on its own too.
  const violations = await runAxeOn(indexHTML, null);
  assert.equal(
    violations.length,
    0,
    `axe found ${violations.length} violation(s) in the first-paint shell:\n\n${formatViolations(violations)}`,
  );
});
