// app-isolation-banner.js decides whether the admin same-origin trust warning
// banner should be visible, and manages its localStorage-backed dismissal. The
// banner element itself (#app-isolation-banner) is static markup in
// index.html; this module only decides its hidden attribute and wires the
// dismiss button. The warning it presents: with no server.app_origin
// configured, every deployed app shares the dashboard's origin and is trusted
// with dashboard users' sessions (see docs/security.md#recommended-deployment-posture,
// and appOriginTrustWarning in cmd/shinyhub for the matching startup log line).

const DISMISS_KEY = 'shinyhub.dismissedAppIsolationWarning';

function storageFor(explicit) {
  if (explicit !== undefined) return explicit;
  try { return globalThis.localStorage || null; } catch { return null; }
}

function isDismissed(storage) {
  const s = storageFor(storage);
  if (!s) return false;
  try { return s.getItem(DISMISS_KEY) === '1'; } catch { return false; }
}

function persistDismissed(storage) {
  const s = storageFor(storage);
  if (!s) return;
  try {
    s.setItem(DISMISS_KEY, '1');
  } catch {
    // Storage can be unavailable in private browsing. The banner simply
    // reappears next load, which is the safe direction to fail in for a
    // security-posture warning.
  }
}

// shouldShowAppIsolationBanner decides visibility from the session payload
// alone: true only when the server says this admin should see the warning
// (server.app_origin unset) and they have not already dismissed it here.
export function shouldShowAppIsolationBanner(warningFlag, storage) {
  return !!warningFlag && !isDismissed(storage);
}

// wireAppIsolationBanner attaches the dismiss button's click handler once.
// Guards a missing root/button so a caller can invoke it unconditionally even
// against an SPA shell that predates this banner (rolling deploy, or a test
// fixture without the markup).
export function wireAppIsolationBanner({ root, dismissButton, storage } = {}) {
  if (!root || !dismissButton) return;
  dismissButton.addEventListener('click', () => {
    persistDismissed(storage);
    root.hidden = true;
  });
}
