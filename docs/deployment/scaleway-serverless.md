# Scaleway Serverless Containers

Use this tier when application traffic is intermittent and paying for idle app
VMs would be wasteful. ShinyHub keeps its dashboard and control plane on a small
always-on host, while each app replica is a private Scaleway Serverless
Container with `min_scale=0` and `max_scale=1`.

This is the recommended shape for `demo.shinyhub.dev`: run the ShinyHub control
plane on a modest VPS (start with 2 vCPU, 2 GB RAM, and 20 GB disk), serve the
documentation separately as a static site at `shinyhub.dev`, and place the
Python demo apps on the `scaleway_serverless` tier. Keep the R gallery on Docker
until an R-capable managed runner is published. The dashboard stays responsive
while most idle app compute falls to zero.

## What sleeps

ShinyHub logically stops an idle replica and removes it from proxy routing.
The retained Serverless Container definition and stable endpoint remain;
Scaleway scales the backing instance to zero after its idle window. On the next
visit, ShinyHub reactivates the logical replica, health-checks the private
endpoint, and Scaleway cold-starts the container.

The ShinyHub control plane itself does not sleep. It owns authentication,
routing, deployment state, and the wake request. Making it serverless would
move those responsibilities into a database, object store, and wake gateway and
would be a different deployment architecture rather than a cheaper version of
this one.

Scaleway currently documents a 15-minute scale-to-zero window, 128–12,228 MB
memory, 70–6,000 mvCPU, and a 60-minute maximum HTTP request. Consult the
[current provider limits](https://www.scaleway.com/en/docs/serverless-containers/reference-content/containers-limitations/)
before changing production sizing.

## Prerequisites

Create these in the same Scaleway project and region:

1. A Serverless Containers namespace.
2. A Container Registry namespace containing a `linux/amd64` runner image.
3. An IAM application and API key with `ContainersFullAccess`,
   `ContainersPrivateAccess`, and `ContainerRegistryReadOnly` on the project.
   Container creation reads registry metadata before deployment, even though
   the namespace's managed identity performs the image pull.
4. Optionally, one Private Network attachment for app egress to private
   services. The app endpoint is still invoked over HTTPS with IAM.

Keep the API key on the ShinyHub control-plane host. ShinyHub uses its access and
secret key for provider API operations and adds the secret only as
`X-Auth-Token` on the proxy-to-container request. It is never sent to the browser
or an app process. Scaleway describes this private-origin flow in its
[container authentication guide](https://www.scaleway.com/en/docs/serverless-containers/how-to/create-auth-token-from-console/).

For CI, put the namespace and registry in a dedicated test project and scope the
IAM policy to that project. Scaleway IAM does not currently support
resource-level conditions for Serverless Containers, so project-scoped
`ContainersFullAccess` would otherwise cover unrelated containers in a shared
project.

## Build the runner

The same reference Python runner used by Fargate works on Scaleway:

```bash
docker buildx build \
  --platform linux/amd64 \
  --tag rg.nl-ams.scw.cloud/shinyhub/runner:latest \
  --push \
  build/fargate-runner
```

The runner downloads the immutable deployment bundle, verifies its SHA-256
digest, prepares dependencies, and execs ShinyHub's launch command. See the
[managed runner contract](../fargate-runner-contract.md) before customizing it.
It contains Python and `uv`, not R. A tier currently selects one runner image,
so a mixed R/Python tier needs a deliberately unified image; otherwise place R
apps on a separate Docker, remote-worker, or Fargate tier with an R runner.

## Configure ShinyHub

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
    name_prefix: shinyhub-demo
    control_plane_url: https://demo.shinyhub.dev
    bundle_token_ttl: 10m
    default_memory_mb: 512
    default_mvcpu: 250
    durable_data: false
```

Provide credentials through the service environment:

```bash
SCW_ACCESS_KEY=SCWXXXXXXXXXXXXXXXXX
SCW_SECRET_KEY=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

`SCW_DEFAULT_PROJECT_ID` and `SCW_DEFAULT_REGION` are also understood. Every
`SHINYHUB_RUNTIME_SCALEWAY_*` setting shown in `shinyhub.yaml.example` can
override its YAML counterpart.

Start with 512 MB and 250 mvCPU for a lightweight demo. Move to 1,024 MB and
500 mvCPU when dependency loading, rendering, or cold-start health checks are
slow. App manifest limits override these defaults and are clamped to Scaleway's
provider range.

## Data and connection limits

Serverless container filesystems are ephemeral. Do not claim durable data by
setting `durable_data: true` unless the runner mounts or connects to an
operator-managed durable backend. ShinyHub's local per-app `data/` directory is
not automatically available inside this tier.

The adapter preserves WebSocket upgrades, and the real-provider suite exercises
an echo connection rather than assuming provider compatibility. Treat that test
as a deployment gate in your own project. The provider's 60-minute request
ceiling may close a long-lived WebSocket; clients must reconnect, and
applications must keep session-critical state outside the individual connection
if uninterrupted sessions longer than an hour are required. For that workload,
prefer ECS/Fargate, a remote Docker worker, or an always-on VPS tier.

## Real-provider verification

Unit tests run the same managed-runtime lifecycle contract against stateful AWS
and Scaleway fakes. The credential-gated integration suite then creates a real
private container, waits for readiness, verifies authenticated HTTP and
WebSocket traffic, sleeps and wakes the same retained resource, and deletes it:

```bash
export SCW_ACCESS_KEY=...
export SCW_SECRET_KEY=...
export SCW_DEFAULT_PROJECT_ID=...
export SCW_DEFAULT_REGION=nl-ams
export SHINYHUB_TEST_SCALEWAY_NAMESPACE_ID=...
export SHINYHUB_TEST_SCALEWAY_IMAGE=rg.nl-ams.scw.cloud/shinyhub/serverless-probe:latest

go test -tags=integration ./internal/scaleway -run '^TestIntegration' -v
```

Build the reproducible probe image from the repository root:

```bash
docker buildx build \
  --platform linux/amd64 \
  --file build/serverless-probe/Dockerfile \
  --tag rg.nl-ams.scw.cloud/shinyhub/serverless-probe:latest \
  --push .
```

The slow suite waits past scale-to-zero and verifies that a WebSocket is closed
at the provider deadline and can reconnect. It incurs real runtime charges:

```bash
SHINYHUB_TEST_SCALEWAY_LONG=1 \
  go test -tags='integration slow' ./internal/scaleway \
  -run '^TestSlowIntegration' -v -timeout=90m
```

The scheduled `scaleway serverless integration` workflow runs the same checks
when these repository values are configured:

- Secrets: `SCW_ACCESS_KEY`, `SCW_SECRET_KEY`, `SCW_DEFAULT_PROJECT_ID`, and
  `SCW_SERVERLESS_TEST_NAMESPACE_ID`.
- Variables: `SCW_SERVERLESS_TEST_IMAGE` and, optionally,
  `SCW_DEFAULT_REGION`.

Missing credentials cause an explicit skip, never a false mock-based pass.
