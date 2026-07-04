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
  group_role_mappings:                            # IdP group -> ShinyHub PLATFORM role
    platform-admins: admin
    data-team:       operator
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
   display name and email, issues the session cookie, and redirects to `/`.

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
    platform-admins: admin
```

Safety and edge behavior:

- **An absent groups claim never demotes.** If the IdP omits the groups claim
  entirely, ShinyHub leaves the user's existing role untouched (a missing claim is
  not read as "no groups"). Verified by
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


## Sessions, cookies, and logout

A successful login sets the `shiny_session` cookie holding a signed HS256 JWT.
Cookie attributes, secure by default:

- **`HttpOnly`** - not readable from JavaScript.
- **`SameSite=Lax`** - sent on top-level navigations (so the IdP redirect back
  carries it) but not on cross-site subrequests.
- **`Secure`** - set whenever the request is HTTPS. Behind a TLS-terminating
  proxy this is decided from `X-Forwarded-Proto`, honored only from a configured
  `trusted_proxies` peer (see below).
- **Lifetime** - the JWT expires after 1 hour and slides on activity, capped by a
  12-hour absolute session age, after which the user must re-authenticate.

**Logout** (`POST /api/auth/logout`) ends the ShinyHub session: it revokes the
session token by its JTI (so the cookie cannot be replayed) and clears the cookie.

**IdP-logout boundary (by design):** ShinyHub does **not** perform RP-initiated
(single) logout against the IdP. After a ShinyHub logout the browser may still
hold an IdP session, so clicking "Sign in with SSO" again can re-authenticate
without a password prompt. If you need the IdP session to end too, configure
single-logout / a short session lifetime at the IdP; ShinyHub logging out never
extends an IdP session, it only ends its own.


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
