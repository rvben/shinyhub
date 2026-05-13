# Bundle manifest (`shinyhub.toml`)

A bundle may include a `shinyhub.toml` file at its root. The manifest is
optional — bundles without one deploy exactly as before — but when present it
is the canonical, declarative source of truth for the app's settings,
post-deploy hooks, and scheduled jobs.

Three sections are recognised: `[app]`, `[[hook]]`, and `[[schedule]]`. They
are independent; any combination (including none) is valid.

```toml
[app]
hibernate_timeout_minutes = 30
replicas = 2
max_sessions_per_replica = 10

[[hook]]
on = "post-deploy"
command = ["python", "scripts/migrate.py"]
timeout = "2m"

[[schedule]]
name = "nightly-refresh"
cron = "0 0 * * *"
cmd = "python helpers/fetch.py"
timeout_seconds = 600
```

## Strict-mode parsing

Unknown top-level keys, unknown fields inside any section, and unknown
trigger values all fail the deploy at parse time with HTTP 400. A typo in
`replicas` (e.g. `replcias`) does not silently no-op — the operator sees the
error immediately. This is deliberate: declarative configuration that
silently drops values is worse than no declarative configuration.

A malformed manifest aborts the deploy before the new bundle replaces the
running one; the previous deployment continues to serve traffic.

## When each section is applied

Deploy proceeds in this order:

1. The bundle is uploaded, validated, and unzipped into a fresh version
   directory.
2. **Phase A — `[app]` settings.** Applied atomically to the database after
   the previous process is stopped and the proxy is deregistered, but before
   the new bundle boots. A failure here aborts the deploy with 400 (validation)
   or 500 (DB error); the app row is left untouched.
3. The new bundle's dependencies are installed (uv / renv).
4. **`[[hook]]` blocks** run sequentially in the bundle directory.
5. The new app processes are started and the proxy is re-registered.
6. **Phase B — `[[schedule]]` blocks** upsert by name into the schedules
   table. The scheduler is reloaded so the new cron expressions take effect
   immediately.

Phase A failure aborts the deploy (the new bundle never starts) with HTTP
400 (validation) or 500 (DB error). Phase B failure returns HTTP 500 but
the new bundle is already durable and serving traffic — the schedule set
may be incomplete; the next deploy converges because the upsert is
idempotent.

Reloading the scheduler is a soft step: if the scheduler is not yet
started (e.g. during early-startup deploys), the reload is skipped and
the schedule rows are still written. The scheduler picks them up when it
starts.

## `[app]` — app-level settings

| Field | Type | Meaning |
|---|---|---|
| `hibernate_timeout_minutes` | int | Idle minutes before the watcher hibernates the app. `0` disables hibernation. `-1` resets the field to the server default (the same convention as `shinyhub apps set --hibernate-timeout -1`). |
| `replicas` | int ≥ 1 | Number of identical replica processes serving this app. See [scaling](scaling.md). |
| `max_sessions_per_replica` | int 0..1000 | Per-replica admission cap for new cookieless sessions. `0` means "use the runtime default". |

All three fields are optional. Omitted fields are left untouched — the
manifest does not assert a complete state, so existing values set via the
UI or CLI survive across deploys unless the manifest explicitly overrides
them.

Settings are applied in a single SQLite transaction. Shrinking `replicas`
removes the now-out-of-range rows from the `replicas` table in the same
transaction; no half-applied state is reachable.

### Sentinel: reset hibernate to default

TOML has no null literal, so the manifest uses `-1` to mean "remove this
app's override and fall back to the server default":

```toml
[app]
hibernate_timeout_minutes = -1
```

Equivalent to `shinyhub apps set --hibernate-timeout -1`.

## `[[schedule]]` — scheduled jobs

Each `[[schedule]]` block defines one cron-driven job. See
[schedules](schedules.md) for the full semantic model; the manifest
mirrors the CLI fields.

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | Unique key within the app. Used to identify the schedule across re-deploys (upsert by name). |
| `cron` | yes | Standard 5-field cron expression. |
| `cmd` | one of | Shell-quoted command. Parsed with shell-words. |
| `cmd_json` | one of | TOML string containing a JSON array of argv. Use this when shell quoting is awkward. |
| `timeout_seconds` | no | Wall-clock cap before SIGTERM. Defaults to 3600. |
| `overlap` | no | `skip` (default), `queue`, or `concurrent`. |
| `missed` | no | `skip` (default) or `run_once`. |
| `disabled` | no | When `true`, the schedule row exists but the runner skips ticks. |

Exactly one of `cmd` or `cmd_json` is required. Both empty or both set is
a parse error.

```toml
[[schedule]]
name = "build-cache"
cron = "*/15 * * * *"
cmd_json = '["python", "-m", "myapp.refresh", "--quiet"]'
timeout_seconds = 120
overlap = "skip"
```

### Upsert semantics

Schedules are matched by `(app_id, name)`. The first deploy with a given
name **creates** the schedule (audit: `schedule_create`); subsequent
deploys that include the same name **update** it in place, preserving its
ID and audit trail (audit: `schedule_update`).

Schedules **not** present in the manifest are left alone — removing a
`[[schedule]]` block does NOT delete the schedule from the database. Use
`shinyhub schedule delete` or the UI to remove a schedule. This avoids
silently dropping schedules that were created interactively while the
manifest was being authored.

## `[[hook]]` — deploy lifecycle hooks

| Field | Required | Meaning |
|---|---|---|
| `on` | yes | Trigger point. Only `post-deploy` is supported. |
| `command` | yes | argv to exec. First element is resolved against the bundle's PATH. |
| `timeout` | no | Wall-clock cap. Defaults to 5 minutes. Accepts Go duration syntax (`30s`, `2m`, `1h`). |

Hooks run sequentially in the order they appear in the manifest. The
first failing hook aborts the deploy — subsequent hooks do not run, and
the new bundle does not start.

Stdout and stderr are merged into the deploy log so the operator sees
exactly what the hook printed.

```toml
[[hook]]
on = "post-deploy"
command = ["python", "scripts/migrate.py"]
timeout = "2m"

[[hook]]
on = "post-deploy"
command = ["python", "scripts/seed.py"]
```

Hooks inherit the app's environment (including secrets injected via
`shinyhub env set`), but not `PORT` (which is per-replica and only set
when an app process starts).

## Idempotency

Re-deploying the same bundle yields the same state:

- `[app]` settings are deterministic — applying twice with the same values
  is a no-op aside from audit-event noise.
- `[[schedule]]` upserts by name — IDs are stable across deploys; cron or
  command changes update the row in place.
- `[[hook]]` blocks run every deploy; they are expected to be idempotent
  (e.g. `migrate.py` should handle "already migrated").

## Audit events

Manifest application emits the same audit events as the equivalent UI/API
actions:

| Action | Recorded when |
|---|---|
| `update_app` | Phase A changed at least one `[app]` field. |
| `schedule_create` | First time a `[[schedule]]` with this name is seen for this app. |
| `schedule_update` | Subsequent deploys that mention an existing schedule. |

Hook executions are logged into the deploy log but do not emit per-hook
audit events. The overall deploy is recorded as `app_deploy`.

## Worked example

A small app that runs a nightly fetch, has tight scaling, and applies a
schema migration on every deploy:

```toml
[app]
hibernate_timeout_minutes = 0
replicas = 2
max_sessions_per_replica = 20

[[hook]]
on = "post-deploy"
command = ["python", "scripts/migrate.py"]
timeout = "5m"

[[schedule]]
name = "nightly-fetch"
cron = "0 3 * * *"
cmd = "python helpers/fetch.py"
timeout_seconds = 900
overlap = "skip"
missed = "run_once"
```

Deploying this bundle:

1. Sets the app to never-hibernate, 2 replicas, cap 20 (Phase A, atomic).
2. Installs dependencies, runs `python scripts/migrate.py` (post-deploy hook).
3. Starts the two replicas behind the proxy.
4. Upserts the `nightly-fetch` schedule (Phase B); the scheduler reloads
   and the new cron takes effect immediately.

A second deploy with the same manifest produces no settings change (Phase
A is a no-op), re-runs the hook (migrations are expected to be
idempotent), and updates the schedule's `updated_at` timestamp without
changing its ID.
