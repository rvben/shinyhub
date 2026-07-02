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
shinyhub apps set --slug demo-iso \
  --worker-isolation per_session \
  --worker-max-workers 60
```

The "heavy app" must perform visible CPU-intensive work on session connect (e.g.
a slow model fit or matrix computation in R/Python) to trigger meaningful HOL
pressure under `multiplex`. k6 just holds the heavy VU's WebSocket open while
measuring latency for the other VUs.

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
