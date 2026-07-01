# Scaling apps

ShinyHub serves traffic through a fixed pool of **replica** processes
per app. New sessions without a sticky cookie are admitted up to a
**per-replica session cap**; beyond the cap, the proxy sheds with
`503 Service Unavailable` and a `Retry-After: 5` header so clients
back off cleanly instead of piling up in the event loop.

The two knobs are:

| Knob | Meaning | Default |
|---|---|---|
| `replicas` | Number of identical processes serving this app's traffic. | `1` |
| `max_sessions_per_replica` | Per-replica admission cap for new cookieless sessions. `0` means "use the runtime default". | `0` → runtime default `10` |

A session that's already been admitted keeps a sticky cookie
(`shinyhub_rep_<slug>`) and routes to the same replica until it
closes — the cap only gates *new* admissions.

The product of the two is the **admission ceiling**:

```
replicas × max_sessions_per_replica = concurrent new sessions served before 503
```

## Setting the knobs

**Via CLI:**

```bash
shinyhub apps set <slug> --replicas 3 --max-sessions-per-replica 10
```

Either flag may be set on its own; `--max-sessions-per-replica 0` resets the
cap to the runtime default. Both knobs are validated client-side (replicas
`>= 1`, cap `0..1000`) and applied atomically by `PATCH /api/apps/<slug>`.

**Via API** (for tooling integrations):

```bash
curl -X PATCH https://shinyhub.example.com/api/apps/<slug> \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"replicas": 3, "max_sessions_per_replica": 10}'
```

**Observing from the UI**: the Overview tab's **Replicas** card lists
each replica with its current session count and turns the session
badge red when the count reaches the cap. If you see red badges on
every replica during normal traffic, you're at the admission ceiling
and new users are getting shed.

## Picking values

### Default: `replicas=1`, cap=10

This is a sensible starting point for almost any app. Profiling
showed a single Shiny process stays healthy (p99 < 350 ms) up to
~10 concurrent sessions and degrades sharply beyond. The cap
prevents the 11th through Nth session from pulling p99 into the
seconds for everyone else; they get a clean 503 + `Retry-After`
instead.

### When to add replicas

Scale **horizontally** (raise `replicas`) when:

- The app is **CPU-bound** — its event loop is the bottleneck and
  extra cores actually help.
- You expect **more than ~10 concurrent users** steady state.
- Each replica's memory footprint × N still fits within your host
  budget.

Each replica is an independent process with its own memory, Python
interpreter, and warm state. Expect memory to scale linearly: 3
replicas use ~3× the RSS of one.

In measured terms, adding replicas at the saturation point is a
near-linear throughput win with no tail cost. One run at
30 concurrent clients produced:

| replicas | interactions / 30 s | errors | p99 ms |
|---------:|--------------------:|-------:|-------:|
| 1        | 1 679               |   16   |  296   |
| 3        | 3 543               |    0   |  349   |

### When to raise the cap instead

Raise `max_sessions_per_replica` (or set it to 0 for the runtime
default) when:

- The app is **I/O-bound** — sessions spend most of their time
  waiting on the network, the database, or a file read, not holding
  the event loop.
- Per-session CPU cost is very low (a few milliseconds per
  interaction).
- Adding replicas isn't an option (memory constraint or a stateful
  pattern that requires a single process).

Pure I/O-bound apps can often safely carry 30–50 sessions per
replica. Still put a ceiling on it — unbounded admission is how
event loops get into trouble under load spikes. A cap of 50 is an
order of magnitude better than no cap.

### Don't raise the cap for CPU-bound apps

If the event loop is the bottleneck, raising the cap just admits
more sessions into the same queue. Measured p99 on a CPU-bound app
climbs from ~350 ms at 10 sessions to multi-second tails by 30+
sessions on a single replica. Horizontal scaling fixes this;
vertical cap-raising makes it worse.

## Pre-warming

By default a hibernated app restarts on demand: the first request after an idle
period waits for Python (or R) to start, and that user sees the "Loading..."
page. `min_warm_replicas` changes this: instead of stopping every replica when
the app goes idle, the platform keeps at least N replicas running. The first
user after an idle period gets an instant response because at least one warm
process is already listening.

### What it does

- **Idle floor.** When the watcher would normally stop all replicas, it stops
  only enough to bring the pool down to `min_warm_replicas`. Those replicas stay
  alive and accepting connections.
- **Instant first response.** A user hitting a warm idle app skips the loading
  page entirely. Burst traffic re-expands the pool to full capacity (up to
  `replicas`) through the same admission logic as a running app; a single
  rejected request triggers burst expansion within one request cycle.
- **Unified scale-down floor.** Manual scale-down (`shinyhub apps set
  --replicas`) and autoscale both treat `min_warm_replicas` as a hard floor:
  neither path reduces the pool below it. When autoscale is enabled, the
  effective floor is the larger of `autoscale_min` and `min_warm_replicas`.
- **Reset interactions.** Deploying a new bundle or manually scaling up restores
  full capacity and boots any parked replicas. Setting `min_warm_replicas = 0`
  re-enables full hibernation (the platform reverts to stopping all replicas on
  idle). If the stored replica count is below the keep-warm floor, the platform
  self-clamps the floor to the replica count; the Configuration tab shows a
  warning when this condition is detected.

### Observability

- `warm_shrink` and `warm_expand` audit events are recorded whenever the
  watcher crosses the warm floor in either direction.
- Replicas that are stopped but parked (warm-floor slots) appear in
  `shinyhub apps show <slug>` so operators can see the pool state at a glance.

### High-availability note

In a multi-instance deployment the owning instance manages the shrink/expand
cycle; other instances converge to the same pool size through the shared replica
registry.

### Configuration

**Via manifest (`shinyhub.toml`):**

```toml
[app]
min_warm_replicas = 1
```

**Via CLI:**

```bash
shinyhub apps set <slug> --min-warm-replicas 1
```

**Via API:**

```bash
curl -X PATCH https://shinyhub.example.com/api/apps/<slug> \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"min_warm_replicas": 1}'
```

## Autoscaling

Autoscale adjusts an app's replica count automatically from session saturation:
it scales toward a **target** average number of active sessions per replica (a
fraction of the per-replica cap) and biases up when the pool sheds 503s.
Scale-up jumps the full delta in one step; scale-down removes one replica at a
time with a drain grace.

Autoscale is **opt-in at two levels**, and both must be true for it to run:

1. **Globally**, via `runtime.autoscale.enabled: true` in `shinyhub.yaml`. Off,
   and no app is autoscaled regardless of its per-app setting.
2. **Per app**, via the policy below. Off (the default), and the app keeps a
   fixed replica count.

### Declaring the policy (travels with the bundle)

Put the policy in `shinyhub.toml [app]` so it is committed with the app and
**reconciled on every deploy**. It then survives hosts rebuilt from config (CDK,
GitOps) instead of having to be re-applied by hand after each deploy:

```toml
[app]
replicas = 1                                # starting count; autoscale takes over from here
autoscale = { enabled = true, min_replicas = 1, max_replicas = 8, target = 0.8 }
```

- `enabled` turns the policy on (still gated on the global flag above).
- `min_replicas` / `max_replicas` are the bounds the controller stays within.
  When enabled they must be `>= 1` with `min <= max`, and `max_replicas` may not
  exceed the runtime `max_replicas` ceiling. The effective floor is the larger
  of `min_replicas` and `min_warm_replicas`.
- `target` is the target average active sessions per replica as a fraction
  `(0,1]` of the per-replica cap. `0.8` with a cap of `10` aims for ~8 sessions
  per replica before adding one. `0` inherits the runtime-wide default target.

The block is atomic: declaring it writes the whole policy; omitting it leaves
whatever was set imperatively untouched. A failed deploy reverts it to the
pre-deploy policy.

### Setting it imperatively

The same policy can be set without a redeploy:

```bash
shinyhub apps set <slug> --autoscale \
  --autoscale-min 1 --autoscale-max 8 --autoscale-target 0.8
```

or in the Configuration tab. A policy set this way is **lost when the host is
rebuilt from config**, so prefer the declarative form for reproducible fleets.

## Interaction with other features

### Output caching

A cross-session output cache (see
[recipes/output-caching.md](recipes/output-caching.md)) makes more
sessions fit under the same replica count. Measured on a CPU-bound
app with a small input domain, adding `@functools.cache` to a
module-scope helper restored throughput to 93 % of the driver
ceiling at 30-session offered load (up from 77 % uncached) and
dropped p50 from ~100 ms to ~3 ms. Cheap to try, big win when it
hits.

### Resource limits (`memory_limit_mb`, `cpu_quota_percent`)

`memory_limit_mb` and `cpu_quota_percent` are enforced **per replica**
in BOTH native and docker mode. If you set `replicas: 3` and
`memory_limit_mb: 512`, each of the three processes is allowed 512 MiB,
for 1.5 GiB total on the host. Size the host accordingly.
`cpu_quota_percent` is a percent of one core (100 = 1 core, 150 = 1.5
cores); a replica that exceeds `memory_limit_mb` is OOM-killed by the
kernel and surfaces through the crash path with a reason naming the limit.

Both can be set per app in `shinyhub.toml [app]` (travels with the bundle,
reconciled on deploy), via `shinyhub apps set --memory-limit-mb N
--cpu-quota-percent M`, or in the Configuration tab. Scheduled jobs inherit
the same per-replica ceiling.

Native enforcement uses cgroup v2 (`memory.max` / `cpu.max`) and is
**best-effort**: it requires the relevant controller to be delegated to the
service (systemd `Delegate=cpu memory`). Without delegation the limit is not
enforced (the app runs uncapped) and a warning is logged; the Configuration
tab shows whether enforcement is active. CPU enforcement additionally needs
`Delegate=cpu`; memory works with `Delegate=memory` alone.

### Hibernation

Hibernated apps restart on demand. The replica count and cap apply
once the pool is warm; during the first request burst after
hibernation, the first user waits for Python to start, and
subsequent users either share that warm-up (sticky cookie) or get
queued/shed by the same rules. Hibernation timeout interacts with
replica count: with more replicas, there's more warm-up cost on
wake, but each replica handles its share of the post-wake burst.

### Docker runtime

Replicas and cap work identically under `runtime.mode: docker`;
each replica is its own container. Container memory/CPU limits
from `memory_limit_mb` and `cpu_quota_percent` apply per container,
and are always enforced (no cgroup-delegation caveat as in native mode).

## Runtime-level defaults

Server-wide defaults in `shinyhub.yaml`:

```yaml
runtime:
  default_replicas: 1                       # applied to apps created without an override
  max_replicas: 32                          # admin-enforced upper bound on replicas
  default_max_sessions_per_replica: 10      # fallback when an app has cap=0
```

Corresponding env vars: `SHINYHUB_RUNTIME_DEFAULT_REPLICAS`,
`SHINYHUB_RUNTIME_MAX_REPLICAS`,
`SHINYHUB_RUNTIME_DEFAULT_MAX_SESSIONS_PER_REPLICA`.

Raising `default_max_sessions_per_replica` at the fleet level affects
every app that kept the default. Prefer per-app overrides unless
you've measured every app in your fleet and they all tolerate a
higher cap.

## What 503s look like to users

The proxy sheds with:

```
HTTP/1.1 503 Service Unavailable
Retry-After: 5
Content-Type: text/plain; charset=utf-8

Service temporarily at capacity, please retry.
```

Browsers that respect `Retry-After` (Chromium, Firefox) will back
off before reloading; users see a brief loading spinner and then
get in. A sustained red badge on every replica's Overview card is
the signal that 503s are happening in anger — either raise
`replicas` or investigate why sessions are lingering (a leaking
session, a chatty per-session WebSocket, etc.).

## Troubleshooting

**"I set replicas=3 but only one is running."** Wait for the
watcher — new replicas come up sequentially. Check the Replicas
card; each should transition `starting` → `running` within a few
seconds. If a replica sticks in `starting`, check its log via the
Logs tab for an import-time error.

**"Sessions stay on one replica even though I have 3."** Sticky
cookies route returning sessions back to their original replica.
This is correct for user session continuity. New sessions (no
cookie) round-robin across all replicas in least-loaded order. To
force redistribution, clear cookies (or restart the app, which
invalidates all stickies).

**"p99 is high but I don't see 503s."** The cap isn't saturated —
your bottleneck is the compute inside each admitted session. Either
scale replicas (if CPU-bound) or add output caching (if work is
repeatable across sessions).
