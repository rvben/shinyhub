---
description: "Give administrators a short-lived, app-scoped way to reproduce a viewer's experience without granting broad impersonation access."
---

# Support sessions

Support sessions let a human administrator troubleshoot one application as a
viewer or developer. They deliberately do not provide GitLab-style global
impersonation: the capability cannot reach the dashboard, API, another app,
an operator or administrator account, or a service account.

The feature is off by default. It requires a dedicated application origin:

```yaml
server:
  base_url: https://hub.example.com
  app_origin: https://apps.example.com

auth:
  support_sessions: true
```

`SHINYHUB_SUPPORT_SESSIONS=true` is the environment equivalent. ShinyHub
refuses to start when support sessions are enabled without `server.app_origin`.
This boundary keeps untrusted application JavaScript away from the
administrator's control-plane cookie.
Host validation uses browser-equivalent IDNA, IP, port, case, and root-dot
canonicalization; ambiguous numeric IP spellings are rejected.
The live WebSocket recheck interval must also remain enabled at 30 seconds or
less; the default is 30 seconds. This bounds explicit-stop and revocation
propagation, while the 15-minute deadline is enforced by a separate connection
timer.

## Administrator flow

On **Identity → People**, choose **Support session** beside a viewer or
developer. Select an app and enter a reason or ticket reference. Starting the
session redirects to the application origin and establishes a separate cookie
scoped to `/app/<slug>/` for at most 15 minutes.

An amber banner remains at the top of every supported app page. It names both
the represented user and administrator, counts down the remaining time, warns
that app actions can change data, and provides **End support session**. Ending
the session revokes its JWT and closes its open WebSockets on the normal
session-recheck sweep. Its app-scoped cookie is cleared immediately; the root
fallback guard remains until the original deadline so a delayed or cross-app
stop request cannot restore an administrator identity on the app origin. The
rail is mounted outside the app's body and repairs
itself after normal single-page-app body rewrites or accidental removal.
Only one live support session is permitted per administrator; end it before
starting another.

## Security properties

- Only a live human administrator can start a session. Native login must be no
  more than 10 minutes old; forward-auth deployments delegate freshness to the
  upstream identity gateway. API keys do not qualify.
- Only human `viewer` and `developer` accounts can be represented. The target
  must already be allowed to open the selected app.
- The launch URL is a single-use random capability accepted for 60 seconds.
  Only its SHA-256 hash is stored. An unactivated launch is aborted on handled
  failures and reaped after a 90-second activation grace.
- The support JWT is nonrenewable, bound to one immutable app identity (not
  merely its reusable slug), rejected by `/api`, and
  carried in a separate HttpOnly, SameSite cookie. The administrator's
  control-origin session is never replaced; any ordinary app-origin identity
  is cleared before the support cookie and cross-app guard are installed.
  Every routed backend is stamped with that immutable app ID. A clustered
  instance fences all old backends before publishing an app ID replacement,
  and both HTTP and WebSocket paths reject any backend-ID mismatch.
- Both actor and subject are live-resolved on every request. Target deletion,
  role or session-epoch changes (including viewer-to-developer expansion),
  administrator deletion/demotion/session
  revocation, explicit stop, and the hard deadline all fail closed. Upgraded
  WebSockets have an independent connection deadline, so expiry does not
  depend on the periodic session-recheck sweep.
- The start event records actor, subject, app, reason, source IP, and the
  deterministic deadline in the same transaction as session creation.
  Explicit, aborted, and lazily reaped stop transitions record their cause and
  deadline transactionally as well (system cleanup carries no client IP). A
  natural deadline fails closed immediately; if no later operation materializes
  that expiry as a stop transition, its terminal time remains established by
  the start audit. Support traffic is excluded from end-user usage analytics.
- Operational support-session rows are pruned after 30 days when new sessions
  are created. The audit log remains the durable record and follows the
  deployment's audit retention policy.
- ShinyHub cannot make arbitrary application behavior read-only. The banner and
  confirmation say this plainly; app-side writes occur with the represented
  user's own permissions.

Application JavaScript runs in the same document as the injected rail. The
self-healing mount protects against ordinary framework rewrites, not code that
is intentionally written to fight or counterfeit platform UI. Treat deployed
application code as trusted for operator-facing presentation; the separate app
origin remains the hard boundary protecting control-plane credentials and APIs.
The response includes a plain, usable rail before enhancement, so the identity
warning and native POST end form remain present when JavaScript is disabled or
`connect-src` denies scripted requests. Policies that block the required
script or same-origin form submission cause the app response to be withheld
behind ShinyHub's own fail-closed pause page.

## Application identity

Identity forwarding keeps the represented user as the JWT subject and exposes
the administrator separately in the `act` claim. Apps that write their own
audit records should preserve both. See [Identity Forwarding](identity.md).
