import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { JSDOM } from 'jsdom';

// The UA stylesheet's `[hidden] { display: none }` rule has the same
// specificity as a single-class author selector, and author rules apply
// after the UA sheet, so any class carrying its own `display:` declaration
// silently wins and the "hidden" element renders as an empty box instead of
// disappearing. This drives the real style.css (not a re-implementation), so
// a regression in one of these guards fails here before it reaches the
// dashboard. Each case was confirmed toggled via `.hidden = true/false` in
// app.js or a views/*.js module; classes with no conflicting `display` rule,
// or already covered by an existing guard, are not listed here.
const CSS = readFileSync(new URL('../static/style.css', import.meta.url), 'utf8');

function computedDisplay(bodyHTML, elementSelector) {
  const dom = new JSDOM(`<!doctype html><html><head></head><body>${bodyHTML}</body></html>`);
  const style = dom.window.document.createElement('style');
  style.textContent = CSS;
  dom.window.document.head.appendChild(style);
  const el = dom.window.document.querySelector(elementSelector);
  assert.notEqual(el, null, `${elementSelector} did not match anything in the test fixture`);
  return dom.window.getComputedStyle(el).display;
}

const SINGLE_CLASS_CASES = [
  ['.snippet', 'the failed-deploy error box on Overview stays an empty bordered box until an error arrives'],
  ['.emptystate-actions', 'the dashboard empty-state action row is hidden for visitors who cannot create apps'],
  ['.deploy-summary', 'the deploy modal summary list is reset to hidden between deploys'],
  ['.deploy-progress', 'the deploy modal progress bar is reset to hidden between deploys'],
  ['.support-session-recovery-meta', 'the support-session phase/countdown line is hidden once status is unavailable'],
];

for (const [className, note] of SINGLE_CLASS_CASES) {
  test(`${className}[hidden] computes to display:none (${note})`, () => {
    const bare = className.slice(1);
    const display = computedDisplay(`<div class="${bare}" hidden></div>`, `.${bare}`);
    assert.equal(display, 'none');
  });
}

test('.support-session-recovery-actions a[hidden] computes to display:none (the dead "Return to app" resume link)', () => {
  const display = computedDisplay(
    '<div class="support-session-recovery-actions"><a hidden>Return to app</a></div>',
    'a',
  );
  assert.equal(display, 'none');
});
