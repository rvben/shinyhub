// Login affordances: show each sign-in option ONLY when the server reports it
// available (via /api/auth/providers { local, forward_auth, github, google, oidc:{enabled} }).
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
  const forwardAuth = p.forward_auth === true;
  return {
    local,
    github,
    google,
    oidc: oidcEnabled,
    oidcLabel: oidcEnabled ? (oidc.display_name || 'Sign in with SSO') : '',
    anySSO,
    forwardAuth,
    // Older servers do not report forward-auth. Never leave a deployment with
    // every native sign-in option disabled at a brand-only dead end.
    recovery: forwardAuth || (!local && !anySSO),
    // The "or" separator divides the password form from the SSO buttons, so it
    // shows only when BOTH are present.
    separator: local && (anySSO || forwardAuth),
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
  setShown('.login-recovery', v.recovery);
  setShown('#login-recovery-help', v.recovery);
  const help = doc.querySelector('#login-recovery-help');
  if (help) help.textContent = v.forwardAuth
    ? 'Reconnect to continue through your organisation’s sign-in service.'
    : 'No sign-in options are available here. Reconnect to try again, or contact your administrator.';

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
  // Carry the app return destination through the provider's server-side state.
  const next = new URLSearchParams(doc.defaultView?.location.search || '').get('next');
  for (const provider of ['github', 'google', 'oidc']) {
    const link = doc.querySelector(`.${provider}-login`);
    if (link) link.href = `/api/auth/${provider}/login` + (next ? '?next=' + encodeURIComponent(next) : '');
  }
  return v;
}

// groupAccessWarningText flags Group access rules that can never take effect.
// user_groups is populated only by an OIDC login's group claims or a
// forward-auth proxy's trusted group header (see ReconcileUserFromGroups in
// internal/db/reconcile.go) - a GitHub or Google login carries no group
// claim, so github/google here do not help even though providerVisibility()
// counts them toward anySSO. Forward-auth's enabled state alone does not show
// whether a trusted group header is configured; the
// wording below names that possibility rather than claiming no provider can
// ever supply groups.
export function groupAccessWarningText(providers) {
  if (providerVisibility(providers).oidc) return '';
  return 'No OIDC provider is configured. Group access rules take effect only for OIDC sign-ins or a forward-auth proxy that supplies a group header, so unless one of those is set up, the rules below will not grant anyone access.';
}
