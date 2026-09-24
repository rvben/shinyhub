// Approving a CLI connection requires typing the short code the CLI printed
// in its own terminal. The browser learns nothing about which credential to
// approve until a person enters this code themselves: an authorization link
// carries no pairing state, so a phishing page has nothing to hand a victim
// that would get an attacker's credential approved.

// normalizeCLIUserCode mirrors the server's normalization
// (internal/api/auth.go normalizeCLIUserCode): uppercase, drop everything but
// letters and digits, then reinsert the separating dash once eight characters
// have been typed. This tolerates a pasted code typed with different case,
// spacing, or dash placement without weakening validCLIUserCode below, which
// still checks the exact shape.
export function normalizeCLIUserCode(raw) {
  const cleaned = String(raw || '').toUpperCase().replace(/[^A-Z0-9]/g, '');
  return cleaned.length === 8 ? `${cleaned.slice(0, 4)}-${cleaned.slice(4)}` : cleaned;
}

// validCLIUserCode checks the exact "XXXX-XXXX" shape the server requires.
// This is a client-side convenience for instant feedback only: the server is
// the actual authority on whether a code is real, unexpired, and unconsumed.
export function validCLIUserCode(code) {
  return /^[A-Z0-9]{4}-[A-Z0-9]{4}$/.test(code || '');
}

// cliConnectDeviceLabel strips the "cli-" prefix and trailing random suffix
// the CLI generates for its credential name, leaving a label a person
// recognizes (e.g. "rubens-macbook") to confirm once approval succeeds.
export function cliConnectDeviceLabel(name) {
  const label = String(name || '').replace(/^cli-/, '').replace(/-[a-f0-9]{6}$/, '');
  return label || 'terminal';
}

// legacyCLIConnectLinkDetected reports whether the current URL carries any of
// the query parameters an old, pre-device-authorization CLI put into the
// authorization link it opened (connect_hash, connect_name, connect_code).
// This only decides whether to show a static "upgrade your CLI" notice: the
// parameter VALUES are never read, stored, or sent anywhere, so there is
// nothing here for a crafted query string to make the page do. The typed-code
// form above is the only way anything ever gets approved.
export function legacyCLIConnectLinkDetected(search) {
  const params = new URLSearchParams(search || '');
  return params.has('connect_hash') || params.has('connect_name') || params.has('connect_code');
}
