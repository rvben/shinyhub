# Native process isolation

The native runtime launches each app as a plain OS process. By default that
process runs with the full filesystem reach of the ShinyHub service user. The
**isolation dial** narrows that reach without requiring the Docker runtime, using
[Landlock](https://docs.kernel.org/userspace-api/landlock.html) - the Linux
kernel's unprivileged, self-imposed access-control mechanism.

It is a **blast-radius boundary**, not a defense against determined hostile code.
If your threat model is genuinely untrusted app code in a multi-tenant setting,
run the [Docker runtime](../README.md#docker) instead, which adds process and
user-namespace isolation, and can isolate apps from each other on the network:
each app runs in its own container, and all app containers share a dedicated
bridge network (`shinyhub-apps`) created with inter-container communication
(ICC) disabled. The proxy always reaches apps over the loopback ports published
on the host, so app-to-app traffic on that network is what ICC blocks.

**Enforcement depends on the daemon.** Whether ICC-disabled actually blocks
container-to-container traffic is enforced by the Docker Engine's netfilter
(iptables/nftables) rules, which is a Linux Docker Engine feature. This is
verified on a stock Linux Engine (Docker 29.x): a container on the ICC-disabled
network cannot open a connection to a sibling container, while on the default
bridge it can. Some developer daemons - notably **OrbStack** on macOS -
flat-route all containers for convenience and do **not** enforce Docker network
isolation at all (containers reach each other even across separate networks), so
this isolation is a property of a Linux Docker Engine deployment, not of every
daemon; confirm it on yours before relying on it for untrusted multi-tenant
code. Setting `runtime.docker.network_mode: host` opts out entirely (the
container shares the host network stack); use it only when apps are trusted.

## The dial

```yaml
runtime:
  native:
    isolation: standard   # off (default) | standard
```

Or via environment: `SHINYHUB_RUNTIME_NATIVE_ISOLATION=standard`.

| Level | Effect |
|-------|--------|
| `off` (default) | No isolation. The historical native behavior. |
| `standard` | Filesystem confinement (below) plus `NO_NEW_PRIVS` (blocks setuid privilege escalation). |

`strict` (tighter reads and network restriction) is reserved for a later release
and is rejected at load until then, rather than silently treated as `standard`.

## What `standard` does

The app process is left free to **read** the whole filesystem (so interpreters
and shared libraries load normally) but may **write** only to:

- its own deployment directory,
- its persistent per-app data directory (`storage.app_data_dir`), if configured,
- `/tmp` and `/dev` (scratch and device nodes such as `/dev/null`, `/dev/urandom`
  - dangerous device nodes stay protected by ordinary file permissions).

Everything else - other apps' bundles, the control-plane database, system
directories - is read-only to the app. `TMPDIR` is pointed at a private
directory inside the app's own tree so a well-behaved app gets an isolated
scratch area while `/tmp` remains available as a fallback.

To keep cache-writing launchers working under the read-only root, `TMPDIR`,
`UV_CACHE_DIR`, and `XDG_CACHE_HOME` are pointed at writable subdirectories of
the app's own tree. (`uv run` initializes a cache even with `--frozen
--no-sync`; without this redirect it would be denied and the app would fail to
start.)

Both the long-running app process and one-shot runs (scheduled jobs) are
confined the same way.

Enforcement is applied by a small re-exec step: ShinyHub launches the app through
a hidden `__sandbox` subcommand of its own binary, which imposes the Landlock
rules on itself and then executes the real app command. The app never sees the
sandbox policy in its environment.

## Dependency builds and post-deploy hooks

The dependency-build step (`uv sync` / `renv::restore`) and manifest `[[hooks]]`
commands execute deployer-controlled code (build backends, package configure
scripts), so on any kernel with Landlock they run under the same `standard`
confinement as the app process - independent of the isolation dial. Their
writable set is:

- the version's bundle directory (where `.venv` and the redirected
  `UV_CACHE_DIR` / `XDG_CACHE_HOME` / `RENV_PATHS_ROOT` caches live),
- the app's managed-Python store (below),
- `/tmp` and `/dev`.

Their environment is the same allow-listed subset of the service environment
that app processes receive (see [Environment](environment.md)): locale,
proxies, TLS trust, tool cache dirs, and package-index configuration
(`UV_EXTRA_INDEX_URL` and friends) pass through; everything else - including
any credential in the service environment - is scrubbed, and
`SHINYHUB_APP_ENV_ALLOW` extends the list. The app's own stored env vars
(including decrypted secrets) are layered on top, so builds and hooks see what
the app sees at start. The sandbox does not restrict network egress (Landlock
is filesystem-only), so dependency downloads reach PyPI/CRAN or a configured
private index normally.

### Managed Python interpreters

When no system Python satisfies a bundle's `requires-python`, uv downloads a
managed interpreter at build time. uv keeps that store in a data directory
(`UV_PYTHON_INSTALL_DIR`), not in its cache, so the build sandbox gives it a
dedicated writable location: `<apps_dir>/<slug>/uv-python`, a sibling of the
app's `versions/` directory. One download serves every version of the app,
survives version-retention pruning, and is removed with the app. The store is
deliberately per-app rather than shared: a shared writable store would let one
app's build backend tamper with an interpreter that another app executes.

Operators who prefer a single shared store (for example, to download each
interpreter once per host) can set `UV_PYTHON_INSTALL_DIR` in the service
environment. The build sandbox honors it: the directory becomes the writable
managed-Python store for every build and hook, and the variable passes through
to app launches. This trades cross-app interpreter isolation for a shared
download; choose it only when all deployers are trusted.

A build step that is still denied a write (a tool writing outside the set
above) fails with the sandbox's writable paths named in the error, rather than
only the tool's raw "Permission denied".

## Requirements and graceful degradation

Isolation is **Linux-only** and **best-effort**:

- On a kernel with Landlock (roughly 5.13+, with the feature compiled in and
  active - e.g. Ubuntu's stock kernels), the confinement above is enforced.
- On an older kernel, or one without Landlock (some minimal/microVM kernels), the
  dial degrades to a no-op: the app still starts, just without confinement.
- On non-Linux builds there is no enforcement backend; configuring isolation logs
  a startup warning and runs without it.

Because Landlock only downgrades and never blocks startup, turning the dial on is
safe to roll out; where the kernel supports it, it takes effect, and where it does
not, apps keep running.

## Choosing native+isolation vs Docker

| | Native + `standard` | Docker runtime |
|---|---|---|
| Filesystem write confinement | yes (Landlock) | yes |
| Privilege-escalation block (`NO_NEW_PRIVS`) | yes | yes |
| Process / PID isolation | no | yes |
| Network isolation | no | yes (namespace) |
| Separate user boundary | no (same UID) | yes |
| Needs a container runtime | no | yes |

Reach for native isolation when you want meaningful hardening of the lightweight
native runtime; reach for Docker when you need full multi-tenant isolation.

---

## Worker isolation (session isolation dial)

The **worker isolation dial** controls how many browser sessions share a single
R/Python worker process. Changing this dial trades resource consumption for
performance isolation between concurrent users.

### Modes at a glance

| Mode | Clients per worker | HOL blocking | RAM scaling | Performance isolation | Governed by |
|---|---|---|---|---|---|
| `multiplex` (default) | Unbounded (all sessions share one process) | Yes - a heavy session stalls others | One process per replica; scales with `replicas` | None within a replica | `replicas`, `max_sessions_per_replica`, autoscale |
| `grouped` | Up to `grouped_size` clients per worker | Reduced (only within the group) | One process per group; scales with active groups up to `max_workers` | Partial - groups are isolated from each other | `grouped_size`, `max_workers`, `max_session_lifetime_secs` |
| `per_session` | 1 client per worker (one process per browser client) | Eliminated | One process per active client; scales with active clients up to `max_workers` | Full - each client runs in its own process | `max_workers`, `max_session_lifetime_secs` |

`multiplex` is the historical mode and is unchanged by this feature. `grouped`
and `per_session` are **elastic**: workers are started on demand and
terminated when their clients disconnect. Both elastic modes require a
single-node deployment (see caveats below).

### Config surface

#### Per-app dial

**Via CLI:**

```bash
# Switch to per_session isolation with a ceiling of 30 workers
shinyhub apps set <slug> \
  --isolation per_session \
  --max-workers 30

# grouped: up to 5 clients per worker, ceiling 20 workers
shinyhub apps set <slug> \
  --isolation grouped \
  --grouped-size 5 \
  --max-workers 20

# Absolute worker lifetime backstop: terminate after 3600 s regardless of activity
shinyhub apps set <slug> --max-session-lifetime 3600

# Revert to multiplex (drops the elastic pool)
shinyhub apps set <slug> --isolation multiplex
```

Verified CLI flags (`shinyhub apps set --help`):

| Flag | Description |
|---|---|
| `--isolation multiplex\|grouped\|per_session` | Session isolation mode |
| `--grouped-size N` | Clients per worker when using `grouped` (>= 1) |
| `--max-workers N` | Demand-driven worker ceiling for `grouped`/`per_session` (>= 1) |
| `--max-session-lifetime SECS` | Absolute worker lifetime in seconds; 0 = unlimited |

**Via PATCH API** (for tooling):

```bash
curl -X PATCH https://shinyhub.example.com/api/apps/<slug> \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "worker_isolation": "per_session",
    "worker_max_workers": 30,
    "worker_max_session_lifetime_secs": 3600
  }'
```

PATCH keys: `worker_isolation`, `worker_grouped_size`, `worker_max_workers`,
`worker_max_session_lifetime_secs`.

#### Via manifest (travels with the bundle)

Declare the policy in `shinyhub.toml` so it is reconciled on every deploy:

```toml
[app.worker]
isolation              = "per_session"
max_workers            = 30
max_session_lifetime_secs = 3600
```

For `grouped` mode, also set:

```toml
[app.worker]
isolation    = "grouped"
grouped_size = 5
max_workers  = 20
```

Manifest fields (`WorkerManifest`, `internal/deploy/hooks.go`):
`isolation`, `grouped_size`, `max_workers`, `max_session_lifetime_secs`.

When the `[app.worker]` block is present, all four columns are reconciled on
every deploy. Omitting the block leaves any previously-set value unchanged
(same semantics as the `[app.autoscale]` block).

#### Fleet defaults (server-wide)

```yaml
# shinyhub.yaml
runtime:
  default_worker_isolation: multiplex   # multiplex | grouped | per_session

server:
  host_budget_mb: 0                     # 0 = no host-level guard; > 0 = MiB limit
  min_available_memory_mb: 256          # unset = 256 (default floor); 0 = disable; > 0 = MiB floor
```

Env vars: `SHINYHUB_RUNTIME_DEFAULT_WORKER_ISOLATION`,
`SHINYHUB_SERVER_HOST_BUDGET_MB`, `SHINYHUB_SERVER_MIN_AVAILABLE_MEMORY_MB`.

`default_worker_isolation` is the fallback applied when an app's
`worker_isolation` is empty. The default is `multiplex`, preserving backward
compatibility for every existing app.

`host_budget_mb` enables the host-capacity guard (see below). Leave it at 0
to disable the guard.

### Caveats and limitations (Phase 1)

**`per_session` isolates the browser CLIENT, not the tab.** A "client" is a
cookie identity. Multiple tabs in the same browser profile share a single
cookie jar and therefore share the same worker process. True per-tab
isolation is not available in Phase 1.

**Single-node only.** `grouped` and `per_session` require a single-node
deployment. Setting either mode when the server is configured with a Postgres
(clustered) DSN is rejected by the PATCH API and the deploy pipeline - the
server itself boots normally; the error surfaces when you save the worker
settings or deploy a manifest that sets the mode. `multiplex` retains full HA
behavior and is unaffected.

**Cold start per session (no warm pool yet).** In Phase 1 there is no warm
pool. The first request from a new client cold-starts a fresh worker process
and the user sees the "Loading..." page while it boots. Subsequent requests
from the same client route to the warm worker. Warm spare pre-allocation is a
Phase 2 item.

**Cold bursts pack onto provisioning workers.** In `grouped` mode a new
client is placed on the fullest worker that still has room under
`grouped_size` - including workers that are still booting. Clients placed on
a booting worker wait on the loading page and connect when it registers. A
burst of simultaneous cold arrivals therefore provisions
`ceil(clients / grouped_size)` workers and sheds nobody until the full
`max_workers x grouped_size` ceiling is genuinely reached, instead of
reserving one worker per arrival and shedding at `max_workers` clients.

**WebSocket upgrades are parked during boot.** An upgrade whose pinned
worker is still booting is held - up to 30 seconds, released immediately if
the client disconnects - and forwarded once the worker registers, instead of
being answered with the loading page (a non-101 that hard-fails WS clients;
browsers re-enter via the splash's reload loop, scripted clients cannot).
If the boot exceeds the window the upgrade falls back to the loading page.

**Per-worker capacity view.** `shinyhub apps show <slug>` renders the live
worker table for elastic apps - slot, routing status (booting/running/
draining), bound sessions against `grouped_size`, pid, port - plus the
admission-ceiling arithmetic, and `shinyhub apps metrics <slug>` adds
per-worker CPU and RSS. The same data rides the app envelope (`worker_pool`)
and the metrics poll consumed by the dashboard. Tune `grouped_size` and
`max_workers` against this real session distribution instead of counting
worker processes by hand.

**Per-worker cgroup limits.** Each elastic worker receives the FULL per-app
`memory_limit_mb` and `cpu_quota_percent`, NOT a fraction. With `max_workers = 30`
and `memory_limit_mb = 512`, the worst-case host RAM for that one app is
`30 * (512 + 150) = ~19 GiB` (150 MiB base overhead per worker). Size the
host accordingly.

**Host-capacity guard.** When `server.host_budget_mb` is set, the API and
deploy pipeline reject any combination where
`max_workers * (memory_limit_mb + 150 MiB)` exceeds the budget. The check
runs when worker settings are saved (via the API or a manifest deploy) and
when an app's `memory_limit_mb` changes, not on server startup and not on
each incoming request. Set the budget to catch misconfigured limits early. The guard is inert unless the app also has an
effective memory limit (per-app or tier default); saving elastic worker
settings with NO active memory guard succeeds but returns an
`X-ShinyHub-Warning` header, which `shinyhub apps set` prints to stderr.

**Runtime memory floor (on by default).** `server.min_available_memory_mb`
is the runtime companion to the static budget: while the host's available
memory (`MemAvailable`) is below the floor, requests that would allocate a
NEW worker are shed with `503` (`Retry-After: 5`,
`X-Shinyhub-Reject: memory-pressure`) instead of spawning. Sessions already
bound to a worker keep routing, and the floor releases as soon as memory
recovers - no restart needed. When the key is not set at all, a default
floor of 256 MiB applies: an elastic OOM takes out a whole worker plus every
session bound to it, so the unset state fails safe, while 256 MiB is low
enough that a host with genuine headroom never sheds. Set an explicit `0`
(or a negative value) to disable the floor entirely - doing so without
arming the static budget guard triggers the unguarded-isolation warning.
Shedding one incoming session is deliberate: without the floor the backstop
is the kernel OOM killer, which kills a live worker together with every
session on it. The `memory-pressure` reject reason is distinct from
`pool-saturated` so autoscaling does not read memory pressure as a scale-up
signal. If the memory reading is unavailable, the floor fails open
(admission proceeds). The floor is consulted only on elastic worker
allocation; multiplex deployments never probe it.

**`max_workers x grouped_size` is a hard ceiling; overflow yields 503.** A
new client is shed with `503 Service Unavailable` and a `Retry-After: 5`
header only when every worker (running or booting) is at its per-worker cap
AND `max_workers` slots are occupied - `max_workers x grouped_size` clients
in `grouped` mode, `max_workers` in `per_session` mode. The shed client is
not queued.

**`max_session_lifetime_secs` is an absolute backstop.** When set (> 0), a
worker is terminated after that many seconds regardless of activity. Clients
whose worker is terminated will be reallocated to a new worker on their next
request (another cold start). Set this to reclaim long-lived workers from
abandoned sessions.

**Not-yet-connected clients are reclaimed after a grace period.** When a new
client is placed on a cold-starting worker (loading page), capacity on that
worker is held for it. If the client never opens a real connection after the
worker becomes ready (e.g. the tab was closed), its claim is automatically
released 15 seconds after the worker finishes booting; a worker whose last
claim is released terminates. This prevents abandoned cold-start flows from
leaking worker capacity. A client that does connect within those 15 seconds
cancels the reclaim and proceeds normally. `max_session_lifetime_secs` is
therefore not the only reclaim path; the 15-second grace window handles the
"seen the loading page, then disappeared" case independently.

**Elastic workers are ephemeral.** They are NOT re-adopted on a server
restart. The elastic pool starts empty after every restart. Connected clients
will cold-start a new worker on their next request.

**Elastic apps skip fixed-replica booting at deploy.** For `grouped` and
`per_session` apps, the deploy pipeline boots no replicas. The app is marked
running, but there are no warm processes yet. Boot errors surface at
first-session time, not at deploy time.

**Elastic apps are still prepared at deploy.** The dependency build
(`uv sync` / `renv::restore`) and the manifest's `[[hook]] on = "post-deploy"`
blocks run once per deploy, before any worker can serve a request, exactly as
they do for a multiplex pool. A failure in either fails the deploy. Preparation
cannot be deferred to the workers: they launch with `uv run --frozen
--no-sync`, which performs no dependency work of its own, and a worker spawn
happens long after the deploy has reported its result, so a failure there would
be unattributable. The same runtime rule applies as everywhere else: under a
container runtime the host does not prepare deps, so hooks are skipped and the
skipped count is reported back to the developer.

**Compute-idle reclaim is Phase 3.** There is currently no mechanism to
suspend or throttle a live-but-idle worker in between requests. An allocated
worker holds its full resource slice until the client disconnects (or
`max_session_lifetime_secs` expires).

### How to enable

1. **Set the dial via CLI**:

   ```bash
   shinyhub apps set myapp --isolation per_session --max-workers 20
   ```

   The new routing policy is applied immediately, and any
   currently-connected sessions cold-start in their new worker on their
   next request.

   On an app whose status is **running**, changing any worker dial also
   triggers a redeploy in the background. That tears down and
   re-registers the pool, and re-runs the dependency build and the
   manifest's `[[hook]] on = "post-deploy"` steps before workers serve
   again. Treat it as a full deploy, not a routing tweak: it drops the
   current pool, and a failing build or hook will fail it and can leave
   the app degraded. On a stopped or hibernated app no redeploy is
   triggered and the change is a pure metadata update.

2. **Or declare it in `shinyhub.toml`** (recommended for reproducible fleets):

   ```toml
   [app.worker]
   isolation   = "per_session"
   max_workers = 20
   ```

   The block is reconciled on every subsequent deploy, so the policy
   survives host rebuilds.

3. **Note on isolation changes.** Switching from `multiplex` to `grouped` or
   `per_session` (or between elastic modes) clears the current pool and drops
   all active sessions. Plan the change for a low-traffic window if session
   continuity matters.

4. **Verify.** After enabling, check `shinyhub apps show <slug>` (or the
   Configuration tab in the UI) to confirm the dial is set as expected.

---

## HOL-elimination acceptance (k6)

Worker isolation at `per_session` eliminates head-of-line (HOL) blocking: a
CPU-heavy session cannot starve other sessions because each session runs in its
own worker process.

### Mechanism

Under the default `multiplex` mode all sessions share one R/Python worker
process. A long-running computation monopolises that process's event loop; other
sessions stall until it yields. Observed degradation at N=50 with one CPU-heavy
session: render latency rises from ~0.8 s (p50 single-session baseline) to ~12 s
(p50 under HOL pressure).

Under `per_session` the heavy session runs in an isolated process and cannot
block others. Non-heavy sessions' latency stays near the single-session baseline.

### Setup

Deploy the same app twice under different slugs and set the worker isolation
mode via the API or the CLI:

```sh
# Deploy once as multiplex (default)
shinyhub deploy /path/to/heavy-app --slug demo-mux
# worker_isolation defaults to multiplex; no extra step needed

# Deploy again as per_session
shinyhub deploy /path/to/heavy-app --slug demo-iso
shinyhub apps set demo-iso \
  --isolation per_session \
  --max-workers 60
```

The "heavy app" must perform **sustained** CPU-intensive work that stays busy for
at least `LT_HOLD` seconds (default 60) - long enough to keep the `multiplex`
event loop occupied throughout the entire light-VU measurement window. A
one-shot computation that finishes in a few seconds lets both `multiplex` and
`per_session` phases show low latency and the `p(95) < 3000` threshold passes
vacuously with no diagnostic value.

Use a continuously-busy app design: a tight compute loop driven by
`reactiveTimer`/`invalidateLater` (R/Shiny), or a long-running matrix or numeric
computation that iterates for the full session duration. The goal is that the
`multiplex` phase actually degrades (latency rises toward the ~12 s figure
described above) so the gate is meaningful.

k6 just holds the heavy VU's WebSocket open while measuring latency for the
other VUs.

### Command

```sh
# Side-by-side run: records both multiplex and per_session in one k6 invocation
make load-test-isolation \
  LT_SLUG_MUX=demo-mux \
  LT_SLUG_ISO=demo-iso \
  ASSERT=1

# per_session only (threshold still checked):
make load-test-isolation LT_SLUG_ISO=demo-iso ASSERT=1
```

Full parameter reference (all optional except at least one slug):

| Variable | Default | Description |
|---|---|---|
| `LT_SLUG_MUX` | (empty) | Slug of the multiplex-mode app (the "before" evidence) |
| `LT_SLUG_ISO` | (empty) | Slug of the per_session-mode app (the "after" evidence) |
| `LT_HOST` | `http://127.0.0.1:8080` | ShinyHub server base URL |
| `LT_SESSIONS` | `50` | Total concurrent VUs per phase (1 heavy + N-1 light) |
| `LT_RAMP` | `30s` | Ramp duration to reach the target VU count |
| `LT_HOLD` | `60` | Seconds the heavy VU keeps its WebSocket open |
| `LT_WS_PATH` | `/websocket/` | WebSocket endpoint beneath the app root |
| `LT_FIRST_MSG_TIMEOUT` | `5` | Seconds to wait for the first server frame |
| `LT_AUTH_COOKIE` | (empty) | Cookie header value for private apps |
| `ASSERT` | `0` | Set to `1` to enable k6 thresholds (fails CI if HOL not eliminated) |

### Threshold (ASSERT=1)

```
hol_light_ms{mode:iso}  p(95) < 3000 ms
```

The 3 s threshold is 3x the ~800 ms single-session baseline and well below the
~12 s multiplex-degraded figure. The run FAILS if `per_session` does not
eliminate HOL blocking. The `multiplex` phase carries no threshold - it is
expected to degrade and its numbers serve as the "before" evidence.

### Results

Run the command above and record the headline output from k6:

```
HOL ELIMINATION RESULTS:
  multiplex   (demo-mux): established X/49 (Y%)  light_ms p50=??   p95=??
  per_session (demo-iso): established X/49 (Y%)  light_ms p50=??   p95=??
```

Fill in the recorded numbers below after the live acceptance run.

| Metric | multiplex (before) | per_session (after) | Pass? |
|---|---|---|---|
| light sessions established | pending | pending | - |
| light_ms p50 | pending | pending | - |
| light_ms p95 | pending | pending | p95 < 3 s required |
| threshold result | n/a (no threshold) | pending | pending |

Acceptance criterion G2 is met when the `per_session` p95 row shows a value
below 3 s and the k6 run exits 0 with ASSERT=1.
