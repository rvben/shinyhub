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
| `hibernate_timeout_minutes` | int | Idle minutes before the watcher hibernates the app. `0` disables hibernation. `-1` resets the field to the server default (the same convention as `shinyhub apps set --hibernate-timeout -1`). |
| `replicas` | int ≥ 1 | Number of identical replica processes serving this app. See [scaling](scaling.md). |
| `max_sessions_per_replica` | int 0..1000 | Per-replica admission cap for new cookieless sessions. `0` means "use the runtime default". |
| `min_warm_replicas` | int 0..1000 | Minimum number of replicas kept running when the app idles. `0` (default) allows full hibernation. When set above `0`, the watcher stops only enough replicas to reach this floor so the first post-idle request hits a warm process. If the stored `replicas` value is less than `min_warm_replicas`, the floor self-clamps to `replicas`. Absent key leaves the stored value unchanged (same declared-only semantics as `replicas`). See [Pre-warming](scaling.md#pre-warming). |
| `command` | array of strings | Launch-command override. See [`[app] command`](#app-command) below. |
| `identity_headers` | bool | Per-app identity-forwarding toggle. See [`[app] identity_headers`](#app-identity_headers) below. |
| `autoscale` | inline table | Per-app session-saturation autoscale policy. See [`[app] autoscale`](#app-autoscale) below. |

All fields are optional. Omitted fields are left untouched: the
manifest does not assert a complete state, so existing values set via the
UI or CLI survive across deploys unless the manifest explicitly overrides
them.

This bundle `shinyhub.toml` is the per-deploy layer. A [fleet
manifest](fleet.md) sits above it: when an app is fleet-managed, a key
declared in the fleet manifest's `[app.config]` is reconciled on every apply
and wins over the value set here. The full order is fleet manifest > bundle
`shinyhub.toml` > server default; see [Config precedence](fleet.md#config-precedence).

Settings are applied in a single SQLite transaction. Shrinking `replicas`
removes the now-out-of-range rows from the `replicas` table in the same
transaction; no half-applied state is reachable.

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
- **Health check is unchanged.** The platform polls `GET /` and waits for a
  non-5xx response, same as for inferred-command apps.
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
run warms the cache in the background. Pass `--wait-for-warm` to `shinyhub deploy`
or `shinyhub fleet apply` to block until the run completes (within the deploy's
wait/health timeout); a genuine warm failure then exits non-zero, while a
`skipped_overlap` (another run is already warming the same schedule) is treated
as "in progress", not a failure. The imperative
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

`manifest.app` is omitted when no `[app]` field changed; `manifest.schedules`
is omitted when no `[[schedule]]` was upserted; `manifest.access_groups` is
omitted when the `[access]` block is empty (each entry has `skipped: true` when
a manual rule preempted it); the entire `manifest` key is omitted when the
bundle has no `shinyhub.toml`. Top-level app fields stay in place so scripts
that read `deploy_count` keep working.

Each schedule entry carries its `schedule_id`; a `first_fire` object with the
dispatched `run_id` is present only when `run_on_register` fired a run on this
deploy. `shinyhub fleet apply --json` surfaces the same data per app under a
`first_fires` array (with the run's `status` when `--wait-for-warm` waited).

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
