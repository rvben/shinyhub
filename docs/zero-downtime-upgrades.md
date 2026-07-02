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
