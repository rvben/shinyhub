import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// Static WCAG contrast gate for the design tokens.
//
// The axe pass in a11y.test.js disables the color-contrast rule for a real
// reason: jsdom has no layout or paint engine, so it cannot resolve a rendered
// colour. That leaves the one accessibility failure class this project has
// actually shipped completely uncovered - a text colour dropping below the AA
// floor is invisible to every other test in the suite.
//
// These tokens are literal hex values, so the ratio is computable without
// painting anything. This file parses style.css and checks the text tokens
// against the surfaces they are drawn on, in both themes.

const css = readFileSync(new URL('../static/style.css', import.meta.url), 'utf8');

// --- contrast maths (WCAG 2.x relative luminance) ---

function channel(c) {
  const v = c / 255;
  return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
}

function luminance(hex) {
  const h = hex.replace('#', '');
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

export function contrast(fg, bg) {
  const [a, b] = [luminance(fg), luminance(bg)];
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

// --- token extraction ---

// themeTokens pulls the custom properties out of one :root block. The dark
// theme is the bare `:root {`; the light theme overrides a subset under
// `:root[data-theme="light"] {`, so light values fall back to dark ones exactly
// as the cascade resolves them in a browser.
function themeTokens(selector) {
  const start = css.indexOf(selector);
  assert.notEqual(start, -1, `style.css no longer contains a ${selector} block`);
  const open = css.indexOf('{', start);
  const end = css.indexOf('\n}', open);
  const body = css.slice(open, end);
  const tokens = {};
  for (const [, name, value] of body.matchAll(/--([\w-]+):\s*(#[0-9A-Fa-f]{6})\s*;/g)) {
    tokens[name] = value;
  }
  return tokens;
}

const dark = themeTokens('\n:root {');
const light = { ...dark, ...themeTokens(':root[data-theme="light"] {') };

// The three panel backgrounds text is drawn on. A token has to clear the floor
// on all of them, because the same token is used across cards, panels and
// nested rows.
const SURFACES = ['surface', 'surface-2', 'surface-3'];
const AA_BODY = 4.5;

// colourTokenOf reads the token a rule paints its text with, so the test tracks
// the stylesheet rather than restating it. A rule recoloured to a different
// token is then checked as the new token, and a rule given a literal colour
// fails loudly instead of silently escaping the gate.
function colourTokenOf(selector) {
  const rule = new RegExp(`${selector.replace('.', '\\.')}\\s*\\{[^}]*color:\\s*var\\(--([\\w-]+)\\)`);
  const m = css.match(rule);
  assert.ok(m, `${selector} no longer sets its colour from a design token`);
  return m[1];
}

test('the contrast helper is calibrated against known values', () => {
  // Without this, every assertion below could be passing because the maths is
  // wrong rather than because the colours are right.
  assert.equal(Math.round(contrast('#FFFFFF', '#000000') * 100) / 100, 21);
  assert.equal(Math.round(contrast('#333333', '#2A2A2A') * 100) / 100, 1.14);
  assert.equal(Math.round(contrast('#7E8CAF', '#141B32') * 100) / 100, 5.08);
});

test('body text tokens clear WCAG AA on every surface, in both themes', () => {
  // --text-dim is deliberately excluded: it is a decorative token (hairlines,
  // disabled glyphs), not body text, and holding it to the body-text floor
  // would make this gate wrong rather than strict.
  const bodyTokens = ['text', 'text-soft', 'text-muted'];
  for (const [themeName, tokens] of [['dark', dark], ['light', light]]) {
    for (const token of bodyTokens) {
      const fg = tokens[token];
      assert.ok(fg, `${themeName} theme has no --${token}`);
      for (const surface of SURFACES) {
        const ratio = contrast(fg, tokens[surface]);
        assert.ok(
          ratio >= AA_BODY,
          `${themeName}: --${token} (${fg}) on --${surface} (${tokens[surface]}) `
            + `is ${ratio.toFixed(2)}:1, below the ${AA_BODY}:1 floor for body text`,
        );
      }
    }
  }
});

test('the Overview activity separator is readable on every surface', () => {
  // The specific regression this file exists for: the separator was painted
  // with --text-dim and measured about 2:1. It is decorative and already
  // aria-hidden (overview-activity.test.js pins that half), but a sighted
  // reader still sees it between two fields, so it is held to the text floor.
  const token = colourTokenOf('.ov-activity-separator');
  for (const [themeName, tokens] of [['dark', dark], ['light', light]]) {
    for (const surface of SURFACES) {
      const ratio = contrast(tokens[token], tokens[surface]);
      assert.ok(
        ratio >= AA_BODY,
        `${themeName}: .ov-activity-separator uses --${token} (${tokens[token]}), `
          + `which is ${ratio.toFixed(2)}:1 on --${surface} - below ${AA_BODY}:1`,
      );
    }
  }
});

test('the gate distinguishes the fixed separator from the broken one', () => {
  // The second bound. Asserting only that the current token passes would also
  // pass if the floor were set to 1:1, or if the maths always returned a large
  // number. The token the finding reported must still be measured as failing.
  const broken = dark['text-dim'];
  const ratio = contrast(broken, dark['surface-2']);
  assert.ok(
    ratio < AA_BODY,
    `--text-dim (${broken}) now measures ${ratio.toFixed(2)}:1 on --surface-2. `
      + 'If that is deliberate, this control needs a new known-bad colour; as '
      + 'written the separator test above can no longer tell fixed from broken.',
  );
});
