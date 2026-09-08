---
description: "Every server setting ShinyHub reads from YAML, the environment variable that overrides each one, and the default applied when both are absent."
---

# Configuration

ShinyHub reads server configuration from YAML and allows environment variables
to override individual values. The complete annotated reference is
[`shinyhub.yaml.example`](https://github.com/rvben/shinyhub/blob/main/shinyhub.yaml.example).

## Configuration file resolution

The server checks, in order:

1. `shinyhub serve --config /path/to/shinyhub.yaml`
2. `SHINYHUB_CONFIG`
3. `./shinyhub.yaml`

`init`, `backup`, and `restore` use the same resolution order.

## Minimal server

```yaml
database:
  driver: sqlite
  dsn: /var/lib/shinyhub/shinyhub.db

server:
  host: 127.0.0.1
  port: 8080
  base_url: https://hub.example.com

auth:
  secret: replace-with-at-least-32-random-characters

storage:
  apps_dir: /var/lib/shinyhub/apps
  app_data_dir: /var/lib/shinyhub/app-data
```

Generate `auth.secret` once and preserve it across restarts:

```bash
openssl rand -hex 32
```

The secret signs sessions and encrypts application secrets at rest. Follow the
[secret rotation procedure](secret-rotation.md) instead of replacing it
directly on a running installation.

## Schema migrations on startup

The server applies any pending migrations when it starts. Before it changes the
schema it copies the SQLite database aside, so an upgrade that turns out bad can
be rolled back by restoring the old binary and that file:

```
INFO pre-migration snapshot written
     path=/var/lib/shinyhub/shinyhub.db.pre-migration-v58-20260819T091223Z.sqlite
     pending_migrations=1 retention=5
```

The snapshot is written only when migrations are actually pending, so ordinary
restarts and same-version reloads write nothing. It is a complete, self-contained
database (no `-wal`/`-shm` sidecars) taken with `VACUUM INTO`, safe to run while
the server is live.

After a new snapshot is written, older ones beyond
`database.pre_migration_snapshot_retention` are pruned automatically, so an
instance upgraded regularly does not accumulate one full-size copy of the
database per upgrade forever. The newest snapshot is always kept regardless of
the retention count: it is the only route back to the build that ran before
the last upgrade, and a snapshot cannot be regenerated afterwards since it is
a copy of a schema version the current binary no longer writes.

```yaml
database:
  pre_migration_snapshot: false        # SHINYHUB_DB_PRE_MIGRATION_SNAPSHOT
  pre_migration_snapshot_retention: 5  # SHINYHUB_DB_PRE_MIGRATION_SNAPSHOT_RETENTION
```

Turn it off when an external backup already covers the upgrade, or when the
database is too large to copy inside the service start timeout. While it is on,
a snapshot that cannot be written **aborts startup**: migrating a database the
operator cannot get back is worse than not starting.

Postgres deployments log a warning instead, because `VACUUM INTO` is SQLite-only.
Take a `pg_dump` before an upgrade that carries migrations.

### Downgrades exit 7

A database migrated by a newer build is never served by an older one: the schema
carries columns this code cannot read. The server refuses to start and exits
with code `7` (`schema_incompatible`), naming the two versions and what to do:

```
database schema is newer than this binary: database is at schema version 59,
this binary supports up to 58. Downgrade is not supported and this will not
succeed on retry.
```

The condition is permanent, so the packaged systemd unit sets
`RestartPreventExitStatus=7`. Without it the unit restart-loops and reports
`activating (auto-restart)`, which most monitoring reads as healthy while the
service is in fact down.

### Getting back to the older build

The fastest resolution is to start the newer build again. To stay on the older
one, restore state it can read. There are two artifacts, and they are restored
differently:

A **pre-migration snapshot** is a bare database file, so it is moved back into
place rather than fed to a command:

```bash
systemctl stop shinyhub
mv /var/lib/shinyhub/shinyhub.db.pre-migration-v58-20260819T091223Z.sqlite \
   /var/lib/shinyhub/shinyhub.db
rm -f /var/lib/shinyhub/shinyhub.db-wal /var/lib/shinyhub/shinyhub.db-shm
systemctl start shinyhub
```

Delete the `-wal` and `-shm` sidecars as shown. They belong to the *migrated*
database, and SQLite replays them over whatever file it finds at that path: leave
them and the rows you just rolled back come straight back, with no error and no
warning. The snapshot itself needs no sidecars, being a self-contained
`VACUUM INTO` copy.

A **backup archive** from `shinyhub backup` is a `.tar.gz` containing the
database plus the apps and app-data trees, and is restored with the command:

```bash
systemctl stop shinyhub
shinyhub restore /var/backups/shinyhub-20260819.tar.gz
systemctl start shinyhub
```

The two are not interchangeable: handing a snapshot to `shinyhub restore` is
rejected, with the move-it-into-place instructions above. Note the difference in
scope, which matters when choosing between them - a snapshot rolls back the
database alone, so any deploy that landed after it was taken stays on disk while
the database no longer knows about it. An archive rolls back all three together.

The `systemctl stop` is not advisory. Restoring into a live server renames the
database out from under an open connection. `shinyhub restore` refuses to run
while it can see a server: a running server publishes `<database>-running.json`
beside its database file, so the refusal works from the database path alone,
which is the one setting a restore cannot get wrong. `server.pid_file` naming a
live process and anything listening on `server.host`/`server.port` are also
treated as a running server. A marker left by a crash names a dead process and
is ignored. `--force` skips the check, for a server you have confirmed stopped
by other means.

## Dedicated application origin

Production deployments should serve application traffic from an origin that is
different from the dashboard and API:

```yaml
server:
  base_url: https://hub.example.com
  app_origin: https://apps.example.com
```

Route both HTTPS hostnames to the same ShinyHub listener. The application origin
exposes app proxy traffic and health checks only; it does not expose the
dashboard, static control-plane assets, or `/api` routes. This prevents
application JavaScript from sharing an origin with control-plane cookies.

The isolated origin also unlocks optional administrator support sessions. See
[Support sessions](support-sessions.md) for the opt-in setting and security
model.

## Application status overlay

When an application's WebSocket drops mid-session, Shiny shows a grey
"disconnected" box. It cannot say more than that: the application process is the
one party that does not know whether it was hibernated, redeployed, or killed.
ShinyHub does know, so its proxy appends a small script to application HTML page
loads that explains the disconnect and offers a reload.

```yaml
server:
  status_overlay: false   # SHINYHUB_SERVER_STATUS_OVERLAY
```

On by default. What it does, and what it deliberately does not do:

- It appends one `<script>` before `</body>` on top-level HTML navigations only.
  Sub-resources, XHR, JSON, WebSocket upgrades, redirects, and error responses
  are never touched.
- It runs after the application's own bootstrap and replaces no global. It
  watches for the `#shiny-disconnected-overlay` element that R and Python Shiny
  both add, and does nothing in an application that never adds one.
- On a disconnect it polls the application's readiness endpoint and reports
  whether the app is back, gone, or still down. Polling is read only: it never
  reaches the application and never wakes it.
- If the application sets a `Content-Security-Policy`, ShinyHub adds the
  overlay's `sha256` script hash to it and nothing else. A policy that forbids
  scripts outright (`'none'`) is honoured by skipping the injection, never by
  relaxing the policy.
- Page loads are fetched from the backend uncompressed so the script can be
  spliced in. The backend hop still negotiates gzip; only the in-process view is
  plain.

Set it to `false` for byte-for-byte untouched application responses. No response
body is rewritten while it is off.

## Application switcher

An open application fills the browser tab, and nothing in it leads anywhere
else: a visitor who reaches one application has no way to get to another, or
back to the dashboard, short of editing the URL. ShinyHub injects a switcher
into application pages that lists the applications this visitor can open,
grouped by project.

```yaml
server:
  app_nav: false   # SHINYHUB_SERVER_APP_NAV
```

On by default. What it does, and what it deliberately does not do:

- It appends one `<script>` before `</body>`, on the same top-level HTML
  navigations the status overlay uses and under the same rules: sub-resources,
  XHR, JSON, WebSocket upgrades, redirects, and error responses are never
  touched. When both are enabled they share a single pass over the response.
- It renders inside a closed shadow root, so the application's own CSS and the
  switcher's cannot reach each other.
- It lists only applications the caller is already authorized to see, resolved
  per request against the same rules as the dashboard. An anonymous visitor
  sees the public ones. It names no application the caller could not have
  listed for themselves.
- A visitor can dismiss it for the tab; it comes back on reload.
- It is also added to the pages ShinyHub serves in the application's place -
  starting, deploying, at capacity, stopped, crashed, awaiting a first deploy,
  and the two access-denied pages - which are the surfaces where a visitor is
  most stuck.
- If the application sets a `Content-Security-Policy`, ShinyHub adds the
  switcher's `sha256` script hash to it and nothing else. A policy that forbids
  scripts outright (`'none'`) is honoured by skipping the injection, never by
  relaxing the policy.

Set it to `false` to leave application pages without it. Off is absent rather
than idle: no page is rewritten, and the endpoint the switcher reads its list
from is not served at all.

## Environment overrides

Most configuration keys have an environment variable, named
`SHINYHUB_<UPPER_SNAKE_CASE>`. For example:

```bash
export SHINYHUB_BASE_URL=https://hub.example.com
export SHINYHUB_APP_ORIGIN=https://apps.example.com
export SHINYHUB_AUTH_SECRET="$(openssl rand -hex 32)"
export SHINYHUB_RUNTIME_DOCKER_DEFAULT_MEMORY_MB=512
```

Prefer environment variables or a secrets manager for credentials such as OAuth
client secrets and database passwords.

### The names are a fixed list, and a name that is not on it is only warned about

The variables are read by name from an explicit list, not derived from the YAML
key at runtime, so the mapping is not always mechanical: `server.base_url` is
`SHINYHUB_BASE_URL` rather than `SHINYHUB_SERVER_BASE_URL`, while the listener
is `SHINYHUB_SERVER_HOST` and `SHINYHUB_SERVER_PORT`. Names are written next to
their key in
[`shinyhub.yaml.example`](https://github.com/rvben/shinyhub/blob/main/shinyhub.yaml.example)
and on the page that documents the setting; take the name from there rather than
deriving it from the key. That file does not carry every name, so a key shown
there without one may still have a variable, `server.host` and `server.port`
being two such keys.

A `SHINYHUB_*` variable that is not on the list changes nothing: the server
still comes up healthy on the value the variable was meant to replace. It does
say so, though. Startup logs one warning per unrecognized name, with the name
ShinyHub thinks you meant when one is close enough:

```
WARN ignoring unrecognized environment variable; nothing reads it
     name=SHINYHUB_PORT did_you_mean=SHINYHUB_SERVER_PORT
WARN ignoring unrecognized environment variable; nothing reads it
     name=SHINYHUB_TOTALLY_MADE_UP
```

`SHINYHUB_PORT` is the classic one: it is not a name ShinyHub reads (the port
is `SHINYHUB_SERVER_PORT`), so setting it leaves the server on port 8080. It
warns rather than refuses to boot, because a shared environment may legitimately
carry a `SHINYHUB_` variable meant for something else, so the warning is yours
to read.

The startup log states the address that actually took effect, which is the
cheapest way to confirm an override landed:

```
INFO listening version=0.15.7 addr=127.0.0.1:8080
```

### Log level and format

Two variables have no configuration-file equivalent, because logging is wired
before the config file is read:

```bash
export SHINYHUB_LOG_LEVEL=debug   # debug | info (default) | warn | error
export SHINYHUB_LOG_FORMAT=json   # json | text
```

The format defaults to `text` when stdout is a terminal and `json` otherwise,
so a service-managed server logs JSON without being told to.

### auth.secret is the exception: prefer a file

A process environment is readable by any process running as the same user
(`/proc/<pid>/environ` on Linux, `ps eww` on macOS), and on the native runtime
every deployed app is such a process. `auth.secret` signs every session token
and derives the key encrypting every app's secret env vars, so it is the one
credential worth taking out of the environment:

```bash
install -m 600 /dev/null /etc/shinyhub/auth.secret
openssl rand -hex 32 > /etc/shinyhub/auth.secret
export SHINYHUB_AUTH_SECRET_FILE=/etc/shinyhub/auth.secret
unset SHINYHUB_AUTH_SECRET
```

Equivalently, `auth.secret_file` in the config file. The server reads it once at
startup, trims surrounding whitespace, and refuses a file that is group- or
world-readable. Setting both the file and `SHINYHUB_AUTH_SECRET` to different
values is an error, since one of them would silently win. While the secret still
comes from the environment on the native runtime, startup logs a warning.

This narrows one exposure; it is not a tenant boundary. See
[isolation.md](isolation.md) for why the native runtime should not host
mutually-untrusting tenants.

## Application logs and how long they are kept

An application's stdout and stderr are written to one file per replica run,
under `<apps_dir>/<slug>/logs/replica-<index>-<run-id>.log`. A run is one start
of one replica: every restart, redeploy, hibernate-and-wake, and watchdog
recovery opens a new file rather than appending to the previous one, so a run's
output is never mixed with another's.

Two separate limits apply, and only the second is configurable.

**Within a run.** The file is capped at 5 MiB. On reaching the cap it is renamed
to `<file>.1` and a fresh file is opened, and only one such backup is kept, so a
single very chatty run retains its last 10 MiB or so and loses the rest. This
cap is a constant with no configuration key: an application that logs a request
per line at volume will lose its startup output.

**Across runs.** Maintenance keeps the newest runs per app replica slot and
deletes the rest, database rows and files together:

```yaml
maintenance:
  app_log_run_retention_count: 20   # SHINYHUB_APP_LOG_RUN_RETENTION_COUNT
  interval: 1h
```

The default is 20 runs per replica slot. `-1` keeps every run, which is the
setting to reach for when you want a long forensic window on a quiet
application; `0` selects the default rather than deleting everything. Pruning
happens at `maintenance.interval` (and once at startup), and only completed runs
are eligible, so the run currently serving is never removed. On an HA data plane
the same setting governs the shared chunks as well; see
[HA data plane](deployment/ha-data-plane.md).

Disk cost is bounded by the product of these numbers: at the defaults, at most
about 200 MiB of logs per replica slot for an application that fills every file,
and far less in practice, since most runs never approach the cap.

## Host capacity

The Overview measures fleet CPU and memory against each application's enforced
per-replica limits. When no application carries one - the common case on a
single-purpose host - it measures against the box itself instead, so the panel
reports what the server is actually using rather than declining to answer.

The size of the box is detected at startup and needs no configuration. Override
it only when the detected number is wrong for your deployment:

```yaml
server:
  host_capacity_cores: 0        # 0 = detect
  host_capacity_memory_mb: 0    # 0 = detect
```

Env vars: `SHINYHUB_HOST_CAPACITY_CORES`, `SHINYHUB_HOST_CAPACITY_MEMORY_MB`.

The startup log records what was found and which source supplied it:

```
INFO host capacity cores=4 cores_source=cgroup-quota memory_mb=8192 memory_source=cgroup-limit
```

Sources for `cores_source` are `config` (you set `host_capacity_cores`),
`cgroup-quota` (a container CPU limit binds below the host's core count), and
`affinity` (the cores this process may run on). For `memory_source` they are
`config`, `cgroup-limit` (a container memory limit binds below the host total),
and `host-total` (what the OS reports for the machine).

Detection can fail - a platform with no cgroup files and no readable total.
When it does, that capacity is reported as unknown rather than as a host with
none: the Overview then shows CPU and memory in use with no percentage and no
meter, since a zero denominator would draw a full bar at any load. The startup
log warns when memory specifically could not be read.

Two cases are worth an explicit override. Under `runtime.mode: docker` the
detected figures describe the shared box, not any one worker's container; those
applications normally carry enforced limits and are measured against those
instead. And on a host ShinyHub shares with other services, the detected total
is the whole machine, so set these keys to the share you intend ShinyHub to
have if you want the panel scaled to that.

These keys are separate from `server.render_capacity_cores`
([Render pacing](scaling.md#render-pacing)), which is a pacing budget rather
than a reporting scale, although both are detected the same way.

## Scale-to-zero application tier

ShinyHub can retain each application replica as a private Scaleway Serverless
Container while Scaleway scales its underlying instance to zero:

```yaml
runtime:
  tiers:
    - name: serverless
      runtime: scaleway_serverless
  scaleway:
    region: nl-ams
    project_id: 00000000-0000-0000-0000-000000000000
    namespace_id: 00000000-0000-0000-0000-000000000000
    image: rg.nl-ams.scw.cloud/shinyhub/runner:latest
    control_plane_url: https://hub.example.com
    default_memory_mb: 512
    default_mvcpu: 250
```

Set `SCW_ACCESS_KEY` and `SCW_SECRET_KEY` in the server environment, never in
YAML. Standard `SCW_DEFAULT_PROJECT_ID` and `SCW_DEFAULT_REGION` variables are
accepted too. See the [Scaleway Serverless deployment guide](deployment/scaleway-serverless.md)
for the runner image, security boundary, lifecycle, and real-provider tests.

## Client configuration is different

For client commands such as `deploy`, `apps`, and `fleet`, `SHINYHUB_CONFIG`
selects the local credentials file, not the server YAML. Automation can avoid a
credentials file by setting `SHINYHUB_HOST` and `SHINYHUB_TOKEN`.
