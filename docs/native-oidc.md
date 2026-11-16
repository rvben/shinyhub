---
description: "Terminate OpenID Connect single sign-on inside ShinyHub, adding a Sign in with SSO button without any external authentication proxy."
---

# Native OIDC login (SSO without an external auth proxy)

ShinyHub can terminate OpenID Connect single sign-on itself. Enable it and the
login page shows a **"Sign in with SSO"** button that runs the standard
authorization-code flow against your identity provider, provisions the user, and
issues a ShinyHub session. No external identity proxy is required.

This is the first-party alternative to the two proxy-based paths:

| You want | Use |
|---|---|
| ShinyHub to be the login surface, talking OIDC directly to your IdP | **Native OIDC (this page)** |
| An existing Caddy/nginx/Cloudflare/Authelia edge to authenticate and forward a user header | [Forward-auth](reverse-proxy/caddy.md) |
| LDAP or SAML fronted by an OIDC bridge, then native OIDC to ShinyHub | [OIDC bridge](reverse-proxy/oidc-bridge.md) |

Any OIDC-compliant provider works: Okta, Microsoft Entra ID (Azure AD), Google
Workspace, Keycloak, Auth0, Authelia, Authentik, and others exposing a discovery
document at `{issuer_url}/.well-known/openid-configuration`.

Whichever mode you run, apps receive the authenticated user's identity the same
way - see [Identity forwarding](identity.md). Native OIDC and forward-auth feed
the same downstream contract.


## Test without a Microsoft tenant

Run the native Entra-shaped compatibility suite from a checkout:

```sh
make test-entra-oidc
# Optional: retain private results in a chosen new/empty directory.
make test-entra-oidc ENTRA_OIDC_OUTPUT=/tmp/shinyhub-entra-results
# For another compatible Python executable:
make test-entra-oidc ENTRA_OIDC_PYTHON=python3.15
```

It requires Python 3.14+, exactly Go 1.27.4 (pinned by the fixture), Git, and network access to download pinned
provider source and Go modules. It does not require Docker, an Azure account, a
Microsoft tenant, or cloud credentials. Both services bind to random loopback
ports, use fresh databases, and stop on completion or failure. It never opens an
existing ShinyHub database. Generated recovery credentials stay in the private
test directory and the password file is removed during cleanup.
The default run also retains its private output directory and prints its path;
remove that directory when you no longer need the report and synthetic state.

The suite builds the current ShinyHub checkout and a pinned
[Entra emulator fixture](https://github.com/calvinchengx/entra-emulator).
The fixture includes reviewed pending app-role and logout changes; it is not an
upstream release. Its source commit, compatibility-patch digest, and resulting
Git tree are checked before building. See
[`scripts/acceptance/fixtures/entra-emulator/README.md`](https://github.com/rvben/shinyhub/blob/main/scripts/acceptance/fixtures/entra-emulator/README.md)
for pins, attribution and the patch-removal policy.

Checks cover authorization-code login, strict app-role grants/refusal and
demotion, explicit account migration with ownership/API-key preservation,
revocation after a verified authorization refusal, nonce/state rejection,
callback replay, local/RP/front-channel logout, session isolation, and real timed
session expiry. `results.json` records the checks, source/build provenance and
binary hashes. A failure exits nonzero; CI runs the same command and retains
only that summary, never process logs or credentials. Uncommitted local changes
are explicitly recorded as a dirty ShinyHub build.
CI requires a clean ShinyHub build. Binary hashes are specific to the recorded
Go toolchain and target platform. The public source repository and module
registries must remain reachable; unavailable dependencies fail the job rather
than substituting a different source.

### HTTPS browser acceptance without a tenant

For a real-browser check on two isolated HTTPS loopback origins, run:

```sh
# Install Chromium using the repository's locked browser tooling (once).
npm --prefix loadtest/render/driver ci --ignore-scripts --no-audit --no-fund
(cd loadtest/render/driver && npx --no-install playwright install chromium)
make test-entra-oidc-browser
```

This additionally requires `uv`, OpenSSL with `req -addext`, and Chromium.
The target installs Python Playwright from its frozen `uv.lock` automatically.
Keep that project's Playwright version and the browser driver's npm lock in sync;
the runner rejects mismatched versions.
It runs the real sign-in/account-menu logout screens, checks Secure/HttpOnly/
SameSite cookie attributes, exercises RP and cross-site iframe logout, verifies
role demotion/refusal and session revocation, and checks local recovery during a
provider outage. Chromium bypasses certificate errors only for the disposable
certificate's public key. Python and Go verify its chain and hostname using
that certificate as a private CA. No machine trust
store or production configuration changes. As with the HTTP test, output is
private, retained and printed; only the sanitized results summary goes to CI
artifacts. Screenshots, browser traces, cookies and process logs are not uploaded.
This checks Chromium's behavior, not all browsers or tenant policies.

### Real-tenant acceptance before production migration

A passing emulator run establishes local compatibility. Real Microsoft Entra
interoperability remains **unverified** until someone with access to a dedicated
test tenant completes the following on isolated HTTPS staging:

- Confirm the exact tenant authority, audience, callback, post-logout return and
  front-channel logout URLs with a dedicated Web app registration.
- Assign pilot users to explicit viewer/developer/admin app roles; verify the
  configured corporate username and signature-verified subject mapping.
- Exercise assignment-required rejection, role removal and demotion, including
  Entra propagation delay and session freshness limits.
- Dry-run and apply explicit links to disposable legacy accounts; verify stable
  user IDs, ownership, memberships and API keys, with local recovery available.
- Exercise normal login, renewal/expiry and each logout path in real browsers.
  Verify that the provider's front-channel iframe commits without framing errors
  in the browser console. Check tenant consent, MFA and Conditional Access
  policies that apply.
- Record the tested ShinyHub commit, tenant/app identifiers, settings, results
  and reviewer privately. Keep tokens, secrets and personal data out of reports.

The emulator issues a role-less token for an unassigned user; it does not prove
Microsoft's assignment-required enforcement, consent, Conditional Access, MFA,
propagation timing, or browser cookie policy. The absence of a real test tenant
does not block local development and CI. Keep the existing production
authentication until this acceptance gate is completed.

## Configuration

```yaml
# shinyhub.yaml
oauth:
  oidc:
    issuer_url:    https://login.example.com     # OIDC discovery root
    client_id:     shinyhub
    client_secret: "..."                          # prefer SHINYHUB_OIDC_CLIENT_SECRET
    callback_url:  https://shiny.example.com/api/auth/oidc/callback
    display_name:  "Company SSO"                  # label on the login button
    groups_claim:  groups                         # ID-token claim holding group names
    groups_scope:  groups                         # optional extra scope to request
    require_valid_groups: false                   # true => fail login on a malformed groups claim

auth:
  oauth_default_role: viewer                      # role for a first-time SSO user
  local_login: true                               # false => SSO-only (hide + reject password login)
  group_role_mappings:                            # IdP group -> ShinyHub PLATFORM role
    - group: platform-admins
      role: admin
    - group: data-team
      role: operator
```

Enabling OIDC requires only `issuer_url`, `client_id`, `client_secret`, and
`callback_url`; the rest have defaults. `display_name` defaults to
`"Sign in with SSO"`, `groups_claim` to `groups`.

Every option has an environment-variable equivalent (env wins over YAML):

| Variable | Description |
|---|---|
| `SHINYHUB_OIDC_ISSUER_URL` | Discovery root; enabling OIDC is gated on this being set |
| `SHINYHUB_OIDC_CLIENT_ID` | Client ID registered with the IdP |
| `SHINYHUB_OIDC_CLIENT_SECRET` | Client secret |
| `SHINYHUB_OIDC_CALLBACK_URL` | Redirect URI; must match one registered at the IdP. Path is always `/api/auth/oidc/callback` |
| `SHINYHUB_OIDC_DISPLAY_NAME` | Login-button text (default `"Sign in with SSO"`) |
| `SHINYHUB_OIDC_GROUPS_CLAIM` | ID-token claim carrying group names (default `groups`) |
| `SHINYHUB_OIDC_GROUPS_SCOPE` | Extra OAuth2 scope to request (e.g. `groups`) |
| `SHINYHUB_OIDC_REQUIRE_VALID_GROUPS` | `true` to fail login on a malformed groups claim (default `false`) |
| `SHINYHUB_AUTH_OAUTH_DEFAULT_ROLE` | Role for a first-time SSO user (default `viewer`) |
| `SHINYHUB_AUTH_GROUP_ROLE_MAPPINGS` | `group:role,group2:role2` |
| `SHINYHUB_AUTH_LOCAL_LOGIN` | `false` for SSO-only: hide and reject username/password login (default `true`) |

ShinyHub always requests the `openid`, `email`, and `profile` scopes (plus
`groups_scope` when set), so the `email` and `name` claims are available without
extra configuration on most IdPs.

**Register the redirect URI at your IdP** as `<your-public-url>/api/auth/oidc/callback`.
The path is fixed; only the scheme/host vary.


## The login flow

1. The login page calls `GET /api/auth/providers`; when OIDC is enabled it renders
   a **"Sign in with SSO"** link to `/api/auth/oidc/login`.
2. `GET /api/auth/oidc/login` generates a random `state`, stores it, sets a
   short-lived state-binding cookie, and redirects to the IdP authorization
   endpoint.
3. The IdP authenticates the user and redirects back to
   `GET /api/auth/oidc/callback?state=...&code=...`.
4. The callback verifies the `state` (server-side nonce **and** the binding
   cookie, constant-time), exchanges the code for tokens, verifies the ID token,
   provisions or finds the user, reconciles the role from groups, persists the
   display name and email, issues the session cookie, and redirects to the saved
   app deep link (or `/` when no destination was requested).

The full flow is exercised end to end against a mock IdP in
`internal/api/oidc_e2e_test.go` (`TestOIDC_EndToEnd_*`).


## Claim-to-identity mapping

| Claim | Becomes | Notes |
|---|---|---|
| `sub` | Stable provider identity | The account key (`oauth_accounts.provider_id`); a `sub` is required |
| `email` | User email + username seed | Persisted; forwarded to apps as `X-Shinyhub-Email`. The local-part seeds the ShinyHub username |
| `name` | Display name | Persisted; forwarded to apps as `X-Shinyhub-Name` |
| `groups_claim` (default `groups`) | Group memberships | Drive the platform role (below) and reach apps verbatim (below) |

The ShinyHub username is derived once, at first login, from the available claims:
email local-part, else a slugified `name`, else `oidc-<sub-prefix>`. It is only a
handle; the durable identity is `sub`.


## Groups and platform roles

IdP groups drive the user's **global ShinyHub role** through
`auth.group_role_mappings`. On each login ShinyHub reconciles the role to the
highest-ranked mapped group the user is in; a user in no mapped group gets
`oauth_default_role` (default `viewer`). Map a group to `admin` to grant platform
admin from group membership:

```yaml
auth:
  group_role_mappings:
    - group: platform-admins
      role: admin
```

Safety and edge behavior:

- **An absent groups claim preserves authorization by default.** If the IdP omits
  the groups claim entirely, ShinyHub leaves the stored role and group snapshot
  untouched. Use `missing_groups: reject` or `empty` for stricter policy (below).
  The compatibility default is verified by
  `TestOIDC_EndToEnd_AbsentGroupsClaimDoesNotDemote`.
- **A malformed groups claim** (present but not a string or array of strings) is
  skipped with a warning by default, leaving the role unchanged. Set
  `require_valid_groups: true` to instead fail the login (HTTP 502) so a
  misconfigured IdP blocks sign-in rather than silently keeping a stale role.
- `forward_auth.admin_groups` is a **deprecated** alias that merges into
  `group_role_mappings` as `role: admin`. Prefer `group_role_mappings` directly;
  it applies to both native OIDC and forward-auth.

Platform roles gate what a user can do **in ShinyHub** (deploy, manage users,
etc.). They are deliberately separate from what an **app** does with the raw
groups: apps receive the unmodified IdP group names via
[identity forwarding](identity.md) and run their own in-app RBAC.


## SSO-only (disable password login)

To require SSO and remove the built-in username/password path entirely, set:

```yaml
auth:
  local_login: false        # or SHINYHUB_AUTH_LOCAL_LOGIN=false
```

With this set:

- The login screen **hides the password form** and leads with the SSO button(s).
- The password endpoints (`/api/auth/login`, `/api/auth/session`) **reject with
  403**. This is enforced server-side, not just in the UI, so a client cannot
  bypass the IdP by POSTing credentials directly. (The UI hiding is driven off
  the `local` flag in `/api/auth/providers` and fails open - the server is the
  authoritative gate.)

**Lockout guard.** Startup **fails fast** if `local_login` is false and no SSO
login path (GitHub, Google, OIDC, or forward-auth) is configured, so you cannot
accidentally lock every user out. GitHub/Google count only when both a
`client_id` and a `client_secret` are set; OIDC's discovery is verified at
startup. The boot log then prints the SSO paths that were counted.

The guard checks that a provider is **configured**, not that it is **working** -
test SSO end to end before turning local login off. This matters most for
forward-auth: it authenticates a request only when the direct peer is in
`server.trusted_proxies` **and** the proxy sends the user header, neither of which
ShinyHub can verify at startup. An SSO-only server whose `trusted_proxies` does
not include the real edge proxy will 403 every login. (OAuth callbacks must also
be reachable at the registered redirect URI.)

**Break-glass caveat.** Disabling local login also disables the built-in admin's
password login (including any `SHINYHUB_LOCAL_ADMIN_*` break-glass account) and
the `SHINYHUB_ADMIN_USER`/`_PASSWORD` bootstrap login. Make sure at least one
admin can reach the platform through SSO (e.g. an IdP group mapped to `admin` via
`group_role_mappings`) before enabling SSO-only. To recover, set
`SHINYHUB_AUTH_LOCAL_LOGIN=true` and restart.

## Sessions, cookies, and logout

A successful login sets the `shiny_session` cookie holding a signed HS256 JWT.
Cookie attributes, secure by default:

- **`HttpOnly`** - not readable from JavaScript.
- **`SameSite=Lax`** - sent on top-level navigations (so the IdP redirect back
  carries it) but not on cross-site subrequests.
- **`Secure`** - set whenever the request is HTTPS. Behind a TLS-terminating
  proxy this is decided from `X-Forwarded-Proto`, honored only from a configured
  `trusted_proxies` peer (see below).
- **Lifetime** - by default, the JWT expires 1 hour after login or renewal.
  A visible dashboard automatically renews it through `/api/auth/me`, and
  hosted apps use `/app/<slug>/.shinyhub/session.json`, normally every 5 minutes.
  Both execute the same renewal controller. Background tabs pause renewal and
  check the session when they return; closed pages do not keep it alive. Renewal
  requires a still-valid cookie and preserves the original login time.
  Token and cookie expiry are capped at exactly 12 hours from that login,
  after which the user must sign in again. Both limits are
  [configurable](configuration.md#browser-session-lifetimes).

Transient network or server errors retry without treating the user as signed
out. An authentication rejection returns the dashboard to sign-in, with a
message explaining that the session ended. Browsers supporting BroadcastChannel
also notify dashboard and app tabs on the same origin when a session ends or
the user signs out. Hosted pages keep their contents intact and offer sign-in
in a new tab, so expiry does not automatically discard unsaved work.

Hosted-app renewal is independent of the optional app switcher. On an isolated
app origin the launch exchange preserves the original login time and session
identity; app activity renews only the app-origin cookie, and dashboard logout
revokes both origins. Existing app WebSockets retain the original maximum-age
deadline and close at that deadline without waiting for a request or reconnect.
Streaming HTTP responses are bounded by the same deadline. Short-lived cookie
renewal does not interrupt a working WebSocket. If an app's HTML or CSP prevents
script injection, automatic renewal is unavailable but server deadlines remain
enforced. API keys, CLI credentials, bearer JWTs, and administrator support
sessions do not become renewable browser sessions. Forward-auth sessions are
governed by the upstream proxy and identity provider.

**Logout** (`POST /api/auth/logout`) ends the ShinyHub session: it revokes the
presented native session and bearer JWTs by JTI (so they cannot be replayed) and clears the cookie.
Renewals retain that session's JTI, and browser-session revocations are kept
through the absolute deadline. A concurrent renewal therefore cannot revive a
session after logout. A new login creates a new session identity. With forward-auth enabled, browser
logout also blocks automatic upstream reauthentication until an explicit
reconnect; see [proxy logout](reverse-proxy/caddy.md#browser-logout-and-reconnect).
If revocation cannot be saved, logout returns a retryable error and keeps the
cookie available for a retry instead of reporting that the session ended.

**IdP logout is optional:** by default ShinyHub ends only its own session. The
browser may still hold an IdP session, so signing in again can succeed without a
password prompt. Enable `oauth.oidc.end_session` for RP-initiated and correlated
front-channel logout; see the Entra logout section below.


## Behind a TLS-terminating reverse proxy

When ShinyHub runs behind a proxy that terminates TLS (the common deployment):

- Set `callback_url` (and the IdP's registered redirect URI) to the **external
  HTTPS URL**, e.g. `https://shiny.example.com/api/auth/oidc/callback`. ShinyHub
  redirects to exactly this value; a mismatch with the IdP registration is the
  usual "redirect_uri mismatch" error.
- Add the proxy's address to `server.trusted_proxies` so ShinyHub honors
  `X-Forwarded-Proto: https` from it and marks the session cookie `Secure`.
  Without this, a proxied HTTPS login would set a non-Secure cookie. See
  [Deploying behind a proxy](reverse-proxy/deploying-behind-a-proxy.md).

`server.base_url` is unrelated to OIDC redirect construction; the redirect URI is
taken verbatim from `callback_url`.


## Multiple replicas / high availability

Native OIDC login works across replicas with no sticky sessions:

- The session is a **stateless signed JWT**. Any control-plane replica that shares
  the same `auth.secret` can verify a session another replica issued, so a load
  balancer may route requests freely.
- The login **`state` nonce** is stored in the `oauth_states` table (works on both
  the SQLite and Postgres backends), so the replica that handles the callback
  validates the state the login replica created.

Keep `auth.secret` identical on every instance (it also signs sessions and derives
per-app identity keys). See [HA data plane](deployment/ha-data-plane.md).


## Troubleshooting

- **`oidc: claims not set` / "failed to verify OIDC ID token":** the IdP returned
  an ID token ShinyHub could not verify (wrong `client_id`/audience, clock skew,
  or an unreadable claims payload). Confirm `client_id` matches the IdP client and
  clocks are in sync.
- **"redirect_uri mismatch":** the IdP's registered redirect URI does not equal
  `callback_url`. They must match exactly, including scheme and host.
- **Role not promoted from a group:** confirm the IdP actually sends the
  `groups_claim` (some IdPs require a `groups` scope - set `groups_scope`), and
  that `group_role_mappings` names the group exactly.
- **Cookie not `Secure` behind a proxy:** add the proxy IP to `trusted_proxies`.

## Microsoft Entra ID

Use a tenant-specific v2 issuer:
`https://login.microsoftonline.com/<tenant-id>/v2.0`. The generic verifier expects
an exact issuer; `common` and `organizations` are not supported tenant selectors.
Register a single-tenant **Web** application with each server's exact HTTPS
`/api/auth/oidc/callback` redirect URI. Prefer separate registrations for production
and test environments. Store the client secret in
`SHINYHUB_OIDC_CLIENT_SECRET`, record its expiry, and test rotation before removing
the previous secret. Caddy remains the TLS proxy: keep its address in
`server.trusted_proxies`, and remove forwarded identity headers at the edge when
switching to native login.

### App roles and admission

Prefer app roles to directory-wide group claims. Define role **values** matching
your existing application authorization strings, for example `alpha_admin` and
`beta_power_user`, then assign users or security groups to these roles on the
**Enterprise application**. Select **Assignment required? Yes** explicitly; creating
roles does not enable this restriction. Set `groups_claim: roles` and leave
`groups_scope` empty. Entra does not need a `groups` scope for roles.

Group-based application assignment requires the appropriate Entra P1/P2 licensing
and does not expand nested groups. Test direct membership, guests and unassigned
ordinary users; do not use a Global Administrator to prove assignment enforcement.
A user rejected by Entra sees an Entra error before reaching ShinyHub. ShinyHub's
own claim checks produce a ShinyHub 403. See Microsoft's
[assignment guidance](https://learn.microsoft.com/en-us/entra/identity/enterprise-apps/assign-user-or-group-access-portal)
and [app-role documentation](https://learn.microsoft.com/en-us/entra/identity-platform/howto-add-app-roles-in-apps).

A corporate baseline, after validating claims in a sandbox:

```yaml
oauth:
  oidc:
    issuer_url: https://login.microsoftonline.com/<tenant-id>/v2.0
    client_id: <application-client-id>
    callback_url: https://hub.example.com/api/auth/oidc/callback
    groups_claim: roles
    missing_groups: reject
    require_groups: true
    require_valid_groups: true
    username_claim: corporate_id
    username_required: true
    link_existing: off
    auto_redirect: true
    end_session: false

auth:
  local_login: false
  oauth_default_role: viewer
  session_ttl: 5m
  session_max_age: 5m
  group_role_mappings:
    - group: alpha_admin
      role: admin
    - group: beta_power_user
      role: developer
```

Configure `corporate_id` with the identity team; it is an illustrative claim name,
not a built-in Entra claim. Verify the actual ID token issued **for this client**.
The UPN (`preferred_username`) is mutable and may differ from the old AD
`sAMAccountName`. Email and display name may be absent or mutable too; neither is
an account-linking proof. `onprem_sid` is a SID, not the corporate username.
An optional `username_strip_domain: true` extracts the part before `@`; use it only
when the identity team guarantees uniqueness across every admitted domain.

Apps receive role values verbatim through the existing groups contract. Choosing
new role names requires updating app RBAC **and stored app group grants**. Existing
user IDs alone do not preserve grants that refer to old group strings. Keep the
old strings when equivalent permissions are intended. Platform role mappings do
not automatically grant access inside an app.

### Username and claim policy

Additional `oauth.oidc` options (all default to disabled/legacy behavior):

| Option | Behavior |
|---|---|
| `username_claim` | Read a top-level string claim as the first-login username. Converted to lowercase without lossy character substitution; allowed characters are ASCII letters, digits, `.`, `_`, `-`, maximum 64 bytes; `.` and `..` are refused. Existing linked users retain their stored username. |
| `username_required` | Refuse login if the configured username claim is absent. A present malformed/empty claim is always refused. |
| `username_strip_domain` | Explicitly strip the suffix beginning with `@` before username validation. |
| `link_existing` | `off` (default), or `username` for a controlled migration using a trusted configured username claim. Email linking is deliberately unsupported. |
| `missing_groups` | `preserve` (default): retain stored groups and role; `reject`: refuse login; `empty`: replace the snapshot with no groups and reconcile the default role. Applies to the configured claim, including `roles`. |
| `require_groups` | Refuse sign-in with zero roles/groups, including an explicitly empty array. |
| `auto_redirect` | Start OIDC automatically only when it is the sole browser login and the session check returns 401. Signed-out/error landings and `?no_auto_login=1` retain the sign-in button. Failed cookie loops are limited to one automatic attempt per minute; unavailable browser storage falls back to the button. |
| `end_session` | Enable RP-initiated and session-correlated front-channel logout; requires a secure discovered logout endpoint. |
| `graph_groups` | Opt-in Microsoft Graph resolution for Entra group overage, with `groups_claim: groups`. Not needed for app roles. |

Every option has an equivalent `SHINYHUB_OIDC_<UPPERCASE_OPTION>` environment
variable, such as `SHINYHUB_OIDC_USERNAME_CLAIM` or
`SHINYHUB_OIDC_MISSING_GROUPS`. Boolean overrides accept `true`/`false`.
Strict missing-claim modes also refuse malformed authorization claims instead of
retaining stale authorization. The legacy `preserve` default is for compatibility;
it is unsuitable as an authoritative permission-removal policy.

Manual platform-role overrides still take precedence. Clear unintended overrides
before migration. Automatic reconciliation protects the last administrator from
demotion; strict OIDC policy refuses a new login if that guard prevents the
expected role from taking effect. Maintain a separate recovery administrator and
test demotion using an ordinary account.

### Preserve existing accounts

Back up the database and inventory user IDs, app ownership, user memberships,
group grants, manual role overrides and personal API tokens before switching.
Do not delete/recreate users or edit `oauth_accounts` by hand.

The preferred migration is an explicit administrative link:

```console
shinyhub users link-oidc abc12345 \
  --issuer https://login.microsoftonline.com/<tenant-id>/v2.0 \
  --client-id <application-client-id> --sub '<verified-sub>' --dry-run
# Review the target, then repeat without --dry-run.
```

Obtain `sub` from a verified ID token or an identity-team approved export for that
registration. It is **not** the Entra directory object ID (`oid`), and it can differ
between app registrations. The issuer and client ID must match the server.
The command preserves the original user row, ownership, memberships, grants,
API keys and usage history. It refuses service/reserved/configuration-managed
accounts, accounts with local passwords, an identity belonging to another user,
or users already linked to another OAuth identity. Identical repeated links are
idempotent; successful new links are audited. Preview does not create a link.

Alternatively, temporarily enable `link_existing: username`, configure a trusted
corporate username claim and `username_required: true`, and test with a small
cohort. Matching ignores case, preserves the existing stored username and refuses
ambiguous matches. Automatic links are restricted to legacy proxy accounts with
the disabled-password marker; administrators and accounts with manual role
overrides require explicit administrative linking. Well-known email, UPN,
display-name and subject claims cannot enable automatic linking. Conflicts fail rather than
silently selecting a different account. Missing claims must not manufacture a new
migration identity. Turn automatic linking off once migration is complete.
Before enabling automatic linking, inventory legacy usernames (for example,
`shinyhub users list --output json`) and compare them with the normalized claim.
`abc12345`, `abc12345@corp.example` and `DOMAIN\abc12345` are different handles;
a nonmatching handle creates a new account. `DOMAIN\user` claims are refused,
and `username_strip_domain` only handles `@` suffixes. Use explicit linking for
these cases. Concurrent legacy forward-auth provisioning must be disabled during
migration: the database's legacy username uniqueness is case-sensitive.
Reassigned corporate IDs require administrator review: matching a mutable handle
alone cannot prove that its new holder owns the old account.

The first upgraded OIDC configuration binds the database's existing generic OIDC
links to the configured issuer/client ID. Confirm that configuration before the
first start. Subsequent authority changes fail closed at startup; changing an
issuer or replacing an app registration requires a separately planned identity
migration. If a first-start typo created a binding before any OIDC accounts or
live OIDC sessions existed, stop **every** server using that database, correct
its config, then run from the service's working directory (or use an absolute
SQLite DSN). The result reports the resolved SQLite database path. Run:

```console
shinyhub oidc-rebind-empty --config shinyhub.yaml --acknowledge-servers-stopped
```

This explicit recovery command refuses established OIDC identities and live
sessions; it cannot transfer identities between registrations. Restart every
replica with the corrected configuration. The recovery command applies pending
database migrations before checking the binding, so back up first. A stable
username does not make subjects portable across registrations.

### Revocation and session freshness

When a verified ID token is refused because the required authorization claim is
absent (`missing_groups: reject`) or empty (`require_groups: true`), ShinyHub also
revokes existing browser sessions for that linked identity. The stored issuer,
client ID and subject must all match; this does not provision or relink accounts.
Malformed tokens and provider failures do not establish authorization loss and
do not trigger this revocation. Entra may refuse an unassigned user before issuing
an ID token, so that case still relies on session expiry or administrative
revocation. Personal API keys remain separate credentials (see below).

Native browser renewal does **not** contact Entra or refresh authorization claims.
The five-minute example limits each browser session, including renewed cookies,
WebSockets and streaming responses, to five minutes from its original login.
At expiry the browser must perform OIDC again; an existing Entra session may make
sign-in seamless. Active Shiny WebSockets still close at expiry and in-app state
can be lost; choose this cap with that interruption in mind. With
`auto_redirect: true` and `end_session: false`, later visits or other tabs can
silently sign back in through the existing Entra session after local logout.
Test account disablement and role removal end to end, including
Entra's own propagation delay. A local five-minute cap cannot guarantee that an
upstream change is visible to Entra within five minutes.

OIDC login-start, callback and front-channel notifications each have an independent
per-IP limit of 600 requests per minute per server process. Size shared-IP cohorts
and synchronized renewal bursts within that capacity, and verify `trusted_proxies`
so the reverse proxy is not mistaken for every user's address. Larger deployments
need a capacity review before applying this short session example.

If your policy requires an actual five-minute directory recheck with no upstream
propagation allowance, session lifetimes alone do not satisfy it. Retain a
revalidation-capable authentication gateway until that policy is implemented and
verified. Native ShinyHub does not retain refresh tokens or claim continuous
access evaluation. `session_ttl` alone is not a revocation bound because cookies
can renew up to `session_max_age`.

API keys/CI tokens are separate credentials and remain valid across browser SSO
changes. Offboarding must revoke personal API keys separately; browser logout and
Entra account disablement do not automatically revoke them. Prefer scoped service
accounts for CI. See `shinyhub users revoke-sessions` and `shinyhub tokens list --all`.

### Group overage (when app roles cannot be used)

Entra JWTs normally contain at most 200 directory groups, including nested
memberships. Over that limit the `groups` claim is replaced by an overage
indicator. ShinyHub logs `oidc_groups_overage` and rejects unresolved overage when
using groups, even under the legacy preserve policy. It never treats overage as
an authoritative empty list. When using `roles`, unrelated directory-group
overage does not replace or invalidate the role claim.

With `graph_groups: true`, ShinyHub requests delegated Graph `User.Read` and
resolves group IDs through `/me/transitiveMemberOf`. This is sufficient for the
signed-in user's membership IDs; broader permissions are not the default.
Configure Entra's group claim format as **Group ID**: non-GUID token groups are
refused when Graph resolution is enabled. Name/SID formats cannot be mixed with
Graph IDs. Graph returns transitive directory memberships, which can be a superset
of a token filtered to groups assigned to the application; apps receive that full
ID set for overage users. Hidden memberships may need additional permission. Grant consent in Entra and
test pagination, Graph denial and outage handling. Failures refuse sign-in.
Graph results remain GUIDs; no display-name mapping is inferred. Requests use
fixed Microsoft Graph endpoints and validated pagination URLs, never arbitrary
URLs from `_claim_sources`. See [Graph membership permissions](https://learn.microsoft.com/en-us/graph/api/user-list-transitivememberof?view=graph-rest-1.0).

### Logout and shared machines

With `end_session: true`, successful browser logout first revokes the local
session, then supplies the discovered IdP logout URL to the dashboard with an
`id_token_hint` and configured post-logout return URI. ID-token hints are encrypted
in the shared database; expired hints are ignored and pruned on subsequent
OIDC sign-ins and on server startup, including when OIDC has been disabled.
Refresh tokens are not stored.
Register `https://<host>/?signed_out=1` as the post-logout redirect URI with the IdP.
For Entra, add it as an additional **Web redirect URI** on the app registration;
see [Microsoft sign-out registration requirements](https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc#send-a-sign-out-request).
The return page suppresses auto-login so logout does not immediately sign back in.
The GET logout URL includes a sensitive ID-token hint and may remain in browser
history; use managed browser profiles on shared machines and protect logout URLs
from application/proxy logs. A corrupt or undecryptable stored hint never blocks local revocation; ShinyHub attempts provider logout
without the hint. A browser session with no stored hint performs local logout only.
Provider behavior may then require an account-selection or
confirmation prompt. Successful local and front-channel logout remove matching
hints, and revoking all user sessions clears that user's hints.

Register `https://<host>/api/auth/oidc/frontchannel-logout` as the front-channel
logout URL. Notifications require matching `iss` and `sid`; ShinyHub revokes the
associated session families across replicas without depending on third-party
cookies. Where supported, register `frontchannel_logout_session_required: true`
for this client. Discovery advertises OP support as
`frontchannel_logout_session_supported`; these are distinct metadata fields.
ShinyHub warns when correlated support is not advertised or a login has no `sid`
(once per server process). For Entra, verify the `sid` ID-token optional claim and
actual `iss`/`sid` notification delivery in staging. If session correlation is
unavailable, only RP-initiated logout applies; retain bounded browser sessions.
See the [front-channel logout specification](https://openid.net/specs/openid-connect-frontchannel-1_0.html). Providers that do not send a correlated session ID cannot use this
endpoint. Front-channel delivery depends on browser behavior and is not a
replacement for bounded sessions. Ending Entra SSO is not an operating-system
sign-out: shared machines still need managed browser/profile policy. Support-session
re-authentication revokes only the local session and preserves `/identity`; a new
OIDC round trip can use existing Entra SSO and does not prove fresh MFA.

### Cutover, validation and recovery

1. Use a separate sandbox registration and backed-up database. Run
   `shinyhub validate-config --check-oidc` with the service environment. Discovery
   verifies reachability/issuer with a 15-second timeout; it does not prove client
   credentials, claim contents, assignment or successful authentication.
2. Prepare explicit account links and role values. Verify the same numeric user ID,
   ownership, direct memberships, group grants and identity forwarded to apps.
3. Native OIDC and forward-auth can be configured together, but forwarded identity
   can mask the native cookie path and overwrite authorization snapshots. Test
   native login through a route/host with no forwarded identity headers and no
   proxy login interception of `/api/auth/oidc/*`. Do not infer native success from
   merely seeing an authenticated page.
4. Test missing/empty/malformed username and role claims; role removal (including
   the final role); local-password/service-account collisions; unassigned users;
   wrong issuer/audience; groups overage when using groups; and multi-replica
   callback/logout. Verify dashboard, apps, active WebSockets and personal tokens
   separately. Confirm app deep links survive the OIDC round trip.
5. Confirm an SSO administrator works, disable local login, then remove Caddy
   forward-auth and the old service. Preserve proxy TLS/header trust configuration.
   Confirm CLI/CI token deployments still work before retiring LDAP credentials.
6. Recovery requires host access: restore the previous proxy configuration or set
   `SHINYHUB_AUTH_LOCAL_LOGIN=true` and restart with a known local administrator.
   Do not expose an unauthenticated identity-header route. Restore the backup only
   after reviewing writes made since cutover; rolling the database back discards
   those writes. Changing login configuration alone preserves account links.
