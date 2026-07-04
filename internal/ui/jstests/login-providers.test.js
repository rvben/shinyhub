import { test } from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
import { providerVisibility, applyLoginProviders } from '../static/views/login-providers.js';

// The GitHub and Google login buttons are static markup in index.html, hidden by
// default; this module reveals a button ONLY when /api/auth/providers reports
// that provider configured, and appends the OIDC button when enabled. A server
// with no SSO configured must show a clean native form and never a dead button
// (clicking an unconfigured provider 501s).

function loginDoc() {
  return new JSDOM(`<!DOCTYPE html><div class="login-box">
    <div class="login-separator" hidden>or</div>
    <a class="github-login" href="/api/auth/github/login" hidden>Sign in with GitHub</a>
    <a class="google-login" href="/api/auth/google/login" hidden>Sign in with Google</a>
  </div>`).window.document;
}

// --- providerVisibility (pure) ---

test('no providers configured: nothing shown', () => {
  const v = providerVisibility({ github: false, google: false, oidc: { enabled: false } });
  assert.equal(v.github, false);
  assert.equal(v.google, false);
  assert.equal(v.oidc, false);
  assert.equal(v.anySSO, false);
});

test('a missing or partial response fails closed (no dead buttons)', () => {
  assert.equal(providerVisibility(undefined).anySSO, false);
  assert.equal(providerVisibility({}).anySSO, false);
  assert.equal(providerVisibility(null).anySSO, false);
  // only a strict boolean true counts as configured
  assert.equal(providerVisibility({ github: 'true' }).github, false);
  assert.equal(providerVisibility({ oidc: { enabled: 1 } }).oidc, false);
});

test('github configured shows github only', () => {
  const v = providerVisibility({ github: true, google: false, oidc: { enabled: false } });
  assert.equal(v.github, true);
  assert.equal(v.google, false);
  assert.equal(v.anySSO, true);
});

test('oidc enabled carries its display name, defaulting to Sign in with SSO', () => {
  assert.equal(providerVisibility({ oidc: { enabled: true, display_name: 'Company SSO' } }).oidcLabel, 'Company SSO');
  assert.equal(providerVisibility({ oidc: { enabled: true } }).oidcLabel, 'Sign in with SSO');
  assert.equal(providerVisibility({ oidc: { enabled: false, display_name: 'x' } }).oidcLabel, '');
});

// --- applyLoginProviders (jsdom) ---

test('only configured buttons and the separator are revealed', () => {
  const doc = loginDoc();
  applyLoginProviders(doc, { github: true, google: false, oidc: { enabled: false } });
  assert.equal(doc.querySelector('.github-login').hidden, false);
  assert.equal(doc.querySelector('.google-login').hidden, true);
  assert.equal(doc.querySelector('.login-separator').hidden, false);
  assert.equal(doc.querySelector('.oidc-login'), null);
});

test('no providers: buttons and separator stay hidden, no oidc button', () => {
  const doc = loginDoc();
  applyLoginProviders(doc, { github: false, google: false, oidc: { enabled: false } });
  assert.equal(doc.querySelector('.github-login').hidden, true);
  assert.equal(doc.querySelector('.google-login').hidden, true);
  assert.equal(doc.querySelector('.login-separator').hidden, true);
  assert.equal(doc.querySelector('.oidc-login'), null);
});

test('oidc enabled creates the button once with its label and reveals the separator', () => {
  const doc = loginDoc();
  applyLoginProviders(doc, { oidc: { enabled: true, display_name: 'Okta' } });
  const btns = doc.querySelectorAll('.oidc-login');
  assert.equal(btns.length, 1);
  assert.equal(btns[0].textContent, 'Okta');
  assert.equal(btns[0].hidden, false);
  assert.equal(btns[0].getAttribute('href'), '/api/auth/oidc/login');
  assert.equal(doc.querySelector('.login-separator').hidden, false);
});

test('applyLoginProviders is idempotent: repeated calls never duplicate the oidc button', () => {
  const doc = loginDoc();
  applyLoginProviders(doc, { oidc: { enabled: true, display_name: 'Okta' } });
  applyLoginProviders(doc, { oidc: { enabled: true, display_name: 'Okta' } });
  assert.equal(doc.querySelectorAll('.oidc-login').length, 1);
});

test('re-applying with oidc disabled hides the previously-created button and the separator', () => {
  const doc = loginDoc();
  applyLoginProviders(doc, { oidc: { enabled: true } });
  assert.equal(doc.querySelector('.oidc-login').hidden, false);
  applyLoginProviders(doc, { github: false, google: false, oidc: { enabled: false } });
  assert.equal(doc.querySelector('.oidc-login').hidden, true);
  assert.equal(doc.querySelector('.login-separator').hidden, true);
});
