// Login provider buttons: show an OAuth/SSO affordance ONLY when the server
// reports that provider configured (via /api/auth/providers).
//
// The GitHub and Google buttons are static markup in index.html, hidden by
// default; this module reveals them per the providers response and appends the
// OIDC button when enabled. Fail-closed by design: a missing or partial response
// (or JS never running) leaves the buttons hidden, so a native-only install
// shows a clean sign-in form and never a dead button that 501s on click.
//
// Pure decision (providerVisibility) + a thin DOM applier (applyLoginProviders)
// taking an explicit document, so both are unit-testable with jsdom.

// providerVisibility maps a /api/auth/providers response to which affordances to
// show. Only a strict boolean true counts as configured, so a malformed field
// never surfaces a dead button.
export function providerVisibility(providers) {
  const p = providers || {};
  const oidc = p.oidc || {};
  const github = p.github === true;
  const google = p.google === true;
  const oidcEnabled = oidc.enabled === true;
  return {
    github,
    google,
    oidc: oidcEnabled,
    oidcLabel: oidcEnabled ? (oidc.display_name || 'Sign in with SSO') : '',
    // The "or" separator only makes sense when at least one SSO option shows.
    anySSO: github || google || oidcEnabled,
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
  setShown('.login-separator', v.anySSO);

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
