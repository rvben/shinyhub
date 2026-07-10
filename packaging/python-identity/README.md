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

`current_user(headers)` returns an `Identity` or `None`. It works with any
header mapping - Shiny for Python's `session.http_conn.headers`, a
Starlette/Flask request's headers, or a plain `dict`.

`Identity` fields:

| Field | Type | Value |
|-------|------|-------|
| `user_id` | `str` | Decimal user ID |
| `username` | `str` | Username |
| `role` | `str` | Platform role: `viewer`, `developer`, `operator`, or `admin` |
| `groups` | `tuple[str, ...]` | Verified group names |
| `groups_truncated` | `bool` | `True` when the group list was capped at 100 |
| `email` | `str` | `""` unless the deployment's IdP asserts one |
| `name` | `str` | Display name; `""` unless the IdP asserts one |
| `claims` | `Mapping` | The raw verified JWT claims |

**Every failure is anonymous, never a crash.** No token, bad signature, wrong
audience/issuer, an expired token, or no ShinyHub in front at all (running the
app locally) all return `None` - no exception is raised - so your app stays
testable without SSO.

`current_user` reads `SHINYHUB_IDENTITY_KEY`/`SHINYHUB_APP_SLUG` from the
environment by default; pass `key=`/`slug=` explicitly for tests.

## Diagnostics: a present-but-rejected token warns

A genuine anonymous visitor sends no token at all, so a token that is
**present but fails verification** almost always means a misconfigured
deployment (missing or wrong `SHINYHUB_IDENTITY_KEY`, audience/issuer
mismatch, clock skew). That case emits a `WARNING` on the stdlib
`"shinyhub_identity"` logger - once per distinct reason per process - while
the return value stays `None` (fail-closed). A request without a token stays
completely silent, so anonymous traffic never spams your logs.

If your platform gates on identity, add a startup self-check so a broken
deployment fails fast instead of silently rendering everyone anonymous:

```python
import os

assert os.environ.get("SHINYHUB_IDENTITY_KEY"), "identity key not injected"
assert os.environ.get("SHINYHUB_APP_SLUG"), "app slug not injected"
```

## Local development

With no ShinyHub proxy in front there is no token, so `current_user` is always
`None`. Instead of writing a per-app mock, set:

```bash
export SHINYHUB_IDENTITY_DEV_USER=devlin
export SHINYHUB_IDENTITY_DEV_GROUPS="team-a, team-b"   # optional
export SHINYHUB_IDENTITY_DEV_EMAIL=devlin@example.com  # optional
export SHINYHUB_IDENTITY_DEV_NAME="Devlin Example"     # optional
export SHINYHUB_IDENTITY_DEV_ROLE=admin                # optional, default viewer
```

`current_user` then returns a synthetic `Identity` marked with
`claims == {"dev": True, ...}`. This can never activate under a real
deployment: it only applies when no token arrived **and**
`SHINYHUB_IDENTITY_KEY` is absent, and ShinyHub always injects that key into
app processes.

## Compatibility

This helper is versioned independently of the ShinyHub server: its version
tracks changes to *this package's API*, not the server's release train. Any
release verifies tokens from any ShinyHub **v0.8.6 or later** (the release
that introduced identity forwarding); the token contract is stable across
server releases. Claims a later server added (`email`, `name`) are simply
`""` when an older server minted the token.

## Why verify, not just read the plain headers?

ShinyHub also forwards convenience plain headers (`X-Shinyhub-User`, `-Role`,
`-Groups`, ...) and strips any client-supplied ones. But app processes listen on
host-local ports, so a co-located process can bypass the proxy and forge plain
headers. **Anything that gates access must verify the token** - which is exactly
what this package does. See ShinyHub's `docs/identity.md` for the full trust
model.
