// Small form behaviors shared by sign-in and profile. Password values stay in
// their inputs; visibility controls never copy them into labels or attributes.
export function installPasswordVisibility(doc) {
  const buttons = [...doc.querySelectorAll('[data-password-toggle]')];
  const hide = button => {
    const input = doc.getElementById(button.dataset.passwordToggle);
    if (!input) return;
    input.type = 'password';
    button.setAttribute('aria-pressed', 'false');
    button.setAttribute('aria-label', button.getAttribute('aria-label').replace(/^Hide/, 'Show'));
    button.textContent = 'Show';
  };
  for (const button of buttons) button.addEventListener('click', () => {
    const input = doc.getElementById(button.dataset.passwordToggle);
    if (!input) return;
    const show = input.type === 'password';
    input.type = show ? 'text' : 'password';
    button.setAttribute('aria-pressed', String(show));
    button.setAttribute('aria-label', button.getAttribute('aria-label').replace(/^(Show|Hide)/, show ? 'Hide' : 'Show'));
    button.textContent = show ? 'Hide' : 'Show';
  });
  return { reset(root = doc) { for (const button of buttons) if (root.contains(button)) hide(button); } };
}

export function profileFieldErrors(name, current, next) {
  const errors = {};
  if (Array.from(name.trim()).length > 80) errors.name = 'Use 80 characters or fewer.';
  if (current || next) {
    if (!current) errors.current = 'Enter your current password.';
    if (Array.from(next).length < 15) errors.new = 'Use at least 15 characters.';
    else if (new TextEncoder().encode(next).length > 72) errors.new = 'This password is too long. Try a shorter passphrase.';
  }
  return errors;
}

export function setProfileFieldError(doc, field, message = '') {
  const ids = { name: 'display-name', current: 'current-password', new: 'new-password' };
  const input = doc.getElementById('profile-' + ids[field]);
  const error = doc.getElementById('profile-' + field + '-error');
  if (!input || !error) return;
  input.setAttribute('aria-invalid', String(!!message));
  error.textContent = message;
  error.hidden = !message;
  if (message && field !== 'name') doc.getElementById('profile-password-section').open = true;
}
