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
     pending_migrations=1 note="never pruned automatically"
```

The snapshot is written only when migrations are actually pending, so ordinary
restarts and same-version reloads write nothing. It is a complete, self-contained
database (no `-wal`/`-shm` sidecars) taken with `VACUUM INTO`, safe to run while
the server is live.

Snapshots are **never deleted automatically**. Removing old ones is an explicit
operator decision, so a disk-space policy is yours to set.

```yaml
database:
  pre_migration_snapshot: false   # SHINYHUB_DB_PRE_MIGRATION_SNAPSHOT
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

Configuration keys generally map to `SHINYHUB_<UPPER_SNAKE_CASE>` variables.
For example:

```bash
export SHINYHUB_BASE_URL=https://hub.example.com
export SHINYHUB_APP_ORIGIN=https://apps.example.com
export SHINYHUB_AUTH_SECRET="$(openssl rand -hex 32)"
export SHINYHUB_RUNTIME_DOCKER_DEFAULT_MEMORY_MB=512
```

Prefer environment variables or a secrets manager for credentials such as OAuth
client secrets and database passwords.

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
selects the local credentials file—not the server YAML. Automation can avoid a
credentials file by setting `SHINYHUB_HOST` and `SHINYHUB_TOKEN`.
