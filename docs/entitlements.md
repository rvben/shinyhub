---
description: "Assign app-specific business permissions to users and IdP groups, with audit records and live-session revocation."
---

# App entitlements

Entitlements describe permissions **inside one app**, such as `power_user` or
`report_export`. ShinyHub stores their definitions and assignments in its existing
database and forwards the effective names in a signed `entitlements` claim.
Python exposes `user.entitlements`; R exposes `user$entitlements`.

They are independent of app access, app management, global platform roles, and
IdP groups. Granting `power_user` does not admit someone to a private app, let
them deploy it, or make them a platform administrator. Platform admins and app
owners do not automatically receive business entitlements. The app checks its
verified entitlements before performing protected operations.

## Define and assign

An app owner, app manager, or authorized platform operator/admin can manage
entitlements. Service credentials must have a management role and include the
app in their scope, following the existing management authorization rules.
This includes developer-role service credentials scoped to the app.

```sh
shinyhub apps entitlements define finance power_user \
  --description "Run advanced financial reports"
shinyhub apps entitlements grant finance power_user analyst
shinyhub apps entitlements group-grant finance power_user finance-team
shinyhub apps entitlements effective finance analyst
shinyhub apps entitlements list finance
shinyhub apps entitlements grants finance
```

Repeating `define` without `--description` preserves an existing description.
Pass `--description ""` explicitly to clear it.

Usernames resolve to existing ShinyHub user IDs. Email and arbitrary external
account names are not identity keys. For SSO users, have them sign in once before
assigning a direct grant. App IDs scope grants so deleting and recreating an app
under the same slug never transfers its entitlements.

An app may define up to 64 names. Each name is 1–64 lowercase ASCII letters,
digits, underscores, or hyphens, starting with a letter. Descriptions are at most
512 UTF-8 bytes. Names must be defined before they can be granted. Entitlements
are not silently truncated in tokens.

## Effective grants and revocation

User and group grants are **additive**. The effective set is their sorted,
deduplicated union. The `effective` command reports each contributing source.
Deleting one source does not deny another source's grant:

```sh
shinyhub apps entitlements revoke finance power_user analyst
shinyhub apps entitlements effective finance analyst
# If finance-team still grants power_user, it remains effective.
shinyhub apps entitlements group-revoke finance power_user finance-team
shinyhub apps entitlements delete finance power_user
```

Deleting a definition requires removing all its grants first. Repeating an
already-applied grant or revocation is a no-op. Every real definition, grant, or
revocation change is committed with its audit record in one transaction. Audit
failure rolls back the change. Events use `entitlement.define`,
`entitlement.delete`, `entitlement.grant`, and `entitlement.revoke` in the existing
audit log and include the app, actor, target/source, and entitlement.

These are role labels, not a hierarchy or deny-policy engine. If an app's tiers
are mutually exclusive, define and test its conflict behavior before migration.
Do not assume `power_user` overrides `enterprise_user`. Direct grants do not
expire automatically; app owners remain responsible for reviews and removals
when people change roles. Account lifecycle and upstream SSO-session invalidation
remain separate responsibilities.

## Propagation and open sessions

Each authenticated admitted HTTP request resolves entitlements from the live
database in one query, without the identity provider's groups cache. A lookup
failure returns HTTP 503 instead of issuing an incomplete identity. Anonymous
requests receive no entitlements.

Shiny sessions retain the identity verified at their WebSocket handshake.
Every instance compares its live connections' effective entitlement sets during
the session sweep. A change closes affected connections; the browser reconnects
through the access gate with a fresh identity. This normally happens within
`server.session_recheck_interval` (default 30 seconds), plus query/sweep time.
Both grants and revocations can cause reconnection and loss of unsaved session
state. A redundant grant that leaves the effective set unchanged does not.

When general session rechecking is disabled, an entitlement-only sweep still
runs every 30 seconds. If the database cannot verify a connection's nonempty
entitlement set, that connection is closed. Connections that held no business
entitlements retain the existing availability behavior on lookup errors.

Group-derived grants use ShinyHub's stored IdP group snapshot. Native OIDC
refreshes that snapshot at login; forward authentication reconciles incoming
groups on requests. The sweep does not query AD or the IdP. An upstream group
change must first reach ShinyHub before the entitlement change is detectable.

Previously issued identity tokens remain cryptographically valid until their
expiry (normally five minutes). Session closure does not undo completed actions
or cancel work already accepted by an app. Apps needing stricter action-time
authorization must enforce it themselves.

## Use in an app

```python
from shinyhub_identity.shiny import session_identity

def server(input, output, session):
    user = session_identity(session)
    can_export = user is not None and "report_export" in user.entitlements
    # Check can_export in the server-side export handler before accessing data.
```

```r
user <- shinyhubidentity::current_user(session)
can_export <- !is.null(user) && "report_export" %in% user$entitlements
```

Use identity helper version 0.5.0 or later. Tokens from older servers yield an empty
entitlement set. `groups`, `role`, and `app_role` retain their existing meanings.
There is no entitlement convenience header: use the verified signed claim.

For tests, Python's `mint_token`/`fake_session` and R's `shinyhub_test_token` accept
`entitlements`. For local development set
`SHINYHUB_IDENTITY_DEV_ENTITLEMENTS=power_user,report_export` with the existing dev
identity variables. Dev identity remains inactive whenever a real identity key
is configured.

## HTTP API

All endpoints require management access to the named app:

| Method and path under `/api/apps/{slug}` | Body or query | Result |
| --- | --- | --- |
| `GET /entitlements` | Standard pagination | Entitlement definitions |
| `PUT /entitlements/{name}` | `{}` or `{"description":"…"}` | Define; preserve omitted description, update explicit description; 204 |
| `DELETE /entitlements/{name}` | None | Delete unused definition; 204 |
| `POST /entitlements/{name}/grants` | Exactly one of `user_id`, `username`, `group` | Grant; 204 |
| `DELETE /entitlements/{name}/grants` | Same principal body | Revoke that source; 204 |
| `GET /entitlement-grants` | Standard pagination | All assignments with sources |
| `GET /entitlements/effective` | Exactly one of `user_id` or `username` | `user_id`, `entitlements`, `sources` |

## Migrating app-local lists

Define the permissions the app actually checks, resolve users to existing
accounts, and inspect the proposed effective sets including group-derived
grants. Update the app to check `entitlements` and remove its CSV override path
in the same deployment. Do not leave both active after migration. Subsequent
assignment changes require no app deployment.
