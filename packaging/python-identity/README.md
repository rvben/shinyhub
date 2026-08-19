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
from shinyhub_identity.shiny import session_identity

def server(input, output, session):
    user = session_identity(session)   # None when anonymous
    if user is None:
        ...  # logged-out visitor
    elif "platform-admins" in user.groups:
        ...  # gate admin features on the VERIFIED groups
```

`session_identity(session)` verifies the session's handshake token once and
returns that answer for the session's life. That is a correctness property, not
a cache: ShinyHub binds identity at the WebSocket handshake, and the token it
forwarded there expires five minutes later, so re-verifying those headers from
a reactive starts failing part-way through a long session even though nothing
about the user changed.

Outside Shiny (Streamlit, Dash, FastAPI, ...) use the framework-free primitive,
which takes any header mapping - a Starlette/Flask request's headers, or a
plain `dict` - and verifies per request:

```python
from shinyhub_identity import current_user

user = current_user(request.headers)   # None when anonymous
```

Both return an `Identity` or `None`. `Identity` fields:

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

Key and slug default to the `SHINYHUB_IDENTITY_KEY`/`SHINYHUB_APP_SLUG`
environment variables ShinyHub injects; pass `key=`/`slug=` explicitly for
tests.

## `None` means anonymous, and nothing else

A genuine anonymous visitor sends **no token at all**, so that is the only case
that returns `None`. A token that is present but fails verification is a broken
deployment - missing or wrong `SHINYHUB_IDENTITY_KEY`, audience or issuer
mismatch, an expired token, clock skew - and raises `IdentityError` instead. An
app that renders that as "logged out" hides the outage behind an empty
dashboard, which is exactly what this contract prevents.

```python
from shinyhub_identity import IdentityError

try:
    user = session_identity(session)
except IdentityError as e:
    log.error("identity broken: %s", e)     # e.reason, e.detail
    raise
```

`IdentityError.reason` is a stable classification (the R helper uses the same
vocabulary, and a cross-language conformance test pins them together):

| `reason` | Meaning |
|----------|---------|
| `no_token` | nothing to verify (`verify_token` only; `current_user` returns `None`) |
| `no_key` | `SHINYHUB_IDENTITY_KEY` unset or empty |
| `bad_key` | `SHINYHUB_IDENTITY_KEY` is not valid hex |
| `no_slug` | `SHINYHUB_APP_SLUG` unset or empty |
| `bad_signature` | signed with a different key |
| `expired` | past its `exp` (tokens live 5 minutes) |
| `wrong_audience` | minted for a different app slug |
| `wrong_issuer` | `iss` is not `shinyhub` |
| `malformed` | unparseable, or missing the required `exp` claim |

`IdentityError.detail` carries the human-readable specifics for a log line.
Letting the error propagate is a reasonable default: the app is not going to
render anything trustworthy anyway.

## Local development

With no ShinyHub proxy in front there is no token, so the identity is always
`None`. Instead of writing a per-app mock, set:

```bash
export SHINYHUB_IDENTITY_DEV_USER=devlin
export SHINYHUB_IDENTITY_DEV_GROUPS="team-a, team-b"   # optional
export SHINYHUB_IDENTITY_DEV_EMAIL=devlin@example.com  # optional
export SHINYHUB_IDENTITY_DEV_NAME="Devlin Example"     # optional
export SHINYHUB_IDENTITY_DEV_ROLE=admin                # optional, default viewer
```

Both helpers then return a synthetic `Identity` marked with
`claims == {"dev": True, ...}`. This can never activate under a real
deployment: it only applies when no token arrived **and**
`SHINYHUB_IDENTITY_KEY` is absent, and ShinyHub always injects that key into
app processes.

## Compatibility

Requires Python 3.10+.

This helper is versioned independently of the ShinyHub server: its version
tracks changes to *this package's API*, not the server's release train. Any
release verifies tokens from any ShinyHub **v0.8.6 or later** (the release
that introduced identity forwarding); the token contract is stable across
server releases. Claims a later server added (`email`, `name`) are simply
`""` when an older server minted the token.

**Upgrading from 0.2:** a rejected token now raises `IdentityError` instead of
returning `None` with a warning, so code that treated `None` as "not logged in"
should either let the error propagate or catch it explicitly. `Identity` is
keyword-only now, and Shiny apps should call `session_identity(session)` rather
than `current_user(session.http_conn.headers)`.

## Why verify, not just read the plain headers?

ShinyHub also forwards convenience plain headers (`X-Shinyhub-User`, `-Role`,
`-Groups`, ...) and strips any client-supplied ones. But app processes listen on
host-local ports, so a co-located process can bypass the proxy and forge plain
headers. **Anything that gates access must verify the token** - which is exactly
what this package does. See ShinyHub's `docs/identity.md` for the full trust
model.
