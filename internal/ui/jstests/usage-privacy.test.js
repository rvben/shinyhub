import { test } from 'node:test';
import assert from 'node:assert/strict';
import { usagePrivacyErrorMessage } from '../static/views/usage-privacy.js';

// Usage privacy's PATCH /api/apps/{slug} validation errors are server-authored
// strings naming the wire field usage_identity_mode (internal/api/apps.go),
// which reads as internal/technical next to the section's plain-language
// labels ("Usage identity granularity"). usagePrivacyErrorMessage rewrites the
// known messages and passes through anything it does not recognize.

test('rewrites the hub-policy-ceiling message to name the on-screen label', () => {
  const rewritten = usagePrivacyErrorMessage('usage_identity_mode cannot collect more identity than the hub policy');
  assert.doesNotMatch(rewritten, /usage_identity_mode/, 'the wire field name must not appear in the rewritten message');
  assert.match(rewritten, /hub policy/);
});

test('rewrites the invalid-enum message to name the on-screen label', () => {
  const rewritten = usagePrivacyErrorMessage('usage_identity_mode must be disabled, unattributed, pseudonymous, identified, or null');
  assert.doesNotMatch(rewritten, /usage_identity_mode/);
});

test('passes through an unrecognized message unchanged rather than guessing', () => {
  assert.equal(usagePrivacyErrorMessage('usage privacy policy unavailable'), 'usage privacy policy unavailable');
  assert.equal(usagePrivacyErrorMessage('Network error'), 'Network error');
  assert.equal(usagePrivacyErrorMessage(''), '');
});
