# shinyhub-identity

Read the signed identity [ShinyHub](https://github.com/rvben/shinyhub) forwards
to your app, in one call. No per-app JWT plumbing.

ShinyHub injects a short-lived, per-app HS256 JWT
(`X-Shinyhub-Identity-Token`) into every request it proxies, and hands your app
its verification key via `SHINYHUB_IDENTITY_KEY` and `SHINYHUB_APP_SLUG`. This
package verifies that token and returns the identity.

```
pip install shinyhub-identity
# or: uv add shinyhub-identity
```

## Use it

```python
from shinyhub_identity import current_user

def server(input, output, session):
    user = current_user(session.http_conn.headers)   # None when anonymous
    if user is None:
        ...  # logged-out visitor
    elif "platform-admins" in user.groups:
        ...  # gate admin features on the VERIFIED groups
```

`current_user(headers)` returns an `Identity` (`user_id`, `username`, `role`,
`email`, `groups`, `groups_truncated`, and the raw `claims`) or `None`. `email`
is `""` unless the deployment's forward-auth SSO asserts one. It works with any
header mapping - Shiny for Python's `session.http_conn.headers`, a
Starlette/Flask request's headers, or a plain `dict`.

**Every failure is anonymous, never a crash.** No token, bad signature, wrong
audience/issuer, an expired token, or no ShinyHub in front at all (running the
app locally) all return `None`, so your app stays testable without SSO.

`current_user` reads `SHINYHUB_IDENTITY_KEY`/`SHINYHUB_APP_SLUG` from the
environment by default; pass `key=`/`slug=` explicitly for tests.

## Why verify, not just read the plain headers?

ShinyHub also forwards convenience plain headers (`X-Shinyhub-User`, `-Role`,
`-Groups`, ...) and strips any client-supplied ones. But app processes listen on
host-local ports, so a co-located process can bypass the proxy and forge plain
headers. **Anything that gates access must verify the token** - which is exactly
what this package does. See ShinyHub's `docs/identity.md` for the full trust
model.
