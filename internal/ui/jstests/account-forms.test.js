import { test } from 'node:test';
import assert from 'node:assert/strict';
import { profileFieldErrors } from '../static/views/auth-fields.js';
import { accountForms } from './fixtures/account-forms.js';
const response = (status, body) => new Response(JSON.stringify(body), { status });

test('profile validation follows server code point and byte boundaries', () => {
  assert.deepEqual(profileFieldErrors('😀'.repeat(80), '', ''), {});
  assert.equal(profileFieldErrors('😀'.repeat(81), '', '').name, 'Use 80 characters or fewer.');
  assert.ok(profileFieldErrors('', '', 'x'.repeat(15)).current);
  assert.ok(profileFieldErrors('', 'old', '😀'.repeat(8)).new);
  assert.deepEqual(profileFieldErrors('', 'old', '😀'.repeat(18)), {});
  assert.match(profileFieldErrors('', 'old', '😀'.repeat(19)).new, /too long/);
});

test('password visibility is independent and resets without copying secrets', () => {
  const f = accountForms();
  try {
    const button = f.doc.querySelector('[data-password-toggle="profile-new-password"]');
    f.ctx.profileNewPw.value = 'private example'; button.click();
    assert.equal(f.ctx.profileNewPw.type, 'text');
    assert.equal(button.getAttribute('aria-label'), 'Hide new password');
    assert.equal(f.ctx.profileCurrentPw.type, 'password');
    f.ctx.passwordVisibility.reset(f.ctx.profileForm);
    assert.equal(f.ctx.profileNewPw.type, 'password');
    assert.equal(button.getAttribute('aria-pressed'), 'false');
    assert.ok(!button.outerHTML.includes('private example'));
  } finally { f.close(); }
});

test('closing respects declined discard and saving; confirmed close clears password fields', () => {
  let discard = false; const f = accountForms({ confirm: () => discard });
  try {
    f.ctx.profileDisplayName.value = 'Changed'; f.ctx.profileNewPw.value = 'a draft password';
    assert.equal(f.ctx.closeProfileModal(), false); assert.equal(f.ctx.profileModal.hidden, false);
    discard = true; f.ctx.profileSaving = true;
    assert.equal(f.ctx.closeProfileModal(), false);
    f.ctx.profileSaving = false; assert.equal(f.ctx.closeProfileModal(), true);
    assert.equal(f.ctx.profileNewPw.value, ''); assert.equal(f.ctx.profileModal.hidden, true);
  } finally { f.close(); }
});

test('invalid fields focus the first correction and prevent backend writes', async () => {
  let calls = 0; const f = accountForms({ api: async () => { calls++; } });
  try {
    f.ctx.profileNewPw.value = 'too short'; await f.submit();
    assert.equal(calls, 0); assert.equal(f.doc.activeElement, f.ctx.profileCurrentPw);
    assert.equal(f.ctx.profilePwSection.open, true);
    assert.equal(f.ctx.profileNewPw.getAttribute('aria-invalid'), 'true');
  } finally { f.close(); }
});

test('incorrect current password remains signed in with edits preserved', async () => {
  const f = accountForms({ api: async () => response(401, { error: 'current password is incorrect' }) });
  try {
    f.ctx.profileDisplayName.value = 'Keep this name'; f.ctx.profileCurrentPw.value = 'wrong'; f.ctx.profileNewPw.value = 'valid password example'; await f.submit();
    assert.equal(f.ctx.expired, 0); assert.equal(f.ctx.profileDisplayName.value, 'Keep this name');
    assert.match(f.$('profile-current-error').textContent, /incorrect/);
    assert.equal(f.doc.activeElement, f.ctx.profileCurrentPw); assert.equal(f.ctx.profileSubmit.disabled, false);
  } finally { f.close(); }
});

test('expired session uses session recovery rather than a password error', async () => {
  const f = accountForms({ api: async () => response(401, { error: 'unauthorized' }) });
  try { f.ctx.profileDisplayName.value = 'Changed'; await f.submit(); assert.equal(f.ctx.expired, 1); }
  finally { f.close(); }
});

for (const [label,api] of [['network failure', async () => { throw new Error('offline'); }], ['incomplete success', async () => response(200, {})]]) {
  test(`${label} preserves profile draft without claiming success`, async () => {
    const f = accountForms({ api });
    try {
      f.ctx.profileDisplayName.value = 'Unsaved'; await f.submit();
      assert.equal(f.ctx.profileDisplayName.value, 'Unsaved'); assert.equal(f.ctx.profileSuccess.hidden, true);
      assert.match(f.ctx.profileError.textContent, /could not be confirmed/); assert.equal(f.ctx.profileSubmit.disabled, false);
    } finally { f.close(); }
  });
}

test('successful profile save refreshes identity and clears sensitive inputs', async () => {
  const user = { id: 1, username: 'alice', display_name: 'Alice', can_set_password: true };
  const f = accountForms({ api: async () => response(200, { user }) });
  try {
    f.ctx.profileDisplayName.value = 'Alice'; await f.submit();
    assert.equal(f.ctx.state.user.display_name, 'Alice'); assert.equal(f.ctx.profileSubmit.disabled, true);
    assert.equal(f.ctx.profileCancel.textContent, 'Close'); assert.equal(f.ctx.profileSuccess.hidden, false);
  } finally { f.close(); }
});

for (const [status,body,expected] of [[401,{error:'unauthorized'},/Wrong username/], [429,{},/Too many attempts/], [503,{},/Sign in failed/], [200,{},/incomplete response/]]) {
  test(`sign-in ${status} recovers with an actionable error`, async () => {
    const f = accountForms({ api: async () => response(status,body) });
    try {
      f.ctx.passwordInput.value = 'preserved credential'; await f.login();
      assert.equal(f.ctx.signedIn, 0); assert.match(f.ctx.loginError.textContent, expected);
      assert.equal(f.ctx.passwordInput.value, 'preserved credential'); assert.equal(f.ctx.loginPending, false);
      assert.equal(f.ctx.loginForm.querySelector('[type=submit]').disabled, false);
    } finally { f.close(); }
  });
}

test('double sign-in submits one request and clears password on success', async () => {
  let release, calls = 0;
  const f = accountForms({ api: () => { calls++; return new Promise(resolve => { release = resolve; }); } });
  try {
    f.ctx.passwordInput.value = 'credential'; const first = f.login(); await f.login(); assert.equal(calls,1);
    release(response(200,{user:{id:1}})); await first;
    assert.equal(f.ctx.signedIn,1); assert.equal(f.ctx.passwordInput.value,'');
  } finally { f.close(); }
});

 test('profile rate limit preserves draft and explains when to retry', async () => {
  const f = accountForms({ api: async () => response(429, { error: 'rate limit exceeded' }) });
  try { f.ctx.profileDisplayName.value = 'Draft'; await f.submit(); assert.match(f.ctx.profileError.textContent, /Wait a minute/); assert.equal(f.ctx.profileDisplayName.value, 'Draft'); assert.equal(f.ctx.profileSubmit.disabled, false); }
  finally { f.close(); }
});

for (const [label,user] of [['SSO', { id: 1, username: 'sso', can_manage_self: true, can_set_password: false }], ['managed', { id: 2, username: 'shared', can_manage_self: false, can_set_password: false }]]) {
  test(`${label} profile respects server account capabilities`, () => {
    const f = accountForms({ user });
    try {
      assert.equal(f.ctx.profileNameField.hidden, true); assert.equal(f.ctx.profilePwSection.hidden, true);
      assert.equal(f.ctx.profilePwManaged.hidden, false);
      assert.equal(f.ctx.profilePhotoControls.hidden, label === 'managed');
      assert.equal(f.ctx.profileTokensLink.hidden, label === 'managed');
      assert.equal(f.ctx.profileSubmit.hidden, label === 'managed');
    } finally { f.close(); }
  });
}

test('a late profile response cannot restore a signed-out account', async () => {
  let release; const f = accountForms({ api: () => new Promise(resolve => { release = resolve; }) });
  try {
    f.ctx.profileDisplayName.value = 'Changed'; const pending = f.submit();
    f.ctx.state.user = null; f.ctx.closeProfileModal(true);
    release(response(200, { user: { id: 1, username: 'alice', display_name: 'Changed' } })); await pending;
    assert.equal(f.ctx.state.user, null); assert.equal(f.ctx.profileSuccess.hidden, true);
  } finally { f.close(); }
});

test('password length feedback appears while typing and clears after correction', () => {
  let calls = 0; const f = accountForms({ api: async () => { calls++; } });
  try {
    const input = f.ctx.profileNewPw, error = f.$('profile-new-error');
    input.value = '😀'.repeat(19); input.dispatchEvent(new f.dom.window.Event('input'));
    assert.equal(input.getAttribute('aria-invalid'), 'true');
    assert.equal(error.hidden, false); assert.match(error.textContent, /shorter passphrase/);
    assert.ok(!error.textContent.includes('bytes')); assert.equal(f.ctx.profilePwSection.open, true);
    input.value = '😀'.repeat(18); input.dispatchEvent(new f.dom.window.Event('input'));
    assert.equal(input.getAttribute('aria-invalid'), 'false'); assert.equal(error.hidden, true);
    input.value = 'short'; input.dispatchEvent(new f.dom.window.Event('input'));
    assert.equal(error.hidden, true, 'minimum length is not flagged before submission');
    assert.equal(calls, 0);
  } finally { f.close(); }
});
