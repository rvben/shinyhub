# ShinyHub docker-compose reference deployment

Single-node deployment that runs ShinyHub from the published distroless image
and spawns R Shiny app processes as sibling Docker containers via the
`runtime.mode: docker` backend.

## Prerequisites

- Docker Engine 20.10+ (or Docker Desktop / OrbStack)
- Docker Compose v2 (`docker compose` subcommand)

## Quickstart

**1. Generate a secret and export it**

```sh
export SHINYHUB_AUTH_SECRET=$(openssl rand -hex 32)
```

**2. Set the data root**

The apps and app-data directories must exist at the same absolute path on the
host and inside the container (see "Path parity" below).

Linux:
```sh
export SHINYHUB_DATA_ROOT=/srv/shinyhub
```

macOS (OrbStack or Docker Desktop): `/srv` does not exist inside the VM by
default. Use a path that is mapped into the guest filesystem. Docker Desktop
maps `/Users` automatically; OrbStack maps `$HOME`.
```sh
export SHINYHUB_DATA_ROOT=$HOME/.shinyhub-data
```

**3. Set the Docker group id (Linux only)**

On Linux, `/var/run/docker.sock` is owned by the `docker` group. The ShinyHub
container must be a member of that group to open the socket:

```sh
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)
```

On macOS with OrbStack or Docker Desktop the in-VM socket is `root:root 0660`.
The container joins group `0` (root) by default, which is correct. Leave
`DOCKER_GID` unset on macOS.

**4. (Optional) Set an admin account for first boot**

```sh
export SHINYHUB_ADMIN_USER=admin
export SHINYHUB_ADMIN_PASSWORD=changeme
```

These have no effect once the user already exists in the database.

**5. Start the stack**

```sh
docker compose up -d
```

On first start an `init-perms` helper (busybox) runs once to create the data
directories under `SHINYHUB_DATA_ROOT` and chown them to the distroless nonroot
user (uid 65532). It exits immediately; subsequent restarts complete instantly
because the ownership is already correct (idempotent).

**6. Open the dashboard**

Navigate to `http://localhost:8080` and log in with the admin credentials you
set above.

**7. Deploy your first app**

```sh
# Install the shinyhub CLI (one-time)
curl -fsSL https://raw.githubusercontent.com/rvben/shinyhub/main/scripts/install.sh | sh

# Log in
shinyhub login --host http://localhost:8080 --username admin --password changeme

# Deploy a Shiny app bundle (zip or directory)
shinyhub deploy /path/to/my-app --slug my-app
```

## Path parity (why apps use a host bind-mount, not a named volume)

When ShinyHub starts an app container it passes the app's bundle directory to
the Docker daemon as a bind-mount source path. The daemon resolves that path on
the **host filesystem**, not inside the ShinyHub container. A named Docker
volume would give the shinyhub container a private path (e.g.
`/var/lib/docker/volumes/…`) that the daemon cannot match to what the server
recorded, so the bind would fail with "bind source path does not exist".

The fix is path parity: `SHINYHUB_DATA_ROOT` is bind-mounted at the same
absolute path inside the container (`${SHINYHUB_DATA_ROOT}:${SHINYHUB_DATA_ROOT}`),
so the path the server writes is the same path the daemon reads. The SQLite
database stays in a named volume because the daemon never needs to reach it.

## Network topology (why host networking)

The shinyhub service uses `network_mode: host`. This is required by the docker
runtime backend:

- `internal/process/docker.go` hardcodes replica endpoint URLs to
  `http://127.0.0.1:<port>`. App containers are started with `--publish
  127.0.0.1:<port>:<port>`, which binds the port on the **host loopback**.
- In bridge mode, the ShinyHub container cannot reach the host loopback;
  requests to app replicas would fail with connection refused.
- With host networking, the ShinyHub process and the app containers share the
  host network stack. The loopback address is the same for all of them.

There is no `ports:` mapping in the compose file. The server binds directly
on the host port specified in `shinyhub.yaml` (`server.port: 8080`).

## Configuration

Edit `shinyhub.yaml` in this directory before starting (or restart after
changes). The file is bind-mounted read-only at `/etc/shinyhub/shinyhub.yaml`.

The storage paths (`apps_dir`, `app_data_dir`) are set via `SHINYHUB_APPS_DIR`
and `SHINYHUB_APP_DATA_DIR` environment variables in `compose.yaml`, derived
from `SHINYHUB_DATA_ROOT`. Do not set them in `shinyhub.yaml`.

Other commonly changed settings:

| Setting | Default | Description |
|---------|---------|-------------|
| `server.port` | `8080` | Host port the server binds on |
| `runtime.docker.default_memory_mb` | `512` | Per-replica memory cap (MiB) |
| `runtime.docker.default_cpu_percent` | `100` | Per-replica CPU cap (100 = 1 core) |
| `lifecycle.hibernate_timeout` | `30m` | Idle timeout before an app is hibernated |

## PostgreSQL backend (optional)

The default deployment uses SQLite. For a HA-capable backend on a single node,
enable the `postgres` profile:

```sh
export POSTGRES_PASSWORD=$(openssl rand -hex 16)
docker compose --profile postgres up -d
```

Then uncomment the `SHINYHUB_DB_DSN` line in `compose.yaml` and change
`database.driver` to `postgres` in `shinyhub.yaml`, then restart:

```sh
docker compose --profile postgres up -d
```

The postgres container binds on the host network at `127.0.0.1:5432`.

## Upgrades

Pull the new image and restart:

```sh
docker compose pull
docker compose up -d
```

The server adopts running app containers across restarts
(`server.shutdown_apps: adopt`), so apps do not cold-start during upgrades.

## Backups

| Data | Location | How to back up |
|------|----------|----------------|
| App bundles | `$SHINYHUB_DATA_ROOT/apps/` | `tar czf apps.tar.gz $SHINYHUB_DATA_ROOT/apps` |
| Per-app data | `$SHINYHUB_DATA_ROOT/app-data/` | `tar czf app-data.tar.gz $SHINYHUB_DATA_ROOT/app-data` |
| SQLite DB | named volume `shinyhub-db` | `docker run --rm -v shinyhub-db:/data -v $(pwd):/backup alpine tar czf /backup/shinyhub-db.tar.gz /data` |

## Health monitoring

ShinyHub exposes `GET /healthz` which returns `200 OK` when the server is
ready. The distroless image has no shell or curl, so the compose file omits a
container healthcheck. Wire `/healthz` into your external monitor instead:

```sh
curl -fsS http://localhost:8080/healthz
```

## Stopping and cleaning up

```sh
# Stop the stack (data preserved)
docker compose down

# Stop and remove all state (named volume + data root)
docker compose down -v
rm -rf $SHINYHUB_DATA_ROOT
```
