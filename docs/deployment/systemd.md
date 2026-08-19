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
Those files are never pruned automatically, so include them in whatever
disk-space policy covers `/var/lib/shinyhub`.
