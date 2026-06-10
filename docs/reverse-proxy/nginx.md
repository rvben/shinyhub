# Forward-auth with nginx

ShinyHub can trust authentication performed by an upstream reverse proxy. This
guide shows how to wire nginx's `auth_request` module so that an external auth
service authenticates every request before it reaches ShinyHub.

## How it works

```
Browser --> nginx (TLS, auth_request) --> ShinyHub :8080
                       |
                       v
                Auth service
                (LDAP, SAML, ...)
```

1. nginx intercepts the browser request and issues a subrequest to the auth
   service via `auth_request`.
2. The auth service returns `2xx` (allow) or `4xx`/`5xx` (deny), and sets
   response headers such as `X-Forwarded-User`, `X-Forwarded-Email`, and
   `X-Forwarded-Groups`.
3. nginx captures those headers with `auth_request_set` and forwards them to
   ShinyHub.
4. ShinyHub sees that the request arrived from a trusted peer IP (loopback in
   a co-located setup) and trusts the headers.

## nginx.conf

```nginx
server {
    listen 443 ssl;
    server_name shiny.example.com;

    # TLS configuration omitted for brevity.

    # Allow large deploy bundles (match storage.max_bundle_mb in shinyhub.yaml).
    client_max_body_size 256m;

    location / {
        # Authenticate every request via the auth service subrequest.
        auth_request /_auth;

        # Capture the identity headers set by the auth service response.
        auth_request_set $auth_user   $upstream_http_x_forwarded_user;
        auth_request_set $auth_email  $upstream_http_x_forwarded_email;
        auth_request_set $auth_groups $upstream_http_x_forwarded_groups;

        proxy_pass http://127.0.0.1:8080;

        # Forward the identity headers to ShinyHub.
        proxy_set_header X-Forwarded-User   $auth_user;
        proxy_set_header X-Forwarded-Email  $auth_email;
        proxy_set_header X-Forwarded-Groups $auth_groups;

        # Standard proxy headers.
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Required for SSE log streaming.
        proxy_buffering       off;
        proxy_read_timeout    3600s;
        proxy_http_version    1.1;
        proxy_set_header Connection "";
    }

    # Internal-only auth subrequest location.
    location = /_auth {
        internal;
        proxy_pass              http://auth-service:9091/api/verify;
        proxy_pass_request_body off;
        proxy_set_header        Content-Length "";
        proxy_set_header        X-Original-URI $request_uri;
    }
}
```

Replace `http://auth-service:9091/api/verify` with the verify endpoint of your
auth service (for example Authelia at `http://authelia:9091/api/verify`, or
oauth2-proxy at `http://oauth2-proxy:4180/oauth2/auth`).

## Headers honored by ShinyHub

| Header | Config key | Description |
|---|---|---|
| `X-Forwarded-User` | `user_header` | Username (required). Default header name. |
| `X-Forwarded-Email` | `email_header` | Email address (optional). Accepted by config but not yet used by ShinyHub (reserved). |
| `X-Forwarded-Groups` | `groups_header` | Comma-separated group list. When `groups_header` is configured the proxy MUST send this header on every request (empty when the user has no groups); the listed groups drive role promotion AND revocation. An absent header is treated as no groups and revokes any group-derived role, so a dropped header demotes the user to the default role. |

## ShinyHub configuration

Add `auth.forward_auth` to your `shinyhub.yaml` and add nginx's address to
`server.trusted_proxies`:

```yaml
server:
  trusted_proxies:
    - 127.0.0.0/8   # loopback (nginx and ShinyHub on the same host)
    - ::1/128

auth:
  secret: "..."     # your existing secret
  forward_auth:
    enabled: true
    user_header: X-Forwarded-User     # must match proxy_set_header names above
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
```

## Notes

**Trust boundary.** ShinyHub checks the DIRECT peer IP of the TCP connection,
not the `X-Forwarded-For` chain. A request is accepted only when the connecting
socket address is in `server.trusted_proxies`. In a co-located setup (nginx and
ShinyHub on the same host) the loopback default (`127.0.0.0/8`, `::1/128`) is
sufficient. If ShinyHub runs on a different host, add nginx's outbound IP to
`server.trusted_proxies`.

**Auto-provisioning.** When a user header is received from a trusted peer and
no matching account exists, ShinyHub creates one with `default_role`. If the
user is a member of any group in `admin_groups`, the role is promoted to
`admin`. Subsequent logins update group-based promotions but do not downgrade
manually-set roles.

**SSE log streaming.** ShinyHub streams app logs over Server-Sent Events. Set
`proxy_buffering off` and a long `proxy_read_timeout` (shown above) so long-lived
SSE connections are not dropped by nginx.

**Large deploy uploads.** Match `client_max_body_size` in nginx to
`storage.max_bundle_mb` in ShinyHub (default 128 MB). Adding multipart overhead,
256 MB is a safe ceiling for a 128 MB bundle.
