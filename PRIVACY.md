# Privacy and data handling

ShinyHub is self-hosted: it runs entirely on infrastructure you operate, and it
makes no outbound calls to any vendor-controlled endpoint at runtime (no
telemetry or analytics). You are the data controller for everything it stores.
This document inventories the personal data ShinyHub holds so you can complete a
DPIA / records-of-processing assessment.

## Personal data inventory

| Data | Where | Source | Purpose |
|------|-------|--------|---------|
| Username | `users.username` | Local signup / OAuth / OIDC / forward-auth | Authentication, display |
| Display name | `users.display_name` | OAuth/OIDC claim or forward-auth `Remote-Name` | UI display |
| Email address | `users.email`, OAuth accounts | OAuth/OIDC claim | Account linking, display; forwarded to apps as `X-Shinyhub-Email` when identity headers are enabled |
| Password hash | `users.password_hash` | Local accounts only | Authentication (bcrypt, cost 12; never the plaintext) |
| Group memberships | `user_groups` | OIDC `groups` claim / forward-auth | Role and per-app access mapping |
| IP address | `audit_events.ip_address` | Request client IP | Security audit trail |
| Action history | `audit_events` | Every mutating action | Accountability / compliance audit trail |
| API tokens | `api_keys` (hashed) | User-created | Programmatic auth (stored hashed, shown once) |
| App data | per-app data dir / object store | Uploaded by app authors/users | App runtime data (opaque to ShinyHub) |

App-uploaded data (files, databases an app writes to its data dir) is opaque to
ShinyHub and may contain personal data depending on the app; treat each hosted
app as its own processing activity.

## Data at rest and in transit

- **At rest:** app environment secrets and the worker CA private key are
  encrypted (AES-256-GCM with HKDF-derived keys). Other data (usernames, emails,
  audit rows) is stored in the SQLite/Postgres database in plaintext; protect the
  database file/host with OS-level controls and disk encryption as your policy
  requires.
- **In transit:** ShinyHub terminates no TLS itself; run it behind a reverse
  proxy that terminates HTTPS (see `docs/reverse-proxy/`). Set
  `server.trusted_proxies` so client IPs are attributed correctly.

## Retention and deletion

- **Audit events:** retained indefinitely by default. Set
  `maintenance.audit_retention_days` to prune older rows.
- **User deletion:** deleting a user removes the account but sets
  `audit_events.user_id` to NULL (`ON DELETE SET NULL`), anonymizing the audit
  trail rather than erasing history - a deliberate balance between the right to
  erasure and audit integrity. Document this in your erasure procedure.
- **App data:** removed when the app is deleted (subject to the durable-data
  guard for apps whose data lives on an external store).

## Export

- Audit events are queryable via `GET /api/audit` (admin, paginated) for
  export to a SIEM or compliance archive.
- The full datastore is captured by `shinyhub backup` for portability.

## Vulnerability reporting

Security issues: see [SECURITY.md](SECURITY.md).
