# shinyhubidentity (R)

Read the signed identity [ShinyHub](https://github.com/rvben/shinyhub) forwards
to your R Shiny app, in one call. No per-app JWT plumbing.

ShinyHub injects a short-lived, per-app HS256 JWT
(`X-Shinyhub-Identity-Token`) into every request it proxies, and hands your app
its verification key via `SHINYHUB_IDENTITY_KEY` and `SHINYHUB_APP_SLUG`. This
package verifies that token and returns the identity.

## Install

```r
# from a checkout of the shinyhub repo:
install.packages(c("jose", "sodium"))
remotes::install_local("packaging/r-identity")
```

## Use it

```r
library(shinyhubidentity)

server <- function(input, output, session) {
  user <- current_user(session)         # NULL when anonymous
  observe({
    if (!is.null(user) && "platform-admins" %in% user$groups) {
      # gate admin features on the VERIFIED groups
    }
  })
}
```

`current_user(session)` returns the verified identity:

| Field | Value |
|-------|-------|
| `user_id` | Decimal user ID |
| `username` | Username |
| `role` | Platform role: `viewer`, `developer`, `operator`, or `admin` |
| `groups` | Character vector of verified group names |
| `groups_truncated` | `TRUE` when the group list was capped at 100 |
| `email` | `""` unless the deployment's IdP asserts one |
| `name` | Display name; `""` unless the IdP asserts one |
| `claims` | The raw verified JWT claims |

The field names match the Python helper's `Identity`, so one documented
contract covers both SDKs.

It verifies the session's handshake token once and returns that answer for the
session's life. That is a correctness property, not a cache: a Shiny session's
`request` is frozen at the WebSocket handshake while the token it carries
expires five minutes later, so re-verifying it inside a reactive would start
failing part-way through a long session even though nothing about the user
changed.

Key and slug default to the `SHINYHUB_IDENTITY_KEY`/`SHINYHUB_APP_SLUG`
environment variables ShinyHub injects; pass `key=`/`slug=` explicitly for
tests.

## `NULL` means anonymous, and nothing else

A genuine anonymous visitor sends **no token at all**, so that is the only case
that returns `NULL`. A token that is present but fails verification is a broken
deployment - missing or wrong `SHINYHUB_IDENTITY_KEY`, audience or issuer
mismatch, an expired token, clock skew - and raises a `shinyhub_identity_error`
condition instead. An app that renders that as "logged out" hides the outage
behind an empty dashboard, which is exactly what this contract prevents.

```r
user <- tryCatch(
  current_user(session),
  shinyhub_identity_error = function(e) {
    message("identity broken: ", conditionMessage(e))   # e$reason, e$detail
    stop(e)
  }
)
```

`e$reason` is a stable classification (the Python helper uses the same
vocabulary, and a cross-language conformance test pins them together):

| `reason` | Meaning |
|----------|---------|
| `no_token` | nothing to verify (`verify_token` only; `current_user` returns `NULL`) |
| `no_key` | `SHINYHUB_IDENTITY_KEY` unset or empty |
| `bad_key` | `SHINYHUB_IDENTITY_KEY` is not valid hex |
| `no_slug` | `SHINYHUB_APP_SLUG` unset or empty |
| `bad_signature` | signed with a different key |
| `expired` | past its `exp` (tokens live 5 minutes) |
| `wrong_audience` | minted for a different app slug |
| `wrong_issuer` | `iss` is not `shinyhub` |
| `malformed` | unparseable, or missing the required `exp` claim |

Letting the condition propagate is a reasonable default: the app is not going
to render anything trustworthy anyway.

## Local development

With no ShinyHub proxy in front there is no token, so `current_user` is always
`NULL`. Instead of writing a per-app mock, set `SHINYHUB_IDENTITY_DEV_USER`
(and optionally `SHINYHUB_IDENTITY_DEV_GROUPS` (comma-separated),
`SHINYHUB_IDENTITY_DEV_EMAIL`, `SHINYHUB_IDENTITY_DEV_NAME`,
`SHINYHUB_IDENTITY_DEV_ROLE`, default `viewer`); `current_user` then returns a
synthetic identity whose `claims` are marked `dev = TRUE`. This can never
activate under a real deployment: it only applies when no token arrived **and**
`SHINYHUB_IDENTITY_KEY` is absent, and ShinyHub always injects that key into
app processes.

## Compatibility

This helper is versioned independently of the ShinyHub server: its version
tracks changes to *this package's API*, not the server's release train. Any
release verifies tokens from any ShinyHub **v0.8.6 or later** (the release
that introduced identity forwarding); the token contract is stable across
server releases. Claims a later server added (`email`, `name`) are simply
`""` when an older server minted the token.

**Upgrading from 0.2:** a rejected token now raises a `shinyhub_identity_error`
condition instead of returning `NULL` with a warning, so code that treated
`NULL` as "not logged in" should either let the condition propagate or catch it
explicitly. The return value is now a normalized identity: read `user_id` and
`username` where you read `sub` and `preferred_username`, and reach for
`$claims` when you want a raw JWT claim.

## Why verify, not just read the plain headers?

ShinyHub forwards convenience plain headers and strips client-supplied ones, but
app processes listen on host-local ports, so a co-located process can bypass the
proxy and forge them. **Anything that gates access must verify the token** -
which is what this package does. See ShinyHub's `docs/identity.md` for the full
trust model.
