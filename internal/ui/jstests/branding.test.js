import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { brandingIntent, applyBranding } from '../static/views/branding.js';

// Operator branding replaces the stock ShinyHub identity in every `.brand` slot.
// The login card is the slot that matters most: it is the only chrome an
// anonymous visitor sees, so a self-hoster's logo has to land there and not just
// in the signed-in sidebar.

// The four brand slots as they appear in index.html: boot splash, mobile top
// bar, sidebar, and the login card.
function shellDoc() {
  return new JSDOM(`<!DOCTYPE html><body>
    <div id="boot-splash"><span class="brand"><span class="brand-art" aria-hidden="true"></span><span class="sr-only">ShinyHub</span></span></div>
    <header id="topbar"><span class="brand"><span class="brand-art" aria-hidden="true"></span><span class="sr-only">ShinyHub</span></span></header>
    <nav id="sidebar"><span class="brand"><span class="brand-art" aria-hidden="true"></span><span class="sr-only">ShinyHub</span></span></nav>
    <section id="login-view"><div class="login-box"><div class="login-brand"><span class="brand"><span class="brand-art" aria-hidden="true"></span><span class="sr-only">ShinyHub</span></span></div></div></section>
  </body>`).window.document;
}

function loginSlot(doc) {
  return doc.querySelector('.login-brand .brand');
}

// --- brandingIntent (pure) ---

test('no branding leaves the stock wordmark: nothing to apply', () => {
  for (const input of [undefined, null, {}, 'nonsense', { logo: '', site_title: '   ' }]) {
    const intent = brandingIntent(input);
    assert.equal(intent.logo, '', `logo for ${JSON.stringify(input)}`);
    assert.equal(intent.brandText, '', `brandText for ${JSON.stringify(input)}`);
    assert.deepEqual(intent.footerLinks, []);
  }
});

test('a logo wins over a site title, and carries the site name as its alt text', () => {
  const intent = brandingIntent({ logo: '/branding/logo.svg', site_title: 'ACME Analytics' });
  assert.equal(intent.logo, '/branding/logo.svg');
  assert.equal(intent.logoAlt, 'ACME Analytics');
  // No text override: the image already carries the identity.
  assert.equal(intent.brandText, '');
});

test('a logo without a site title falls back to the product name for alt text', () => {
  assert.equal(brandingIntent({ logo: '/branding/logo.svg' }).logoAlt, 'ShinyHub');
});

test('a site title alone replaces the wordmark text', () => {
  const intent = brandingIntent({ site_title: 'ACME Analytics' });
  assert.equal(intent.logo, '');
  assert.equal(intent.brandText, 'ACME Analytics');
});

test('whitespace is trimmed, so a stray-space config still brands', () => {
  const intent = brandingIntent({ logo: '  /branding/logo.svg  ', site_title: '  ACME  ' });
  assert.equal(intent.logo, '/branding/logo.svg');
  assert.equal(intent.logoAlt, 'ACME');
});

test('malformed footer links are dropped rather than rendered half-built', () => {
  const intent = brandingIntent({
    footer_links: [
      { label: 'Support', url: 'https://example.com/support' },
      { label: 'No URL' },
      { url: 'https://example.com/no-label' },
      null,
      'nonsense',
    ],
  });
  assert.deepEqual(intent.footerLinks, [{ label: 'Support', url: 'https://example.com/support' }]);
});

test('a non-array footer_links is ignored, not iterated', () => {
  assert.deepEqual(brandingIntent({ footer_links: 'https://example.com' }).footerLinks, []);
});

// --- applyBranding (DOM) ---

test('zero branding leaves every stock wordmark untouched', () => {
  const doc = shellDoc();
  applyBranding(doc, {});
  for (const slot of doc.querySelectorAll('.brand')) {
    assert.ok(slot.querySelector('.brand-art'));
    assert.equal(slot.querySelector('.sr-only').textContent, 'ShinyHub');
    assert.equal(slot.querySelector('img'), null);
  }
});

test('a configured logo reaches the LOGIN card, not just the signed-in chrome', () => {
  const doc = shellDoc();
  applyBranding(doc, { logo: '/branding/logo.svg', site_title: 'ACME Analytics' });

  const img = loginSlot(doc).querySelector('img');
  assert.ok(img, 'login card must carry the operator logo');
  assert.equal(img.getAttribute('src'), '/branding/logo.svg');
  assert.equal(img.getAttribute('alt'), 'ACME Analytics');
  assert.equal(img.className, 'brand-logo');
  // The stock wordmark is replaced, not left behind next to the logo.
  assert.equal(loginSlot(doc).querySelector('.brand-name'), null);
});

test('every brand slot gets the logo, so the identity is consistent across views', () => {
  const doc = shellDoc();
  applyBranding(doc, { logo: '/branding/logo.svg' });
  const slots = [...doc.querySelectorAll('.brand')];
  assert.equal(slots.length, 4);
  for (const slot of slots) {
    assert.equal(slot.querySelectorAll('img.brand-logo').length, 1);
  }
});

test('a site title with no logo fully replaces the stock identity', () => {
  const doc = shellDoc();
  applyBranding(doc, { site_title: 'ACME Analytics' });
  const slot = loginSlot(doc);
  assert.equal(slot.querySelector('.brand-name').textContent, 'ACME Analytics');
  assert.equal(slot.querySelector('.brand-art'), null, 'stock Orbit Hub mark must not leak into a white-label');
  assert.equal(slot.querySelector('.sr-only'), null);
  assert.equal(slot.querySelector('img'), null);
});

test('applying twice does not stack duplicate nodes', () => {
  const doc = shellDoc();
  const branding = { logo: '/branding/logo.svg', footer_links: [{ label: 'Support', url: 'https://example.com' }] };
  applyBranding(doc, branding);
  applyBranding(doc, branding);
  assert.equal(loginSlot(doc).querySelectorAll('img').length, 1);
  assert.equal(doc.querySelectorAll('footer.brand-footer').length, 1);
  assert.equal(doc.querySelectorAll('footer.brand-footer a').length, 1);
});

test('footer links render once, with rel set so an operator link cannot reach back', () => {
  const doc = shellDoc();
  applyBranding(doc, {
    footer_links: [
      { label: 'Support', url: 'https://example.com/support' },
      { label: 'Status', url: 'https://example.com/status' },
    ],
  });
  const links = [...doc.querySelectorAll('footer.brand-footer a')];
  assert.deepEqual(links.map((a) => a.textContent), ['Support', 'Status']);
  assert.deepEqual(links.map((a) => a.getAttribute('href')), [
    'https://example.com/support',
    'https://example.com/status',
  ]);
  for (const a of links) assert.equal(a.getAttribute('rel'), 'noopener noreferrer');
});

test('no footer links means no empty footer element', () => {
  const doc = shellDoc();
  applyBranding(doc, { site_title: 'ACME' });
  assert.equal(doc.querySelector('footer.brand-footer'), null);
});

test('a label is set as text, never parsed as markup', () => {
  const doc = shellDoc();
  applyBranding(doc, { footer_links: [{ label: '<img src=x onerror=alert(1)>', url: 'https://example.com' }] });
  const a = doc.querySelector('footer.brand-footer a');
  assert.equal(a.querySelector('img'), null);
  assert.equal(a.textContent, '<img src=x onerror=alert(1)>');
});
