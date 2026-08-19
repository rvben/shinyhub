---
description: "Every proxied request carries the authenticated user to the app as X-Shinyhub headers, so apps personalize without running their own login."
---

# Identity Forwarding

ShinyHub forwards the authenticated user's identity to every app process it
proxies. For each request, the proxy injects a set of `X-Shinyhub-*` plain
headers and a short-lived signed JWT (`X-Shinyhub-Identity-Token`). The plain
headers are a convenience for quick reads. The JWT is the authoritative
artifact for any code that makes access-control decisions. Both are on by
default and can be disabled per-app in
[`shinyhub.toml`](manifest.md#app-identity_headers).


## Trust model

**This section is the most important one. Read it before writing any app logic
that acts on identity.**

### What you can trust

The proxy strips every inbound `X-Shinyhub-*` header unconditionally on every
request, regardless of what the client or an upstream reverse proxy sends.
Headers that arrive at your app process through the ShinyHub proxy therefore
originated from ShinyHub itself.

### What you cannot trust from plain headers alone

App processes listen on host-local ports (for example `127.0.0.1:PORT`). Any
other process running on the same host can connect directly to that port and
supply arbitrary `X-Shinyhub-*` headers. The strip only fires in the proxy's
Director. A request that bypasses the proxy never passes through it.

**Apps that make security decisions (show/hide data, gate writes, check roles)
must verify the JWT, not rely on the plain headers.** The plain headers are
useful for display or logging where a forgery carries no consequence.

### Anonymous requests

An absent `X-Shinyhub-User` header means the visitor is not authenticated.
This happens for public apps accessed by a logged-out user, or for any request
that bypassed the proxy entirely. Do not infer identity from absence; treat
absent headers as anonymous.

### WebSocket and session binding

For Shiny apps, identity is bound at WebSocket upgrade time. The session that
is opened belongs to the user who triggered the upgrade, and messages on that
WebSocket are not re-authenticated per-message.

The connection is re-authorized on a timer instead. Every ShinyHub instance
sweeps its own live upgraded connections every `server.session_recheck_interval`
(default 30s) and closes the ones whose access has lapsed:

| What changed | How it is detected |
| --- | --- |
| An admin revoked the user's sessions, or their password changed | `users.token_epoch` no longer matches the connection's |
| The user was deleted | the user row is gone |
| The user's global role changed | the live role no longer matches the one the app was told |
| The user signed out | that session token's `jti` is revoked |
| The user lost access to the app (membership or group grant removed) | the admission check is re-run against the live database |
| A public app was made private | same, for a connection admitted anonymously |

A lapsed connection is closed at the socket, without a WebSocket close frame.
The browser sees a dropped connection, reconnects, and meets the normal access
check, which answers with the proper 401 or 403. So the window in which a
revoked user keeps a session they already had open is one sweep interval, not
the session's natural lifetime.

The sweep is per-instance by design. A hijacked connection can only be closed by
the process holding it, so in a high-availability deployment every instance
sweeps its own connections, standbys included, rather than the control-plane
owner doing it for the cluster.

Two kinds of drift are deliberately not swept, because they are not access
decisions: group membership and profile fields (email, display name) change what
a live session is *told* about the user, not whether it may stay open. Both are
already bounded by the caches described below, and reach the app on its next
HTTP request.

### Token validity window

Tokens are valid for 5 minutes from issuance (the `exp` claim). A token
remains cryptographically valid for up to 5 minutes even if the user logs out
or their role changes between requests. New HTTP requests immediately carry a
freshly minted token reflecting the current state, so the lag only matters for
long-lived connections that do not issue new requests. Apps that require
sub-5-minute revocation (for example a payment flow) should issue their own
short-lived action tokens after verifying the identity token.

### Groups cache lag

Group membership is cached server-side per user for up to 30 seconds. An IdP
change (group add or remove) can therefore lag by up to 30 seconds before it
appears in forwarded headers and tokens.

### Key exposure and blast radius

`SHINYHUB_IDENTITY_KEY` is injected into the app process environment as a
plaintext hex string. It is visible in:

- `docker inspect` output on the host
- ECS task descriptions returned by `ecs:DescribeTasks`, even when Secrets
  Manager routing is enabled for other app env vars
- Remote Docker worker hosts

The blast radius of a leaked key is limited to the single app it belongs to:
an attacker with the key can only forge or verify identity tokens addressed to
that app's audience. Keys are derived from the app's numeric database ID (not
its slug), so a deleted-and-recreated app under the same slug receives a fresh
key and cannot be reached with the old one.


## Headers reference

| Header | Value | Notes |
|--------|-------|-------|
| `X-Shinyhub-User` | Username string | Absent for anonymous visitors |
| `X-Shinyhub-User-Id` | Decimal user ID | Integer encoded as a string |
| `X-Shinyhub-Role` | One of `viewer`, `developer`, `operator`, `admin` | The user's global platform role |
| `X-Shinyhub-App-Role` | One of `owner`, `manager`, `viewer` | The user's capability on THIS app: `owner` owns it; `manager` covers platform admins/operators and manager-role members (by membership or group rule); everyone else who passed the access gate is `viewer`. Absent when the membership lookup was unavailable. Changes propagate within ~30 s (cached) |
| `X-Shinyhub-Email` | Email address | Present when the IdP asserts one: from the forward-auth `email_header`, or persisted from an OAuth/OIDC login for native sessions. Absent for local-password accounts and anonymous visitors |
| `X-Shinyhub-Name` | Display name | The user's friendly name when the IdP asserts one: from the forward-auth `name_header`, or persisted from an OAuth/OIDC login for native sessions. Absent for local-password accounts and anonymous visitors |
| `X-Shinyhub-Groups` | Comma-joined sorted group names | Capped at 100 names; group names that contain a comma are omitted from this header (they appear in the JWT claim instead) |
| `X-Shinyhub-Groups-Truncated` | `"true"` | Present only when the group list exceeded 100 and was truncated |
| `X-Shinyhub-Identity-Token` | Signed HS256 JWT | The authoritative identity artifact; verify before acting on it |

`X-Shinyhub-Groups` is absent when the user belongs to no groups. It is also
absent when every group name contains a comma (in that case only the JWT claim
carries them). `X-Shinyhub-Groups-Truncated` is absent when the cap did not
fire.

Email comes from one of two sources. Behind forward-auth SSO it is read
request-scoped from the header named by `auth.forward_auth.email_header` (e.g.
Authelia's `Remote-Email`). For native ShinyHub sessions it is the address the
identity provider asserted at OAuth/OIDC login, persisted on the user record and
refreshed on each SSO login. Local username/password accounts have no email
source, so the header (and the helper) return empty for them.

The display name (`X-Shinyhub-Name`) follows the same rule. Behind forward-auth
it is read request-scoped from the header named by `auth.forward_auth.name_header`;
for native sessions it is the name the IdP asserted at OAuth/OIDC login, persisted
on the user record and refreshed on each SSO login. Local username/password
accounts carry no IdP-governed name, so the header (and the helper) return empty
for them.


## Token reference

The identity token is a standard JWT signed with HS256. Its claims are:

| Claim | Type | Value |
|-------|------|-------|
| `iss` | string | `"shinyhub"` |
| `aud` | string (serialized as a one-element array) | The app slug (also available as `SHINYHUB_APP_SLUG` in the process env) |
| `sub` | string | Decimal user ID |
| `preferred_username` | string | Username |
| `role` | string | One of `viewer`, `developer`, `operator`, `admin` |
| `app_role` | string | One of `owner`, `manager`, `viewer` (see the `X-Shinyhub-App-Role` header); omitted when the membership lookup was unavailable |
| `email` | string | The user's email when the IdP asserts one (forward-auth `email_header`, or persisted from an OAuth/OIDC login); omitted for local-password accounts |
| `name` | string | The user's display name when the IdP asserts one (forward-auth `name_header`, or persisted from an OAuth/OIDC login); omitted for local-password accounts |
| `groups` | array of strings | Sorted group names, capped at 100 (all names, including comma-bearing ones) |
| `groups_truncated` | bool | `true` when the list was truncated to 100; absent otherwise |
| `iat` | NumericDate | Token issue time |
| `exp` | NumericDate | `iat + 5 minutes` |

When verifying, check `iss == "shinyhub"`, `aud == SHINYHUB_APP_SLUG`, the
signature, and `exp` (allow about 30 seconds of clock leeway).


## Client helpers (recommended)

Rather than decode the token yourself, use the one-call helper for your
language, so your app needs no JWT plumbing and stays testable without SSO.
Both read the injected `SHINYHUB_IDENTITY_KEY` / `SHINYHUB_APP_SLUG`
automatically, and both return the same identity shape: `user_id`, `username`,
`role`, `groups`, `name`, `email`, `groups_truncated`, and the raw verified
`claims`. `email` and `name` are empty when the IdP asserted none.

The helpers are versioned independently of the server: their versions track
changes to the helper APIs, not the server release train. Any helper release
verifies tokens from any ShinyHub v0.8.6 or later (identity forwarding
shipped in v0.8.6); the token contract is stable across server releases, and
claims a later server added (`email`, `name`) are simply empty when an older
server minted the token.

### Anonymous and broken are different answers

`None` / `NULL` means exactly one thing: **the request carried no identity
token**, so the visitor is anonymous. A token that is present but fails
verification - missing or wrong key, audience or issuer mismatch, expired,
clock skew - is a broken deployment rather than a visitor, so it raises:

- Python: `IdentityError`, carrying `.reason` and `.detail`.
- R: a `shinyhub_identity_error` condition carrying `$reason` and `$detail`,
  caught with `tryCatch(expr, shinyhub_identity_error = function(e) ...)`.

`reason` is a stable classification, identical in both languages and pinned by
a cross-language conformance test:

| `reason` | Meaning |
|----------|---------|
| `no_token` | nothing to verify (`verify_token` only; `current_user` returns the anonymous value instead) |
| `no_key` | `SHINYHUB_IDENTITY_KEY` unset or empty |
| `bad_key` | `SHINYHUB_IDENTITY_KEY` is not valid hex |
| `no_slug` | `SHINYHUB_APP_SLUG` unset or empty |
| `bad_signature` | signed with a different key |
| `expired` | past its `exp` (tokens live 5 minutes) |
| `wrong_audience` | minted for a different app slug |
| `wrong_issuer` | `iss` is not `shinyhub` |
| `malformed` | unparseable, or missing the required `exp` claim |

Let it propagate unless you have something better to do with it. An app that
renders a misconfigured deployment as "logged out" hides the outage behind an
empty dashboard, which is exactly what this contract exists to prevent.

For local development (no proxy, so no token and no injected key), both
helpers honor `SHINYHUB_IDENTITY_DEV_USER` (plus optional
`SHINYHUB_IDENTITY_DEV_GROUPS`, `..._DEV_EMAIL`, `..._DEV_NAME`,
`..._DEV_ROLE`, default `viewer`) and return a synthetic identity marked with
a `dev` claim. The dev identity never activates when `SHINYHUB_IDENTITY_KEY`
is set, so it cannot mask a real verification failure in a deployment.

### Python

```
pip install shinyhub-identity   # or: uv add shinyhub-identity
```

```python
from shinyhub_identity.shiny import session_identity

def server(input, output, session):
    user = session_identity(session)   # None when anonymous
    if user and "platform-admins" in user.groups:
        ...  # gate on the VERIFIED groups
```

`session_identity` verifies the handshake once and returns that answer for the
session's life. That is a correctness property, not a cache: ShinyHub binds
identity at the WebSocket handshake, and the token it forwarded there expires
five minutes later, so an app that re-verifies the handshake headers from a
reactive starts failing part-way through a long session even though nothing
about the user changed.

Outside Shiny (Streamlit, Dash, FastAPI, ...) use the framework-free
primitive, which takes any header mapping and verifies per request:

```python
from shinyhub_identity import current_user

user = current_user(request.headers)   # None when anonymous
```

### R

```r
install.packages(c("jose", "sodium"))
remotes::install_github("rvben/shinyhub", subdir = "packaging/r-identity")
```

```r
library(shinyhubidentity)

server <- function(input, output, session) {
  user <- current_user(session)   # NULL when anonymous
  # user$username, user$role, user$email, user$name, user$groups
}
```

`current_user(session)` verifies once per session, for the same reason the
Python helper does.

**Migrating from a hand-rolled per-app JWT fetch?** Delete it - the client-side
`get_jwt` / `decode_jwt` code and any browser-side token fetch. ShinyHub already
injects and signs the identity server-side; call `current_user` instead. That
also removes the internal-CA `ERR_CERT_AUTHORITY_INVALID` failure mode a
client-side fetch hits, and makes the app load-testable (a session with no
token is simply anonymous, and a broken one is a classified error instead of a
silent empty page).

The manual recipes below show what the helpers do under the hood, for languages
or frameworks the packages do not cover.


## Verifying manually (Python)

Install [PyJWT](https://pyjwt.readthedocs.io/en/stable/):

```
uv add PyJWT
# or: pip install PyJWT
```

```python
import os
import jwt  # PyJWT

KEY = bytes.fromhex(os.environ["SHINYHUB_IDENTITY_KEY"])
SLUG = os.environ["SHINYHUB_APP_SLUG"]

def current_user(headers) -> dict | None:
    """Verified identity of the request, or None for anonymous."""
    token = headers.get("x-shinyhub-identity-token")
    if not token:
        return None
    return jwt.decode(token, KEY, algorithms=["HS256"],
                      audience=SLUG, issuer="shinyhub", leeway=30,
                      options={"require": ["exp"]})
```

`jwt.decode` raises `jwt.exceptions.InvalidTokenError` (or a subclass) when the
token is expired, has the wrong audience or issuer, omits `exp`, or fails the
signature check. Let it propagate: the anonymous case already returned `None`
above, so an exception here means the deployment is broken, not that the
visitor is logged out.

In Shiny for Python, read request headers inside the server function via
`session.http_conn.headers`:

```python
from shiny import App, ui, render, session as shiny_session

def server(input, output, session):
    user = current_user(dict(session.http_conn.headers))
    # user is None for anonymous visitors
```

Verify once, at the top of the server function as above, and keep the result.
The handshake headers stay in memory for the session's life while the token
they carry expires five minutes later, so calling `current_user` from inside a
reactive starts raising part-way through a long session even though nothing
about the user changed. `session_identity` does this for you.

A runnable demo is in `examples/identity-demo/`.


## Verifying manually (R)

Install the [`jose`](https://cran.r-project.org/package=jose) and
[`sodium`](https://cran.r-project.org/package=sodium) packages:

```r
install.packages(c("jose", "sodium"))
```

```r
library(jose)
library(sodium)

# Decode the hex key injected by ShinyHub into the process environment.
identity_key <- sodium::hex2bin(Sys.getenv("SHINYHUB_IDENTITY_KEY"))
app_slug     <- Sys.getenv("SHINYHUB_APP_SLUG")

current_user <- function(session) {
  token <- session$request$HTTP_X_SHINYHUB_IDENTITY_TOKEN
  # No token at all: an anonymous visitor.
  if (is.null(token) || token == "") return(NULL)

  # Present but unverifiable: a broken deployment. jose signals a bad signature
  # or an expired token as an error; let it propagate rather than turning it
  # into "logged out".
  claims <- jose::jwt_decode_hmac(token, secret = identity_key)

  # jose validates the signature, and exp/nbf when present (with a fixed
  # 60-second grace period of its own). Assert iss, aud, the presence of exp,
  # and the narrower 30 seconds of skew the Python recipe above allows, so the
  # same token gets the same answer in both languages.
  if (is.null(claims$exp))                 stop("identity token: no exp claim")
  if (as.numeric(claims$exp) + 30 < as.numeric(Sys.time()))
                                           stop("identity token: expired")
  if (!identical(claims$iss, "shinyhub"))  stop("identity token: wrong issuer")
  if (!(app_slug %in% claims$aud))         stop("identity token: wrong audience")

  claims
}
```

Use `current_user(session)` inside your Shiny server function. It returns
`NULL` only for a visitor who sent no token; anything else is an error.

A real Shiny session's `request` is frozen at the WebSocket handshake while the
token it carries expires five minutes later, so verify once and keep the
result for the session rather than re-verifying inside a reactive. Both client
packages do this for you.


## Worked example: per-group UI gating

After verifying the token, check the `groups` claim to gate access to parts of
your app. The following Shiny for Python snippet shows an admins-only panel:

```python
from shiny import App, ui, render, session as shiny_session

def server(input, output, session):
    user = current_user(dict(session.http_conn.headers))
    groups = user.get("groups", []) if user else []

    @output
    @render.ui
    def admin_panel():
        if "platform-admins" not in groups:
            return ui.p("Access restricted.")
        return ui.div(
            ui.h3("Admin controls"),
            # ... admin widgets ...
        )

app = App(ui.page_fluid(ui.output_ui("admin_panel")), server)
```

For a complete working app (Python and R, covering anonymous/viewer/admin
flows, plus the R equivalent), see `examples/identity-demo/`.


## Configuration

### Global switch

```yaml
# shinyhub.yaml
auth:
  identity_headers: true   # default; omitting this key has the same effect
```

Set `identity_headers: false` (or `SHINYHUB_IDENTITY_HEADERS=false`) to
disable forwarding across the entire installation. This is a hard operator
kill switch: no per-app manifest setting can override it. Use this to satisfy
a compliance requirement or to roll out the feature gradually.

### Per-app opt-out

Add an `[app]` section to your bundle's `shinyhub.toml`:

```toml
[app]
identity_headers = false
```

Setting `identity_headers = false` opts this app out while leaving the rest of
the fleet forwarding identity. Removing the key (or the whole `[app]` section)
reverts to the global default on the next deploy.

The global `false` kill switch always wins. If the operator has set
`auth.identity_headers: false`, setting `identity_headers = true` in a
manifest has no effect.

### Session re-check interval

```yaml
# shinyhub.yaml
server:
  session_recheck_interval: 30s   # default; 0 disables the sweep
```

How often each instance re-authorizes its live WebSocket app sessions (see
[WebSocket and session binding](#websocket-and-session-binding)). It bounds how
long a revoked user keeps a session that was already open; ordinary HTTP
requests are authorized on every request and are unaffected by this setting.

The value is a duration, so write `30s` or `2m`, not `30`. A bare `0` turns the
sweep off, restoring the older behaviour where an open session outlived every
revocation. `SHINYHUB_SESSION_RECHECK_INTERVAL` overrides the YAML key.

Lowering it costs little: a sweep decides once per distinct (app, user) pair
that holds a live connection, not once per connection and not once per request,
so a user with eight tabs open costs one decision. An instance with no upgraded
connections does no work at all. If the database is unreachable the sweep fails
open and keeps every connection, because dropping every live session on a
transient error is worse than a few more seconds of access for one revoked user.

### Key rotation

Identity keys are derived from `auth.secret`. To rotate, change `auth.secret`,
restart the ShinyHub server, and restart (or redeploy) each app so it picks up
its new `SHINYHUB_IDENTITY_KEY`. Tokens minted with the old key become invalid
immediately after the server restarts.

Restarting the apps is required, not cosmetic: a running app keeps the key it
was spawned with, so until it restarts it rejects every token the server mints
(the client helpers report `bad_signature`). See
[Rotating `auth.secret`](secret-rotation.md) for the full procedure, including
the at-rest secrets that need `shinyhub rotate-secret`.

### High-availability deployments

In a multi-instance deployment, every control-plane instance derives the same
per-app keys from `auth.secret`, so tokens minted by one instance are
verifiable by apps regardless of which instance proxied the request. Keep the
global `auth.identity_headers` flag identical on every instance; each instance
resolves a per-app `NULL` (inherit) against its own local config.


## Overhead

The group list is capped at 100 names, but long group names still add
kilobytes per request. Intermediate proxies or load balancers with small header
size limits (commonly 8 KB) may need tuning if your users belong to many
groups with long names. Per-app opt-out (`identity_headers = false` in
`shinyhub.toml`) is the relief valve for high-traffic apps that do not consume
identity.
