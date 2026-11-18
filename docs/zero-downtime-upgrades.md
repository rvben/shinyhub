---
description: "Upgrade ShinyHub with acknowledged listener handoff, session drain budgets, and opt-in WebSocket continuity for native multiplex apps."
---

# Zero-downtime upgrades (systemd)

ShinyHub hands new connections to a successor during a compatible upgrade.
Existing app sessions keep using the retiring proxy until they close or its
drain deadline expires; sessions still open at that deadline are disconnected.
On an acknowledged `shinyhub reload` request (or `SIGHUP`), the running control plane re-execs the
new binary, hands off its listening sockets to the successor, drains in-flight
WebSocket sessions, releases the ownership lease, and exits. App processes keep
running throughout (`server.shutdown_apps: adopt`, the default).

## Deployment acceptance tests

`make test-systemd-upgrade` runs on a disposable Linux host with systemd,
passwordless sudo, flock, nginx, uv, Go, Node and the render driver's Playwright
dependencies installed. It uses synthetic accounts and fresh SQLite state;
it does not connect to an existing ShinyHub deployment. CI runs it, and the
release workflow requires it before publishing. The Make targets serialize these
fixtures with a host lock: independently launched native servers can otherwise
compete for worker ports while starting. Run these targets on a disposable host
without other ShinyHub instances.

The test selects the latest non-yanked stable PyPI version below the candidate
version, installs its wheel with `uv tool`, starts the reference
systemd unit, and deploys a real Shiny app behind a loopback nginx TLS proxy.
It builds the candidate wheel from the current source, reinstalls it through
Ansible, and invokes `systemctl reload`. Fresh authenticated HTTP probes must
succeed throughout installation and handoff. The original browser must remain
reactive on the same WebSocket beyond the drain deadline; a new browser session
must also work. Served version, active server PID, supervisor PID and service
restart count are checked. When a role override is used, its source files are fingerprinted in the report.
Sampling detects observed failures; it cannot prove
the absence of every possible transient failure under arbitrary load.

After the continuity checks finish, the test kills only the active successor
while the original session and retired relay remain alive. It requires exactly
one automatic systemd restart, removal of the old service's processes, and a
fresh reactive Shiny session on the candidate version. This separate crash
phase expects the old WebSocket to disconnect; continuity is checked during the
earlier planned handoff. Both reference and role units must use `serve --supervise`
with a stable direct-child `MainPID`, without systemd `PIDFile=` tracking.
Another crash check kills the supervisor's direct child and requires a
retryable failure, one automatic restart, and a new reactive session.
The role fixture first migrates a plain-`serve`/`PIDFile=` service through the
role's actual unit-install task and restart handlers, before opening sessions.
Set `SHINYHUB_E2E_LEGACY_UNIT_TEMPLATE` to a previous unit template to rehearse
that exact starting definition; the report records its SHA-256. Without it,
the fixture synthesizes the legacy supervision shape from the current unit.
After crash recovery, a same-version reload verifies that closing the preserved
browser lets the supervisor's direct child retire without a restart or loss of
the successor PID file. A separate future-schema database verifies the supervised
binary exits with status 7 on permanent startup refusal.

To test a deployment repository's actual package tasks, handlers and hardened
unit, set `SHINYHUB_E2E_ROLE_DIR` to its `shinyhub` Ansible role directory. The
role must provide `tasks/main.yml`, `tasks/package.yml`, and `templates/shinyhub.service.j2`, with
an `Install ShinyHub systemd unit` task and restart handlers, service name and
runtime directory overrides. The fixture supplies its own
configuration, credentials and paths. The reference fixture starts with its
selected unit installed. The role fixture first performs the legacy unit
migration and its planned restart; both then verify package-only upgrades.
Production SSO, the production proxy configuration, and other config,
environment, or unit changes that require a restart remain outside this test.

Run `make test-systemd-upgrade-negative` for the negative control. It deliberately
uses stop/start instead of reload and passes only when the first failure is the
original WebSocket continuity assertion, after the candidate has started serving,
and the HTTPS probes also observed failures during stop/start.
Setup failures never count as successful controls. CI and the release gate run
this control too. Cleanup only stops the fixture's uniquely named units.

`make test-browser-session-relays-e2e` runs the same-version native reload checks
on macOS or Linux without systemd. It verifies the original WebSocket objects
remain open without reconnecting beyond the fixture's drain deadline, a distinct
successor session can recalculate, and closing all browser sessions reduces the
CLI's retired-generation and session counts to zero without changing the active
PID. Authenticated HTTP probes run throughout the handoffs and retirement.
`make test-entra-oidc-browser` separately checks that an emulator-authenticated
OIDC session survives an acknowledged reload and a full page navigation without
another provider sign-in. These isolated tests do not validate production DNS,
proxy routing, or Microsoft tenant policies.

## One-time setup

1. Install the binary at `/usr/local/bin/shinyhub` (a real built binary; `go run`
   cannot perform graceful reloads).
2. Set a PID file in `shinyhub.yaml`:
   ```yaml
   server:
     pid_file: /run/shinyhub/shinyhub.pid
   ```
3. Install `deploy/systemd/shinyhub.service`, then `systemctl daemon-reload` and
   `systemctl enable --now shinyhub`.

## Upgrading

### Special procedure: 0.12.x to 0.13.0

The schedule-provenance upgrade is an intentional exception to the normal
zero-downtime procedure. Use a maintenance window and stop the 0.12.x control
plane; do **not** use `systemctl reload` for this version boundary. Version
0.13.0 installs a database admission fence that prevents a still-running 0.12.x
server from launching another unattributed schedule process.

1. While 0.12.x is still running, inventory every enabled schedule with
   `on_success = "roll"`. Version 0.12.x allowed these schedules on local
   Docker; 0.13.0 requires every data-producing schedule to use effective
   `worker_isolation = "multiplex"` and a local native runtime. Move each app to
   that topology, or disable/remove the producer policy before upgrading.
2. Replace every manifest `run_on_register` declaration with an explicit
   `deploy_trigger`: normally `bundle_change` for a cache that must match its
   bundle, or `first_deploy` for a genuine one-time bootstrap.
3. Let all in-flight schedule runs finish, verify their terminal state with
   `shinyhub schedule runs`, then stop the old control plane with
   `systemctl stop shinyhub`. Confirm no scheduled-job process or descendant is
   left on the host; reboot if that is the only reliable way to establish the
   process fence.
4. Install 0.13.0 and start the service. Startup validates every enabled stored
   producer before serving. An error names the app, schedule, isolation, tier,
   or runtime that still needs correction. Stop the service again before
   correcting a startup error so `Restart=on-failure` cannot retry around your
   maintenance work.
5. If startup reports `legacy schedule writer fence`, keep all old servers and
   the new service stopped, verify the listed process trees are gone, and run
   the new binary's offline resolver:

   ```bash
   systemctl stop shinyhub
   shinyhub resolve-legacy-schedule-writers
   shinyhub resolve-legacy-schedule-writers --acknowledge-processes-stopped
   systemctl start shinyhub
   ```

   The first invocation is read-only and lists the affected app, schedule, and
   run IDs. The acknowledged invocation records conservative data uncertainty;
   it does not declare the cache safe.
6. Reapply the updated manifests after startup. Migrated schedules default to
   `deploy_trigger = "never"` because 0.12.x did not persist
   `run_on_register`. If the resolver marked a producer as requiring repair,
   rerun that exact schedule under the new fence and wait for success:

   ```bash
   shinyhub schedule run APP SCHEDULE --follow
   ```

   Check `shinyhub apps show APP` and `shinyhub schedule ls APP`; do not return
   the app to service while `compatibility_quarantined` or
   `producer_repair_required` remains true.

For later compatible releases, use the normal handoff:

```bash
# 1. Replace the binary in place with the new version.
install -m 0755 ./shinyhub /usr/local/bin/shinyhub
# 2. Trigger the zero-downtime handoff.
systemctl reload shinyhub
```

The reference systemd unit uses `shinyhub reload --wait 10m`. The command
returns success only after the successor acknowledges readiness and its PID
matches `server.pid_file`. Standalone successors must regain mutation ownership;
session-preserving standalone reloads also verify that previously serving apps
have reachable, adopted replicas meeting `ready_before`.
The predecessor counts ready replicas using the same checks as the successor;
only normal replica slots count toward the predecessor's obligation, since
scheduled-roll surge replicas are temporary. Apps with no ready normal replicas
create no serving obligation. A host already serving
below its configured warm floor does not need to reach that floor to reload;
the predecessor logs a warning with the ready count and configured floor.
This includes apps that begin serving while the command waits for admitted work.
An unavailable app is named in the readiness failure rather than reporting success.
Clustered successors may be healthy standbys after their first pool sync. A refusal
or failed readiness check returns nonzero, so `systemctl reload` reports failure.
A refusal before handoff leaves the current process serving; after an accepted
handoff, inspect the successor and the named app before retrying.
The unit allows fourteen minutes for the default ten-minute transition wait,
startup acknowledgement, and a separate readiness budget. `--wait` only bounds
the transition wait before handoff. After handoff acknowledgement, readiness has
its own `server.upgrade_timeout + 1m` limit (2 minutes with the default 60s
upgrade timeout), regardless of `--wait`. On timeout, the CLI names the blockers
and reports them to the successor for an error log in the server journal. This
report is best effort if the successor's control socket is unavailable.
If you increase `--wait` or `server.upgrade_timeout`, set `TimeoutStartSec` to at least
`wait + 2 * upgrade_timeout + 75s`.

For a local invocation without systemd:

```bash
shinyhub reload --config /etc/shinyhub/shinyhub.yaml --wait 10m
```

During a waiting session-preserving reload, new schedule runs and scheduled
activation claims are paused so newly due producers and queued hourly rolls do
not extend the wait. Already-admitted schedule runs finish their execution and
durable result handling before handoff; blockers name the app, schedule, and run.
This prevents retirement from interrupting an in-place producer and quarantining
its serving app. An already-admitted roll
finishes normally, and retries for work that already owns runtime state remain
admitted, including synchronous producers needed by an admitted deployment.
A canceled or failed reload resumes schedule admissions and the activation queue;
the successor resumes durable queued work after a successful handoff. Schedules
missed during the pause follow their configured missed-run policy. Draining replicas
remain a reload blocker; preserving their sessions across reload is not yet
supported. Connection-identity blockers name the app, connection ID, deployment,
and replica so they can be matched to WebSocket logs.

These admission protections run in the retiring server. When upgrading from
0.20.6, arrange the first reload after active producers and rolls have finished;
replacing the binary cannot change the already-running process's retirement logic.
The readiness replica cap is also captured by the retiring server; a first reload
from 0.20.7 or 0.20.8 still uses that server's uncapped warm-floor requirement.
For that first reload, temporarily lower the live app's `min_warm_replicas` to
its current ready count, then restore the target once the fixed server is running.
If it has already handed off with a nonzero exit, verify the successor PID and
readiness before retrying.

`make test-browser-reload-producers-e2e` exercises this boundary with real native
processes, a cron producer overlapping a scheduled roll, and a Shiny browser
session. It verifies exact replica adoption, uninterrupted page probes, and
session continuity beyond the reload drain deadline. A second case deliberately
fails a producer and verifies that an accepted handoff with no ready app replica
returns nonzero with the app named. New manual schedule requests during the
reload pause return 503 with `Retry-After: 1` and can be retried afterward.

Without `--wait`, temporary blockers fail immediately. With it, the command
waits up to that duration for app operations, candidates, draining generations,
serving activations, parent retirement, or relay capacity. Elastic pools and successor errors fail
immediately. Canceling the caller cancels the transition wait. Once a handoff
starts, the server uses `server.upgrade_timeout` to finish or recover it; if the
acknowledgement connection is lost, verify the published live PID before retrying.
The private `.reload` directory beside the PID file contains process-specific
control sockets and must remain accessible while the server is running. Keep
its path short enough for the platform's Unix socket limit. Socket setup failures
are logged and leave the server serving; acknowledged reload then fails clearly
until the configuration is corrected. Crash-left sockets for the current PID
are reclaimed without replacing live sockets or other files.

When upgrading a server that predates acknowledged reloads, replace the binary
and use the existing signal-based unit once (or send SIGHUP to its `MainPID`).
Confirm the successor is ready, then install the updated unit and run
`systemctl daemon-reload`. The new command refuses to silently fall back to a
signal when the old server has no reload control socket. Existing custom units
that still use `/bin/kill -HUP` continue to report signal delivery, not the
handoff result; update `ExecReload` to use the command above.

A continuous client sees no connection-refused gap; in-flight sessions drain on
the old process for up to `server.reload_drain_timeout` (defaults to
`server.drain_timeout`, normally 60s) before any
straggler is force-closed.

For a longer upgrade drain without extending app-version retirement:

```yaml
server:
  drain_timeout: 10m
  reload_drain_timeout: 8h
```

This setting is read by the retiring process at its own startup. Editing the
file immediately before reload changes the successor's future drain budget,
not the already-running parent's budget.

This postpones the upgrade disconnect; it does not transfer existing sessions
to the successor. The retiring proxy continues checking session authorization
throughout its drain. A SIGINT/SIGTERM shortens a reload drain to at most the
ordinary `drain_timeout`; it does not reset an earlier reload deadline.

For stable single-host **native multiplex** apps, session relays can remove the
reload deadline entirely:

```yaml
server:
  drain_timeout: 10m
  reload_sessions: preserve
  reload_relay_limit: 4
```

This is opt-in. Configure it before starting the process whose sessions you want
to retain; the already-running parent's settings govern that handoff. The old
process keeps its existing WebSocket tunnels, closes its database and lifecycle
control plane, and retires its session lease when its final session closes. If it launched
native backends, it keeps only their stdout/stderr readers and process reapers
alive until those backends exit, preserving logs without database access.
The retiring process logs how many launched replicas it is waiting for. Do not
kill it while those replicas are running: their output pipes still belong to it,
and killing it breaks their logs or can terminate them with SIGPIPE.
Adopted backends do not create another output reader on each reload. A ready successor reads
sealed leases for the exact backend processes, counts them for admission and
hibernation, and performs authorization and usage persistence for the relays.
Later reloads can proceed while earlier relays remain alive. Login expiration,
access revocation and ordinary service stop still close sessions as usual.

A preserving handoff cannot begin while an app operation, candidate or previous
generation is still draining, or when any pool uses elastic isolation. The
current process keeps serving; the upgrade error names the blocking apps and
reasons (elastic pool, draining generation, candidate generation, or serving
activation). An acknowledged reload can wait for temporary blockers with
`--wait`; a signal-based reload is refused immediately. At the relay limit,
another preserving reload is refused
instead of disconnecting older sessions, reserving capacity even if a new session
arrives during handoff. Close old tabs to
reclaim capacity. The limit bounds retained session relays, not their age or output-only
processes still needed by native backends. Retained tunnels continue running the old proxy code, so use an ordinary
service restart when a security fix must replace every running proxy immediately.

Compatible app deployments and rolling restarts may run after the control plane
has retired. Their own `drain_timeout` still applies to retained backend sessions;
preserving a hub reload does not preserve a session across replacement of its
app process. Stop-first changes remain fenced while relayed sessions exist.
A waiting reload begins its handoff after the app-generation drain finishes. This
mode preserves WebSocket transport; XHR fallback transport and recovery after
an owner crash are outside its continuity guarantee.

Relay IPC uses the private app-data registry when its path fits the Unix socket
limit, then a short private `RUNTIME_DIRECTORY` if available. For unusually long
paths it falls back to private `/tmp/shinyhub-relay-*` directories; exclude those
live directories from age-based temporary-file cleanup. Removing a live socket
interrupts authorization checks and closes retained sessions after the verification
grace period.

Do not replace the binary with a legacy build while relays are alive. The
side-effect-free relay-protocol preflight refuses a handoff to a binary lacking
relay support. Auth-secret rotation invalidates relay RPC authentication and
ends retained sessions after the fail-closed authorization grace (at least 90s).
Runtime leases live in the private `.reload-relays` directory beneath app-data;
a private, uid-scoped local Unix socket carries owner RPCs. Relay protocols use
frozen versioned identity and usage fields, so future upgrades must retain that
compatibility while older relays remain alive.

Run the real Shiny acceptance test with
`SHINYHUB_E2E_SESSION_RELAYS=1 node scripts/browser-lifecycle-e2e.mjs`. It holds
reactive sessions across repeated reloads, probes page loads, checks the relay
limit, deploys a replacement, and revokes access to a relayed session.

## Tunables (`shinyhub.yaml`)

| Key | Default | Meaning |
|---|---|---|
| `server.env_file` | (empty) | Private reloadable environment settings; see [systemd](deployment/systemd.md#private-service-configuration). |
| `server.max_retired_lifetime` | `24h` | Maximum preserved-session lifetime after control-plane retirement. Remaining sessions receive WebSocket 1012 when a complete outbound frame permits it; partial frames or slow transports close directly. |
| `server.pid_file` | (empty) | PID file naming the active server; required for acknowledged reloads and supervision. |
| `server.upgrade_timeout` | `60s` | How long the old process waits for the successor to become ready before aborting the upgrade and continuing to serve. |
| `server.drain_timeout` | `60s` | App-generation retirement, replica removal, and ordinary service shutdown session drain. |
| `server.reload_sessions` | `drain` | Opt-in `preserve` for stable single-host native multiplex WebSockets. Environment: `SHINYHUB_RELOAD_SESSIONS`. |
| `server.reload_relay_limit` | `4` | Maximum retained relay processes, from 1 to 32. Environment: `SHINYHUB_RELOAD_RELAY_LIMIT`. |
| `server.reload_drain_timeout` | `server.drain_timeout` | Positive duration for established WebSockets on a retiring proxy after a successful reload. Environment override: `SHINYHUB_RELOAD_DRAIN_TIMEOUT`. |

## Notes & limits

- **Repeated upgrades in drain mode.** The handoff refuses another upgrade
  while the successor's parent is still alive. A long reload drain therefore
  delays the next upgrade; use `reload --wait` to wait for the retiring proxy
  to exit, or retry after it exits. It also retains that process's memory and database connection.
- **Backend protection during handoff.** The successor cannot count sessions
  still flowing through its parent. While that parent is alive, idle
  hibernation and lifecycle mutations such as deploy, stop, resize and
  non-rolling restart are fenced until the parent exits and recovery cleanup
  finishes. Rolling restarts are accepted into the durable queue and wait. This deliberately trades lifecycle availability for session
  continuity in drain mode. Session-preserving relays supply cross-process accounting after their control plane retires.
- **Multiplex scope.** Multiplex workers can be re-adopted. Elastic workers
  (`grouped` and `per_session`) have process-local client bindings and are
  rebuilt during recovery; a longer proxy drain does not preserve their
  backend sessions.
- **Service stop budget.** Set systemd `TimeoutStopSec` above your ordinary
  `drain_timeout` plus HTTP and ownership shutdown (allow at least another
  minute). For a ten-minute drain, a twelve-minute stop budget is appropriate.
  The supervisor's termination policy can close sessions sooner than ShinyHub.
- **App changes remain separate.** A deploy or schedule roll may retire the
  backend an old proxy connection uses before the reload drain ends. The longer
  reload budget does not extend that backend's generation deadline.
- **Failure is safe.** If the successor fails to start within `upgrade_timeout`,
  the old process keeps serving - the upgrade simply does not happen.
- **Startup preflight.** The handoff re-execs the process's own `argv[0]`, so at
  boot the server checks that `argv[0]` resolves to an executable (the same
  `PATH` lookup the handoff uses) and logs a warning when it does not. With the
  warning present every reload fails safe with the current process still
  serving; fix how the service launches the binary (an absolute `ExecStart`
  path, or a name resolvable on the unit's `PATH`) before relying on reloads.
- **Database migrations must be backward-compatible and non-blocking.** During
  the handoff window both versions briefly run against the same SQLite database.
  The successor applies any new migrations at startup while the previous version
  is still serving on the old schema. An upgrade that adds migrations must
  therefore use additive / expand-contract changes (the previous version keeps
  working against the new schema) - never a destructive rename or column drop in
  the same release - and must avoid long-running/locking migrations (SQLite runs
  them in a transaction that can block the old process); split large backfills or
  table rewrites out of the upgrade and run them separately afterward.
  Same-version restarts apply no migrations and are always safe.
- **A rollback point is taken automatically.** Before applying pending
  migrations the successor copies the SQLite database aside as
  `<dsn>.pre-migration-v<version>-<timestamp>.sqlite`, and logs the path. It is
  written only when migrations are pending, and older snapshots are pruned
  down to a configurable retention count afterward. See
  [Schema migrations on startup](configuration.md#schema-migrations-on-startup)
  for the opt-out and the Postgres equivalent.
- **Rolling back to an older binary needs the snapshot.** An older build refuses
  to serve a database a newer one migrated, exiting `7`
  (`schema_incompatible`); the unit sets `RestartPreventExitStatus=7` so systemd
  stops rather than restart-loops. Put back the pre-migration snapshot along
  with the old binary, following
  [Getting back to the older build](configuration.md#getting-back-to-the-older-build)
  - a snapshot is moved into place, not fed to `shinyhub restore`, and its
  `-wal` sidecar has to go or the rollback silently undoes itself.
- **systemd supervision.** The reference unit runs `serve --supervise`. Its
  stable direct child remains `MainPID`; each ready server reports that
  supervisor PID through `NotifyAccess=all`. The supervisor watches the active
  server's `server.pid_file` with Linux pidfds. An unexpected active-server exit
  fails the supervisor and triggers `Restart=on-failure`, even while retired
  parents or replicas remain alive. Do not configure a systemd `PIDFile=` that
  would retarget `MainPID` away from the supervisor. Installing supervision on
  an existing unit requires one announced restart; bare `serve` still supports
  the older notification behavior, with its non-child tracking limitation.
- **Scope.** This is the systemd/VM path and covers all app runtimes (native,
  Docker, Fargate, remote-worker). Multi-pod Kubernetes rolling upgrades are part
  of the separate high-availability project.
- **Platform.** Linux and macOS only (tableflip does not support Windows).

## Page polling during a handoff

`/app/APP/.shinyhub/version.json` returns 503 while the active version is not
locally routable, including the successor's route recovery window.
`/app/APP/.shinyhub/ready` returns 503 with `ready: false` until local readiness
is satisfied. The readiness response includes `Retry-After: 1`. Both responses
are temporary polling outcomes; the page overlay retries without showing an
error. A persistent 503 requires investigating readiness or recovery.

HTTP-only probes never produce the “no WebSocket has connected” warning. The
warning requires an upstream WebSocket upgrade to fail and no successful
upgrade to clear the diagnostic during the grace window.

## Retired generation visibility and lifetime

`shinyhub status --config /etc/shinyhub/shinyhub.yaml` inspects the host-local
PID file and locked relay registry. JSON output includes `pid` and `retired`
(`count`, `sessions`, `unknown`, and each generation's PID, retirement time and
session count). Older relay snapshots have no PID/time metadata. Unknown live
snapshots remain counted and explicitly flagged, rather than being treated as idle.
Prometheus exposes `shinyhub_retired_generations`, `shinyhub_retired_sessions`,
and `shinyhub_retired_state_unknown` from the same registry.

`server.max_retired_lifetime` (or `SHINYHUB_MAX_RETIRED_LIFETIME`) defaults to
24 hours and must be positive. It bounds preserved WebSockets independently of
`drain_timeout`; service stop still uses the ordinary shutdown grace. A retiring
server logs `retired process exiting` after its sessions and owned output finish.
New shared-user native launches use independent, capped output pumps, allowing
the retiring server to exit without breaking replica stdout. Isolated native
replicas use the journal. Legacy replicas whose stdout still terminates in an
older controller need that output owner until the replicas exit; a handoff cannot
retroactively replace their pipe endpoints. Their output-only owners stay in the
registry with zero sessions. Replace those replicas in an announced app rollout
before expecting the lifetime limit to eliminate every legacy process.
