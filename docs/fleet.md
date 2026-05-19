# Fleet manifest (`shinyhub-fleet.toml`)

`shinyhub fleet` reconciles a whole set of apps against a single declarative
manifest, the way `kubectl apply` reconciles a cluster against a directory of
YAML. You describe the apps you want, their source, visibility, and
fleet-managed config; the CLI computes the difference against what the server
actually runs and converges it.

Reconcile is client-orchestrated: the CLI fetches server state, builds the
plan locally, and drives the existing per-app deploy and patch APIs. There is
no server-side fleet controller and no new privileged endpoint.

```toml
fleet_id = "prod-eu"

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
| `hibernate_timeout_minutes` | int | Idle minutes before hibernation. `-1` resets the field to the server default (the same sentinel as the bundle `shinyhub.toml`). Otherwise must be `>= 1`. |
| `replicas` | int `>= 1` | Number of replica processes. See [scaling](scaling.md). |
| `max_sessions_per_replica` | int `>= 1` | Per-replica admission cap for new cookieless sessions. |

Only the keys you declare are reconciled. An omitted key is not asserted: a
value set through the UI, the CLI, or the bundle's own `shinyhub.toml`
survives untouched. The fleet manifest does not assert a complete config
state, so drift protection covers exactly the keys it declares and nothing
else.

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

## Config precedence

When the same setting can come from more than one place, the fleet manifest
wins:

1. **Fleet manifest `[app.config]`** - highest. A declared key is enforced on
   every apply; out-of-band drift is corrected back.
2. **Bundle `shinyhub.toml` `[app]`** - applies on deploy for keys the fleet
   manifest does not declare.
3. **Server default** - lowest.

Because the manifest only declares the keys you write, a setting you manage
elsewhere (UI, CLI, or bundle) keeps working as long as the fleet manifest
stays silent about that key.

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

Writes `shinyhub-fleet.toml` containing `fleet_id` and one `[[app]]` block per
existing app, slug-sorted, with each app's current visibility and config.
With `--source-root <dir>` each `source` is set to `<dir>/<slug>` and the file
is immediately plan-able. Without it the `source` line is left commented so
you set each path explicitly; an unset source trips the pre-flight check with
a precise message rather than a confusing parse error.

`--fleet-id` is required (prompted when run interactively); the file is not
overwritten unless `--force` is passed.

### 2. `shinyhub fleet plan`

Show what `apply` would do, and change nothing:

```
shinyhub fleet plan -f shinyhub-fleet.toml
```

`plan` recomputes the diff from live server state every time; it never
replays a saved plan. `--detailed-exitcode` makes it exit `2` when changes
are pending (useful in CI gates). `--json` emits a stable machine-readable
envelope; `-q/--quiet` collapses to the summary.

### 3. `shinyhub fleet apply`

Converge the fleet:

```
shinyhub fleet apply -f shinyhub-fleet.toml --prune --yes
```

`apply` recomputes the same diff as `plan` (a prior plan is never replayed),
then for each app, in order: deploys changed apps, reconciles fleet-declared
config drift, and stamps ownership. Convergence is non-atomic and
continue-on-error: one failing app does not abort the rest, and the exit code
reflects the worst outcome.

| Flag | Effect |
|---|---|
| `--dry-run` | Identical to `fleet plan`; makes no changes. |
| `--adopt` | Take ownership of in-scope apps that exist but are not yet fleet-managed. Without it, an un-owned app in scope is reported, not modified. |
| `--prune` | Delete fleet-owned apps that are absent from the manifest. **This also removes their persistent data directory and all bundles.** |
| `-y/--yes` | Skip the interactive destructive-action confirmation. `--prune` in a non-interactive shell requires `--yes`. |
| `--retries N` | Retry attempts *after* the first for deploy-bearing actions. Default 1 (so two attempts total). |
| `--allow-unsafe-degraded-prune` | Permit prune against a server without precondition support, accepting a documented race (see [Degraded mode](#degraded-mode)). |
| `--json` | Emit the machine-readable result envelope. |
| `-q/--quiet` | Collapse to the summary plus result line. |

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
