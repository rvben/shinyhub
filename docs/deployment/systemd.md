---
description: "Run the standalone binary as an unprivileged service with delegated cgroup controllers, so native apps get real CPU and memory limits."
---

# systemd

The standalone binary (Linux 5.3+) can run as an unprivileged systemd service with delegated
CPU and memory controllers for native application limits.

## Install the unit

Use the reference
[`shinyhub.service`](https://github.com/rvben/shinyhub/blob/main/deploy/systemd/shinyhub.service)
as a starting point:

```bash
sudo install -m 0755 shinyhub /usr/local/bin/shinyhub
sudo install -m 0644 deploy/systemd/shinyhub.service /etc/systemd/system/shinyhub.service
sudo systemctl daemon-reload
sudo systemctl enable --now shinyhub
```

Create the service user, configuration, and data directories referenced by the
unit before starting it. Store `auth.secret` outside the repository with owner-
only permissions.

## Reload the server

Set `server.pid_file: /run/shinyhub/shinyhub.pid`. The unit creates that runtime
directory with mode `0700`. Its `serve --supervise` parent stays systemd's direct
child across reloads and watches the active server with Linux pidfds.
`MainPID` remains the supervisor; the PID file names the active server.
Use `Type=notify`, `NotifyAccess=all`, `GuessMainPID=no`, and
`KillMode=control-group`, without a systemd `PIDFile=` directive.
An unexpected active-server exit makes the supervisor fail, allowing
`Restart=on-failure` to restart the unit even when older relays remain.
Its `ExecReload` calls `shinyhub reload --wait 10m`
and waits for the successor's readiness acknowledgement. `systemctl reload`
returns nonzero when the reload is refused, its transition wait expires, or the
successor fails to start; the current server keeps serving after a failed handoff.

For servers installed with a signal-based unit, follow the one-time transition
in [zero-downtime upgrades](../zero-downtime-upgrades.md) before replacing
`ExecReload`. Install the updated unit, run `systemctl daemon-reload`, and perform
one announced restart when adding `--supervise` to an existing service;
replacing the binary alone does not update systemd's reload command.

## Private service configuration

The unit is normally `0644`; keep secret values out of `Environment=` lines
and drop-ins. Use `auth.secret_file: /etc/shinyhub/auth.secret` in
`shinyhub.yaml` for the existing root secret. The secret file must be readable
by the service user and have mode `0600`; ShinyHub rejects group- or
world-readable files. Moving the existing value is not a rotation. Follow
[secret rotation](../secret-rotation.md) before replacing it, because it also
encrypts persistent app secrets.

Keep `shinyhub.yaml` owned by `shinyhub:shinyhub` with mode `0600` when it
contains proxy, OAuth, deploy, or database credentials. For settings supplied through environment variables, use a service-user-readable
owner-only `/etc/shinyhub/server.env` (0600) and set:

```yaml
server:
  env_file: /etc/shinyhub/server.env
```

ShinyHub reads this file at startup and again before launching each successor.
Use `KEY=value` lines; blank lines and full-line `#` comments are allowed.
Single quotes preserve literal text, double quotes accept escaped characters.
There is no shell expansion, `export`, or inline comment syntax. Duplicate or
unsupported keys, malformed quotes, non-regular files, and public permissions
refuse the reload before spawning a successor. Errors never print file values.

The file overrides inherited values for known `SHINYHUB_*` settings, proxy
variables (`HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY` and their lowercase
forms), and `UV_*` settings. Removed keys are removed from the successor's
environment; an empty value also overrides an inherited value. `SHINYHUB_CONFIG`,
`SHINYHUB_PID_FILE`, and internal handoff/systemd variables cannot be changed by
this file. Keep `server.pid_file` stable across handoffs. Existing replicas keep
their launch environments; changed settings affect the new controller and future
builds/replicas. Root-secret rotation still requires the documented migration;
keep it in `auth.secret_file`, rather than this environment file.

A systemd `EnvironmentFile=` remains supported, but systemd does not re-read it
when ShinyHub forks a successor: changes there still require a restart. Moving
changing settings to `server.env_file` keeps them out of the unit and makes a
reload sufficient. For systemd-only settings, use a `[Service]` drop-in and
`systemctl daemon-reload`; clear `ExecReload=` before replacing its command.

Installing `--supervise` on an existing signal-based or unsupervised unit needs
one announced restart. Future handoffs retain this minimal supervisor. To verify,
compare `systemctl show -p MainPID shinyhub` (stable supervisor) with the PID file
(changing active server) and `shinyhub status --config /etc/shinyhub/shinyhub.yaml`.
The supervisor stays at its starting version until a service restart; a package
reload upgrades the active server. The maintained acceptance test covers the
immediately preceding published stable supervisor with the candidate server.
If the running supervisor is older than that baseline, rehearse that exact pair
in isolation or schedule a restart before upgrading. Also restart when release
notes require a supervisor-protocol change. `--supervise` requires ShinyHub
0.20.9 or later; rolling back to an earlier binary also requires its compatible
unit and a planned restart.

Broker-native workers with journal output survive controller restarts when
`server.shutdown_apps: adopt`; see [native user isolation](../native-user-isolation.md).
Shared-user native replicas remain in this service's cgroup and are stopped by
`systemctl restart`. `Delegate=` allows resource management; it does not exempt
sub-cgroups from service termination. `KillMode=mixed` still sends the final
SIGKILL to the entire cgroup, so it is not a restart-survival setting.

The opt-in [per-app user backend](../native-user-isolation.md) adds a separate
root broker and dedicated app accounts. The following limitation applies when
`runtime.native.broker_socket` is empty.

These permissions protect against other local users, not native replicas
running as the same UID. Native apps can read other replicas' environments
and server-readable files. Use a separate runtime boundary for app code you
do not trust; see [native isolation](../isolation.md). The shipped hardening
and cgroup delegation are not per-app user isolation. Do not enable
`ProtectControlGroups` or `ProtectSystem=strict` blindly: the native runtime
needs delegated cgroups and writable app data.

## Reverse proxy

Keep the ShinyHub listener on loopback and terminate HTTPS with Caddy, nginx, or
another reverse proxy. Forward both `base_url` and `app_origin` hostnames when a
dedicated application origin is configured.

## Upgrades

The reference unit supports listener handoff on `SIGHUP`. Follow the
[zero-downtime upgrade guide](../zero-downtime-upgrades.md) to replace the
binary while existing HTTP and WebSocket connections drain normally.

The unit sets `RestartPreventExitStatus=7`. Exit `7` means the database was
migrated by a newer build, which this binary refuses to serve; retrying cannot
fix it, so systemd stops and the unit shows `failed` instead of looping in
`activating (auto-restart)`. If you copy the unit rather than installing it,
keep that line, or a botched downgrade looks healthy to monitoring while the
service is down. `journalctl -u shinyhub` shows the two versions involved.

Before applying pending migrations the server writes a pre-migration snapshot of
the SQLite database beside it; see
[Schema migrations on startup](../configuration.md#schema-migrations-on-startup).
Those files are pruned automatically down to a configurable retention count
(default 5), so include at most that many full-size database copies in
whatever disk-space policy covers `/var/lib/shinyhub`.
