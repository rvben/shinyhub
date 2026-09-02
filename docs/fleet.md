---
description: "Declare a whole set of apps in fleet.toml and let the CLI compute the difference against what the server runs, then converge it."
---

# Fleet manifest (`fleet.toml`)

`shinyhub fleet` reconciles a whole set of apps against a single declarative
manifest, the way `kubectl apply` reconciles a cluster against a directory of
YAML. You describe the apps you want, their source, visibility, and
fleet-managed config; the CLI computes the difference against what the server
actually runs and converges it.

Reconcile is client-orchestrated: the CLI fetches server state, builds the
plan locally, and drives the existing per-app deploy and patch APIs. There is
no server-side fleet controller and no new privileged endpoint.

Fleet commands read `fleet.toml` from the working directory when `-f` is
omitted. The previous name `shinyhub-fleet.toml` is still read as a fallback
when `fleet.toml` is absent (with a one-line deprecation note on stderr), so
existing repositories keep working; rename the file to `fleet.toml` at your
convenience. Pass `-f <path>` to point at any other location.

```toml
fleet_id = "prod-eu"

[[bundle_file]]
from      = "_shared/plotly_template.py"
to        = "helpers/plotly_template.py"
consumers = ["sales-dashboard"]

[[app]]
slug       = "sales-dashboard"
source     = "./apps/sales-dashboard"
visibility = "private"

  [app.config]
  hibernate_timeout_minutes = 30
  replicas                  = 2
  max_sessions_per_replica  = 10

[[app]]
slug       = "status-page"
source     = "git+https://github.com/acme/status.git@v1.4#deploy/status"
visibility = "public"
```

## Fields

### Top level

| Field | Required | Meaning |
|---|---|---|
| `fleet_id` | yes | Ownership scope. Must match `[a-z0-9-]`, 1-64 chars. Stamped onto every app this manifest manages as `managed_by = fleet:<fleet_id>`. |
| `[[app]]` | yes (>=1) | One block per app the fleet should own. |
| `[[project]]` | no | One block per project the fleet should name. Optional: an app can declare a `project` without a matching block, and the project is then created unnamed. |
| `[[bundle_file]]` | no | Copy one canonical local file into the bundles of explicitly named consumers. |

### `[[app]]`

| Field | Required | Meaning |
|---|---|---|
| `slug` | yes | App slug. Unique within the manifest; a duplicate is a validation error. |
| `source` | yes | Where the bundle comes from (see [Source resolution](#source-resolution)). |
| `visibility` | no | `private` (default), `shared`, or `public`. |
| `[app.config]` | no | Fleet-managed app settings (see [Config](#appconfig--fleet-managed-settings)). |

### `[app.config]` - fleet-managed settings

| Field | Type | Meaning |
|---|---|---|
| `name` | string 1..128 | Friendly display name shown on the dashboard card and the detail heading. Trimmed; may not be empty. Distinct from `slug`, which is the URL identifier and is not settable here. |
| `description` | string 0..280 | One-line description shown under the name. Trimmed; `""` is a real value that clears it. |
| `project` | string | Project slug that groups this app on the dashboard, the launchpad and the sidebar. Must match `[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?`. `""` is a real value that ungroups the app. The project row is created automatically if it does not exist. |
| `hibernate_timeout_minutes` | int | Idle minutes before hibernation. `-1` resets to the server default, `0` disables hibernation, and a positive value sets the timeout. |
| `replicas` | int `>= 1` | Number of replica processes. See [scaling](scaling.md). |
| `max_sessions_per_replica` | int `>= 1` | Per-replica admission cap for new cookieless sessions. |
| `autoscale` | inline table | Session-saturation autoscale policy. Reconciled atomically and drift-protected as one unit. See [autoscale](#appconfig-autoscale) below. |

Only the keys you declare here are owned by the fleet manifest. An omitted key
may still be owned by the source bundle's `shinyhub.toml`; if neither manifest
declares it, a value set through the UI or CLI survives untouched.

In other words, `[app]` keys are a sparse overlay: omitting a key leaves its
current server value in place; omission does **not** reset it to the default.
To inherit the global hibernation timeout explicitly, declare
`hibernate_timeout_minutes = -1`. The adjacent value `0` means **never
hibernate**, not "use the default".

Declaring `name` or `description` therefore makes the manifest their owner: a
rename in the dashboard shows up as drift on the next `fleet plan` and is
reverted by `fleet apply`. `plan` renders both quoted, so an empty or
space-padded value is visible rather than rendering as nothing:

```
  ~  update  reporting  name "Reporting" -> "Quarterly Revenue"
```

#### `[app.config]` autoscale

Manage the per-app autoscale policy from the fleet manifest so it is reconciled
on every `fleet apply` and drift back to the manifest is corrected:

```toml
  [app.config]
  autoscale = { enabled = true, min_replicas = 1, max_replicas = 8, target = 0.8 }
```

| Key | Type | Meaning |
|---|---|---|
| `enabled` | bool | **Required.** Turn the policy on or off. Also gated on the global `runtime.autoscale.enabled` server flag. |
| `min_replicas` / `max_replicas` | int | Steady-state bounds the controller stays within. When enabled, `min_replicas >= 1`, `max_replicas >= min_replicas`, and `max_replicas` may not exceed the runtime steady-state `max_replicas` ceiling. A [scheduled serving-data roll](schedules.md#activating-refreshed-data-in-serving-replicas) may temporarily admit one memory-checked surge replica above it without changing the configured count. |
| `target` | float `(0,1]` | Target average active sessions per replica as a fraction of the per-replica cap. `0` inherits the runtime default. |

The block reconciles atomically (all four columns) and is one drift unit: `fleet
plan` shows a single line (e.g. `autoscale off -> on(1-8 @ 0.80)`) when the
server policy differs from the manifest. It is the same policy the bundle
`shinyhub.toml [app] autoscale` block sets; per [config precedence](#config-precedence)
the fleet manifest wins. See [Autoscaling](scaling.md#autoscaling) for behaviour.

### `[[project]]` - project display metadata

A project is created automatically the first time an app declares it, with no
name and no icon. A `[[project]]` block declares that metadata so a fleet
manifest can produce a fully labelled dashboard on its own:

```toml
fleet_id = "acme"

[[project]]
slug = "reporting"
name = "Quarterly Reporting"
description = "Finance-facing dashboards"
icon = "📊"

[[app]]
slug = "revenue"
source = "./apps/revenue"

  [app.config]
  project = "reporting"
```

| Field | Required | Meaning |
|---|---|---|
| `slug` | yes | Project slug. Unique within the manifest; a duplicate is a validation error. Same charset as an app slug. |
| `name` | no | Display name, up to 128 characters. Trimmed. `""` is a real value that clears it. |
| `description` | no | One-line description, up to 280 characters. Trimmed. |
| `icon` | no | A single emoji shown beside the group heading. `""` clears it. |

Only the keys you declare are reconciled, matching `[app.config]`: an omitted
key is not asserted, so a name set through the dashboard survives a manifest
that declares only an icon.

A `[[project]]` block that no `[[app]]` references is a validation error. The
fleet manifest reconciles what it declares; a project that no app joins would
be created and then never converge, since nothing in the manifest can restore
it if someone deletes it.

Projects are reconciled **before** apps in a single `fleet apply`, so an app
that creates its project lands in one that is already named.

`fleet apply --prune` never deletes a project. Projects are a shared namespace
that apps outside this fleet can also be in, and a project holding no apps is
a valid state an operator may be preparing. Remove one with
`shinyhub projects rm <slug>`.

## Source resolution

`source` is resolved one of two ways:

- **Local path.** A relative path is resolved against the directory
  containing the manifest, not the current working directory, so a manifest
  is portable regardless of where `shinyhub fleet` is run from. The path must
  exist; existence is checked in a pre-flight step before any change is made.
- **Git URL.** `git+<url>[@ref][#subdir]`. `@ref` pins a branch, tag, or
  commit; `#subdir` deploys a subdirectory of the repository as the bundle
  root. The URL format is validated when the manifest is parsed; the clone
  happens during pre-flight.

## Shared bundle inputs

Use `[[bundle_file]]` when a small number of files are intentionally identical
across several local app bundles:

```toml
[[bundle_file]]
from      = "_shared/plotly_template.py"
to        = "helpers/plotly_template.py"
consumers = ["sales", "operations"]
```

`from` resolves against the directory containing `fleet.toml`. `to` is the
path inside each consumer's bundle. `consumers` is explicit: an app receives
the file only when its slug is listed. V1 supports local app sources only; a
declaration that names a `git+` consumer fails validation. This is bundle-time
composition, not a shared runtime or shared environment. Each resulting bundle
is still self-contained and independently deployable.

Validation is intentionally strict:

- `from` and `to` must be normalized, portable, relative slash paths. The
  source must be a regular file inside the manifest root. Every source path
  component is checked without following symlinks; this also rejects the common
  monorepo layout where `_shared` itself is a symlink.
- A destination may not already exist in the app source. Exact and
  file/directory-prefix conflicts between declarations are also errors.
- `.shinyhubignore` (or its `.gitignore` fallback) applies to the destination
  and every destination ancestor. Declaring an ignored destination is an error,
  not a silent omission. The normal reserved-path, extension, and file-size
  bundle rules apply too.
- `shinyhub.toml`, `.shinyhubignore`, and `.gitignore` cannot be composed. These
  control how the base bundle is interpreted and must live in the app source.

Preflight resolves and snapshots every canonical shared source before any
server mutation. The same source is read once per invocation even when several
apps consume it, and each successful consumer deploy uses that immutable
snapshot. A later edit during the apply is picked up on the next invocation.
Executable owner mode is normalized to `0755`; other files use `0644`, with a
fixed archive timestamp for reproducible bundles. The checks reduce file-swap
risk but are not a hostile-filesystem sandbox; V1 assumes the local checkout is
trusted while the command runs.

Plan output reports each declaration's consumers and which of those consumers
already have a planned source-bearing action. That is fan-out visibility, not
file-level remote causality: the server stores a bundle digest, not the prior
contents of each shared file. Apply remains non-atomic and continue-on-error,
so one consumer can fail after another has deployed; the report names that
partial outcome.

Single-app `shinyhub run`, `shinyhub plan`, and `shinyhub deploy` do not compose
fleet inputs. When they can discover a valid nearest-parent fleet manifest for
the selected local source, they warn on stderr and point to the corresponding
fleet command. Discovery is best-effort and cannot find a manifest supplied
elsewhere with `-f`; absence of a warning is not proof that the source has no
fleet composition.

To migrate hand-copied files, add the canonical files under a directory such
as `_shared`, declare their destinations and consumers, and delete the old
vendored targets in the same commit. Then run `shinyhub fleet validate`, review
`shinyhub fleet plan`, and converge with `shinyhub fleet apply`.

Develop a composed consumer through the same declaration rather than restoring
a vendored copy:

```bash
shinyhub dev . --app sales --file fleet.toml
```

This offline command validates and runs only the selected local app;
an unrelated app with a missing local source does not block focused development.
The whole manifest's syntax and cross-references must still be valid. Shared
file and app-source edits trigger staged reload, while a broken or missing
shared input leaves the current healthy process online. Manifest-structure
edits require restarting the command. See
[Fleet development](development/fleet.md) for selection, shared inputs,
generated state, data directories, and concurrency behavior.

## Config precedence

When the same setting can come from more than one place, the fleet manifest
wins:

1. **Fleet manifest `[app.config]`** - highest. A declared key is enforced on
   every apply; out-of-band drift is corrected back.
2. **Bundle `shinyhub.toml` `[app]`** - durable settings are checked on every
   plan/apply and corrected with a config PATCH even when the bundle digest is
   unchanged. Boot-only settings such as `command` and startup/build timeouts
   continue to take effect when the bundle is deployed or started.
3. **Server default** - lowest.

If neither manifest declares a durable key, a setting managed through the UI
or CLI remains outside fleet reconciliation.

## Strict-mode parsing

Parsing reports *every* problem it finds, compiler-style, not just the first.
Unknown keys are rejected with a "did you mean" suggestion (a typo such as
`replcias` does not silently no-op). `fleet_id` is required and syntax-checked;
each app must have a slug and a source; duplicate slugs and invalid visibility
values are errors. A manifest with any problem is never used to make changes.

## Workflow

### 1. `shinyhub fleet init`

Scaffold a manifest from the apps already deployed on the server:

```
shinyhub fleet init --fleet-id prod-eu --source-root ./apps
```

Writes `fleet.toml` containing `fleet_id` and one `[[app]]` block per
existing app, slug-sorted, with each app's current visibility, config, and
project membership. Referenced projects are emitted once as `[[project]]`
blocks so grouping survives an init/plan/apply round trip.
With `--source-root <dir>` each `source` is set to `<dir>/<slug>` and the file
is immediately plan-able. Without it the `source` line is left commented so
you set each path explicitly; an unset source trips the pre-flight check with
a precise message rather than a confusing parse error.

`--fleet-id` is required (prompted when run interactively); the file is not
overwritten unless `--force` is passed. Even with `--force`, init refuses to
replace an existing manifest that contains `[[bundle_file]]` declarations,
because regenerating app inventory cannot preserve those hand-authored inputs.

### 2. `shinyhub fleet validate`

Validate the complete manifest locally before contacting a server:

```
shinyhub fleet validate
```

Besides schema and source checks, validation resolves every shared input for
every consumer and rejects missing or escaping sources, symlinks, target
collisions, ignored destinations, and bundle-policy violations. This is the
authoritative offline pre-merge gate for supported local consumers.

### 3. `shinyhub fleet plan`

Show what `apply` would do, and change nothing:

```
shinyhub fleet plan
```

`plan` recomputes the diff from live server state every time; it never
replays a saved plan. `--detailed-exitcode` makes it exit `2` when changes
are pending (useful in CI gates). `--json` emits a stable machine-readable
envelope; `-q/--quiet` collapses to the summary.

An operational setting omitted from both manifest layers is not drift and is
never changed by `apply`. When its stored value is an override rather than the
field's unset/default representation, `plan` nevertheless labels it
`unmanaged` in the app row. JSON schema version 3 exposes the same information
as `apps[].unmanaged`, with `key`, `server`, and `default` fields. This signal
is informational and does not change the action or detailed exit code. Schema
version 3 also exposes `bundle_files[]` in both plan and apply envelopes, with
the declared `consumers` and the source-bearing `planned_consumers`.

### 4. `shinyhub fleet apply`

Converge the fleet:

```
shinyhub fleet apply --prune --yes
```

`apply` recomputes the same diff as `plan` (a prior plan is never replayed),
then for each app, in order: deploys changed apps, reconciles durable config
declared by either manifest (with `[app.config]` taking precedence), and stamps
ownership. Convergence is non-atomic and
continue-on-error: one failing app does not abort the rest, and the exit code
reflects the worst outcome.

Adopting an existing app does not create a deployment when its non-empty
content digest and every declared setting already match. On servers that
support fleet preconditions, apply transfers ownership with one conditional
metadata update that asserts both the observed digest and prior owner. A source
or declared-config difference still follows the normal convergence path. Older
servers without precondition support conservatively redeploy during adoption
rather than trusting a stale observation.

| Flag | Effect |
|---|---|
| `--dry-run` | Identical to `fleet plan`; makes no changes. |
| `--adopt` | Take ownership of in-scope apps that exist but are not yet fleet-managed. Without it, an un-owned app in scope is reported, not modified. |
| `--prune` | Delete fleet-owned apps that are absent from the manifest. **This also removes their persistent data directory and all bundles.** |
| `-y/--yes` | Skip the interactive destructive-action confirmation. `--prune` in a non-interactive shell requires `--yes`. |
| `--retries N` | Retry attempts *after* the first for deploys and transient config PATCH failures. Default 1 (so two attempts total). |
| `--wait-for-warm` | Ask the server to reconcile every persisted enabled deploy-triggered schedule, including on unchanged apps; wait for each exact durable obligation and require the authoritative producer state to match the current digest and command. |
| `--warm-timeout DURATION` | Per-app deadline shared by deploy-run waits and the final bundle-convergence check. Default 15 minutes. |
| `--verify-schedules` | Read-only: require cron freshness and authoritative producer convergence for every enabled schedule, and reject unresolved producer-write uncertainty even if its schedule was later disabled; never dispatches work. |
| `--verify-health` | Require every app, unchanged as well as changed, to be in a state it serves from without operator action: `running`, `idle`, or parked (`hibernated` after its idle timeout, `suspended`). A parked app wakes on its first request and passes in one poll; the gate never wakes it, so a broken bundle in a hibernated app surfaces on wake, not here. Intentionally stopped apps remain excluded. The post-deploy wait for changed apps is stricter and still requires a serving replica. |
| `--restart-after-warm` | After convergence repairs an already-running or unchanged app, cycle replicas so startup-loaded caches see the new data. Pre-start deploy/rollback producers already run before replica boot and do not cause a redundant cycle. Deliberately stopped apps stay stopped. |
| `--allow-unsafe-degraded-prune` | Permit prune against a server without precondition support, accepting a documented race (see [Degraded mode](#degraded-mode)). |
| `--json` | Emit the machine-readable result envelope. |
| `-q/--quiet` | Collapse to the summary plus result line. |
| `--provenance auto\|none` | Detect CI attribution (default) or intentionally omit it. |

### Deployment provenance

On servers that advertise the `fleet_provenance` capability, `fleet apply`
registers one immutable run before it makes any changes. Every deployment and
audit event produced by that apply carries the run ID, so the dashboard can
link the live version and deployment history back to its source.

Current servers also allocate a monotonic sequence to each run, heartbeat it
while the CLI is active, and record an immutable terminal result. This has two
important failure semantics: an older overlapping apply cannot overwrite app
convergence recorded by a newer run, and a process killed before it reports a
result remains observably abandoned rather than looking successful. If the CLI
cannot persist the terminal result after convergence, the apply itself exits
partial and reports the run-recording failure instead of printing a false OK.

An identical apply is recorded as a new audit run and refreshes the app's
`checked_at` convergence timestamp, but it remains `unchanged`: it does not
advance the last application timestamp or provenance and does not create a new
release. Changing only which already-matching fields the manifest declares
does advance the declaration application, because the fleet ownership baseline
itself changed even though no live app value needed patching.

GitLab CI works without extra credentials. In `auto` mode the CLI reads
GitLab's predefined `CI_PIPELINE_*`, `CI_JOB_*`, `CI_COMMIT_*`, and
`CI_MERGE_REQUEST_*` variables. It stores a bounded snapshot containing the
pipeline, job, commit/ref, and optional merge request links; ShinyHub does not
call the GitLab API or store a GitLab token.

Provider-neutral overrides are available for other CI systems:

```
shinyhub fleet apply \
  --source-provider buildkite \
  --source-label "Production pipeline #812" \
  --source-url "https://buildkite.example/pipelines/812" \
  --revision "$GIT_COMMIT" \
  --revision-ref "$GIT_BRANCH"
```

The full override set is `--source-provider`, `--source-label`,
`--source-url`, `--job-label`, `--job-url`, `--revision`, `--revision-ref`,
`--revision-url`, `--change-label`, and `--change-url`. Links must use HTTPS.
Use `--provenance=none` to opt out; it cannot be combined with explicit
provenance flags. Older servers simply receive the existing run header and
continue normally without attribution.

`--prune` is guarded: when prune candidates exist and prune will actually
run, an interactive run asks you to type the word `prune` to confirm.

## Ownership

Every app a manifest manages is stamped `managed_by = fleet:<fleet_id>`.
This marker is what makes `--prune` safe: prune only ever deletes apps that
carry *this* fleet's marker and are absent from the manifest. An app with no
marker, or a different fleet's marker, is never pruned. The same predicate
drives the read-only [dashboard surface](#dashboard-surface).

## Degraded mode

Fleet preconditions let `apply` patch config and prune against a precise
expected state (a compare-and-set). If the server does not advertise
precondition support, `apply` runs in degraded mode:

- Source deploys cannot be fenced against the planned digest and owner, so a
  concurrent writer can win the upload race. On a current server, a mismatch is
  a pre-mutation conflict and the bundle is not promoted.
- Config patches fall back to a re-GET immediately before the write (a
  smaller TOCTOU window, not zero).
- `--prune` is disabled unless `--allow-unsafe-degraded-prune` is set, which
  accepts the documented race that an app could change between the read and
  the delete.

`fleet plan` and `apply` print a warning when degraded mode is in effect.

## Exit codes

`plan` and `apply` share one exit-code contract. `apply` returns the highest
applicable code.

| Code | Meaning |
|---|---|
| `0` | Success, or a report was printed (including `--dry-run` and a clean plan). |
| `1` | Usage error or manifest validation failure. |
| `2` | `plan --detailed-exitcode` only: changes are pending. |
| `3` | Transport or auth error (could not reach the server / not logged in). |
| `4` | Partial: at least one app failed after retries. |
| `5` | Conflicts: at least one app was skipped on a precondition `409`. |

Prune candidates that are skipped because `--prune` was not passed do not
change the exit code; they are reported and the run is still `0`.

## `shinyhub fleet status`

`status` is the manifest-less companion to `plan`: it makes one read-only
`GET` and lists every app the server knows with its fleet ownership marker
and live deployment digest, no manifest required. It never makes changes and
returns `0` (overview printed) or `3` (transport / auth error). `--json` and
`-q/--quiet` behave as elsewhere. Use it for a quick ownership overview;
use `plan` when you want the diff against a specific manifest.

## Dashboard surface

The dashboard reflects fleet ownership read-only. It is a status view, not a
control surface; there is no apply, prune, or drift action in the UI.

- **Ownership badge.** Apps managed by a fleet show a `managed by
  fleet:<fleet_id>` badge in the grid and on the app detail view, with a
  tooltip explaining the marker.
- **Segment filter.** The apps view adds an All / Fleet-managed / Unmanaged
  selector (the choice is remembered across reloads) so an operator can see
  at a glance which apps are under fleet control.
- **Live deployment digest.** The app detail view shows the live content
  digest of the running deployment. This is the digest of what is *deployed
  now*, not a conformance signal: it does not by itself tell you whether the
  app matches the manifest. Run `fleet plan` for that. The UI labels the
  value accordingly so it is not mistaken for a drift indicator.
- **Deployment source.** A quiet header strip identifies how the live version
  was produced. Fleet deploys link back to their pipeline, commit/ref, and
  optional merge request; dashboard, CLI, API, and rollback actions name their
  channel and authenticated operator. Deployment history repeats the source
  per release. Rows created before source tracking say only that no source was
  recorded, without implying that a manual deployment is an error.
