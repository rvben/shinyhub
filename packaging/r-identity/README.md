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
(`preferred_username`, `role`, `groups`, `sub`, ...) or `NULL`. Every failure -
no token, bad signature, wrong audience/issuer, expired, or no ShinyHub in front
(running locally) - returns `NULL` rather than erroring, so your app stays
testable without SSO.

Key and slug default to the `SHINYHUB_IDENTITY_KEY`/`SHINYHUB_APP_SLUG`
environment variables ShinyHub injects; pass `key=`/`slug=` explicitly for
tests.

## Why verify, not just read the plain headers?

ShinyHub forwards convenience plain headers and strips client-supplied ones, but
app processes listen on host-local ports, so a co-located process can bypass the
proxy and forge them. **Anything that gates access must verify the token** -
which is what this package does. See ShinyHub's `docs/identity.md` for the full
trust model.
