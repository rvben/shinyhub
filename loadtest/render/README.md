# Render-saturation rig

Reproduces a production failure mode: a CPU-heavy Shiny dashboard, run
concurrently by several users on a small (2-core) host, misses its client
heartbeat and gets disconnected by the browser even though the server never
crashes, errors, or logs anything unusual. This rig exists to produce
evidence for that failure before any fix is designed, so the fix can be
justified against a real, reproducible number instead of a guess.

## Why the existing k6 scenarios cannot measure this

`loadtest/sessions.js` and `loadtest/hol.js` both use k6's `k6/ws` blocking
API: they open a WebSocket, hold it open for a fixed duration, and measure
time-to-first-frame and connection establishment. Neither scripted client
implements Shiny's heartbeat protocol, and neither performs periodic
interaction on the connection the way a real user does. An idle, silent
socket held open by k6 never triggers a heartbeat timeout, no matter how
saturated the server's CPU is, because there is no heartbeat traffic to miss
in the first place. The failure mode this rig targets is specifically a
client-observed heartbeat miss over a live, interacting session, which
requires a client that actually behaves like a browser: sends periodic
input, watches the connection, and notices when it drops. That is why this
rig drives real headless Chrome via Playwright instead of adding another k6
scenario.

## What the rig does

- `rig.sh` boots a 2-vCPU husker microVM running ShinyHub plus a synthetic
  Python Shiny app (`app/app.py`), pinned to a small core count so it
  reproduces the constrained-host condition instead of a beefy CI box.
- `app/app.py` exposes 6 render outputs behind an **Apply** button and 2
  tabs. Each output burns CPU for a configurable slice of wall time via a
  busy loop (`burn()`, never `time.sleep`), so it genuinely contends for the
  host's vCPUs the way a real computation-heavy dashboard does. Total
  render cost is controlled by the `RENDER_COST_MS` app environment
  variable (default 1300 ms, split evenly across the 6 outputs).
- `driver/drive.mjs` launches one browser with N contexts (one per
  simulated user), each loading the app and then clicking Apply / switching
  tabs on a fixed cadence until the run duration elapses or the session
  disconnects. It classifies each session as healthy, disconnected,
  degraded, or errored (`driver/detect.mjs`) and writes a JSON summary to
  `loadtest/results/` (gitignored; regenerate rather than expect it in git).

## Tuning render cost: `RENDER_COST_MS`

`rig.sh up` boots the VM and starts ShinyHub before the app is deployed, so
the script itself has no app process to set an environment variable on; a
variable read at that point would have nowhere to go. The synthetic app
reads `RENDER_COST_MS` from its own process environment (default 1300 ms),
and ShinyHub applies each app's stored environment variables at process
start. So the working way to tune render cost is after deploy, through the
already-authenticated CLI:

```bash
./bin/shiny env set rig RENDER_COST_MS=<ms> --restart
```

This was verified empirically, not assumed: a single-session run at the
1300 ms default measured `firstRenderMs` of 2821 ms and 2740 ms across two
runs; after `env set rig RENDER_COST_MS=6000 --restart`, an equivalent run
measured `firstRenderMs` of 7135 ms, a roughly 4.3 s increase for a nominal
+4.7 s change in configured cost, confirming the value actually reaches the
running app process rather than being silently ignored.

## Required environment

- `RIG_DAEMON_HOST` - the address this machine uses to reach the husker
  daemon running the rig VM. Required by every `rig.sh` and `make
  render-rig-*` invocation. Never commit a real value; examples in this
  file use the RFC 5737 documentation address `203.0.113.10`.
- `RIG_BROWSER_CHANNEL=chrome` - required for every driver invocation in
  this environment. Playwright's bundled chromium is fetched from a CDN
  that is not reliably reachable here; passing `channel: 'chrome'` drives
  the already-installed system Chrome instead of trying to download one.
  Without it, `load-test-render` fails at browser launch rather than
  producing a false result, but setting it explicitly avoids that failure
  and additionally drives a higher-fidelity target, since real Chrome is
  what actual users run.

## Commands

```bash
# Bring the rig up (prints the reachable base URL).
RIG_DAEMON_HOST=203.0.113.10 make render-rig-up

# Deploy the synthetic app once, then tune render cost as needed:
./bin/shiny login --host http://203.0.113.10:18080 --username admin --password admin
./bin/shiny deploy loadtest/render/app --slug rig
./bin/shiny apps access set rig public   # avoids managing a session cookie in the driver
./bin/shiny env set rig RENDER_COST_MS=1300 --restart

# Unit tests for the rig itself (burn calibration + verdict classifier).
# No VM or browser required; safe to run any time.
make test-render-rig

# Drive the rig with real headless browsers.
RIG_DAEMON_HOST=203.0.113.10 RIG_BROWSER_CHANNEL=chrome \
  make load-test-render RIG_SESSIONS=5 RIG_CADENCE_MS=2000 RIG_DURATION_S=180

# Tear down. Always run this, regardless of how the run ended.
RIG_DAEMON_HOST=203.0.113.10 make render-rig-down
```

`RIG_SESSIONS` is the concurrent session count, `RIG_CADENCE_MS` is the
interaction interval per session, `RIG_DURATION_S` is the run length.

## Control matrix

Runs must execute in this order. Each control gates the next: a failing
negative control means the rig itself is broken (bad app, bad driver, an
overloaded host unrelated to the app under test) and **voids the run**. Do
not proceed to the positive control or dose-response curve until the
negative controls pass cleanly.

| Run | N | Cadence | Purpose | Expected result |
|---|---|---|---|---|
| Negative control 1 | 1 | 2000 ms (aggressive) | One session can never contend with itself; must always survive. | 0% disconnects |
| Negative control 2 | 10 | 45000 ms (sparse) | Many sessions, but so lightly loaded the host is never saturated. | 0% disconnects |
| Positive control | 5 | 2000 ms (aggressive) | The condition believed to reproduce the production bug. | Disconnects |
| Dose-response | 3, 5, 8, 12 | 2000 ms (aggressive) | Show the failure scales with concurrency, not a fixed threshold artifact. | Non-decreasing disconnect rate |

## Measured results

Measured 2026-07-22 against a worktree build of ShinyHub at commit
`5b8431b` (branch `worktree-render-saturation-rig`; `/api/server-info`
reports `commit: 8131632e8d32` and `version: dev`, see "Known limits and
surprises" below for why the reported commit is not the one actually
built), `RENDER_COST_MS=1300` (default), rig VM pinned to 2 vCPUs / 4096
MiB, driver on a 12-core host with `RIG_BROWSER_CHANNEL=chrome`.

**Negative control 1 was re-run and its number below is from the re-run, not
the original Task 6 run.** The original run (`render-1s-2000ms-2026-07-22T11-07-42-607Z.json`)
was captured before the `waitForFunction` timeout fix described below (every
session-establishment check, including NC1's, goes through that call), so it
was evidence gathered under a known-buggy harness even though its result
happened to pass. It was re-run on a fresh rig VM under the fixed harness
(`render-1s-2000ms-2026-07-22T11-53-59-678Z.json`, worktree HEAD
`2b923ddafd4a805df2fab630c380bc471f9442f4`, which includes the fix) and
produced the same gating result: `disconnectRate: 0`, `actionSuccessRate: 1`,
session established (`firstRenderMs` 2725 ms vs. the original run's 4733 ms,
both far under any timeout, as expected for two independent runs). See
`.superpowers/sdd/task-6-report.md`, "Re-run: NC1 under the final harness",
for the full before-and-after comparison. All other rows below are
unaffected: the positive control and dose-response runs (11:20:42 onward)
were already captured after the fix.

| Run | N | Cadence | Disconnect rate | Action success rate |
|---|---|---|---|---|
| Negative control 1 (re-run under fixed harness) | 1 | 2000 ms | 0 / 1 (0%) | 100% |
| Negative control 2 | 10 | 45000 ms | 0 / 10 (0%) | 100% |
| Positive control | 5 | 2000 ms | 5 / 5 (100%) | 100% |
| Dose-response | 3 | 2000 ms | 2 / 3 (66.7%) | 100% |
| Dose-response | 5 | 2000 ms | 5 / 5 (100%) | 100% |
| Dose-response | 8 | 2000 ms | 8 / 8 (100%) | 99.4% |
| Dose-response | 12 | 2000 ms | 12 / 12 (100%) | 100% |

Both negative controls passed cleanly, and the positive control reproduced
the production disconnect at 100%. The dose-response curve is monotonic and
saturates at 100% by N=5: this is consistent with CPU saturation on the
2-vCPU host as the mechanism (ShinyHub's default `multiplex` worker
isolation serializes all sessions of an app onto one worker process/event
loop, so render work queues up and per-session heartbeat gaps grow with N
until the client gives up). "Action success rate" stayed at or near 100%
throughout: sessions that disconnected had already completed every action
they attempted before the disconnect, so the failure is a heartbeat/socket
teardown, not an application error, in every run.

Negative control 2 is also worth reading in detail: `firstRenderMs` ranged
from 3698 ms (session 0) to 37895 ms (session 9) even though every session
survived. All 10 sessions connect within a couple of seconds of each other,
and with only one multiplexed worker, later sessions' first render queues
behind earlier ones' render work; at a 45 s cadence, that queuing delay
never crosses the interaction cadence, so nothing is missed. It does mean
the same queuing effect that stays harmless at a sparse cadence becomes the
saturation source at an aggressive one, and the two negative-control and
positive-control results are the same mechanism at different loads, not two
different phenomena.

## Known limits

- **Pure CPU burn, not real work.** `burn()` is a synthetic busy loop
  calibrated to spend wall time as CPU time (see `app/burn_test.py`). It
  reproduces CPU contention but not the memory pressure, I/O, or GC
  behavior of a real plotly-heavy dashboard; a real app's disconnect
  threshold may differ from this rig's for reasons other than raw CPU-ms.
- **The driver host needs spare cores.** Twelve concurrent headless Chrome
  contexts are not free; if the machine running `drive.mjs` is itself
  under load, driver-side scheduling delays can masquerade as server-side
  saturation. Runs here used a 12-core driver host with no other
  significant load; check `uptime` before trusting a number from a busier
  machine.
- **Results are not comparable across differently-loaded daemon hosts.**
  The husker daemon backing `RIG_DAEMON_HOST` is shared infrastructure. A
  run competing with other guests for the same physical cores will show
  worse numbers for reasons that have nothing to do with ShinyHub. Treat
  absolute numbers as specific to the daemon-load conditions noted above,
  and prefer the shape of the dose-response curve (monotonic, saturating)
  over exact percentages when comparing across sessions.
- **Single-node only.** This rig, like the `worker_isolation` modes it is
  exercising, says nothing about horizontally scaled (`replicas > 1`)
  deployments; see `docs/isolation.md`.

## Known surprises

- **Go's VCS build stamp is wrong for worktree builds.** `rig.sh up`
  cross-compiles the ShinyHub binary with a plain `go build`, and the
  resulting binary's embedded `vcs.revision` (surfaced at
  `/api/server-info` as `commit`) reports the *main checkout's* HEAD, not
  the worktree's, even though the worktree's own source is what actually
  got compiled (confirmed by diffing the two commits and verifying the
  worktree-only files they differ on are present and compiled into the
  binary). This is a cosmetic Go/git-worktree tooling quirk, not evidence
  of the wrong source being built; state the ShinyHub version from
  `git rev-parse HEAD` in the worktree, not from `/api/server-info`, when
  working from a worktree.
- **The guest image has python3 but not `uv`.** ShinyHub's native build
  sandbox deploys Python apps exclusively via `uv run`/`uv sync`, and its
  runtime probe reports Python availability strictly via
  `exec.LookPath("uv")`. `rig.sh up` now installs `uv` into the guest
  before installing the app's own requirements; without it, deploying the
  synthetic app fails with "Python runtime not found on the server
  (uv/python3 is not in PATH)" even though `pip install -r
  requirements.txt` succeeds moments later. This line was ported into
  `rig.sh` from a manual guest fix and, at the time, was only verified by
  equivalence (the same command run by hand against an already-provisioned
  guest), not by a fresh `up` cycle. It has since been exercised in-flow
  from a clean image (the NC1 re-run below): `/api/server-info` reported
  `runtimes.python: true` on the very first check after boot, with no
  manual guest patching, and a deploy succeeded on the first attempt. The
  fix is now confirmed to work end to end, not just by equivalence.
- **A driver timeout bug produced false failed-to-establish results before
  it was fixed.** `drive.mjs` originally called
  `page.waitForFunction(fn, { timeout: 120000 })`. Playwright's real
  signature is `(pageFunction, arg, options)`, so that object is bound
  positionally to `arg` (unused by the predicate) rather than sniffed as
  `options`, and Playwright silently falls back to its 30 s default. At
  N=10 this produced two spurious "failed to establish" results for
  sessions that were in fact healthy but slow to render behind the
  multiplexed worker queue (one measured `firstRenderMs` of 37895 ms, well
  past the undetected 30 s ceiling). Fixed by passing an explicit
  `undefined` as the second argument; re-verified with an isolated
  reproduction and a clean re-run of negative control 2 (10/10 established,
  0% disconnects). Any future change to this driver should keep the
  3-argument call shape.
