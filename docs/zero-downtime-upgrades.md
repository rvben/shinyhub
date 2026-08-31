---
description: "Upgrade or restart on SIGHUP without dropping live sessions or refusing connections, by re-execing the control plane in place."
---

# Zero-downtime upgrades (systemd)

ShinyHub can be upgraded or restarted without dropping live app sessions or
refusing client connections. On `SIGHUP` the running control plane re-execs the
new binary, hands off its listening sockets to the successor, drains in-flight
WebSocket sessions, releases the ownership lease, and exits. App processes keep
running throughout (`server.shutdown_apps: adopt`, the default).

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

A continuous client sees no connection-refused gap; in-flight sessions drain on
the old process for up to `server.drain_timeout` (default 60s) before any
straggler is force-closed.

## Tunables (`shinyhub.yaml`)

| Key | Default | Meaning |
|---|---|---|
| `server.pid_file` | (empty) | PID file the ready process writes; required for systemd MAINPID tracking. |
| `server.upgrade_timeout` | `60s` | How long the old process waits for the successor to become ready before aborting the upgrade and continuing to serve. |
| `server.drain_timeout` | `60s` | How long to wait for live WebSocket sessions to close before force-closing them. |

## Notes & limits

- **Failure is safe.** If the successor fails to start within `upgrade_timeout`,
  the old process keeps serving - the upgrade simply does not happen.
- **Startup preflight.** The handoff re-execs the process's own `argv[0]`, so at
  boot the server checks that `argv[0]` resolves to an executable (the same
  `PATH` lookup the handoff uses) and logs a warning when it does not. With the
  warning present every `SIGHUP` fails safe with the current process still
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
  written only when migrations are pending, and never pruned. See
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
- **systemd MAINPID.** With `Type=notify`, ShinyHub sends `READY=1` plus
  `MAINPID=<own pid>` on startup and after each handoff, so systemd retargets the
  main PID to the successor. The unit sets `Restart=on-failure` (not
  `Restart=always`): a genuine crash is restarted, but the original PID's
  deliberate exit 0 after a handoff is a clean success, so systemd does not fight
  it. After your first `systemctl reload`, verify with
  `systemctl show -p MainPID shinyhub` that it matches the new PID file.
- **Scope.** This is the systemd/VM path and covers all app runtimes (native,
  Docker, Fargate, remote-worker). Multi-pod Kubernetes rolling upgrades are part
  of the separate high-availability project.
- **Platform.** Linux and macOS only (tableflip does not support Windows).
