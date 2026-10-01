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

`init`, `validate-config`, `backup`, and `restore` use the same resolution order.

## Validate before restarting

Run the candidate binary against the candidate configuration before stopping the
running service:

```sh
shinyhub validate-config --config /etc/shinyhub/shinyhub.yaml
```

The command uses the same configuration loader, defaults, and environment
overrides as `serve`. Run it with the service's environment (including secrets),
working directory, and file permissions so it checks the effective deployment
configuration. It exits with status `0` on success and `1` on a configuration
error, reporting the validation error without dumping the configuration.
`--output json` is available for automation.

A file explicitly selected with `--config` or `SHINYHUB_CONFIG` must exist.
Without an explicit path, a missing `./shinyhub.yaml` permits environment-only
configuration. An existing default file is still loaded and validated.

Validation does not start listeners, open or migrate a database, launch apps,
or modify server state. It does not verify DNS, certificate coverage, SSO,
database connectivity, or runtime readiness. Continue to check `/readyz` after
starting the service.

If startup validation fails, ShinyHub exits before opening its HTTP listener.
A refused connection to `/readyz` therefore cannot show the configuration error;
read the process's stderr or service log. For a systemd service named
`shinyhub`, use `journalctl -u shinyhub -n 100 --no-pager`. Run preflight before
the deployment's stop/restart step; an `ExecStartPre` check alone is too late to
preserve the running instance during a restart.

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

**Without `app_origin`, every deployed app runs same-origin with the dashboard,
and ShinyHub trusts every deployed app with dashboard users' sessions.** App
JavaScript shares the dashboard's origin, so a malicious or compromised app can
attempt to read same-origin state, ride an admin's session for same-origin
requests, or otherwise act with the authority of whoever is viewing it. The
CSRF double-submit-cookie check and the `Referer`-based defense in
`internal/auth/csrf.go` reduce this risk but do not eliminate it - they are a
mitigation for the same-origin case, not a substitute for isolation. Set
`app_origin` whenever any deployed app's code is not fully trusted; a server
started without it logs a startup warning for exactly this reason.

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
  plain, and the finished page is compressed on its way to the visitor (see
  [Response compression](#response-compression)).

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

## Response compression

R and Python Shiny serve their pages and dependencies uncompressed. A typical
application page pulls over a megabyte of JavaScript and CSS (Bootstrap, jQuery,
`shiny.js`, widget libraries), which gzip shrinks about four-fold. ShinyHub
gzip-encodes responses for every browser that accepts it: application pages and
their assets, the dashboard, and the API.

```yaml
server:
  compression: false   # SHINYHUB_SERVER_COMPRESSION
```

On by default. What it compresses, and what it leaves alone:

- Text formats only: `text/*`, JavaScript, JSON, XML, SVG, WebAssembly, and
  uncompressed font formats. Images, `woff2`, archives and other binary formats
  are already compressed and pass through untouched.
- Bodies under 1 KiB, responses the application already encoded, partial
  (`206`) responses, `HEAD` requests, and `Cache-Control: no-transform`
  responses are sent as-is.
- WebSocket connections are not gzip-encoded; their messages are compressed
  separately (see [WebSocket compression](#websocket-compression)).
- Server-sent events (`text/event-stream`) are never compressed, so each event
  is delivered the moment it is written. Other streamed responses stay
  streamed: every flush from the application reaches the browser immediately.
- A compressed response turns a strong `ETag` into a weak one, because the
  encoded bytes differ from the original. Conditional requests keep working,
  so a returning visitor still gets `304 Not Modified` for unchanged assets.
- Every response whose encoding depends on the browser carries
  `Vary: Accept-Encoding`, so a shared cache never serves gzip to a client that
  did not ask for it.

Behind a reverse proxy that compresses as well, leave it on: the proxy sees
`Content-Encoding` and passes the response through rather than compressing it
twice. Turn it off only if you would rather not spend ShinyHub's CPU on it.

## WebSocket compression

A Shiny session sends every rendered output over its WebSocket: a table of a
few thousand rows is hundreds of kilobytes of JSON per update. R Shiny's server
(httpuv) never compresses WebSocket messages, so on a slow or metered link each
update travels at full size. ShinyHub compresses them on the application's
behalf.

```yaml
server:
  websocket_compression: false   # SHINYHUB_SERVER_WEBSOCKET_COMPRESSION
```

On by default. How it works:

- When the browser offers `permessage-deflate` (every current browser does) and
  the application answers the upgrade without negotiating any extension,
  ShinyHub accepts the offer itself. The application keeps sending and
  receiving plain messages; ShinyHub compresses what it sends and decompresses
  what the browser sends.
- An application that negotiates compression itself, such as Python Shiny, is
  left alone and its WebSocket bytes are relayed untouched.
- Messages under 256 bytes, messages that do not get smaller, fragmented
  messages, and messages over 16 MiB are sent uncompressed. Each message is
  compressed on its own, so ShinyHub keeps no compressor state per connection
  between messages.
- A compressed message from the browser may be at most 16 MiB compressed and
  64 MiB decompressed; a larger one closes the connection with code `1009`.

Set it to `false` to relay every WebSocket byte-for-byte.

## Result cache

Each app gets a disk directory its replicas, workers, and scheduled jobs share
for computed results, and R apps use it for `bindCache` automatically. See
[Result cache](result-cache.md).

```yaml
storage:
  app_cache_dir: /var/lib/shinyhub/app-cache   # SHINYHUB_APP_CACHE_DIR
  app_cache_max_mb: 1024                       # SHINYHUB_APP_CACHE_MAX_MB; 0 turns the cache off
```

Unset, `app_cache_dir` is an `app-cache` directory beside `app_data_dir`, so
the minimal server above caches in `/var/lib/shinyhub/app-cache` with no extra
key. The cache is disposable: leave it out of backups.

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
comes from the environment on the shared-user native runtime, startup logs a warning.

This narrows one exposure; it is not a tenant boundary. See
[isolation.md](isolation.md) for why the native runtime should not host
mutually-untrusting tenants.

The opt-in Linux [native app-user backend](native-user-isolation.md) separates
app identities from the controller. Its setting is
`runtime.native.broker_socket` (`SHINYHUB_RUNTIME_NATIVE_BROKER_SOCKET`); the
same-UID limitations above apply when that setting is empty.
It remains opt-in for this release, including on new Linux installations.
Upgrades keep the existing backend until explicitly configured. A configured
broker that cannot be used stops startup; it never selects the shared-UID backend
as a fallback. See the [default and migration policy](native-user-isolation.md#default-and-migration-policy).

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

## Browser session lifetimes

Browser sessions have a renewable validity window and a strict maximum age:

```yaml
auth:
  session_ttl: 1h       # SHINYHUB_AUTH_SESSION_TTL
  session_max_age: 12h  # SHINYHUB_AUTH_SESSION_MAX_AGE
```

`session_ttl` is the time a signed browser token remains valid between
renewals. Visible dashboards and hosted app pages renew it every third of that
window, up to once every five minutes. Hosted pages use an app-local endpoint
that remains available when the app switcher is disabled or apps run on an
isolated origin. Background tabs pause renewal and verify their session when
they return. An expired session cannot be renewed.

`session_max_age` is measured from the original login and never slides.
Renewed JWTs and cookies expire at that deadline, even if their normal TTL
would extend beyond it. Signing in again reruns SSO group reconciliation.

Both settings accept durations from `1m` to `720h` (30 days), and
`session_ttl` must not exceed `session_max_age`. Zero and negative values are
rejected; neither limit can be disabled. Environment variables override YAML.
The defaults are one hour and twelve hours. Choose shorter limits for shared
devices or privileged deployments; longer limits reduce sign-in frequency but
delay mandatory SSO reauthentication.

These settings apply to local-password, GitHub, Google, and OIDC browser
sessions. They do not change API/CLI credential expiry, support-session
deadlines, or upstream forward-auth policy. On an isolated app origin, app
activity renews that origin's cookie; it does not renew the separate dashboard
cookie. Both retain the original login time and logout identity. Existing
authenticated app WebSockets and streaming responses end at the maximum login
age even without another browser request. Apps whose HTML or CSP prevents
script injection or same-origin requests cannot run automatic renewal; their
signed cookies still expire and the server still enforces the maximum age. See
[sessions and logout](native-oidc.md#sessions-cookies-and-logout) for
background-tab, WebSocket, and identity-provider behavior.

## Fleet run and development session retention

Two more `maintenance:` settings bound history unrelated to application logs,
pruned on the same schedule:

```yaml
maintenance:
  fleet_run_retention_count: 20              # SHINYHUB_FLEET_RUN_RETENTION_COUNT
  development_session_retention_days: 90     # SHINYHUB_DEVELOPMENT_SESSION_RETENTION_DAYS
  interval: 1h
```

`fleet_run_retention_count` keeps this many newest terminal runs per fleet and
deletes older ones. The default (`0`) keeps every run. A run still recorded as
an app's latest or last-successful `fleet apply` is kept regardless of its
rank, since fleet status reads those two rows directly; a run still in
progress is never removed either.

`development_session_retention_days` deletes ended, non-ephemeral development
sessions older than this many days. The default (`0`) keeps every session.
Ephemeral sessions are never affected by this setting: they are removed with
their app, not by this sweep.

Both settings share `app_log_run_retention_count`'s convention: `0` is the
default and means keep everything; `-1` also means keep everything, accepted
explicitly for an operator who copies the sibling setting's `-1` and expects
the same result; any other negative value is rejected at startup rather than
silently treated as "keep all", so a typo like `-3` fails loudly instead of
quietly disabling pruning.

Pruning happens at `maintenance.interval` (and once at startup), same as the
application log run pruning above.

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
