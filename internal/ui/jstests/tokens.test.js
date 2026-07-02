import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { tokenListModels, renderTokenList } from '../static/views/tokens.js';

const NOW = Date.parse('2026-07-02T12:00:00Z');

function tok(id, name, iso) {
  return { id, name, created_at: iso };
}

test('tokenListModels sorts newest-first and labels created time', () => {
  const models = tokenListModels(
    [
      tok(1, 'old', '2026-07-01T12:00:00Z'), // 1d ago
      tok(2, 'new', '2026-07-02T11:59:30Z'), // 30s ago
      tok(3, 'mid', '2026-07-02T10:00:00Z'), // 2h ago
    ],
    NOW,
  );
  assert.deepEqual(models.map((m) => m.id), [2, 3, 1], 'newest-first by created_at');
  assert.equal(models[0].name, 'new');
  assert.equal(models[0].createdLabel, '30s ago');
  assert.equal(models[1].createdLabel, '2h ago');
  assert.equal(models[2].createdLabel, '1d ago');
  assert.equal(models[0].createdISO, '2026-07-02T11:59:30Z', 'ISO passthrough for the title/tooltip');
});

test('tokenListModels returns [] for no tokens', () => {
  assert.deepEqual(tokenListModels([], NOW), []);
  assert.deepEqual(tokenListModels(null, NOW), []);
});

function fixture() {
  const dom = new JSDOM('<!DOCTYPE html><body><div id="list"></div></body>');
  return dom.window.document;
}

test('renderTokenList renders one revoke-able row per token', () => {
  const doc = fixture();
  const container = doc.getElementById('list');
  renderTokenList(container, tokenListModels([tok(7, 'ci-deploy', '2026-07-02T11:00:00Z')], NOW), doc);
  const rows = container.querySelectorAll('[data-token-row]');
  assert.equal(rows.length, 1);
  assert.match(rows[0].textContent, /ci-deploy/);
  assert.match(rows[0].textContent, /1h ago/);
  const btn = rows[0].querySelector('button[data-token-id]');
  assert.ok(btn, 'row has a revoke button');
  assert.equal(btn.getAttribute('data-token-id'), '7');
  assert.equal(btn.getAttribute('data-token-name'), 'ci-deploy');
});

test('renderTokenList shows an empty state when there are no tokens', () => {
  const doc = fixture();
  const container = doc.getElementById('list');
  renderTokenList(container, [], doc);
  assert.equal(container.querySelectorAll('[data-token-row]').length, 0);
  const empty = container.querySelector('[data-tokens-empty]');
  assert.ok(empty, 'renders an empty-state element');
  assert.match(empty.textContent, /No API tokens/i);
});

test('renderTokenList clears prior content on re-render', () => {
  const doc = fixture();
  const container = doc.getElementById('list');
  renderTokenList(container, tokenListModels([tok(1, 'a', '2026-07-02T11:00:00Z')], NOW), doc);
  renderTokenList(container, tokenListModels([tok(2, 'b', '2026-07-02T11:00:00Z')], NOW), doc);
  const rows = container.querySelectorAll('[data-token-row]');
  assert.equal(rows.length, 1, 'previous rows are cleared');
  assert.equal(rows[0].querySelector('.token-name').textContent, 'b');
});

test('renderTokenList escapes token names via textContent (no HTML injection)', () => {
  const doc = fixture();
  const container = doc.getElementById('list');
  renderTokenList(container, tokenListModels([tok(1, '<img src=x onerror=alert(1)>', '2026-07-02T11:00:00Z')], NOW), doc);
  assert.equal(container.querySelectorAll('img').length, 0, 'name is not parsed as HTML');
  assert.match(container.textContent, /<img src=x/);
});
