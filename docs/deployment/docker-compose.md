---
description: "A reference Compose stack that runs the control plane and starts each app as a sibling container with its own CPU and memory limits."
---

# Docker Compose

The reference Compose stack runs the ShinyHub control plane and starts
applications as sibling Docker containers with CPU and memory limits.

## Start the reference stack

```bash
git clone https://github.com/rvben/shinyhub.git
cd shinyhub/deploy/docker-compose

export SHINYHUB_AUTH_SECRET="$(openssl rand -hex 32)"
export SHINYHUB_DATA_ROOT=/srv/shinyhub
export DOCKER_GID="$(stat -c %g /var/run/docker.sock)"

docker compose up -d
```

The control plane listens on host loopback at `http://127.0.0.1:8080`. Put an
HTTPS reverse proxy in front of it before exposing the service publicly.

## Why the stack uses host networking

Application containers publish their allocated ports on host loopback. Host
networking lets the control-plane container reach those endpoints while keeping
them inaccessible from external interfaces.

The bundle and app-data directories are mounted at the same absolute path on
the host and inside the control-plane container. Docker resolves sibling
container bind mounts on the host, so this path parity is required.

## Production boundaries

The Compose topology is suitable for a trusted single-tenant installation. The
control-plane container has access to the Docker socket, and app containers
share the host network namespace. Use remote workers or the
[AWS ECS/Fargate deployment](aws-ecs.md) for stronger tenant boundaries.

See the complete reference, including PostgreSQL, backups, upgrades, and macOS
path handling, in
[`deploy/docker-compose/README.md`](https://github.com/rvben/shinyhub/blob/main/deploy/docker-compose/README.md).
