# Mixed Linux load test

This rig measures the real ShinyHub server under concurrent authenticated HTTP,
WebSocket, reporting, and wake-up traffic. It creates its own disposable Linux
container and SQLite volume, deploys two apps through the CLI, seeds retained
usage history, drives the server from the host, and removes the container,
volume, and synthetic credentials on exit. It never selects an existing server
or saved CLI connection.

Requirements: Python 3.10+, Go matching the repository, k6, and a running local
Docker engine using cgroup v2. Cache `debian:bookworm-slim` first. The runner resolves the image
to its immutable local ID and records it; it does not pull images or publish
anything. Binaries are cross-built for the Docker engine's architecture.

```sh
# Fast harness control, including auth, actual deployment and usage persistence.
python3 loadtest/mixed/run.py --steps 1 --seconds 10 --sessions 10000 --report-interval 2

# Default saturation sweep: two CPU quota, 2 GiB memory, 100k retained sessions.
make load-test-mixed

# Repeat a bracket around the first failing stage with a longer observation.
python3 loadtest/mixed/run.py --steps 50,75,100 --seconds 60

# Repeat identical sweeps on three fresh targets; reject busy hosts first.
python3 loadtest/mixed/run.py --steps 1,100,150,200 --seconds 60 --repeats 3 --require-quiet

# Exercise the repeated-run workflow locally at low load.
python3 loadtest/mixed/run.py --steps 1 --seconds 10 --sessions 10000 --repeats 3

# Vary the server budget or report workload independently.
python3 loadtest/mixed/run.py --cpus 4 --memory 4g --report-interval 2

# Deterministic harness and database-telemetry checks.
make test-load-mixed
```

Results land in `loadtest/results/mixed-<UTC timestamp>-<random suffix>/` and are
ignored by Git. `REPORT.md` is a readable summary; `stages.json` contains stage
boundaries, verdicts, measured resource use and durable session counts.
`metadata.json` records source commit/dirty state, tool versions, image ID,
binary SHA-256 checksums, host platform and workload parameters. Keep the same
image, CPU/memory limits, history size and driver for comparisons.

With `--repeats`, every sweep gets a fresh container and seeded database. A
separate `repeat-<id>/REPORT.md` and `comparison.json` compare the completed
runs. Each stage is a consistent pass, consistent saturation, variable, or
insufficient evidence. Missing runs and mismatched binaries/workloads cannot
produce a consistent-pass result. Per-run percentiles are listed separately;
they are never averaged or presented as a pooled percentile.

## Workload

- At N clients, the HTTP stream offers **2N uncached page navigations/second**;
  each navigation fetches approximately 32 KiB HTML plus JavaScript and CSS.
  This is deliberately an aggressive gateway workload, not a claim that every
  production user navigates twice per second. k6 uses an arrival-rate executor;
  slow responses do not silently reduce offered load. VUs are bounded, and
  dropped iterations fail the stage.
- A separate stream holds **N WebSockets** for the stage duration, sending and
  checking an echo heartbeat every second. Establishment requires the fixture's
  first frame; success requires the socket to stay open and exchange heartbeats.
  Session cookies and affinity cookies are forwarded through the real proxy.
- One administrator requests a **7-day usage report every five seconds**,
  independently of the page and session streams. The report must include all
  seeded sessions. After each stage, the rig waits for successful WebSocket
  sessions to appear in the durable usage count. Lost observations invalidate
  the run rather than looking like a successful performance optimization.
- A separate app is explicitly **put to sleep and woken by a page request**
  about every 10 seconds. Its fixed 750 ms startup delay gives wake-up timing a
  known floor. The continuously used app is never slept by this scenario.
- The deployed Go fixture has no external runtime dependencies. It isolates
  gateway costs and implements ordinary HTTP and WebSocket echo endpoints.
  It is **not** a Shiny rendering engine or a browser. Browser JS execution,
  application computation, Shiny-specific disconnect rules, TLS and remote
  network latency require separate tests; the existing `loadtest/render` rig
  covers the browser/render case.

The normal server admission logic, logging and usage persistence remain enabled.
Per-app session caps and render pacing are disabled explicitly in the fixture
manifest so the test explores server resources rather than an arbitrary
configured session ceiling. The HTTP user is a shared-app viewer; reports and
sleep operations use an administrator session token. Only two synthetic users
are created, so this does not model the cardinality of a large user directory.

## Evidence and verdicts

The initial latency objectives are versioned in `mixed.js`: page p95 <250 ms,
page/asset p99 <500 ms, report p95 <2 s, WebSocket establishment p95 <1 s,
heartbeat RTT p99 <250 ms, and wake p95 <3 s. Ordinary and session failure
rates must be below 1%; wakes and dropped iterations must have no failures.
These are exploratory defaults, not promised production SLOs. A one-client
stage must pass before a sweep starting at one continues. Missing samples
invalidate any stage. With deliberately overloaded targets, threshold failure
is an expected measurement; the runner records it and continues the sweep.
The runner exits nonzero for broken controls, incomplete evidence or tool errors.

Read percentile counts: a 30-second run produces only about six report samples
and three wakes, so their upper percentiles are provisional. Repeat longer near
the threshold and look for a consistent boundary. A "largest passing stage"
is only the highest tested load meeting these objectives, not a maximum safe
production session count.

- `metrics.ndjson`: two-second samples of server CPU, RSS, heap, goroutines,
  descriptors, database-pool wait counters and exceptional usage-persistence
  outcomes. `generator` contains host k6 CPU percentage and RSS in KiB.
  `driver_load_average` records the generator host's 1/5/15-minute load.
  `target` records cgroup v2 CPU usage, quota, throttled periods/time, and Linux
  kernel CPU ticks, CPU count and load averages. Missing target observations
  invalidate the stage. Reports include counter deltas and throttled-period
  fractions; throttled time is scheduler accounting, not request latency.
- `container.ndjson`: Docker CPU and memory statistics for the entire target,
  including managed apps. Docker's CPU percentage uses 100% for one CPU;
  a two-CPU quota approaches saturation at 200%. Unavailable Docker samples
  are counted separately and excluded from CPU summaries; a stage with no
  usable container samples is invalid.
- `*-cpu.pprof`: ten seconds of server CPU samples during each stage lasting
  at least 15 seconds. Profiling is held constant between stages; its small
  overhead is included. Use `go tool pprof` with `tmp/mixed-build/shinyhub`.
- `*-summary.json`, `*-k6.log`, deployment logs and server logs: request counts,
  latency distributions, failure thresholds, dropped work and diagnostics.

`shinyhub_db_wait_*` measures waits to acquire a connection from ShinyHub's pool,
not SQLite lock waits or query execution time. A slow report with low pool wait
and a SQL-heavy CPU profile points toward query work; rising pool waits indicate
queueing before execution. The host generator runs outside the target quota,
but shares the physical machine: check its utilization and repeat on a separate
load generator before making hardware-sizing commitments.

Ports bind only to host loopback. SQLite lives on a Linux Docker volume, not a
macOS bind mount. Metrics are forwarded through a fixture-only `/metrics`
listener; pprof remains container-loopback-only. Profiles are retrieved by a
short-lived helper inside the disposable container. To stop a run, send SIGINT
or SIGTERM and let cleanup finish. SIGKILL or an engine crash cannot run cleanup;
remove only resources named with that run's `shinyhub-mixed-<id>` prefix.

## Quiet-host checks and physical separation

`--require-quiet` takes two observations five seconds apart after deployment
and seeding, before each sweep. It rejects a run when either host's one-minute
load exceeds 0.5 per logical CPU or target-kernel CPU activity exceeds 20%.
The observations and reasons are saved in `preflight.json`. Busy or missing
observations stop the run; they do not silently become a pass. During traffic,
CPU throttling is measured rather than rejected, since saturation of the
configured CPU quota is an expected experimental outcome.

A quiet preflight is a measured starting condition, not a reservation of CPU
resources. Other workloads can start later. Inspect the continuous host-load,
CPU and throttling observations when comparing repetitions. Inside a VM,
`/proc` describes that Linux kernel, not other workloads on the physical
hypervisor; cgroup accounting also cannot reveal all ancestor/hypervisor limits.

This runner currently drives local Docker from the host: the target and k6
still share physical hardware. Three local low-load controls verify the rig,
but do not substitute for a quiet Linux target and a physically separate
load generator. That capacity experiment requires explicitly selected machines
and a transport appropriate to their environment. Do not point this runner at
a remote Docker context and assume local bind mounts or loopback ports work.
