// Login affordances: show each sign-in option ONLY when the server reports it
// available (via /api/auth/providers { local, github, google, oidc:{enabled} }).
//
// - The GitHub/Google buttons are static markup in index.html, hidden by default;
//   this reveals them per the response and appends the OIDC button when enabled.
//   Fail-closed for SSO: a missing/partial response leaves the buttons hidden, so
//   a native-only install never shows a dead button that 501s on click.
// - The password form is hidden when local login is disabled (SSO-only). This
//   fails OPEN: an absent/failed response keeps the form, and the server rejects
//   password logins independently (403) when it is actually disabled, so a bad
//   fetch can never hide the only real login path.
//
// Pure decision (providerVisibility) + a thin DOM applier (applyLoginProviders)
// taking an explicit document, so both are unit-testable with jsdom.

// providerVisibility maps a /api/auth/providers response to which affordances to
// show. Only a strict boolean true counts as a configured SSO provider, so a
// malformed field never surfaces a dead button.
export function providerVisibility(providers) {
  const p = providers || {};
  const oidc = p.oidc || {};
  const github = p.github === true;
  const google = p.google === true;
  const oidcEnabled = oidc.enabled === true;
  // Local (password) login defaults to shown: only an explicit local:false hides
  // the form (fail open).
  const local = p.local !== false;
  const anySSO = github || google || oidcEnabled;
  return {
    local,
    github,
    google,
    oidc: oidcEnabled,
    oidcLabel: oidcEnabled ? (oidc.display_name || 'Sign in with SSO') : '',
    anySSO,
    // The "or" separator divides the password form from the SSO buttons, so it
    // shows only when BOTH are present.
    separator: local && anySSO,
  };
}

// applyLoginProviders reveals/hides the SSO affordances in `doc` per the
// providers response. Idempotent: the OIDC button is created at most once and
// toggled on subsequent calls, so it is safe to call more than once.
export function applyLoginProviders(doc, providers) {
  const v = providerVisibility(providers);

  const setShown = (selector, shown) => {
    const el = doc.querySelector(selector);
    if (el) el.hidden = !shown;
  };
  setShown('.github-login', v.github);
  setShown('.google-login', v.google);
  setShown('.login-separator', v.separator);
  // Hide the username/password form for an SSO-only deployment.
  setShown('#login-form', v.local);

  let oidcBtn = doc.querySelector('.oidc-login');
  if (v.oidc) {
    if (!oidcBtn) {
      oidcBtn = doc.createElement('a');
      oidcBtn.className = 'oidc-login';
      oidcBtn.href = '/api/auth/oidc/login';
      const anchor = doc.querySelector('.google-login') || doc.querySelector('.github-login');
      if (anchor) {
        anchor.insertAdjacentElement('afterend', oidcBtn);
      } else {
        const box = doc.querySelector('.login-box');
        if (box) box.appendChild(oidcBtn);
      }
    }
    oidcBtn.textContent = v.oidcLabel;
    oidcBtn.hidden = false;
  } else if (oidcBtn) {
    oidcBtn.hidden = true;
  }
  return v;
}
