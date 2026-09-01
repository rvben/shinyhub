---
description: "How ShinyHub counts app sessions, minimizes viewer identity, excludes support traffic, and retains raw and aggregate usage history."
---

# Usage analytics

ShinyHub records durable per-app usage so app managers can answer how often an
app was opened and when it was last used. Collection is privacy-minimized by
default: session totals are kept without a stable viewer identifier.

## What counts as an open

One session starts after the app's WebSocket connection successfully upgrades
through the proxy and ends when that connection closes. Static files,
loading-page polls, readiness probes, and failed launches do not count. A reload
or reconnect starts a new session.

The dashboard reports sessions, peak concurrent sessions, average connected
time, active sessions, last-opened time, daily opens, and a
person/anonymous/service-account audience mix. Peak concurrency counts
overlapping live connections—not distinct people—so it is available in every
identity mode without retaining another identifier. Unique viewers are shown
only when every person row in the selected window
uses the same retained identity scheme. A prospective mode upgrade can therefore
leave unique viewers unavailable for a mixed-history window even though newer
sessions retain identity. Reporting windows use UTC calendar days.

This is operational usage, not in-app product analytics. Instrument explicit
workflow events inside the app when needed and apply your organization's consent
and disclosure rules to that separate processing.

## Identity modes

The hub policy is a ceiling. An app may inherit it or choose a stricter setting,
but can never retain more identity than the hub permits.

| Mode | Stored for a person session | Available reporting |
|---|---|---|
| `unattributed` (default) | No account ID or stable viewer key | Sessions, duration, last open, audience type |
| `pseudonymous` | App-scoped HMAC viewer key; never returned by the API | The above plus exact unique viewers while raw rows remain |
| `identified` | User account reference | The above plus administrator-only viewer and recent-session detail |
| App override `disabled` | Nothing new | Previously retained totals remain until retention expires |

Pseudonymous data is still personal data: an installation can relate the key to
an account while processing a session. The mode is minimization, not legal
anonymization. Keys are deliberately app-scoped so the same person cannot be
correlated across apps from the stored values.

Audience classification is separate from identity. Anonymous visitors and
service accounts are counted independently and are never treated as people for
unique-viewer reporting. Usage analytics do not store IP addresses, user agents,
device identifiers, or browser fingerprints.

## Access

| Role | Usage access |
|---|---|
| App owner or app manager | Metrics supported by the effective privacy mode |
| Platform administrator | The same, plus named details only in `identified` mode |
| App viewer | No Usage tab or API access |
| Service account | No identity detail; classified separately from people |

Every response is `Cache-Control: no-store` and declares its capabilities. A
missing unique-viewer figure means it was not collected or is no longer exact;
it is never rendered as zero. Deleting a user clears its foreign-key reference.
Deleting an app cascades its raw and daily usage history.

## Configuration and retention

```yaml
usage:
  enabled: true
  identity_mode: unattributed
  raw_retention_days: 30
  aggregate_retention_days: 365
```

| YAML field | Environment variable | Default |
|---|---|---|
| `enabled` | `SHINYHUB_USAGE_ENABLED` | `true` |
| `identity_mode` | `SHINYHUB_USAGE_IDENTITY_MODE` | `unattributed` |
| `raw_retention_days` | `SHINYHUB_USAGE_RAW_RETENTION_DAYS` | `30` |
| `aggregate_retention_days` | `SHINYHUB_USAGE_AGGREGATE_RETENTION_DAYS` | `365` |

`0` means unlimited retention and should be an explicit policy decision.
Negative values are rejected. When both periods are finite, aggregate retention
must be at least as long as raw retention.

Completed raw sessions that cross the raw cutoff are merged into daily,
non-identifying totals, including the maximum overlapping session count for
each UTC day, and deleted in the same database transaction. Peak concurrency is
calculated from ShinyHub's own retained connection intervals; Prometheus is not
queried and remains an optional one-way export. Reports stream intervals in
start order and retain only active end times, so peak-calculation memory grows
with actual concurrency rather than total opens in the retention window. Each
historical app/day peak is finalized once before its first raw batch is deleted,
preventing repeated full-day scans on busy installations. A genuinely
live session is retained even if it crosses the cutoff; an abandoned open row
whose heartbeat has been stale for 90 seconds is finalized at its last observed
heartbeat before rollup. Daily totals are then removed at the aggregate cutoff.
Windows containing person-session rollups, deleted-user rows, or identity modes
from both sides of a prospective upgrade cannot claim an exact distinct-viewer
count. The session totals remain available; uniqueness is reported as unknown,
never as zero or as a partial count.

Disabling collection stops new rows but does not erase retained totals. Cleanup
continues on the elected owner while collection is paused.

## Per-app override

App managers can use **Configuration → Usage privacy**, or declare the setting
with the app bundle:

```toml
[app]
usage_identity_mode = "pseudonymous" # disabled | unattributed | pseudonymous | identified
```

Removing the bundle key restores inheritance. The override and a new policy
generation commit atomically. Queued starts are clamped to the stricter of the
policy captured when their connection opened and the newly committed policy;
retained raw identity is then scrubbed before a successful response. That
downgrade is irreversible. Raising the mode later affects only connections that
open afterward and never reconstructs names or pseudonyms. Startup repeats the
scrub for every stricter app override, repairing an interrupted transition.
Secret rotation re-encrypts the stable pseudonym master atomically with app
secrets and the worker CA key.

## Reliability semantics

After each successful WebSocket upgrade, policy resolution uses an in-memory
snapshot: no database query is added to connection establishment. The snapshot
is fully refreshed every 30 seconds so HA policy changes and slug reuse converge
quickly. Every queued insert independently clamps its identity against the
current durable hub and app policy in the same SQL statement. A stale cache can
therefore temporarily undercount after collection is loosened, but it cannot
persist identity above the committed privacy ceiling. Session persistence uses
a bounded in-process queue with a short overflow buffer, and transient starts
retry up to three times. Active rows heartbeat every 30 seconds, and an
abandoned row stops appearing active after 90 seconds.

`shinyhub_usage_persistence_events_total{result=...}` exposes overflow, retry,
permanent-failure, dropped-start, and policy-refresh outcomes. Alert on
`start_failed` and `start_dropped`, which indicate undercounting; investigate a
sustained `policy_refresh_failed`. In HA, each control-plane writes the
connections it proxies into the shared Postgres database.
