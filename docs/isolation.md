# Native process isolation

The native runtime launches each app as a plain OS process. By default that
process runs with the full filesystem reach of the ShinyHub service user. The
**isolation dial** narrows that reach without requiring the Docker runtime, using
[Landlock](https://docs.kernel.org/userspace-api/landlock.html) - the Linux
kernel's unprivileged, self-imposed access-control mechanism.

It is a **blast-radius boundary**, not a defense against determined hostile code.
If your threat model is genuinely untrusted app code in a multi-tenant setting,
run the [Docker runtime](../README.md#docker) instead, which adds process, user,
and network isolation.

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
```

Env vars: `SHINYHUB_RUNTIME_DEFAULT_WORKER_ISOLATION`,
`SHINYHUB_SERVER_HOST_BUDGET_MB`.

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

**Per-worker cgroup limits.** Each elastic worker receives the FULL per-app
`memory_limit_mb` and `cpu_quota_percent`, NOT a fraction. With `max_workers = 30`
and `memory_limit_mb = 512`, the worst-case host RAM for that one app is
`30 * (512 + 150) = ~19 GiB` (150 MiB base overhead per worker). Size the
host accordingly.

**Host-capacity guard.** When `server.host_budget_mb` is set, the API and
deploy pipeline reject any combination where
`max_workers * (memory_limit_mb + 150 MiB)` exceeds the budget. The check
runs when worker settings are saved (via the API or a manifest deploy), not
on server startup and not on each incoming request. Set the budget to catch
misconfigured limits early.

**`max_workers` is a hard ceiling; overflow yields 503.** When all
`max_workers` slots are occupied, a new client receives `503 Service
Unavailable` with a `Retry-After: 5` header. The client is not queued.

**`max_session_lifetime_secs` is an absolute backstop.** When set (> 0), a
worker is terminated after that many seconds regardless of activity. Clients
whose worker is terminated will be reallocated to a new worker on their next
request (another cold start). Set this to reclaim long-lived workers from
abandoned sessions.

**Not-yet-connected clients are reclaimed after a grace period.** When a new
client triggers a cold start (loading page), the worker slot is held for that
client. If the client never opens a real connection after the worker becomes
ready (e.g. the tab was closed), the slot is automatically released 15 seconds
after the worker finishes booting. This prevents abandoned cold-start flows
from leaking worker capacity. A client that does connect within those 15 seconds
cancels the reclaim and proceeds normally. `max_session_lifetime_secs` is
therefore not the only reclaim path; the 15-second grace window handles the
"seen the loading page, then disappeared" case independently.

**Elastic workers are ephemeral.** They are NOT re-adopted on a server
restart. The elastic pool starts empty after every restart. Connected clients
will cold-start a new worker on their next request.

**Elastic apps skip fixed-replica booting at deploy.** For `grouped` and
`per_session` apps, the deploy pipeline returns immediately without booting
any replicas. The app is marked running, but there are no warm processes yet.
Boot errors surface at first-session time, not at deploy time.

**Post-deploy manifest hooks are skipped for elastic apps in Phase 1.** The
`[[hook]]` blocks in `shinyhub.toml` are not executed for elastic apps.
Run setup steps as part of the app's own startup instead.

**Compute-idle reclaim is Phase 3.** There is currently no mechanism to
suspend or throttle a live-but-idle worker in between requests. An allocated
worker holds its full resource slice until the client disconnects (or
`max_session_lifetime_secs` expires).

### How to enable

1. **Set the dial via CLI** (no redeploy needed for existing apps):

   ```bash
   shinyhub apps set myapp --isolation per_session --max-workers 20
   ```

   The change takes effect immediately for new incoming sessions. Any
   currently-connected sessions will cold-start in their new per-session
   worker on their next request.

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
