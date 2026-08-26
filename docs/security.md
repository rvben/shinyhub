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
