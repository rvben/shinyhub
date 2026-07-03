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

    # Step 2: proxy to ShinyHub. A bare reverse_proxy already tunnels the Shiny
    # WebSocket automatically (see "WebSockets" below); flush_interval -1 also
    # disables buffering so SSE log-streaming works.
    reverse_proxy localhost:8080 {
        flush_interval -1
    }
}
```

Adjust `auth-service:9091` and `/api/verify` to match your auth service (for
example Authelia at `authelia:9091/api/verify`, or oauth2-proxy at
`oauth2-proxy:4180/oauth2/auth`).

## WebSockets (Shiny reactivity)

Shiny drives every interaction (button clicks, tab switches, reactive updates)
over a **WebSocket**. The initial page HTML and static assets load over plain
HTTP, so a broken WebSocket looks deceptive: the app renders and static requests
return `200`, but **every interaction fails with "Shiny disconnected"** because
the reactive channel never opens. Python Shiny uses a raw WebSocket with no
polling fallback, so a failed upgrade disconnects immediately and completely.

Caddy v2's `reverse_proxy` tunnels WebSockets automatically, so the minimal
config above already works. The upgrade breaks only when something in the chain
interferes with it. If interactions disconnect, check these in order:

1. **`forward_auth` runs on the WebSocket upgrade too.** Caddy issues the auth
   subrequest for **every** request, including the WebSocket handshake. If the
   auth service answers the handshake with a redirect or `401` (for example
   because the session cookie is not carried on the upgrade, or the WebSocket
   path is not treated as already-authenticated), Caddy never proxies the
   upgrade to ShinyHub and the interaction fails, even though ordinary GETs
   succeed. Make sure the auth service authorizes the WebSocket request the same
   way it authorizes the page that opened it.
2. **Keep HTTP/1.1 to the upstream.** A WebSocket `Upgrade` cannot ride HTTP/2.
   The default transport is HTTP/1.1, which is correct; do **not** force
   `transport http { versions h2c 2 }` or an HTTP/2 upstream for the ShinyHub
   route.
3. **Do not strip the upgrade headers.** A `header_up` / `request_header`
   directive that overwrites `Connection`, or a `handle` / `route` split that
   sends the app's WebSocket subpath to a `file_server` or default handler
   instead of `reverse_proxy`, turns the `101` into a non-upgrade response.
4. **Watch global timeouts.** Short `servers { timeouts { read_timeout ... } }`
   values can close long-lived WebSocket sessions. ShinyHub itself never
   times out an established WebSocket.

### Diagnosing an upgrade failure

- **Read Caddy's access log for the WebSocket request** (the one with
  `Upgrade: websocket`). Status `101` means it tunneled; `200`, `302`, `401`, or
  `502` is the smoking gun and points at one of the causes above.
- **Use ShinyHub's built-in readiness probe.** `GET /app/<slug>/.shinyhub/ready`
  returns `200 {"ready":true}` only after at least one WebSocket handshake has
  completed for that app; it returns `503 {"ready":false}` (with `Retry-After: 1`)
  before the first handshake, and `404` for an unknown slug. If this probe never
  reports `ready:true` while users are actively interacting, no WebSocket is
  reaching the app, which localizes the fault to the proxy hop rather than the
  app.
- **Bisect the proxy.** Drive the app directly against the ShinyHub port
  (`http://<host>:8080/app/<slug>/`, bypassing Caddy). If interactions work
  there but fail through Caddy, the Caddy configuration is the cause.

## Headers honored by ShinyHub

| Header | Config key | Description |
|---|---|---|
| `X-Forwarded-User` | `user_header` | Username (required). Default header name. |
| `X-Forwarded-Email` | `email_header` | Email address (optional). Accepted by config but not yet used by ShinyHub (reserved). |
| `X-Forwarded-Groups` | `groups_header` | Comma-separated group list. When `groups_header` is configured the proxy MUST send this header on every request (empty when the user has no groups); the listed groups drive role promotion AND revocation. An absent header is treated as no groups and revokes any group-derived role, so a dropped header demotes the user to the default role. |

## ShinyHub configuration

Add `auth.forward_auth` to your `shinyhub.yaml` and add Caddy's address to
`server.trusted_proxies`.

> **Cross-host requirement.** If Caddy and ShinyHub run on DIFFERENT hosts, you
> MUST add the Caddy host's IP or CIDR to `server.trusted_proxies`. The loopback
> default (`127.0.0.0/8`, `::1/128`) only covers the case where both processes
> run on the same machine. Without the correct entry, ShinyHub silently ignores
> the forwarded identity headers and users land on the login page with no
> indication of what went wrong.

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
    require_groups_header: false      # set true to REFUSE (403) any request missing the groups header
```

When `groups_header` is set, always emit it - send an empty value for users with
no groups - because ShinyHub treats an absent header as "no groups" and revokes
group-derived roles on that request.

Set `require_groups_header: true` to REFUSE (403) any forward-auth request that
lacks the groups header instead of treating it as no groups. Use this when your
proxy always sends the header and you want a misconfiguration to fail loudly
rather than silently demote users.

Or with environment variables:

```
SHINYHUB_FORWARD_AUTH_ENABLED=true
SHINYHUB_FORWARD_AUTH_USER_HEADER=X-Forwarded-User
SHINYHUB_FORWARD_AUTH_EMAIL_HEADER=X-Forwarded-Email
SHINYHUB_FORWARD_AUTH_GROUPS_HEADER=X-Forwarded-Groups
SHINYHUB_FORWARD_AUTH_ADMIN_GROUPS=shinyhub-admins
SHINYHUB_FORWARD_AUTH_DEFAULT_ROLE=developer
SHINYHUB_FORWARD_AUTH_REQUIRE_GROUPS_HEADER=false
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
