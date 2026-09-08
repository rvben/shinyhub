---
description: "The authentication, authorization, and isolation model for self-hosted installs where platform operators control who may deploy code."
---

# Security

ShinyHub is designed for self-hosted environments where platform operators
control who may deploy code. Its security model combines authentication,
per-application authorization, encrypted secrets, bounded resources, audit
history, and optional process or container isolation.

## Recommended deployment posture

- Terminate TLS at a maintained reverse proxy or load balancer.
- Bind the ShinyHub listener to a private interface or loopback.
- Use separate control-plane and application origins.
- Keep applications private unless anonymous access is intentional.
- Use Docker, remote workers, Fargate, or private Scaleway Serverless Containers
  when application authors should not share the control-plane host boundary.
- Apply CPU, memory, session, replica, bundle, and data quotas.
- Leave `server.session_recheck_interval` enabled so revoking a user also closes
  the app sessions they already have open, not only their next request. See
  [WebSocket and session binding](identity.md#websocket-and-session-binding).
- Store `auth.secret`, OAuth credentials, deploy tokens, and database passwords
  in a secrets manager or owner-readable environment file.
- Enable metrics, structured logs, and retention appropriate to the installation.
- Back up the database, bundles, and persistent app-data directory together.

## Deployment identities

Interactive people and non-interactive automation are separate principal
types. Human admins can still deploy apps and fleets with their own account.
CI should use the built-in **Deployment automation** service account, with one
credential per team or pipeline so each has an independent role, app allowlist,
expiry, last-used timestamp, and revocation path.

For service credentials, an app allowlist is both an explicit grant to those
apps (including human-owned private apps) and a hard ceiling on app-specific
operations. It does not scope global platform administration. A scoped admin
can still manage people and server settings, while project-catalog writes and
the global audit log require an unrestricted operator/admin credential. Use
Developer for ordinary deployment automation and issue Admin only when a
pipeline intentionally manages the platform.

The legacy `SHINYHUB_DEPLOY_TOKEN` remains supported as a configuration-managed
credential on that account. ShinyHub stores its hash, refuses interactive login
for the compatibility username `__deploy__`, and fails startup rather than
silently taking over a human account with that name. Its credential label is
reserved, case-insensitively, so team-managed credentials need a descriptive
name such as `analytics production CI`. If the configured raw value is reused
from an existing API credential, ShinyHub atomically adopts it as the managed
credential so one bearer secret never represents two principals. Fleet runs are bound to
the exact credential that registered them, so another credential on the same
service account cannot take over the run lifecycle.

## Who can open an app

Each app carries one of three visibility levels, and the rule that decides
`/app/<slug>/` is written in exactly one place in the server, so a live
WebSocket session is governed by the same rule its first request was.

| Visibility | Who gets in |
| --- | --- |
| `public` | Anyone, signed in or not. |
| `shared` | Any signed-in user. |
| `private` | The owner, users added as viewer or manager, and every operator and admin. |

The last clause is the one worth stating plainly: **`private` does not hide an
app from operators and admins.** Those two roles bypass the membership check
entirely, for every private app on the server. They are also treated as managers
of every app, which is what `GET /api/apps`, the logs, the environment variable
list, and the persistent data directory are gated on. Secret env values stay
masked in the API for everyone, but their names do not.

This is deliberate - an operator has to be able to restart, inspect, and unbreak
anything running on the host - but it means `private` reads as "not visible to
other ordinary users", not "not visible to anyone else". If an app holds data
that platform operators should not see, the boundary you need is a separate
ShinyHub, not a visibility level.

A service credential behaves like its role for this purpose, except that an app
allowlist on it is a hard ceiling: a scoped admin credential sees only the apps
in its allowlist, whatever its role would otherwise permit. The allowlist is
checked before role and visibility, so an out-of-scope app stays invisible even
when it is public.

## Which authorization scheme to send

ShinyHub accepts two kinds of credential on the API, and each one has its own
`Authorization` scheme. The scheme is not interchangeable: a credential sent
under the other one is rejected with `401`, whatever else is right about it.

| Credential | Where it comes from | Header |
| --- | --- | --- |
| API credential, deploy token | `shinyhub tokens create`, `SHINYHUB_TOKEN`, `SHINYHUB_DEPLOY_TOKEN` | `Authorization: Token <value>` |
| Session token | `POST /api/auth/login` | `Authorization: Bearer <jwt>` |

`Token` is the one you want for scripts and CI. Bearer is the more familiar
convention, so reaching for it is the common mistake; ShinyHub answers that
particular `401` with a `WWW-Authenticate` header naming the scheme to use, so
`curl -i` tells you which half is wrong:

```text
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Token realm="shinyhub", error="invalid_request", error_description="credential sent as \"Bearer\" must use the \"Token\" authorization scheme"
```

That header appears only for a scheme mismatch, which is decided from the shape
of the string you sent. A credential that is expired, revoked, or simply wrong
gets a plain `401` under either scheme, so the header never reveals whether a
credential exists.

The CLI picks the scheme for you; this only matters when you call the API
yourself.

## What an anonymous caller can see

A ShinyHub with no reverse-proxy authentication in front of it answers a few
requests before anyone signs in. This is the complete list, and each entry is
deliberate.

`GET /api/server-info` reports the running version, the API protocol version,
the capability flags, and which app runtimes the host can start. The CLI reads
this before it has a credential, to tell a healthy server from a front proxy
answering for one that is still starting, and to refuse a call the server is too
old to serve. The build commit is **not** in that answer: it is more precise than
the version tag, and the only thing that precision buys an anonymous caller is an
exact revision to compare against a list of security fixes. Sign in and the same
endpoint includes it.

`GET /app/<slug>/` distinguishes an app that exists from one that does not:
a private app answers `401` (a sign-in page for a browser navigation,
`{"error":"unauthorized"}` otherwise), and an unknown slug answers `404` with
`{"error":"unknown app","slug":"..."}`. So an anonymous party who guesses a slug
learns whether it is in use. Neither answer names the app, describes it, or
reveals anything about its configuration or data. The distinction is kept
because both halves are load-bearing: following a link to a private app has to
reach a sign-in form, and external monitoring has to be able to tell "this app is
gone" from "this app is starting" without a credential.

Treat app slugs as public, then. If a slug would itself disclose something (a
client name, an unannounced product), give the app a neutral slug and put the
real name in its display name, which is only shown to people who may open it.
Installations where even the set of slugs is sensitive should authenticate at the
reverse proxy, so that nothing reaches ShinyHub unauthenticated.

## Ending sessions

Three actions end a signed-in session, and they differ in scope on purpose.

| Action | Scope | Who |
| --- | --- | --- |
| `POST /api/auth/logout` | The one credential that made the call | Anyone |
| `POST /api/auth/revoke-sessions` | Every session and bearer token for your own account | Anyone |
| `POST /api/users/{id}/revoke-sessions` | Every session and bearer token for another account | Admin |

Logout is narrow by design: signing out of a borrowed machine should not sign
you out of your own laptop. When the question is instead "someone else may have
one of my credentials", use `revoke-sessions`, which bumps the account's token
epoch and so invalidates every token issued before that moment. Changing your
own password does the same thing as a side effect.

API credentials are not sessions and none of these three affect them. They are
named, listed, and revoked one at a time under `/api/tokens` (`shinyhub tokens`),
so revoking one does not interrupt an unrelated pipeline.

## Reading the audit log

Every mutating action is recorded. **Audit Log** in the dashboard, and
`GET /api/audit` behind it, accept four narrowing parameters:

| Parameter | Meaning |
| --- | --- |
| `action` | One action type, exactly (`deploy`, `env.set`, …) |
| `since` | `YYYY-MM-DD`, inclusive, UTC |
| `until` | `YYYY-MM-DD`, inclusive of the whole named day, UTC |
| `run` / `event` | A specific fleet run or a single event, for deep links |

`action`, `since`, and `until` combine, so "every `env.set` during the incident
window" is one request:

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "$SHINYHUB/api/audit?action=env.set&since=2026-09-01&until=2026-09-06"
```

Dates name UTC days, matching how the log stamps and renders `created_at`. Each
end is optional, so a single `since` reads as "everything from here on". A
malformed or inverted range is rejected with a 400 rather than ignored: silently
dropping a bound would return events from outside the window the caller asked
for, which reads as those events being inside it.

`run` and `event` are deep links into one context and replace the other filters
rather than narrowing further.

## Report a vulnerability

Do not open a public issue for a suspected vulnerability. Follow the private
reporting instructions and supported-version policy in
[`SECURITY.md`](https://github.com/rvben/shinyhub/blob/main/SECURITY.md).

## Related guides

- [Isolation](isolation.md)
- [Identity forwarding](identity.md)
- [Native OIDC](native-oidc.md)
- [Secret rotation](secret-rotation.md)
- [Reverse proxy configuration](reverse-proxy/deploying-behind-a-proxy.md)
- [Scaleway Serverless Containers](deployment/scaleway-serverless.md)
