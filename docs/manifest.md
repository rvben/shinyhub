---
description: "An optional shinyhub.toml at a bundle root that sets the launch command, dependencies, post-deploy hooks, and per-app configuration."
---

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
7. **Phase C - `[access]` group rules** reconcile into the per-app group
   access table as `source = manifest`, preserving any manually-managed
   rules. Unlike schedules, this is declarative: a group removed from the
   manifest loses its manifest rule on the next deploy.

Phase A failure aborts the deploy (the new bundle never starts) with HTTP
400 (validation) or 500 (DB error). Phase B failure returns HTTP 500 but
the new bundle is already durable and serving traffic — the schedule set
may be incomplete; the next deploy converges because the upsert is
idempotent. Phase C failure likewise returns HTTP 500 with the bundle
already live; re-deploying re-runs the reconcile.

Reloading the scheduler is a soft step: if the scheduler is not yet
started (e.g. during early-startup deploys), the reload is skipped and
the schedule rows are still written. The scheduler picks them up when it
starts.

## `[app]` — app-level settings

| Field | Type | Meaning |
|---|---|---|
| `name` | string 1..128 | Friendly display name shown on the dashboard card, the detail heading, and the launchpad tile. See [`[app] name` and `[app] description`](#app-name-and-app-description) below. |
| `description` | string 0..280 | One-line description shown under the name. `""` clears it. See [`[app] name` and `[app] description`](#app-name-and-app-description) below. |
| `project` | string | Project slug grouping this app on the dashboard. Must match `[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?`; `""` ungroups. The project row is created automatically. Only the slug is settable here: a project's name, description and icon belong to the server (`shinyhub projects set`) or a fleet manifest's `[[project]]` block, because they describe a namespace shared by apps from many bundles. |
| `hibernate_timeout_minutes` | int | Idle minutes before the watcher hibernates the app. `-1` inherits the global server default, `0` disables hibernation forever, and a positive value sets that many minutes (the same convention as `shinyhub apps set --hibernate-timeout`). |
| `replicas` | int ≥ 1 | Desired number of identical replica processes serving this app. Runtime surfaces report the actual `replicas_running` separately. See [scaling](scaling.md). |
| `max_sessions_per_replica` | int 0..1000 | Per-replica admission cap for new cookieless sessions. `0` means "use the runtime default". |
| `render_seconds` | float 0..600 | CPU cost of one page render, used to pace admission so a burst queues on a wait page instead of stalling every session. `0` disables pacing. Applied live on deploy (no restart). See [Render pacing](scaling.md#render-pacing). |
| `min_warm_replicas` | int 0..1000 | Minimum number of replica processes kept running when the app idles. Workers live inside a replica; this setting counts outer app processes, not `[app.worker]` workers. `0` (default) allows full hibernation. When set above `0`, the watcher stops only enough replicas to reach this floor so the first post-idle request hits a warm process. If the stored `replicas` value is less than `min_warm_replicas`, the floor self-clamps to `replicas`. Absent key leaves the stored value unchanged (same declared-only semantics as `replicas`). Inert under `[app.worker] isolation = "grouped"` or `"per_session"` (elastic pools boot workers on demand and report `idle` with none running); the server accepts it and reports a manifest warning; use `[app.worker] warm_spares` there. See [Pre-warming](scaling.md#pre-warming). |
| `command` | array of strings | Launch-command override. See [`[app] command`](#app-command) below. |
| `startup_timeout_seconds` | int 1..3600 | Readiness deadline for deploy, wake, scale, rollback, and `shinyhub run`; default 120 seconds. Read at boot and not stored in the database. |
| `build_timeout_seconds` | int 30..7200 | Host-side uv/renv dependency-build deadline; default 900 seconds. Read at build time and inert for Docker runtimes. |
| `readiness_path` | absolute HTTP path | Path polled before a process becomes routable; default `/`. Queries and fragments are rejected. See [Readiness](#app-readiness). |
| `readiness_status` | int 100..599 | Require this exact response status. When omitted, any 2xx or 3xx response is healthy. See [Readiness](#app-readiness). |
| `identity_headers` | bool | Per-app identity-forwarding toggle. See [`[app] identity_headers`](#app-identity_headers) below. |
| `autoscale` | inline table | Per-app session-saturation autoscale policy. See [`[app] autoscale`](#app-autoscale) below. |
| `worker` | table | Elastic worker-isolation policy, including the warm-spare floor. See [`[app.worker]`](#appworker) below. |
| `icon` | string | Single emoji app icon. See [`[app] icon`](#app-icon) below. |

All fields are optional. Omitted database-backed fields are left untouched:
the manifest does not assert a complete stored state, so existing values set
via the UI or CLI survive unless the manifest explicitly overrides them.
Omitted boot-time fields use their documented platform defaults.

This bundle `shinyhub.toml` is the per-deploy layer. A [fleet
manifest](fleet.md) sits above it: when an app is fleet-managed, a key
declared in the fleet manifest's `[app.config]` is reconciled on every apply
and wins over the value set here. The full order is fleet manifest > bundle
`shinyhub.toml` > server default; see [Config precedence](fleet.md#config-precedence).

Settings are applied in a single SQLite transaction. Shrinking `replicas`
removes the now-out-of-range rows from the `replicas` table in the same
transaction; no half-applied state is reachable.

The command, startup/build timeouts, and readiness fields are boot-time
settings rather than database state. They travel with each bundle and are
re-read for every process start, including local runs and rollbacks.

### `[app]` readiness

By default, ShinyHub polls `GET /` without following redirects and accepts a
2xx or 3xx response. Apps whose root route is not a reliable health signal can
declare a dedicated path and, when useful, one exact status:

```toml
[app]
readiness_path = "/health/ready"
readiness_status = 204
startup_timeout_seconds = 180
```

The contract is shared by deployed processes and `shinyhub run`, preventing a
local smoke test from passing under looser rules than production. Avoid checks
that depend on authentication or external services unless those dependencies
must genuinely block the app from receiving traffic.

### `[app] command`

Override the platform's automatic launch-command inference. When set, the
platform exec's this command directly (no shell) instead of detecting the app
type and building its own invocation.

```toml
[app]
command = ["uv", "run", "streamlit", "run", "app.py",
           "--server.port", "{port}", "--server.address", "{host}"]
```

#### Placeholders

The command is a template. Three tokens are substituted per replica at boot:

| Token | Substituted with |
|---|---|
| `{port}` | The replica's assigned TCP port. Each replica gets its own port. |
| `{host}` | The bind address the platform expects: `127.0.0.1` under the native runtime, `0.0.0.0` inside Docker containers. Never hardcode an address; use this placeholder so the command works correctly under both runtimes. |
| `{data_dir}` | The persistent data directory relative to the app's working directory. Resolves to `data` (a symlink the platform provisions). Use this instead of a hardcoded path to stay portable across app slugs and host layouts. |

The placeholder grammar is exactly `{lowercase_word}` (regex `\{[a-z_]+\}`).
Anything else that contains braces (`${VAR}`, `{1..5}`, `{Key:`) is passed
through unchanged. There is no escaping mechanism: a literal lowercase
`{word}` argument cannot be expressed in a command template.

Validation runs at deploy time and again at boot (which covers rollbacks to
bundles that were deployed before stricter rules). An unknown token such as
`{prot}` (likely a typo for `{port}`) fails the deploy with an error naming
the offending token rather than passing a silent mistyping through.

#### Semantics

- **Type detection is skipped.** A bundle with neither `app.py` nor `app.R`
  becomes deployable once `command` is set.
- **Dependency sync is skipped.** The platform does not run `uv sync` or
  `renv::restore`. To install Python dependencies, include a `uv run` prefix
  with a `requirements.txt` (e.g. `uv run --with-requirements requirements.txt
  python app.py ...`) or manage dependencies in your own entrypoint.
- **Tracing auto-instrumentation is skipped.** The `[tracing] auto` flag and
  the fleet default have no effect on command-mode apps. Add the
  `opentelemetry-instrument` wrapper explicitly in your command if you want
  instrumentation.
- **Health check is shared.** The platform uses the same 2xx/3xx default and
  optional `readiness_path` / `readiness_status` contract as inferred apps.
- **The command versions with the bundle.** Rolling back to an earlier
  deployment boots the command that was in that deployment's `shinyhub.toml`.
- **Commands are exec'd without a shell.** No environment-variable expansion
  happens in the command array. Use placeholders for the values the platform
  controls (`{port}`, `{host}`, `{data_dir}`); use `shinyhub env set` for
  app-level env vars.
- **An unparseable manifest at boot is fatal.** The platform does not fall
  back to type detection if the manifest is present but unreadable. This is
  intentional: silently booting the wrong server on a hand-edited bundle is
  worse than a clear error.

### `[app] identity_headers`

Opt this app out of (or explicitly into) identity forwarding.

```toml
[app]
identity_headers = false   # opt out: proxy does not inject X-Shinyhub-* headers
```

The field has tri-state semantics because it is stored as a nullable boolean:

| Value | Effect |
|---|---|
| absent (key not in manifest) | Inherit the global `auth.identity_headers` setting (the default). |
| `false` | Opt this app out. The proxy strips and does not inject `X-Shinyhub-*` headers for this app, regardless of the global setting. |
| `true` | Explicit opt-in. Equivalent to the absent case when the global setting is `true`; has no effect when the global setting is `false`. |

Removing the `identity_headers` key (or the entire `[app]` section) reverts
the app to inheriting the global default on the next deploy.

The global `auth.identity_headers: false` kill switch always wins. If the
operator has disabled identity forwarding globally, setting
`identity_headers = true` in a manifest has no effect. See
[Identity Forwarding](identity.md) for the full semantics, header reference,
and JWT verification examples.

### `[app] autoscale`

Declare the session-saturation autoscale policy so it travels with the bundle
and is reconciled on every deploy. Autoscale also requires the global
`runtime.autoscale.enabled` flag; see [Autoscaling](scaling.md#autoscaling).

```toml
[app]
autoscale = { enabled = true, min_replicas = 1, max_replicas = 8, target = 0.8 }
```

| Key | Type | Meaning |
|---|---|---|
| `enabled` | bool | **Required.** Turn the policy on or off. Still gated on the global `runtime.autoscale.enabled` flag. |
| `min_replicas` | int | Lower bound. Must be `>= 1` when enabled. The effective floor is `max(min_replicas, min_warm_replicas)`. |
| `max_replicas` | int | Upper bound. Must be `>= min_replicas` when enabled and may not exceed the runtime `max_replicas` ceiling. |
| `target` | float `(0,1]` | Target average active sessions per replica as a fraction of the per-replica cap. `0` inherits the runtime-wide default target. |

The block is atomic: when present it writes the full policy (all four columns);
when absent the stored policy is left untouched, so a policy set with `shinyhub
apps set --autoscale ...` survives a deploy that does not declare one. `enabled`
must be stated explicitly - a block that omits it (for example only `target`) is
rejected, so an incomplete block can never silently persist an all-zero policy.
Bounds are range-checked `0..1000` even when disabled, so a later re-enable never
hits an out-of-range stored value. An unknown key inside the table fails the
deploy under strict-mode parsing.

### `[app.worker]`

Declare how browser identities share worker processes. `grouped` and
`per_session` are single-node elastic pools; see the full
[worker-isolation guide](isolation.md#worker-isolation-session-isolation-dial).

```toml
[app.worker]
isolation = "per_session"
max_workers = 30
warm_spares = 2
max_session_lifetime_secs = 3600
```

| Key | Type | Meaning |
|---|---|---|
| `isolation` | `multiplex`, `grouped`, or `per_session` | Session-sharing model. `multiplex` is the default. |
| `grouped_size` | int >= 1 | Clients admitted to each worker in `grouped` mode. |
| `max_workers` | int >= 1 | Hard elastic worker ceiling for `grouped` and `per_session`. |
| `warm_spares` | int 0..`max_workers` | Healthy workers kept pristine for new clients. With snapshot support they are frozen and memory-reclaimed; otherwise they remain running. Default 0. |
| `max_session_lifetime_secs` | int >= 0 | Absolute lifetime after a consumed worker is ready; 0 is unlimited. Waiting time as a pristine spare is not included. |

The block is reconciled as a unit when present and left untouched when absent.
Warm spares count toward `max_workers` and are never reused after serving a
client. Frozen spares resume the same process; this is not copy-on-write process
cloning.

A replica is an outer app process. Workers live inside that runtime pool:
`min_warm_replicas` counts replica processes, while `[app.worker].max_workers`
and `warm_spares` count demand-driven workers within an elastic app pool.
Under `grouped` or `per_session` isolation the pool boots workers on demand and
the app reports `idle` (healthy) with none running, so a `min_warm_replicas`
floor is accepted and stored but has no effect; the deploy response carries a
manifest warning (`Note:` in the CLI, `manifest.warnings` in the response) when a
manifest declares that combination. Use `warm_spares` to keep elastic workers
pre-booted.

### `[app] name` and `[app] description`

Set the app's display metadata declaratively, so a bundle carries the label it
is presented under instead of relying on someone typing it into the dashboard
after the first deploy.

```toml
[app]
name = "Quarterly Revenue"
description = "Regional revenue roll-up, refreshed nightly"
```

`name` is the friendly label rendered on the dashboard card, the app detail
heading, and the launchpad tile. It is **not** the slug: the slug is the URL
identifier (`/app/<slug>/`), is fixed at deploy time, and is unaffected by this
field. `name` is trimmed and must be 1..128 characters; an empty or
whitespace-only value is rejected rather than stored, because every surface
renders the name as the app's primary label and there is no sensible fallback.

`description` is the one-line subtitle shown under the name, trimmed and capped
at 280 characters. Unlike `name`, `""` is a meaningful value: it clears the
description.

Both follow the same declared-only rule as the rest of the table: an absent key
leaves the stored value alone, so a name set in the dashboard survives deploys
from a manifest that stays silent. Once declared, every deploy reasserts the
manifest's value over a rename made in the UI - declaring the key is what makes
the manifest the owner.

Set the same values imperatively with `shinyhub apps set <slug> --name "..."
--description "..."`.

### `[app] icon`

Set the app's icon to a single emoji, declaratively.

```toml
[app]
icon = "🚀"
```

The field states who owns the app's icon rather than encoding three
mechanical outcomes:

| Manifest | Meaning |
|---|---|
| `icon = "..."` | Config owns this app's icon. |
| absent (key not in manifest) | Nobody is asserting ownership; leave whatever is stored. |
| `icon = ""` | Uploads own this app's icon; config stands down. |

Once the manifest declares an icon, every deploy reasserts it over an image
uploaded through the dashboard. The image bytes are retained, so `icon = ""`
in a later deploy brings the image back; removing the key entirely does not,
because absent means "leave alone". This matches the declared-only semantics
of every other field in this table, but is surprising if undocumented, since
an uploaded image is otherwise the only way to set an icon.

A deploy that shadows an uploaded image is reported back to the operator; see
[Deploy response](#deploy-response) for the wire field and sample output.

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
| `run_on_register` | no | When `true`, fire this schedule once on first registration if the app has never had a *successful* run of it, warming the cache on a fresh deploy. Re-deploys of an already-warmed schedule do not re-fire. See [First-fire on register](#first-fire-on-register-run_on_register). |

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

### First-fire on register (`run_on_register`)

Setting `run_on_register = true` makes the platform fire the schedule once,
asynchronously, the first time it is registered on an app that has never had a
successful run of it. This warms the app's cache on a fresh deploy without
re-blocking every deploy the way a deploy-time `[[hook]]` would.

The gate is "has this schedule ever succeeded?": a brand-new schedule fires; a
schedule that has already succeeded is never re-fired by a re-deploy. A failed
first-fire is non-fatal (the deploy stays live and durable) and is re-attempted
on the next deploy until a run succeeds. A `disabled` schedule is never
first-fired. If a re-deploy arrives while a first-fire is still running, the gate
is still open (no success yet) and a second fire is dispatched, which the
schedule's `overlap` policy (default `skip`) records as `skipped_overlap` rather
than running the job twice.

By default the fire is fire-and-forget: the deploy returns immediately and the
run warms the cache in the background. Pass `--wait-for-warm` to require a
recorded success. `shinyhub deploy` uses `--wait-timeout`; `shinyhub fleet
apply` uses one `--warm-timeout` deadline per app (default 15 minutes) across
all first-fires. A failure, timeout, missing dispatch reference, or unreadable
final schedule state exits non-zero. A `skipped_overlap` proves only that
another run existed and passes only when the final state already records a
success. For `fleet apply`, the level gate runs after every non-delete action,
including an unchanged bundle: an enabled `run_on_register` schedule with no
successful run remains unconverged without being re-fired. When an active run
is already repairing that condition, fleet apply joins its exact run ID within
the existing per-app deadline; a later overlap-skip row cannot hide it. Waiting
does not reload a replica that already read the old cache at startup. For that
application pattern, pass `--restart-after-warm` instead; it implies the wait
and cycles serving replicas only after the final success check passes. An app
that was deliberately stopped remains stopped and sees the warmed data on its
next start. The imperative
`shinyhub schedule add --run-on-register` fires the same way and reports the
triggered run id (add `--follow` to stream it).

## `[[hook]]` — deploy lifecycle hooks

| Field | Required | Meaning |
|---|---|---|
| `on` | yes | Trigger point. Only `post-deploy` is supported. |
| `command` | yes | argv to exec. First element is resolved against the bundle's PATH. |
| `timeout` | no | Wall-clock cap. Defaults to 5 minutes. Accepts Go duration syntax (`30s`, `2m`, `1h`). |

Hooks run sequentially in the order they appear in the manifest. The
first failing hook aborts the deploy — subsequent hooks do not run, and
the new bundle does not start.

Stdout and stderr are merged into the version's `deploy-hooks.log`. Each hook
also writes start and completion/failure records with its duration and exit
status, so a quiet successful command is still distinguishable from a hook that
was never declared.

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

Hooks run after the dependency build and before any app process starts, for
every worker-isolation mode: a `grouped` or `per_session` app gets the same
preparation as a multiplex one, even though its workers spawn on demand later.

**Hooks run when a bundle is promoted, not every time it starts.** Deploying,
rolling forward, and changing an app's env vars all promote and therefore run
them. Restarting, rolling back, scaling, and the automatic recovery after a
failed deploy re-activate a bundle that already served, so they do not: your
hooks already ran for it, and nothing guarantees a second run is safe. A
restart is therefore not a way to re-run a hook - deploy again for that.

The other case where a declared hook does not run is a container runtime, where
dependencies are installed inside the image and the host has no view of the
app's environment. That skip is reported: the deploy tells you how many hooks
it did not run, so bake those steps into your image entrypoint instead.

Because hooks are skipped on those paths, whatever they produce has to survive
alongside the bundle. Write generated assets into the bundle directory (they are
pruned with their version) or the persistent app data directory - not to a
scratch location a host reboot can clear, which would leave a restarted app
without them.

## `[access]` - per-app group access rules

Declare which IdP groups may view or manage this app. Groups come from the
OIDC `groups` claim or the forward-auth groups header (see the auth docs).

```toml
[access]
viewer_groups  = ["finance", "analysts"]   # granted the viewer role
manager_groups = ["finance-leads"]         # granted the manager role
```

| Field | Type | Meaning |
|---|---|---|
| `viewer_groups` | list of strings | groups granted `viewer` access to this app |
| `manager_groups` | list of strings | groups granted `manager` access to this app |

Semantics:

- **Declarative.** On every deploy, the manifest's `source = manifest` group
  rules are reconciled to exactly the `[access]` block. Removing a group (or
  the whole block) deletes its manifest rule on the next deploy.
- **Manual rules win.** Rules added through the UI / API / CLI (`shinyhub apps
  access group-grant`) are `source = manual` and are never modified or deleted
  by a manifest reconcile. If the manifest names a group that already has a
  manual rule, the manifest entry is skipped (reported with `skipped: true` in
  the deploy response) and the manual rule stands.
- **Manager wins on overlap.** A group listed in both `viewer_groups` and
  `manager_groups` is granted `manager`.
- **Additive.** Group access grants access; it does not restrict a `public` or
  `shared` app.
- Group names must be non-empty (validated at parse time).

## Idempotency

Re-deploying the same bundle yields the same state:

- `[app]` settings are deterministic — applying twice with the same values
  is a no-op aside from audit-event noise.
- `[[schedule]]` upserts by name — IDs are stable across deploys; cron or
  command changes update the row in place.
- `[[hook]]` blocks run every deploy; they are expected to be idempotent
  (e.g. `migrate.py` should handle "already migrated").
- `[access]` reconciles to exactly the declared groups each deploy; re-applying
  the same block is a no-op, and manual rules are always preserved.

## Audit events

Manifest application emits the same audit events as the equivalent UI/API
actions:

| Action | Recorded when |
|---|---|
| `update_app` | Phase A changed at least one `[app]` field. |
| `schedule_create` | First time a `[[schedule]]` with this name is seen for this app. |
| `schedule_update` | Subsequent deploys that mention an existing schedule. |
| `reconcile_group_access` | Phase C reconciled at least one `[access]` group rule. |

Hook executions are logged into the deploy log but do not emit per-hook
audit events. The overall deploy is recorded as `app_deploy`.

## Deploy response

When a manifest was applied, the JSON response from `POST /api/apps/:slug/deploy`
includes a `manifest` field summarising what landed. The CLI uses this to
print confirmation lines after `Deployed ...`:

```
Deployed myapp (deployment #4)
URL: https://hub.example.com/app/myapp/
Applied [app] settings: max_sessions_per_replica=10; replicas=2
Note: [app] icon "🚀" is now shown instead of this app's uploaded image.
      The image is still stored. Set icon = "" in shinyhub.toml to use it.
Schedules: 1 created, 0 updated
```

The wire shape:

```json
{
  "slug": "myapp",
  "deploy_count": 4,
  ...other app fields...,
  "manifest": {
    "app": { "replicas": 2, "max_sessions_per_replica": 10 },
    "icon_shadowed_upload": true,
    "schedules": [
      { "name": "nightly", "action": "created", "schedule_id": 7, "first_fire": { "run_id": 42 } }
    ],
    "access_groups": [
      { "group": "finance", "role": "viewer" },
      { "group": "finance-leads", "role": "manager" },
      { "group": "ops", "role": "viewer", "skipped": true }
    ]
  }
}
```

`manifest.app` is omitted when no `[app]` field changed; `manifest.icon_shadowed_upload`
is omitted unless the manifest's `icon` shadowed an uploaded image (present
and `true` only in that case); `manifest.schedules` is omitted when no
`[[schedule]]` was upserted; `manifest.access_groups` is omitted when the
`[access]` block is empty (each entry has `skipped: true` when a manual rule
preempted it); the entire `manifest` key is omitted when the bundle has no
`shinyhub.toml`. Top-level app fields stay in place so scripts that read
`deploy_count` keep working.

When hooks are present, the top-level response also reports
`hooks_declared` and `hooks_run`; `hooks_skipped` is present when a non-host
runtime could not execute declared hooks. This distinguishes "no hooks in the
deployed manifest" from "all declared hooks completed" without inspecting the
server log.

Each schedule entry carries its `schedule_id`; a `first_fire` object with the
dispatched `run_id` is present only when `run_on_register` fired a run on this
deploy. `shinyhub fleet apply --json` surfaces the same data per app under a
`first_fires` array (with the run's `status` when `--wait-for-warm` waited).
With `--restart-after-warm`, fleet JSON also reports `warm_restarted: true`
when serving replicas were cycled after those runs succeeded.

For a broader data-freshness policy, `fleet apply --verify-schedules` checks the
server-computed `stale` state of every enabled schedule, including schedules
without `run_on_register` and schedules whose last success has aged out. The
check never dispatches a run. `stale` describes proven data age independently
from `refreshing`, which reports a live run; a schedule can be both. Human
output labels these as schedule-freshness failures; JSON reports them in
`schedule_verification`, gives failures stable `failure_kind` values, and
includes the exact atomic `last_run_id` plus relevant run tails under
`result.schedule_logs` when available.

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
