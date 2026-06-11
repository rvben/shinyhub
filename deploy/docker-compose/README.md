# ShinyHub docker-compose reference deployment

Single-node deployment that runs ShinyHub from the published distroless image
and spawns R Shiny app processes as sibling Docker containers via the
`runtime.mode: docker` backend.

## Prerequisites

- Docker Engine 20.10+ (or Docker Desktop)
- Docker Compose v2 (`docker compose` subcommand)

## Quickstart

**1. Generate a secret and export it**

```sh
export SHINYHUB_AUTH_SECRET=$(openssl rand -hex 32)
```

**2. Set the Docker group id (Linux only)**

The ShinyHub image runs as a non-root user. On Linux, `/var/run/docker.sock` is
owned by the `docker` group. Pass the group id so the container can open the
socket:

```sh
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)
```

On macOS with Docker Desktop the VM socket is world-accessible, so the group
id does not matter. Leave `DOCKER_GID` unset or set it to `0`.

**3. (Optional) Set an admin account for first boot**

```sh
export SHINYHUB_ADMIN_USER=admin
export SHINYHUB_ADMIN_PASSWORD=changeme
```

These have no effect once the user already exists in the database. Comment
them out after the first start.

**4. Start the stack**

```sh
docker compose up -d
```

**5. Open the dashboard**

Navigate to `http://localhost:8080` and log in with the admin credentials you
set above.

**6. Deploy your first app**

```sh
# Install the shinyhub CLI (one-time)
curl -fsSL https://raw.githubusercontent.com/rvben/shinyhub/main/scripts/install.sh | sh

# Log in
shinyhub login --host http://localhost:8080 --username admin --password changeme

# Deploy a Shiny app bundle (zip or directory)
shinyhub deploy /path/to/my-app --slug my-app
```

## Network topology (why host networking)

The shinyhub service uses `network_mode: host`. This is required by the docker
runtime backend:

- `internal/process/docker.go` hardcodes replica endpoint URLs to
  `http://127.0.0.1:<port>`. App containers are started with `--publish
  127.0.0.1:<port>:<port>`, which binds the port on the **host loopback**.
- In bridge mode, the ShinyHub container cannot reach the host loopback;
  requests to app replicas would fail with connection refused.
- With host networking, the ShinyHub process and the app containers share the
  host network stack. The loopback address is the same for all of them, so
  the proxy can reach every app replica.

There is no `ports:` mapping in the compose file. The server binds directly
on the host port specified in `shinyhub.yaml` (`server.port: 8080`). Change
that value to pick a different host port.

## Configuration

Edit `shinyhub.yaml` in this directory before starting (or restart with
`docker compose up -d` after changes). The file is bind-mounted read-only at
`/etc/shinyhub/shinyhub.yaml` inside the container.

The most commonly changed settings:

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

All state lives in three named volumes:

| Volume | Contents |
|--------|----------|
| `shinyhub-apps` | Deployed app bundle directories |
| `shinyhub-app-data` | Persistent per-app data (`shinyhub data push`) |
| `shinyhub-db` | SQLite database (`shinyhub.db`) |

Back them up with `docker run --rm -v <volume>:/data -v $(pwd):/backup alpine
tar czf /backup/<volume>.tar.gz /data` or use a volume backup tool of your
choice.

## Health monitoring

ShinyHub exposes `GET /healthz` which returns `200 OK` when the server is
ready. The distroless image has no shell or curl, so the compose file omits a
container healthcheck. Wire `/healthz` into your external monitor instead:

```sh
curl -fsS http://localhost:8080/healthz
```

## Stopping and cleaning up

```sh
# Stop the stack (volumes are preserved)
docker compose down

# Stop and remove all volumes (destroys all data)
docker compose down -v
```
