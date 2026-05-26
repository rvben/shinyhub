# Join a remote Docker worker over Tailscale

This walks through adding a second host as a ShinyHub remote Docker worker so a
deployment tier runs its containers there. The data plane is mTLS end to end:
the control plane signs the worker a short-lived, node-bound client certificate
at join, and the app container's port stays bound to `127.0.0.1` on the worker
(only the agent's mTLS tunnel is exposed off-host).

## How the pieces connect

A control plane with the worker API enabled exposes three listeners:

- the user/API server (`server.port`), where you and the dashboard talk to it,
- the worker-facing mTLS API (`worker.listen_addr`), where workers register,
  heartbeat, and fetch their certificates,
- each worker's data-plane tunnel (`--advertise-addr`), which the control plane
  dials to route user traffic into the worker's containers.

A worker connects out to `worker.listen_addr`, so that is the URL you pass as
`--server`, not the user-facing `server.port`. The control plane dials back to
the worker on `--advertise-addr`, so that address must be reachable from the
control plane. Tailscale gives both hosts stable, routable IPs that satisfy
both directions.

## Prerequisites

- Tailscale up on both hosts (`tailscale status` lists each other).
- Docker installed and running on the worker host.
- The control plane's worker API reachable from the worker over Tailscale.

## 1. Enable a remote tier and the worker API on the control plane

In the control plane's `shinyhub.yaml`:

```yaml
runtime:
  mode: native
  tiers:
    - name: local
      runtime: native
    - name: remote
      runtime: remote_docker
worker:
  enabled: true
  join_token_file: /etc/shinyhub/join-token
  ca_dir: /var/lib/shinyhub/ca
  listen_addr: 0.0.0.0:8443
  advertise_hosts:
    - <control-plane-tailscale-ip>
```

Write a join token, then restart the control plane:

```bash
head -c 32 /dev/urandom | base64 > /etc/shinyhub/join-token
```

On first start with `worker.enabled`, the control plane generates its internal
CA under `ca_dir`. The worker must trust that CA at join (step 2). Copy
`<ca_dir>/ca-cert.pem` to the worker host, or read it from `ca_dir` if both
roles share a filesystem.

## 2. Start the worker on the second host

Point `--server` at the control plane's worker API listener, advertise the
worker's own Tailscale IP, and pin the control plane's CA with `--ca-file`:

```bash
shinyhub worker \
  --server https://<control-plane-tailscale-ip>:8443 \
  --token "$(cat /path/to/join-token)" \
  --advertise-addr <worker-tailscale-ip>:9443 \
  --tier remote \
  --ca-file /path/to/ca-cert.pem \
  --data-dir /var/lib/shinyhub/worker
```

`--ca-file` is required for the internal self-signed CA; omit it only when the
worker API is fronted by a publicly trusted certificate. The worker
self-registers, receives a node id and a node-bound certificate, and begins
heartbeating. The worker log prints `worker joined control plane` once it is up.

## 3. Deploy onto the remote tier

Create the app, place replicas on the remote tier, then deploy the bundle. With
`SHINYHUB_HOST` and `SHINYHUB_TOKEN` (or `SHINYHUB_DEPLOY_TOKEN` on the control
plane) set:

```bash
# Place two replicas on the remote tier.
curl -fsS -X PATCH "$SHINYHUB_HOST/api/apps/myapp" \
  -H "Authorization: Token $SHINYHUB_TOKEN" -H "Content-Type: application/json" \
  -d '{"placement":{"remote":2}}'

# Deploy; --wait blocks until the replicas are healthy. First-run dependency
# installs inside the container can take minutes.
shinyhub deploy ./myapp --slug myapp --wait --wait-timeout 300
```

Verify:

- The worker logs a single `bundle: pulled` line. The bundle cache dedups the
  second replica's start, so only one pull happens regardless of replica count.
- `https://<control-plane>/app/myapp/` serves the app, with traffic routed
  through the worker's mTLS tunnel into the `127.0.0.1`-bound container.

## 4. Verify failure behavior

- **Control-plane restart:** restart the control plane. It re-adopts the running
  replicas from the worker's container inventory (`recovery: re-adopted remote
  replica` in the control-plane log) and routing survives the restart.
- **Worker loss:** stop the worker process. Once its heartbeat goes stale
  (90s timeout, swept every 30s) the control plane marks the worker down and its
  replicas `lost` (`worker monitor: replica lost`), and removes them from
  routing. With no live replicas the app's URL serves the loading page instead
  of the app until a redeploy re-places it on a live worker.

## Notes

- Worker data under `--data-dir` is node-local and is not replicated.
  Cross-node shared mounts are rejected at deploy time, so an app's data always
  lives on the same node as the app.
- The worker's certificate is short-lived and renewed on heartbeat, so a worker
  that is offline past its certificate lifetime re-establishes trust on rejoin.
- `make test-remote-e2e` runs this whole path hermetically on a single host with
  a real Docker daemon: control plane plus worker, deploy, bundle-dedup,
  tunnel routing, restart re-adoption, and worker-loss handling.
