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

`current_user(session)` returns the verified JWT claims
(`preferred_username`, `role`, `email`, `name`, `groups`, `groups_truncated`,
`sub`, ...) or `NULL` (`email`/`name` are present only when the deployment's
IdP asserts them). Every failure -
no token, bad signature, wrong audience/issuer, expired, or no ShinyHub in front
(running locally) - returns `NULL` rather than erroring, so your app stays
testable without SSO.

A token that is **present but rejected** additionally raises an R `warning()`
(once per distinct reason per session), because that almost always means a
misconfigured deployment - missing or wrong `SHINYHUB_IDENTITY_KEY`,
audience/issuer mismatch, clock skew - rather than an anonymous visitor. A
request without a token stays silent.

Key and slug default to the `SHINYHUB_IDENTITY_KEY`/`SHINYHUB_APP_SLUG`
environment variables ShinyHub injects; pass `key=`/`slug=` explicitly for
tests.

## Local development

With no ShinyHub proxy in front there is no token, so `current_user` is always
`NULL`. Instead of writing a per-app mock, set `SHINYHUB_IDENTITY_DEV_USER`
(and optionally `SHINYHUB_IDENTITY_DEV_GROUPS` (comma-separated),
`SHINYHUB_IDENTITY_DEV_EMAIL`, `SHINYHUB_IDENTITY_DEV_NAME`,
`SHINYHUB_IDENTITY_DEV_ROLE`, default `viewer`); `current_user` then returns a
synthetic claims list marked `dev = TRUE`. This can never activate under a
real deployment: it only applies when no token arrived **and**
`SHINYHUB_IDENTITY_KEY` is absent, and ShinyHub always injects that key into
app processes.

## Compatibility

This helper is versioned independently of the ShinyHub server: its version
tracks changes to *this package's API*, not the server's release train. Any
release verifies tokens from any ShinyHub **v0.8.6 or later** (the release
that introduced identity forwarding); the token contract is stable across
server releases. Claims a later server added (`email`, `name`) are simply
absent when an older server minted the token.

## Why verify, not just read the plain headers?

ShinyHub forwards convenience plain headers and strips client-supplied ones, but
app processes listen on host-local ports, so a co-located process can bypass the
proxy and forge them. **Anything that gates access must verify the token** -
which is what this package does. See ShinyHub's `docs/identity.md` for the full
trust model.
