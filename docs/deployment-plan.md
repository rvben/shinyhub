---
description: "Use plan and apply when a deploy needs review, an audit trail, or a strict split between approving the bundle and shipping it."
---

# Plan and apply

ShinyHub has two deployment paths:

- `shinyhub deploy` is the fast path. It prepares the current source and deploys
  it immediately.
- `shinyhub plan --out` followed by `shinyhub apply` is the reviewable path. It
  saves the exact bundle, desired state, target, expiry, and remote revision that
  were reviewed, then applies those bytes without rebuilding from the working
  tree.

Use the exact path when a deployment needs human approval, an audit trail, or a
strict separation between review and execution:

```bash
shinyhub doctor .
shinyhub plan . --out sales.plan
shinyhub plan show sales.plan
shinyhub apply sales.plan
```

Pass the source path explicitly. Plan, like deploy, never assumes that the
current directory is safe to bundle.

## Read a plan

Human output follows the same decision order for a single app and a fleet:

1. outcome;
2. user, availability, ownership, or destructive impact;
3. semantic changes;
4. bundle or resource detail;
5. create/update/adopt/delete totals;
6. the safest next command.

At narrow terminal widths the layout stacks instead of discarding information.
Color is additive: action words, symbols, headings, and ordering retain the same
meaning with `--no-color`, `NO_COLOR`, or ASCII-only output.

The default view is decision-sized. Ask for implementation detail only when it
is useful:

```bash
shinyhub plan . --details
shinyhub plan show --details sales.plan
shinyhub plan show --files sales.plan
```

The detail view includes every bundled path, launch command, readiness contract,
manifest effect, permission, target URL, and saved-plan integrity field. JSON is
always complete and does not apply the human view's progressive disclosure.

## What plan verifies

`shinyhub plan` uses the same bundle preparation, launch resolution, manifest
validation, and target selection as deploy. It reports:

- create, update, or unchanged/redeploy intent;
- current and planned content digests and typed configuration values;
- create/manage permission, access, ownership, and lifecycle effects;
- source size, upload size, included paths, ignore rules, and protected data or
  cache paths;
- runtime, dependency preparation, launch command, readiness endpoint, and
  startup deadline;
- hooks, schedules, access declarations, tracing, and other manifest effects;
- fleet adoption and every prune candidate in an isolated destructive section;
- degraded comparisons, version skew, in-progress deployment, ownership, and
  availability warnings.

Planning is read-only. It performs local preparation and remote reads, but never
creates an app, uploads a bundle, changes access, starts a stopped app, or probes
permission with a write.

An unchanged digest means the bundle's files, executable bits, and manifest
match the newest successful deployment. Applying is still explicit because a
redeploy may replace replicas. If an older server cannot report a live digest,
plan reports the comparison as `unknown`; it never invents equality.

## Saved plans are exact and private

`--out` writes an owner-only (`0600`) plan container containing application
source. Treat it like a release artifact:

- do not commit it;
- transfer and retain it only where the source itself is allowed;
- use `--expires-in` to shorten the default 24-hour lifetime;
- use `--force` only when deliberately replacing an existing plan file.

Before any mutation, `shinyhub apply` verifies the container structure,
integrity digest, embedded bundle digest, expiry, target host, desired-state
consistency, server compatibility, and the target's resource revision. A
changed working tree is irrelevant: apply uploads the reviewed embedded bundle.

If the app changed after planning, apply returns a conflict and tells you to
create a new plan. It does not silently recompute or apply against the newer
state. Creates use an expected-absent precondition, so another actor claiming
the slug is also rejected before deployment.

Use `plan show` for offline inspection. It verifies the artifact but permits an
expired plan because inspection cannot mutate the server; `apply` never permits
an expired plan.

## Fleet plan and apply

Fleet planning uses the same action vocabulary and count model:

```bash
shinyhub fleet plan -f fleets/eu.toml
shinyhub fleet apply -f fleets/eu.toml
```

Ownership transfers require `--adopt`. Deletion requires `--prune` plus an
interactive confirmation, or an explicit `--yes` supplied by the operator.
Suggested and recovery commands intentionally never add `--yes`.

Fleet apply is non-atomic and continues across resources. Its report therefore
records a terminal status and a separate mutation state for every app or
project:

- `none`: ShinyHub can prove no mutation occurred;
- `committed`: the requested resource change completed;
- `partial`: a mutation committed but a later convergence step failed;
- `unknown`: the client cannot prove whether the remote mutation committed.

Every run has a cryptographically random run ID for correlation with server
audit records. Current servers add a monotonic sequence, client heartbeat, and
immutable terminal status. Late writes from an older overlapping run cannot
replace newer per-app convergence, and a run that stops heartbeating without a
terminal result is distinguishable from success. On failure, human and JSON
output identify committed, partial, and unknown resources and provide one of
three recovery strategies:

- `resume`: transient failure; re-run apply;
- `repair_then_resume`: fix the deterministic or post-commit failure, then
  re-run apply;
- `replan`: remote state conflicted; review a fresh plan before re-applying.

Two `repair_then_resume` cases are deliberate refusals rather than defects, and
the recovery output names the specific next action for each instead of a
generic repair. A `downtime_required` failure means the deploy could not be
completed without dropping live sessions, because parallel generation handoff
does not support that app's current shape; rather than drop them unasked the
server made no change and left the working version serving. Re-run with
`--allow-downtime` to permit the stop-first deployment. A `schedule_stale`
failure means a freshness gate is unsatisfied and apply will not dispatch
producers itself; the recovery lists the `shinyhub schedule run <slug>
<schedule>` commands to run first, and only for schedules that are plainly
overdue rather than already refreshing.

A `generation_retiring` failure is a `resume` case: the server did not start
the deploy because an earlier version of the app is still draining its sessions
or stopping its processes. Nothing changed, the working version keeps serving,
and `safe_to_retry` is `true`. Re-run apply once the earlier version has gone;
`--allow-downtime` does not shorten the wait. A refusal that still returns
after the app's drain deadline has passed is worth a look at the server log
for the earlier version's cleanup before intervening. The server marks this
refusal with `X-ShinyHub-Conflict-Remedy: wait` beside `X-ShinyHub-Conflict:
generation-handoff-deferred`. A server that predates the remedy header reports
the same refusal as `downtime_required`.

### Capacity during live updates

A live code update admits the complete replacement pool alongside the current
pool. Admission checks available memory and CPU before preparation and again
under the host-wide launch reservation immediately before starting workers.
A scheduled roll budgets one surge worker instead of a complete second pool.

CPU admission first accounts for local starting, serving, suspended, and
unconfirmed workers, even when idle. Draining predecessors retain their request
only while sessions or grouped-client bindings remain; reload-relayed sessions
also count. Session-free predecessors release their static CPU request before
physical cleanup finishes. A separate 200 ms pressure check catches contention
from current traffic and other programs; that sample does not reserve CPU.

Configure the accounting policy under `server.update_capacity`:

```yaml
server:
  update_capacity:
    worker_cpu_request_cores: 0.1 # per worker; default accounting request
    reserved_cpu_cores: 0.1       # absolute control-plane/background-work reserve
    max_surge_replicas: 1         # per app, including retained old generations
```

Unset CPU requests default to 0.1 core per worker; the unset CPU reserve remains
10% of effective node capacity. These are accounting defaults, not measured
startup requirements. Requests differ from `cpu_quota_percent`, which controls
runtime enforcement; choose requests appropriate to your workers' workload.
Requests are accounting budgets, not OS reservations against unrelated processes.

An explicit `worker_cpu_request_cores: 0` disables static worker CPU accounting;
it does **not** mean "use the default". The sampled pressure check still requires
0.1 core per candidate plus the configured node reserve. Memory admission and
the surge ceiling remain enabled. This lets single-node operators use memory
as the primary sizing constraint without reserving CPU for every idle app:

```yaml
server:
  update_capacity:
    worker_cpu_request_cores: 0
```

If the candidate startup requests plus reserve exceed the node's entire CPU
capacity, the refusal is permanent even with static accounting disabled.
Waiting cannot make that pool fit on an idle node.

Memory admission checks available host and cgroup memory against the whole
candidate pool plus the host floor (`server.min_available_memory_mb`). It uses
the configured worker memory limit when present; otherwise it uses observed
startup peak or live RSS with a 25% plus 64 MiB safety margin. Draining workers'
physical memory remains included until freed.

The default surge ceiling is `0` (no additional replica-count ceiling; resource
admission still applies). Scheduled rolls use one surge worker. Code handoffs
require a complete healthy candidate pool: a positive ceiling smaller than that
pool refuses the update without partially replacing it. Normal parallel updates
have zero planned unavailable workers. `--allow-downtime` authorizes stop-first
replacement explicitly; an app with no connected session needs no authorization
(see [Redeploy without interrupting the current version](cli.md#redeploy-without-interrupting-the-current-version)). A one-core node can admit fractional requests when the
serving and replacement requests, reserve, memory and pressure checks all fit.

Linux detection resolves process membership and controller mounts, including
nested systemd services, cgroup namespaces and v1/v2 controllers. Every visible
ancestor CPU limit and its usage are checked, even with a capacity override.
Memory headroom is the minimum of host availability and each finite ancestor's
remaining budget, including sibling workers. Missing or invalid accounting under
a finite limit refuses parallel starts. Hidden ancestors outside a container's
cgroup namespace cannot be measured; provision its visible allocation accordingly.

A refused upload returns HTTP 409 with `update_state: "blocked_capacity"`, the
resource, available and required budgets when known, and `automatic_retry: false`.
The working version keeps serving. Deployment history records the attempt as
`blocked`, with no release number; it does not mark a healthy app as needing
attention. App details show the most recent capacity block separately from app
health, and a successful retry clears it. Historical capacity failures that
preserved the working version are reclassified on upgrade.

`fleet apply` retries measured CPU pressure and CPU-budget shortages that would
fit once draining predecessors retire, bounded by `--capacity-wait` (default
`5m` per app). It also retries surge-ceiling shortages caused by retiring
generations. For a `10m` drain timeout, use `--capacity-wait 11m` to cover that
window and a retry interval. A permanent worker-budget shortage still fails
immediately. The CLI owns this wait; an individual API refusal is not a queued
deployment. Free or add
capacity, then retry the deployment. `--allow-downtime` explicitly permits
stop-first replacement when simultaneous pools cannot fit. Independently
draining generations and reload-relayed sessions must retire first; this flag
does not bypass their existing ownership fence. ShinyHub does not
provision additional instances automatically.

Scheduled rolls use their existing durable `deferred_capacity` queue and retry
backoff for both memory and CPU shortages; their `roll_fallback` policy controls
whether an explicit stop-first fallback is allowed.

### Worker isolation and generation handoff

Code updates to an app using `grouped` isolation can deploy without stopping
its existing workers. ShinyHub prepares the replacement bundle and health-checks
one new worker before publishing it. Existing client bindings and WebSockets
remain on the old version; new clients use the replacement. The **Switch to
latest** action gives that browser a new binding without moving other clients.
No additional flag is needed, and `--allow-downtime` still permits a fallback
rather than forcing one.

This path also supports switching between `grouped` and `multiplex` isolation
on the default native tier of a single server. The candidate starts with its
target policy; each generation retains its own recorded isolation. Metadata,
resource limits, worker policy, and schedule declarations
can be staged and published with the replacement when no producer needs to run.
Hooks, deploy-time shared producers, other isolation modes, and unsupported placement
still require stop-first deployment. While a session is connected the deploy is
refused and the refusal names the reason; with none connected it proceeds
stop-first on its own and reports the same reason as `stop_first_reason`. A consecutive
compatible deploy can publish another generation while earlier sessions drain.
Each outgoing generation retains its original retirement deadline; a later
deploy does not extend it. Candidate admission uses the host's remaining memory,
so retained generations can defer a new handoff for capacity. Unconfirmed
process cleanup still fences new launches, and stop-first operations wait for
all retained generations. Rolling restarts can also proceed beside a previous
deploy's drain; a currently executing roll remains serialized.

The server reserves memory for the candidate and configured warm spares, using
explicit limits or observed worker startup peaks. A candidate that fails
readiness leaves the old version and its configuration serving.

An outgoing multiplex generation retains already-open HTTP streams and
WebSockets; subsequent requests use the new generation. An outgoing grouped
generation also retains its existing client bindings through reconnect grace.
New clients always use the target mode. Structural settings changes, scaling,
and unrelated rolling activations defer until the previous drain completes.

After a hub restart, recorded isolation determines how living processes are
interpreted even if the fleet default changed. Grouped client bindings live
in hub memory and cannot be recovered after a restart; their workers are
confirmed stopped before fresh demand-driven workers start. A verified cold
start with no old process, route, or cleanup identity may adopt newly desired
isolation for the same bundle. Suspended apps resume with their recorded
isolation; a saved isolation change takes effect after a cold replacement.

Old workers count as draining and do not accept new clients or consume the new
generation's `max_workers` allowance. They retain memory until their clients
finish, including the reconnect grace window. `server.drain_timeout` remains
the hard limit: sessions still using the old version at that deadline are
terminated. Further workers and warm spares start on demand using the newly
published bundle. A server restart ends grouped sessions and cleans up recorded
workers before starting a fresh pool; this feature covers app deployment, not
session preservation across a server restart.

Re-running fleet apply recomputes current state. Already completed resources
become unchanged, so they are not applied twice. Deploy attempts automatically
retry only readiness timeouts, transport failures, and server errors; config
patches retry only server-side failures. Invalid bundles, missing runtimes,
build/hook failures, validation errors, conflicts, and crashes are not repeated
implicitly. Once an upload has committed, its retry budget re-checks health and
digest readback without uploading the bundle again. Source uploads on current
servers are also fenced by the digest and fleet owner observed by plan, so a
late overlapping apply conflicts before promotion instead of becoming the last
writer.

Older servers without fleet preconditions remain usable in a clearly marked
degraded mode. Config reconciliation narrows the race with a fresh read, while
pruning is disabled unless the operator explicitly accepts the risk with
`--allow-unsafe-degraded-prune`.

## Automation contract

Request JSON explicitly instead of parsing terminal text:

```bash
shinyhub plan . --output json
shinyhub plan . --detailed-exitcode --output json
shinyhub fleet plan -f fleets/eu.toml --output json
shinyhub fleet apply -f fleets/eu.toml --output json
```

The plan envelope includes a schema version, typed resources and values,
impacts, warnings, counts, and next actions. Single-app output also includes the
bundle, launch, manifest, remote state, and deploy command. Fleet apply adds the
run ID, run status, per-resource result and mutation state, summary exit fields,
and structured recovery guidance.

Plan exit codes:

| Code | Meaning |
|---:|---|
| `0` | Plan printed; with detailed exit codes, content is unchanged |
| `1` | Local validation or CLI/server protocol compatibility failed |
| `2` | Detailed mode only: content is new, changed, or cannot be compared |
| `3` | Network, authentication, or authorization failed |
| `6` | The host answered, but ShinyHub was not ready |

Fleet apply additionally uses `4` for partial convergence and `5` for a remote
precondition conflict. A successfully printed ordinary plan returns `0` unless
`--detailed-exitcode` (or its `--fail-on-changes` alias) was requested.

## Fast path

For an interactive deployment where a separately approved artifact is not
needed:

```bash
shinyhub doctor .
shinyhub plan .
shinyhub deploy . --open
```

`--open` waits for health, verifies the routed app when possible, and opens its
canonical URL. The plan's final command remains copy-pasteable and uses `--wait`
for automation; replace it with `--open` for the browser handoff.

Startup warns after worker recovery if the local live fleet plus one surge
candidate cannot fit; with an empty fleet it checks the minimum serving-plus-surge
pair. The warning is a snapshot: later wakes and starts can consume headroom.
`validate-config` checks that minimum pair on the machine running the command
and reports effective cores and their source in JSON. Run it on the server.
Validation does not inspect the
running fleet or sample pressure, so a successful validation cannot guarantee
admission. On a 1-core host the defaults budget four live workers and one
candidate at 0.5 cores plus a 0.1-core reserve. On a 16-core host, 17 live workers
and 16 candidates use 3.3 cores plus a 1.6-core reserve. Memory and measured CPU
pressure must still fit. Verify that a successful update publishes a new serving
generation; successful traffic probes alone can also describe a refused update.
