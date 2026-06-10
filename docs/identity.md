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
is opened belongs to the user who triggered the upgrade. Subsequent messages
on that WebSocket are not re-authenticated per-message; the initial identity
check governs the session's lifetime.

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
| `X-Shinyhub-Groups` | Comma-joined sorted group names | Capped at 100 names; group names that contain a comma are omitted from this header (they appear in the JWT claim instead) |
| `X-Shinyhub-Groups-Truncated` | `"true"` | Present only when the group list exceeded 100 and was truncated |
| `X-Shinyhub-Identity-Token` | Signed HS256 JWT | The authoritative identity artifact; verify before acting on it |

`X-Shinyhub-Groups` is absent when the user belongs to no groups. It is also
absent when every group name contains a comma (in that case only the JWT claim
carries them). `X-Shinyhub-Groups-Truncated` is absent when the cap did not
fire.


## Token reference

The identity token is a standard JWT signed with HS256. Its claims are:

| Claim | Type | Value |
|-------|------|-------|
| `iss` | string | `"shinyhub"` |
| `aud` | string | The app slug (also available as `SHINYHUB_APP_SLUG` in the process env) |
| `sub` | string | Decimal user ID |
| `preferred_username` | string | Username |
| `role` | string | One of `viewer`, `developer`, `operator`, `admin` |
| `groups` | array of strings | Sorted group names, capped at 100 (all names, including comma-bearing ones) |
| `groups_truncated` | bool | `true` when the list was truncated to 100; absent otherwise |
| `iat` | NumericDate | Token issue time |
| `exp` | NumericDate | `iat + 5 minutes` |

When verifying, check `iss == "shinyhub"`, `aud == SHINYHUB_APP_SLUG`, the
signature, and `exp` (allow about 30 seconds of clock leeway).


## Verifying in Python

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
                      audience=SLUG, issuer="shinyhub", leeway=30)
```

`jwt.decode` raises `jwt.exceptions.InvalidTokenError` (or a subclass) when
the token is missing, expired, has the wrong audience, or fails the signature
check. Treat any exception as unauthenticated.

In Shiny for Python, read request headers inside the server function via
`session.http_conn.headers`:

```python
from shiny import App, ui, render, session as shiny_session

def server(input, output, session):
    user = current_user(dict(session.http_conn.headers))
    # user is None for anonymous visitors
```

A runnable demo is in `examples/identity-demo/`.


## Verifying in R

Install the [`jose`](https://cran.r-project.org/package=jose) and
[`openssl`](https://cran.r-project.org/package=openssl) packages:

```r
install.packages(c("jose", "openssl"))
```

```r
library(jose)
library(openssl)

# Decode the hex key injected by ShinyHub into the process environment.
identity_key <- openssl::hex2bin(Sys.getenv("SHINYHUB_IDENTITY_KEY"))
app_slug     <- Sys.getenv("SHINYHUB_APP_SLUG")

current_user <- function(session) {
  token <- session$request$HTTP_X_SHINYHUB_IDENTITY_TOKEN
  if (is.null(token) || token == "") return(NULL)

  claims <- tryCatch(
    jose::jwt_decode_hmac(token, secret = identity_key),
    error = function(e) NULL
  )
  if (is.null(claims)) return(NULL)

  # jose verifies the signature; assert the remaining claims explicitly.
  now <- as.numeric(Sys.time())
  leeway <- 30  # seconds
  if (!identical(claims$iss, "shinyhub"))       return(NULL)
  if (!identical(claims$aud, app_slug))         return(NULL)
  if (is.null(claims$exp) || claims$exp + leeway < now) return(NULL)

  claims
}
```

Use `current_user(session)` inside your Shiny server function. It returns
`NULL` for anonymous visitors or when the token fails any check.


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

### Key rotation

Identity keys are derived from `auth.secret`. To rotate, change `auth.secret`,
restart the ShinyHub server, and restart (or redeploy) each app so it picks up
its new `SHINYHUB_IDENTITY_KEY`. Tokens minted with the old key become invalid
immediately after the server restarts.

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
