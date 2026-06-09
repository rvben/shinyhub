# HA Data-Plane Operator Guide

This guide explains how ShinyHub's high-availability (HA) mode separates
the data plane (serving Shiny app traffic) from the control plane (ownership
lease, deployment, scaling). Understanding the separation is the key to
configuring your load balancer correctly and setting realistic recovery
expectations.

## Two signals, two purposes

ShinyHub exposes two health endpoints that serve different functions:

| Endpoint | Meaning | Who to probe |
|----------|---------|--------------|
| `GET /readyz` | Data plane is healthy: listener up, DB reachable, first pool sync completed | All instances in the serving pool |
| `GET /activez` | Active control plane: this instance holds the ownership lease and is ready | Single active instance only (optional admin monitoring) |

**Route user traffic to every instance that passes `/readyz`.** You do not
need to route only to the active instance: every healthy instance can serve
app requests because the pool syncer runs on all of them.

`/readyz` returns `200 OK` when the instance is ready to serve, `503` with
a JSON reason when it is not (e.g. `{"ready":false,"reason":"syncing"}` during
startup before the first DB sync completes).

`/activez` returns `200 OK` only on the single instance that currently holds
the control-plane lease. Use it for monitoring and alerting, not for routing.

## Kubernetes example

```yaml
readinessProbe:
  httpGet:
    path: /readyz
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 5
  failureThreshold: 3

livenessProbe:
  httpGet:
    path: /readyz
    port: 8080
  initialDelaySeconds: 15
  periodSeconds: 10
  failureThreshold: 6
```

Add all pods to a single Service with `selector:` pointing at the shinyhub
label. Kubernetes removes a pod from Endpoints when its readiness probe fails,
so a pod that loses its DB connection or stalls on the first sync is
automatically taken out of rotation.

Monitor the active instance with a separate non-routing check if you want
alerting on control-plane failover:

```yaml
# In a separate monitoring-only probe or ServiceMonitor:
# GET /activez on each pod; exactly one should return 200 at any time.
```

## nginx / HAProxy example

### nginx upstream

```nginx
upstream shinyhub {
    server instance-a:8080;
    server instance-b:8080;

    # Remove a backend from rotation when /readyz fails.
    # nginx Plus: health_check uri=/readyz;
    # Open-source nginx: use passive checks + keepalive.
    keepalive 16;
}
```

For active health checks with open-source nginx, use `nginx_upstream_check_module`
or a `vhost_traffic_status`-based approach; the probe path is `/readyz`.

### HAProxy backend

```haproxy
backend shinyhub_backend
    balance leastconn
    option httpchk GET /readyz
    http-check expect status 200
    server instance-a instance-a:8080 check inter 5s fall 3 rise 2
    server instance-b instance-b:8080 check inter 5s fall 3 rise 2
```

HAProxy removes a backend from rotation when `/readyz` returns non-200. Both
instances serve traffic when healthy; a single active instance for the control
plane is transparent to the load balancer.

## Apps-on-workers requirement

In clustered mode, apps that run on the same host as the ShinyHub process
(the `native` and `docker` tiers) cannot be served by a standby instance: the
process lives on the active node only, and its port is not reachable over the
network from other instances.

Clustered deployments must use off-host replicas:

- **`fargate`** - AWS Fargate tasks; each replica has a private VPC IP exposed
  over plain HTTP (inside the VPC). No special transport configuration needed.
- **`remote_docker`** - Docker containers on registered worker nodes; the proxy
  uses a per-worker mTLS transport derived from the worker's DB row.

When you try to deploy a `native` or `docker` app in clustered mode the server
rejects the request with `400 Bad Request: native/docker tiers are not
supported in clustered mode`. Migrate the app to a `fargate` or `remote_docker`
tier before clustering.

## Control-plane RTO: `lease_ttl`

The `cp_owner` table records a lease with an expiry timestamp. The active
instance renews it every `lease_renew_every` interval (default 10 s). If the
active crashes without releasing its lease, the standby cannot acquire until
the timestamp expires.

Configure both values in YAML under `server:`:

```yaml
server:
  lease_ttl: 30s          # default: 30s
  lease_renew_every: 10s  # recommend < lease_ttl/2; ShinyHub raises the effective
                          # TTL to at least 2x lease_renew_every if set higher
```

```
RTO for control plane = server.lease_ttl (default: 30 s)
```

During the `lease_ttl` window after an active crash:

- **Data plane on standby**: fully operational. The pool syncer populated every
  standby's proxy pool from the DB before the crash. Requests continue reaching
  the same running replicas, sticky cookies still route correctly.
- **Control plane on standby**: gated. The standby cannot deploy, scale, or
  hibernate until it acquires the lease.

## Reconnect behavior after a crash: honest framing

When the active instance crashes mid-stream (SIGKILL, OOM kill, hardware
fault), WebSocket connections that were established through that instance are
severed at the TCP level. The browser terminates the WebSocket and reconnects.

What happens next depends on where the app process runs:

- **Fargate / remote_docker replica (clustered mode)**: the app process runs
  independently of the ShinyHub instance that crashed. The replica keeps running.
  The browser reconnects to any healthy ShinyHub instance (including the
  standby), presents its sticky cookie, and the proxy routes the new WebSocket
  to the same running replica index. This happens immediately: no `lease_ttl`
  wait, no app restart. The Shiny session state the app process held in memory
  is still there.

  What this is: "fast reconnect to the same live app process". The WebSocket
  session is re-established; in-app R session state continuity is the Shiny
  application's own responsibility (standard Shiny session semantics apply).

  What this is not: "the session never blips". A kill -9 on the ShinyHub proxy
  severs the WebSocket. The browser will show a brief disconnection. A well-
  written Shiny app uses `session$onSessionEnded` and persistent storage (e.g.
  a reactive file, a database, `shinystore`) to survive a reconnect. ShinyHub
  resumes the routing connection; the app manages the session state.

- **Native / docker tier (single-node)**: the app process runs on the same
  host. If the host crashes, the process is gone and the app must restart.
  This is inherently single-node behavior; the `lease_ttl` discussion does not
  apply.

## Access logs and rejection metrics

Each ShinyHub instance logs what it served. In a two-instance cluster:

- Instance A's logs contain only the requests it forwarded.
- Instance B's logs contain only the requests it forwarded.
- The `/api/metrics` rejection counters are per-instance, reset on restart.

To get aggregate access logs and metrics across the cluster, ship logs to a
centralized sink (e.g. a structured log aggregator, Loki, Datadog) and sum
the per-instance counters in your dashboards. No built-in log aggregation is
provided by ShinyHub itself.

## Run an HA cluster locally

You can run a two-instance HA cluster on one machine against a local Postgres to
see failover behavior first-hand. Each instance is a `shinyhub serve` process
with its own `server.port` and `server.instance_id`, sharing one `database.dsn`
and the same `SHINYHUB_AUTH_SECRET` (the auth secret derives the sticky-cookie
key, so a shared secret gives cross-instance session affinity):

```yaml
# instance-a.yaml (instance-b.yaml differs only in port + instance_id)
server:
  host: 127.0.0.1
  port: 8080
  instance_id: a
  lease_ttl: 30s
  lease_renew_every: 10s
database:
  dsn: postgres://shinyhub:secret@127.0.0.1:5432/shinyhub?sslmode=disable
runtime:
  tiers:
    - name: fargate
      runtime: fargate
  fargate: { ... }   # your real off-host tier config
```

Start both with the same `SHINYHUB_AUTH_SECRET`, point a load balancer's health
check at `/readyz` on both, and route app traffic to the `/readyz`-healthy pool.
Exactly one instance reports `/activez=200` (the active control plane).

This failover path is covered by an automated kill-the-active integration test
(`make test-ha`): it boots two real instances against a throwaway Postgres,
SIGKILLs the active, and asserts the standby keeps serving the same running
replica immediately (sticky reconnect, no app restart) and then acquires the
control-plane lease. Run it yourself with `make test-ha` (requires Docker).

## Summary: what the operator configures

1. Point the load balancer's health check at `/readyz` on every instance.
2. Route all user traffic to the `readyz`-healthy pool (both instances when both
   are healthy).
3. Deploy apps with `fargate` or `remote_docker` tiers in clustered mode.
4. Accept that `lease_ttl` is the control-plane RTO; data-plane serving is
   continuous across lease handover.
5. Optionally monitor `/activez` (exactly one 200 at any time) for alerting on
   control-plane leadership.
