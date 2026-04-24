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

**Via API** (until a UI/CLI surface exists):

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

### Memory limits (`memory_limit_mb`)

`memory_limit_mb` is enforced **per replica** (Docker runtime). If
you set `replicas: 3` and `memory_limit_mb: 512`, each of the three
processes is allowed 512 MiB — total 1.5 GiB on the host. Size the
host accordingly.

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
from `memory_limit_mb` and `cpu_quota_percent` apply per container.

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
Content-Type: text/plain

service saturated; try again shortly
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
