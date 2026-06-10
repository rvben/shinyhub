# Forward-auth with Caddy

ShinyHub can trust authentication performed by an upstream reverse proxy. When
`auth.forward_auth` is enabled, ShinyHub reads the authenticated username (and
optionally email and group memberships) from HTTP request headers set by the
proxy. This lets sites that already use LDAP, SAML, Kerberos, mTLS client
certificates, or any other authentication mechanism integrate without ShinyHub
needing to implement those protocols itself.

## How it works

```
Browser --> Caddy (TLS, auth) --> ShinyHub :8080
                 |
                 v
          Auth service
          (LDAP, SAML, ...)
```

1. Caddy receives the browser request and calls an auth service via
   `forward_auth`.
2. The auth service returns `2xx` when the user is authenticated, setting
   response headers such as `X-Forwarded-User` and optionally
   `X-Forwarded-Email` and `X-Forwarded-Groups`.
3. Caddy copies those headers onto the upstream request and proxies it to
   ShinyHub.
4. ShinyHub sees that the request arrived from a trusted peer IP (loopback in
   a co-located setup) and trusts the headers. It looks up or auto-provisions
   the user account and issues a session.

## Caddyfile

```caddy
shiny.example.com {
    # Step 1: authenticate via your auth service.
    forward_auth auth-service:9091 {
        uri /api/verify

        # Copy the authenticated user identity onto the upstream request.
        copy_headers X-Forwarded-User X-Forwarded-Email X-Forwarded-Groups
    }

    # Step 2: proxy to ShinyHub.
    reverse_proxy localhost:8080 {
        # Disable buffering so SSE log-streaming works.
        flush_interval -1
    }
}
```

Adjust `auth-service:9091` and `/api/verify` to match your auth service (for
example Authelia at `authelia:9091/api/verify`, or oauth2-proxy at
`oauth2-proxy:4180/oauth2/auth`).

## Headers honored by ShinyHub

| Header | Config key | Description |
|---|---|---|
| `X-Forwarded-User` | `user_header` | Username (required). Default header name. |
| `X-Forwarded-Email` | `email_header` | Email address (optional). Accepted by config but not yet used by ShinyHub (reserved). |
| `X-Forwarded-Groups` | `groups_header` | Comma-separated group list. When `groups_header` is configured the proxy MUST send this header on every request (empty when the user has no groups); the listed groups drive role promotion AND revocation. An absent header is treated as no groups and revokes any group-derived role, so a dropped header demotes the user to the default role. |

## ShinyHub configuration

Add `auth.forward_auth` to your `shinyhub.yaml` and add Caddy's address to
`server.trusted_proxies`:

```yaml
server:
  trusted_proxies:
    - 127.0.0.0/8   # loopback (Caddy and ShinyHub on the same host)
    - ::1/128

auth:
  secret: "..."     # your existing secret
  forward_auth:
    enabled: true
    user_header: X-Forwarded-User     # matches copy_headers above
    email_header: X-Forwarded-Email
    groups_header: X-Forwarded-Groups  # when set, always emit - empty value for users with no groups
    admin_groups: ["shinyhub-admins"] # users in this group get admin role
    default_role: developer           # role for newly provisioned accounts
```

When `groups_header` is set, always emit it - send an empty value for users with
no groups - because ShinyHub treats an absent header as "no groups" and revokes
group-derived roles on that request.

Or with environment variables:

```
SHINYHUB_FORWARD_AUTH_ENABLED=true
SHINYHUB_FORWARD_AUTH_USER_HEADER=X-Forwarded-User
SHINYHUB_FORWARD_AUTH_EMAIL_HEADER=X-Forwarded-Email
SHINYHUB_FORWARD_AUTH_GROUPS_HEADER=X-Forwarded-Groups
SHINYHUB_FORWARD_AUTH_ADMIN_GROUPS=shinyhub-admins
SHINYHUB_FORWARD_AUTH_DEFAULT_ROLE=developer
```

## Notes

**Trust boundary.** ShinyHub checks the DIRECT peer IP of the TCP connection,
not the `X-Forwarded-For` chain. A request is accepted only when the connecting
socket address is in `server.trusted_proxies`. In a co-located setup (Caddy and
ShinyHub on the same host) the loopback default (`127.0.0.0/8`, `::1/128`) is
sufficient. If ShinyHub listens on a private network interface reachable by
Caddy running on a different host, add that interface's CIDR to
`server.trusted_proxies`.

**Auto-provisioning.** When a user header is received from a trusted peer and
no matching account exists, ShinyHub creates one with `default_role`. If the
user is a member of any group listed in `admin_groups`, the role is promoted to
`admin` regardless of `default_role`. Subsequent logins re-apply group-based
admin promotion, but the middleware never downgrades a role: a user removed
from an admin group keeps the `admin` role until an operator changes it.

**Large deploy uploads.** ShinyHub accepts bundles up to `storage.max_bundle_mb`
(default 128 MB). Caddy's default request body limit is high, but if your auth
service enforces a body limit on the `forward_auth` subrequest make sure it
allows a `HEAD`-style pass-through for the `/api/apps/{slug}/deploy` path, or
set a generous limit on the auth service route.
