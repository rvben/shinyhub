# Security

## Reporting a vulnerability

Please report security issues privately. Do not open a public GitHub issue
for a suspected vulnerability.

- Preferred: GitHub private vulnerability reporting ("Report a vulnerability"
  on the repository Security tab).
- Alternative: email ruben.jongejan@gmail.com with a description, affected
  version, and reproduction steps.

You will get an acknowledgement and a coordinated-disclosure timeline. Fixes
ship in a tagged release; the advisory is published once a fixed version is
available.

## Threat model

ShinyHub is a self-hosted control plane that deploys and runs operator-supplied
Shiny applications. Its security posture depends on which runtime you choose.

### Native runtime executes app code on the host (trusted-code model)

With `runtime.mode: native` (the default), ShinyHub runs each deployed app, and
its dependency build steps, **as a subprocess on the host under the ShinyHub
service account**. This includes:

- `Rscript -e 'renv::restore()'` and equivalent Python dependency installs,
  which execute arbitrary code from the app's lockfile / dependency tree at
  deploy time.
- The app process itself (`shinyApp`, Shiny for Python, etc.), which runs with
  the full privileges of the ShinyHub user for the life of the process.

There is no sandbox in native mode. **Anyone who can deploy an app can run
arbitrary code on the host as the ShinyHub user.** Treat deploy access as
equivalent to shell access. The native runtime is appropriate only when every
principal able to deploy (interactive users and any holder of a deploy token)
is a trusted operator.

Recommended hardening for native mode:

- Run the ShinyHub service as a dedicated unprivileged user with no sudo, a
  restricted shell, and ownership limited to its own data directories.
- Put it on a dedicated host or VM, not one that also holds unrelated secrets
  or production workloads.
- Restrict who can obtain a deploy token or an interactive `developer`/
  `operator`/`admin` account.

### Docker runtime for lower-trust scenarios

If app authors are **not** fully trusted operators, use `runtime.mode: docker`.
Each app then runs in its own container rather than directly on the host. This
is the recommended configuration for multi-tenant or semi-trusted deployments.
Containers are not a complete security boundary by themselves; combine with a
hardened Docker daemon (rootless or a dedicated unprivileged host/user),
resource limits, and a network policy appropriate to your environment.

### ECS/Fargate runtime

When a tier declares `runtime: fargate`, ShinyHub launches each app replica as
an AWS ECS Fargate task. The security posture differs from the native and Docker
runtimes because workloads run in AWS-managed infrastructure, not on the
control-plane host.

#### Production posture: in-VPC private-IP routing

The recommended production configuration runs the ShinyHub control plane inside
the same VPC (or a peered VPC) as the Fargate tasks, with `route_via_public_ip:
false` (the default) and `assign_public_ip: false`. In this posture:

- The control plane routes to each task's private IP on the awsvpc ENI.
- The bundle token travels only over private VPC networking and never crosses
  the public internet.
- No public IP is assigned to the task ENI, so the task is not directly
  reachable from the internet.

Minimum recommended security group rules for this posture:

- **Inbound:** allow TCP on the app port from the control-plane security group
  (or the control-plane's specific /32 CIDR) only. No other inbound rules are
  needed.
- **Outbound:** allow outbound HTTPS to the `control_plane_url` host on its
  configured port (443 unless you set a non-default port) only (for the bundle
  fetch). Deny all other egress to prevent a compromised container from
  exfiltrating the bundle token or reaching arbitrary internet endpoints.

The IAM permission `ec2:DescribeNetworkInterfaces` is NOT required in this
posture (private-IP routing reads the IP directly from the ECS task record
without an EC2 API call).

#### Dev/test posture: public-IP routing

`route_via_public_ip: true` routes to each task's public IP. This requires
`assign_public_ip: true` and the task must be placed in a public subnet. In
this mode:

- The bundle token travels over the public internet on the path from the task
  to the control plane.
- **Without HTTPS, the bundle token is sent in cleartext and is replayable by
  any network observer for the full `bundle_token_ttl` window** (default 10
  minutes). A leaked token grants any holder the ability to download the app
  bundle for that deployment.
- **`control_plane_url` must use `https://` when `route_via_public_ip: true`.**
  The server enforces this at startup and refuses to start if the URL is
  `http://`.
- The extra IAM permission `ec2:DescribeNetworkInterfaces` is required: the
  control plane must resolve the public IP via EC2 after the task ENI is
  attached (the ECS task record only carries the private IP).
- The control plane proxies to the task over plain HTTP
  (`http://<public-ip>:<port>`); the runner does not terminate TLS. In
  public-IP mode that means all proxied request and response data, not just the
  bundle token, crosses the public internet unencrypted. This is a further
  reason public-IP routing is for dev/test only; production should use in-VPC
  private-IP routing.

Minimum security group rules in public-IP mode:

- **Inbound:** allow TCP on the app port from the control-plane /32 (your NAT
  gateway IP or the control plane's elastic IP) only. Do not allow inbound from
  `0.0.0.0/0`.
- **Outbound:** allow outbound HTTPS to the `control_plane_url` host on its
  configured port (443 by default) only.

#### Bundle token trust model

When a Fargate task starts, the control plane mints a short-lived capability
token (`SHINYHUB_BUNDLE_TOKEN`) and injects it as an environment variable via
the ECS container override. The token:

- Is scoped to a single content digest (one bundle version), not to the task
  ARN or the app slug.
- Expires after `bundle_token_ttl` (default 10 minutes). Tasks whose cold-start
  (including image pull and boot) regularly exceeds this window should increase
  `bundle_token_ttl` accordingly.
- Is verified statelessly on the control plane using HMAC-SHA256 with a key
  derived from `auth.secret` via HKDF-SHA256 with info string
  `shinyhub-fargate-bundle-v1`. There is no revocation: a token that has not
  yet expired is always valid. The short TTL and HTTPS transport are the
  required mitigations.

**Any AWS principal with `ecs:DescribeTasks` on the cluster can read every
container-override environment variable on the task** - `DescribeTasks` returns
the overrides verbatim. That is not only `SHINYHUB_BUNDLE_TOKEN`: the runtime
injects each NON-secret app environment variable as a container override too
(see `replicaEnv` in `internal/fargate/fargate.go`), so any such value on a
Fargate tier is visible to every principal that can describe the task. Scope
`ecs:DescribeTasks` tightly: do not attach it to roles used by app workloads
themselves, and do not include it in broadly-shared developer policies without
scoping to the cluster ARN.

#### Secret env vars on Fargate (`runtime.fargate.secrets`)

Env vars marked secret (`is_secret`, set via the env-var UI/CLI) are routed
**out of band** when `runtime.fargate.secrets.name_prefix` is configured: each
secret value is stored in AWS Secrets Manager and referenced by ARN from a
per-app task-definition revision's `containerDefinitions[].secrets` block. The
ECS agent resolves the value at task start using the **task execution role**, so
`ecs:DescribeTasks`/`DescribeTaskDefinition` show only the ARN, never the value,
and the app process never needs Secrets Manager permissions of its own.

This **fails closed**: if an app has secret env vars but no secrets backend is
configured, the Fargate replica refuses to start rather than fall back to
plaintext task overrides. Configure `runtime.fargate.secrets.name_prefix` (and,
for cross-account isolation, a value unique to this installation) before placing
apps with secret env on a Fargate tier.

Caveats:

- **Start-time only.** A running task captures secret values when it starts;
  rotating a secret takes effect on the next fresh task, not in place.
- **Secret store names** are `<name_prefix>/app-<app-id>/<KEY>`; the app id (not
  the slug) namespaces them so a delete-then-recreate of a slug never reuses a
  stale value.
- **Bundle token.** `SHINYHUB_BUNDLE_TOKEN` is still delivered as a container
  override (it is short-lived and digest-scoped, see above) and remains visible
  via `ecs:DescribeTasks`; it is not routed through Secrets Manager.
- On app delete, the secrets and the per-app task-definition revisions are
  removed; a delete interrupted by a crash is finished by the startup reconcile.

#### Least-privilege IAM policy

The following IAM policy grants the minimum permissions the ShinyHub control
plane needs to operate a Fargate tier. Replace `REGION`, `ACCOUNT_ID`, and
`CLUSTER_NAME` with your values. `TASK_ROLE_ARN` and `EXECUTION_ROLE_ARN` are
the ARNs of the ECS task role and task execution role respectively.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ECSTaskControl",
      "Effect": "Allow",
      "Action": [
        "ecs:RunTask",
        "ecs:StopTask",
        "ecs:DescribeTasks",
        "ecs:TagResource"
      ],
      "Resource": [
        "arn:aws:ecs:REGION:ACCOUNT_ID:task/CLUSTER_NAME/*",
        "arn:aws:ecs:REGION:ACCOUNT_ID:task-definition/*"
      ],
      "Condition": {
        "ArnLike": {
          "ecs:cluster": "arn:aws:ecs:REGION:ACCOUNT_ID:cluster/CLUSTER_NAME"
        }
      }
    },
    {
      "Sid": "ECSListTasks",
      "Effect": "Allow",
      "Action": "ecs:ListTasks",
      "Resource": "*",
      "Condition": {
        "ArnLike": {
          "ecs:cluster": "arn:aws:ecs:REGION:ACCOUNT_ID:cluster/CLUSTER_NAME"
        }
      }
    },
    {
      "Sid": "PassRoleToECS",
      "Effect": "Allow",
      "Action": "iam:PassRole",
      "Resource": [
        "TASK_ROLE_ARN",
        "EXECUTION_ROLE_ARN"
      ],
      "Condition": {
        "StringEquals": {
          "iam:PassedToService": "ecs-tasks.amazonaws.com"
        }
      }
    },
    {
      "Sid": "ENILookupForPublicRouting",
      "Effect": "Allow",
      "Action": "ec2:DescribeNetworkInterfaces",
      "Resource": "*",
      "Condition": {
        "StringEquals": {
          "aws:RequestedRegion": "REGION"
        }
      }
    },
    {
      "Sid": "ECSTaskDefForSecrets",
      "Effect": "Allow",
      "Action": [
        "ecs:DescribeTaskDefinition",
        "ecs:RegisterTaskDefinition",
        "ecs:DeregisterTaskDefinition",
        "ecs:ListTaskDefinitions"
      ],
      "Resource": "*"
    },
    {
      "Sid": "SecretsManagerForAppSecrets",
      "Effect": "Allow",
      "Action": [
        "secretsmanager:CreateSecret",
        "secretsmanager:PutSecretValue",
        "secretsmanager:DeleteSecret",
        "secretsmanager:ListSecrets"
      ],
      "Resource": "*"
    }
  ]
}
```

The last two statements are required only when `runtime.fargate.secrets` is
configured (routing secret env vars out of band); omit them otherwise.

Notes on the policy:

- `ecs:RunTask`, `ecs:StopTask`, `ecs:DescribeTasks`, `ecs:ListTasks` are the
  direct ECS API calls the runtime makes. The `ECSClient` interface in
  `internal/fargate/fargate.go` lists exactly these four. `ecs:ListTasks` is in
  its own statement: it has no task-level resource type, so it must be granted
  on `Resource: "*"` scoped by the `ecs:cluster` condition (the orphan sweep
  lists by cluster and `startedBy`). Scoping `ecs:ListTasks` to task ARNs, as a
  single combined statement would, denies the call.
- `ecs:TagResource` is required because `RunTask` is called with a `Tags` field
  (AWS applies the tags via an implicit `ecs:TagResource` action). ShinyHub
  stamps `shinyhub.managed=true` on each task for orphan-sweep identification.
  This is not a separate API call in the code; it is an IAM-level requirement
  when tagging at RunTask time.
- `ec2:DescribeNetworkInterfaces` has no resource-level constraint available in
  IAM; the `aws:RequestedRegion` condition limits exposure to the deployment
  region. The `ENILookupForPublicRouting` statement is needed only when
  `route_via_public_ip: true`. For private-IP-only deployments it can be
  omitted entirely.
- `ecs:DescribeTasks` is intentionally included for the control plane (it polls
  task status and performs recovery inventory), but must NOT be granted to app
  task roles (see bundle token note above).
- The `ECSTaskDefForSecrets` and `SecretsManagerForAppSecrets` statements back
  the secret-routing feature: the control plane describes the operator's base
  task definition, registers a per-app revision carrying the secrets block, and
  deregisters those revisions on delete; and it creates/updates/deletes/lists
  the app secrets in Secrets Manager. `RegisterTaskDefinition` has no
  resource-level scoping in IAM, hence `Resource: "*"` (the `iam:PassRole`
  statement still constrains which task/execution roles the registered
  definition may reference). Scope `secretsmanager:*` to a name prefix with a
  `secretsmanager:Name`-style condition if your account hosts unrelated secrets.
  These two statements are unnecessary when `runtime.fargate.secrets` is unset.
  With a customer-managed KMS key (`runtime.fargate.secrets.kms_key_id`) the
  control plane additionally needs `kms:GenerateDataKey` and `kms:Decrypt` on
  that key; the default `aws/secretsmanager` key needs no extra grant.

#### Task execution role (image pull + logs)

The least-privilege policy above is the *control-plane* permission set used by
the principal that runs ShinyHub. It is separate from the ECS **task execution
role** named in your task definition, which ECS itself assumes (not ShinyHub) to
pull the runner image and write task logs. That role needs ECR pull plus
CloudWatch Logs; the AWS-managed `AmazonECSTaskExecutionRolePolicy` covers both
(`ecr:GetAuthorizationToken`, `ecr:BatchCheckLayerAvailability`,
`ecr:GetDownloadUrlForLayer`, `ecr:BatchGetImage`, `logs:CreateLogStream`,
`logs:PutLogEvents`). A missing ECR permission surfaces at task start as
`CannotPullContainerError`, before the runner ever fetches its bundle.

When `runtime.fargate.secrets` is configured, the **task execution role** also
needs `secretsmanager:GetSecretValue` on the app secrets (ARNs
`arn:aws:secretsmanager:REGION:ACCOUNT_ID:secret:<name_prefix>/*`) so the ECS
agent can resolve the secrets block at task start, plus `kms:Decrypt` on the KMS
key ONLY when a customer-managed key is used (the default `aws/secretsmanager`
key is handled implicitly). The app's own **task role** is deliberately NOT
granted any Secrets Manager access: the values are injected as env by the agent,
so app code never holds secret-store permissions.

#### Pre-existing JWT signing key note

`auth.secret` serves two purposes: JWT session token signing and (via HKDF
derivation) Fargate bundle capability token signing. These are separate derived
keys - the bundle token key uses info `shinyhub-fargate-bundle-v1` and is never
used directly for JWT signing - but they share the same root secret. Rotating
`auth.secret` immediately invalidates all outstanding JWTs, session cookies, AND
bundle tokens. For a Fargate deployment, plan `auth.secret` rotation around
task startup windows so in-flight tasks do not fail bundle fetch mid-cold-start.

## Secret handling

All server secrets are sourced from the environment and are never written to
the database or app-visible state.

| Secret | Source | Notes |
|--------|--------|-------|
| `auth.secret` | `SHINYHUB_AUTH_SECRET` | Session/JWT signing key. Must be at least 32 characters and not the example placeholder; the server refuses to start otherwise. Generate with `openssl rand -hex 32`. |
| OAuth client secrets | `SHINYHUB_GITHUB_CLIENT_SECRET`, `SHINYHUB_GOOGLE_CLIENT_SECRET`, `SHINYHUB_OIDC_CLIENT_SECRET` | Only the configured providers need a value. |
| Deploy token | `SHINYHUB_DEPLOY_TOKEN` (+ `SHINYHUB_DEPLOY_TOKEN_ROLE`) | Pre-shared CI bearer credential. At least 32 characters. Not persisted. |

Server secrets are kept out of the environment exposed to deployed app
subprocesses, so app code cannot read the signing key or OAuth secrets from its
own process environment.

## Deploy-token rotation

Two kinds of deploy credentials exist:

- **Env deploy token** (`SHINYHUB_DEPLOY_TOKEN`): a single pre-shared token for
  CI. It is not stored anywhere. Rotation is: set a new value in the
  environment and restart the server. The old token stops working immediately
  on restart.
- **API-minted tokens** (`POST /api/tokens`): per-token credentials carrying an
  `shk_` prefix, stored hashed. Rotate by minting a replacement, updating the
  consumer, then revoking the old token. Revocation takes effect immediately
  without a restart.

Scope every token to the least role that works (`viewer`, `developer`,
`operator`, `admin`). The env deploy token defaults to `developer`.

## Network trust

If ShinyHub runs behind a reverse proxy, set `server.trusted_proxies` to the
proxy addresses so client-IP attribution (used for rate limiting and logging)
cannot be spoofed via forwarded headers. Do not set it when ShinyHub is
directly internet-facing.

In-process rate limiting is per-process and in-memory; it is a single-node
abuse control, not a distributed quota. Front multi-node deployments with a
shared edge rate limiter if you need a global ceiling.

## Backup, restore, and recovery drill

`shinyhub backup --out <archive>` writes a snapshot of the database plus the
apps and app-data trees. The database is captured point-in-time consistent via
SQLite `VACUUM INTO`. The apps and app-data trees are then walked while the
server may still be running, so those trees are a best-effort, *not* a
point-in-time-consistent, copy: a deploy, prune, upload, or app-written file
that lands during the walk may be partially or inconsistently captured
relative to the database snapshot.

This is acceptable for routine periodic backups (a deploy mid-backup is rare
and the next backup converges). For a strictly consistent cross-tree snapshot,
either stop the server for the duration of the backup, or take the backup from
a filesystem-level snapshot (LVM/ZFS/cloud volume snapshot) so the database and
trees are captured at the same instant.

- **RPO (recovery point objective):** the snapshot is point-in-time, so your
  worst-case data loss is the interval between scheduled backups. Run it from
  cron at the frequency your tolerated loss window allows, and copy the archive
  off-host.
- **RTO (recovery time objective):** `shinyhub restore <archive>` is offline
  (stop the server first). It refuses archives from a newer schema, moves the
  current database, apps, and app-data aside with a `.pre-restore-<timestamp>`
  suffix (it never deletes, so that copy is your rollback path), then extracts
  in place. Recovery time is dominated by archive size, typically minutes.

Rehearse the restore drill before you need it. Recommended periodic drill:

1. Take a backup from the live server.
2. On a scratch host or directory, run `shinyhub restore` against it.
3. Start the server and confirm apps list, deploy, and serve correctly.
4. Confirm the `.pre-restore-*` copies exist and are discardable.

A backup you have never restored is an untested backup.
