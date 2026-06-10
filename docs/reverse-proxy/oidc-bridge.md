# OIDC bridge for LDAP and SAML sites

ShinyHub has no built-in LDAP client or SAML service provider. Sites that
authenticate users via LDAP or SAML have two integration paths:

1. **Forward-auth** (Caddy or nginx): deploy an auth service that speaks LDAP
   or SAML, call it from the reverse proxy, and forward the authenticated
   username to ShinyHub as an HTTP header. See [caddy.md](caddy.md) and
   [nginx.md](nginx.md).

2. **OIDC bridge** (this guide): deploy an identity provider that wraps your
   LDAP or SAML source and exposes standard OpenID Connect, then configure
   ShinyHub's existing OIDC support to log in through it.

Use the table below to pick the right path:

| Situation | Recommended path |
|---|---|
| You already run Caddy or nginx with an LDAP/SAML auth service | Forward-auth (caddy.md or nginx.md) |
| You have LDAP but no existing auth proxy | OIDC bridge (this guide) |
| You have a SAML IdP | OIDC bridge - most SAML IdPs also expose an OIDC endpoint; or deploy Authelia/Authentik as a bridge |
| You have an OIDC IdP (Okta, Azure AD, Google Workspace, Keycloak) | ShinyHub's built-in OIDC support directly (no bridge needed) |
| You use mTLS client certificates at the perimeter | Forward-auth (caddy.md) with a proxy that validates client certs |

## OIDC bridge with Authelia and docker compose

The example below wires Authelia (which speaks LDAP and SAML and exposes OIDC)
with ShinyHub. ShinyHub uses its built-in OIDC login; no forward-auth
configuration is needed.

```yaml
# docker-compose.yml
services:
  authelia:
    image: authelia/authelia:latest
    volumes:
      - ./authelia:/config
    ports:
      - "9091:9091"
    environment:
      - TZ=UTC

  shinyhub:
    image: ghcr.io/rvben/shinyhub:latest
    ports:
      - "8080:8080"
    environment:
      - SHINYHUB_AUTH_SECRET=change-me-to-a-random-string
      # OIDC: point at Authelia as the issuer.
      - SHINYHUB_OIDC_ISSUER_URL=https://auth.example.com
      - SHINYHUB_OIDC_CLIENT_ID=shinyhub
      - SHINYHUB_OIDC_CLIENT_SECRET=your-client-secret
      - SHINYHUB_OIDC_CALLBACK_URL=https://shiny.example.com/api/auth/oidc/callback
      - SHINYHUB_OIDC_DISPLAY_NAME=Sign in with Authelia
    depends_on:
      - authelia
```

## Minimal Authelia configuration sketch

The Authelia config lives in `./authelia/configuration.yml`. Only the
LDAP backend and OIDC client registration are shown; see the Authelia
documentation for TLS, session, storage, and notification settings.

```yaml
# authelia/configuration.yml (excerpt)

authentication_backend:
  ldap:
    address: ldap://ldap.example.com:389
    base_dn: dc=example,dc=com
    username_attribute: uid
    mail_attribute: mail
    groups_filter: (&(member={dn})(objectClass=groupOfNames))
    user: cn=authelia,dc=example,dc=com
    password: ldap-bind-password

identity_providers:
  oidc:
    clients:
      - client_id: shinyhub
        client_secret: your-client-secret
        redirect_uris:
          - https://shiny.example.com/api/auth/oidc/callback
        scopes:
          - openid
          - profile
          - email
          - groups
        grant_types:
          - authorization_code
```

Authelia will handle user authentication against LDAP and issue OIDC tokens
that ShinyHub exchanges via the standard authorization code flow. The
`/api/auth/oidc/callback` path is handled by ShinyHub's built-in OIDC handler.

## OIDC env vars for ShinyHub

| Variable | Description |
|---|---|
| `SHINYHUB_OIDC_ISSUER_URL` | OIDC discovery root (e.g. `https://auth.example.com`) |
| `SHINYHUB_OIDC_CLIENT_ID` | Client ID registered with the bridge IdP |
| `SHINYHUB_OIDC_CLIENT_SECRET` | Client secret |
| `SHINYHUB_OIDC_CALLBACK_URL` | Must match a redirect URI registered with the IdP. Path is always `/api/auth/oidc/callback`. |
| `SHINYHUB_OIDC_DISPLAY_NAME` | Text shown on the login button (default: "Sign in with SSO") |
| `SHINYHUB_OIDC_GROUPS_CLAIM` | ID-token claim holding group names (default: `groups`) |
| `SHINYHUB_OIDC_GROUPS_SCOPE` | Optional extra scope to request from the IdP (e.g. `groups`) |
| `SHINYHUB_OIDC_REQUIRE_VALID_GROUPS` | `true` to fail login on a malformed groups claim (default: `false` - logs and skips) |
| `SHINYHUB_AUTH_OAUTH_DEFAULT_ROLE` | Role assigned to users on first OIDC login (default: viewer) |

## OIDC YAML config reference

The same options are available via `shinyhub.yml` under `oauth.oidc`:

```yaml
oauth:
  oidc:
    issuer_url: "https://auth.example.com"
    client_id: "shinyhub"
    client_secret: "your-client-secret"
    callback_url: "https://shiny.example.com/api/auth/oidc/callback"
    display_name: "Sign in with SSO"
    groups_claim: "groups"        # ID-token claim holding group names
    groups_scope: "groups"        # extra scope to request (optional)
    require_valid_groups: false   # set to true to fail login on malformed groups claim
```

Set `require_valid_groups: true` to FAIL the login (HTTP 502) when the IdP
sends a malformed groups claim, instead of the default behavior of logging and
skipping group reconciliation (which leaves the user's existing role unchanged).
Use this if you would rather a misconfigured IdP block sign-in than silently
keep stale roles.

## Notes

**Group-based role assignment.** When using the OIDC bridge path, ShinyHub
assigns `oauth_default_role` to all new OIDC users. There is no automatic
promotion based on LDAP group membership in this mode. For group-based
promotion (including auto-admin from a group name), use the forward-auth path
instead.

**Authelia vs Authentik vs Keycloak.** All three work as an OIDC bridge in
front of LDAP or SAML. Choose based on what you already run:
- Authelia is lightweight and straightforward for LDAP-backed OIDC.
- Authentik supports LDAP, SAML, and custom attribute mapping with a web UI.
- Keycloak is the most feature-rich option with broad protocol support.
Any OIDC-compliant provider with a discovery document at
`{issuer_url}/.well-known/openid-configuration` works with ShinyHub's OIDC
support.
