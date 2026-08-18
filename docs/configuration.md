# Configuration

ShinyHub reads server configuration from YAML and allows environment variables
to override individual values. The complete annotated reference is
[`shinyhub.yaml.example`](https://github.com/rvben/shinyhub/blob/main/shinyhub.yaml.example).

## Configuration file resolution

The server checks, in order:

1. `shinyhub serve --config /path/to/shinyhub.yaml`
2. `SHINYHUB_CONFIG`
3. `./shinyhub.yaml`

`init`, `backup`, and `restore` use the same resolution order.

## Minimal server

```yaml
database:
  driver: sqlite
  dsn: /var/lib/shinyhub/shinyhub.db

server:
  host: 127.0.0.1
  port: 8080
  base_url: https://hub.example.com

auth:
  secret: replace-with-at-least-32-random-characters

storage:
  apps_dir: /var/lib/shinyhub/apps
  app_data_dir: /var/lib/shinyhub/app-data
```

Generate `auth.secret` once and preserve it across restarts:

```bash
openssl rand -hex 32
```

The secret signs sessions and encrypts application secrets at rest. Follow the
[secret rotation procedure](secret-rotation.md) instead of replacing it
directly on a running installation.

## Dedicated application origin

Production deployments should serve application traffic from an origin that is
different from the dashboard and API:

```yaml
server:
  base_url: https://hub.example.com
  app_origin: https://apps.example.com
```

Route both HTTPS hostnames to the same ShinyHub listener. The application origin
exposes app proxy traffic and health checks only; it does not expose the
dashboard, static control-plane assets, or `/api` routes. This prevents
application JavaScript from sharing an origin with control-plane cookies.

## Environment overrides

Configuration keys generally map to `SHINYHUB_<UPPER_SNAKE_CASE>` variables.
For example:

```bash
export SHINYHUB_BASE_URL=https://hub.example.com
export SHINYHUB_APP_ORIGIN=https://apps.example.com
export SHINYHUB_AUTH_SECRET="$(openssl rand -hex 32)"
export SHINYHUB_RUNTIME_DOCKER_DEFAULT_MEMORY_MB=512
```

Prefer environment variables or a secrets manager for credentials such as OAuth
client secrets and database passwords.

## Scale-to-zero application tier

ShinyHub can retain each application replica as a private Scaleway Serverless
Container while Scaleway scales its underlying instance to zero:

```yaml
runtime:
  tiers:
    - name: serverless
      runtime: scaleway_serverless
  scaleway:
    region: nl-ams
    project_id: 00000000-0000-0000-0000-000000000000
    namespace_id: 00000000-0000-0000-0000-000000000000
    image: rg.nl-ams.scw.cloud/shinyhub/runner:latest
    control_plane_url: https://hub.example.com
    default_memory_mb: 512
    default_mvcpu: 250
```

Set `SCW_ACCESS_KEY` and `SCW_SECRET_KEY` in the server environment, never in
YAML. Standard `SCW_DEFAULT_PROJECT_ID` and `SCW_DEFAULT_REGION` variables are
accepted too. See the [Scaleway Serverless deployment guide](deployment/scaleway-serverless.md)
for the runner image, security boundary, lifecycle, and real-provider tests.

## Client configuration is different

For client commands such as `deploy`, `apps`, and `fleet`, `SHINYHUB_CONFIG`
selects the local credentials file—not the server YAML. Automation can avoid a
credentials file by setting `SHINYHUB_HOST` and `SHINYHUB_TOKEN`.
