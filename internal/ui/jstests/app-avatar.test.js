import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { appAvatar, appIconUrl, avatarView, renderAppAvatar } from '../static/views/app-avatar.js';

function doc() {
  return new JSDOM('<!DOCTYPE html><body></body>').window.document;
}

test('appAvatar: deterministic initials + hue', () => {
  const a = appAvatar({ name: 'Sales Dashboard', slug: 'sales-dash' });
  assert.equal(a.initials, 'SD');
  assert.ok(a.hue >= 0 && a.hue < 360);
  assert.equal(appAvatar({ name: 'demo', slug: 'demo' }).initials, 'D');
});

test('appIconUrl: empty without an icon, slug-encoded + cache-busted with one', () => {
  assert.equal(appIconUrl({ slug: 'a' }), '', 'no icon_mime -> no url');
  assert.equal(appIconUrl({ slug: 'a', icon_mime: 'image/png' }), '/api/apps/a/icon',
    'icon without updated_at -> bare url');
  assert.equal(
    appIconUrl({ slug: 'my app', icon_mime: 'image/png', updated_at: '2026-06-23T10:00:00Z' }),
    '/api/apps/my%20app/icon?v=2026-06-23T10%3A00%3A00Z',
    'slug + updated_at are URL-encoded',
  );
});

test('renderAppAvatar: monogram when no icon', () => {
  const node = renderAppAvatar(doc(), avatarView({ name: 'Churn Model', slug: 'churn' }), 'lp-avatar');
  assert.equal(node.tagName, 'SPAN');
  assert.equal(node.className, 'lp-avatar');
  assert.equal(node.textContent, 'CM');
  assert.match(node.getAttribute('style') || '', /--avatar-hue/);
});

test('renderAppAvatar: <img> when an icon is set, with the --img modifier', () => {
  const view = avatarView({ name: 'Churn', slug: 'churn', icon_mime: 'image/png', updated_at: '2026-06-23T10:00:00Z' });
  const node = renderAppAvatar(doc(), view, 'lp-avatar');
  assert.equal(node.tagName, 'IMG');
  assert.equal(node.className, 'lp-avatar lp-avatar--img');
  assert.equal(node.getAttribute('src'), '/api/apps/churn/icon?v=2026-06-23T10%3A00%3A00Z');
  assert.equal(node.getAttribute('alt'), '');
  assert.equal(node.getAttribute('aria-hidden'), 'true');
});

test('renderAppAvatar: a broken icon image falls back to the monogram in place', () => {
  const d = doc();
  const parent = d.createElement('div');
  const view = avatarView({ name: 'Churn Model', slug: 'churn', icon_mime: 'image/png' });
  const img = renderAppAvatar(d, view, 'lp-avatar');
  parent.appendChild(img);
  assert.equal(parent.firstChild.tagName, 'IMG');
  // Simulate the image failing to load.
  img.dispatchEvent(new d.defaultView.Event('error'));
  assert.equal(parent.firstChild.tagName, 'SPAN', 'broken icon is replaced by the monogram');
  assert.equal(parent.firstChild.textContent, 'CM');
});
