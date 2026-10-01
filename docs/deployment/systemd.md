---
description: "Run the standalone binary as an unprivileged service with delegated cgroup controllers, so native apps get real CPU and memory limits."
---

# systemd

The standalone binary can run as an unprivileged systemd service with delegated
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

## Private service configuration

The unit is normally `0644`; keep secret values out of `Environment=` lines
and drop-ins. Use `auth.secret_file: /etc/shinyhub/auth.secret` in
`shinyhub.yaml` for the existing root secret. The secret file must be readable
by the service user and have mode `0600`; ShinyHub rejects group- or
world-readable files. Moving the existing value is not a rotation. Follow
[secret rotation](../secret-rotation.md) before replacing it, because it also
encrypts persistent app secrets.

Keep `shinyhub.yaml` owned by `shinyhub:shinyhub` with mode `0600` when it
contains proxy, OAuth, deploy, or database credentials. For settings supplied
through environment variables, create a root-owned `/etc/shinyhub/shinyhub.env`
with mode `0600` and add the following inside `[Service]` in the unit or a
drop-in:

```ini
EnvironmentFile=/etc/shinyhub/shinyhub.env
```

Use `KEY=value` lines without `export`. Systemd's system service manager reads
the file before dropping privileges; the path can appear in the public unit,
but the values should not. Keep the root secret in its secret file rather
than this environment file. Do not print secret values in diagnostics or
commit private files. Reload systemd after changing the unit, and restart
the service to apply changed environment-file values.

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
