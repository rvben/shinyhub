import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { CURATED_EMOJI, isSingleEmoji, renderEmojiPicker } from '../static/views/emoji-picker.js';

function doc() {
  return new JSDOM('<!DOCTYPE html><body></body>').window.document;
}

test('curated list is non-empty, unique, and shaped {emoji, name}', () => {
  assert.ok(CURATED_EMOJI.length >= 40, `expected at least 40 entries, got ${CURATED_EMOJI.length}`);
  const seen = new Set();
  for (const entry of CURATED_EMOJI) {
    assert.equal(typeof entry.emoji, 'string');
    assert.ok(entry.emoji.length > 0, 'emoji must not be empty');
    assert.equal(typeof entry.name, 'string');
    assert.ok(entry.name.trim().length > 0, 'name must not be empty');
    assert.ok(!seen.has(entry.emoji), `duplicate emoji: ${entry.emoji}`);
    seen.add(entry.emoji);
  }
});

test('model accepts one emoji and rejects empty, plain text, and a multi-emoji paste', () => {
  assert.equal(isSingleEmoji('🚀'), true);
  assert.equal(isSingleEmoji('🧑‍💻'), true, 'a ZWJ sequence is one grapheme, one emoji');
  assert.equal(isSingleEmoji(''), false, 'empty string is rejected');
  assert.equal(isSingleEmoji('   '), false, 'whitespace-only is rejected');
  assert.equal(isSingleEmoji('ab'), false, 'plain text is rejected');
  assert.equal(isSingleEmoji('🚀🚀'), false, 'two emoji concatenated is not a single glyph');
});

test('renders one real <button> per emoji, each with the entry name as aria-label', () => {
  const picked = [];
  const grid = renderEmojiPicker(doc(), { onPick: (emoji) => picked.push(emoji) });
  const buttons = Array.from(grid.querySelectorAll('button'));
  assert.equal(buttons.length, CURATED_EMOJI.length);
  buttons.forEach((btn, i) => {
    assert.equal(btn.tagName, 'BUTTON', 'each cell is a real button, not a div with a click handler');
    assert.equal(btn.getAttribute('aria-label'), CURATED_EMOJI[i].name);
    assert.equal(btn.textContent, CURATED_EMOJI[i].emoji);
  });
  buttons[3].click();
  assert.deepEqual(picked, [CURATED_EMOJI[3].emoji], 'clicking a cell calls onPick with that entry\'s emoji');
});
