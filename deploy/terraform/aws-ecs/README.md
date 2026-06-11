# ShinyHub AWS ECS/Fargate Terraform module

Deploys the ShinyHub control plane on AWS ECS Fargate with:
- An Application Load Balancer (HTTP always; HTTPS when `certificate_arn` is set)
- A single-AZ PostgreSQL 16 RDS instance (encrypted, private subnets)
- ECS cluster with the control-plane service (desired count variable; default 1)
- IAM roles scoped to the exact API surface used by `internal/fargate`
- CloudWatch log groups for control-plane and app-runner tasks
- Security groups: ALB->CP:8080, CP->RDS:5432, CP->app tasks (all TCP)

The module does NOT create a VPC. Every organisation has one; pass your existing
`vpc_id`, `public_subnet_ids`, and `private_subnet_ids`.

## Apply prerequisites

1. An AWS account with credentials available (environment, profile, or instance metadata).
2. An existing VPC with public subnets (for the ALB) and private subnets (for ECS + RDS).
3. A Secrets Manager secret holding a 32+ character random string for `SHINYHUB_AUTH_SECRET`:
   ```sh
   aws secretsmanager create-secret \
     --name shinyhub/auth-secret \
     --secret-string "$(openssl rand -hex 32)"
   ```
   Pass the returned ARN as `auth_secret_arn`.

## Trusted proxy CIDRs (required)

The `trusted_proxy_cidrs` variable is **required** (no default). It is injected
as `SHINYHUB_TRUSTED_PROXIES` so the server trusts `X-Forwarded-For` headers
set by the ALB.

Without this, every HTTP connection peers from an ALB node IP:
- The per-IP login rate limit applies to ALB nodes rather than end users.
- Audit log entries record ALB IPs instead of real client IPs.

Set `trusted_proxy_cidrs` to the CIDR blocks of your ALB subnets:

```hcl
trusted_proxy_cidrs = ["10.0.1.0/24", "10.0.2.0/24"]
```

## Fargate runner contract

The control plane runs R Shiny app replicas as Fargate tasks using a
"runner image" that fetches the app bundle from the control plane and executes
it. See `docs/fargate-runner-contract.md` for the full protocol.

The module passes the runner the cluster ARN, private subnet IDs, app task
security group, IAM role ARNs, log group name, and AWS region as
`SHINYHUB_RUNTIME_FARGATE_*` environment variables. These satisfy every
required `FargateRuntimeConfig` field in `internal/config/config.go`.

## Plan and apply

```sh
cd examples/minimal
terraform init
terraform plan   # requires AWS credentials
terraform apply
```

`terraform plan` and `terraform apply` are account-bound and are NOT run in
this repository's CI pipeline. The `make iac-validate` target runs only
`fmt -check`, `init -backend=false`, and `validate` (no AWS credentials needed).

## Inputs

| Name | Type | Default | Description |
|------|------|---------|-------------|
| `vpc_id` | string | (required) | Existing VPC ID |
| `public_subnet_ids` | list(string) | (required) | Subnet IDs for the ALB |
| `private_subnet_ids` | list(string) | (required) | Subnet IDs for ECS and RDS |
| `auth_secret_arn` | string | (required) | Secrets Manager ARN for SHINYHUB_AUTH_SECRET |
| `trusted_proxy_cidrs` | list(string) | (required) | ALB subnet CIDRs for SHINYHUB_TRUSTED_PROXIES |
| `image` | string | `ghcr.io/rvben/shinyhub:latest` | Control-plane image |
| `runner_image_python` | string | `ghcr.io/astral-sh/uv:python3.12-bookworm-slim` | Python runner image |
| `runner_image_r` | string | `rocker/r-base` | R runner image |
| `certificate_arn` | string | `""` | ACM certificate ARN (enables HTTPS listener) |
| `cp_cpu` | number | `512` | Control-plane task CPU units |
| `cp_memory` | number | `1024` | Control-plane task memory (MiB) |
| `cp_desired_count` | number | `1` | Control-plane service replica count |
| `app_cpu_units` | number | `512` | App task CPU units |
| `app_memory_mb` | number | `1024` | App task memory (MiB) |
| `db_instance_class` | string | `db.t4g.micro` | RDS instance class |
| `db_name` | string | `shinyhub` | PostgreSQL database name |
| `db_username` | string | `shinyhub` | PostgreSQL master username |
| `fargate_secrets_name_prefix` | string | `""` | Secrets Manager prefix for per-app secrets |
| `name_prefix` | string | `shinyhub` | Resource name prefix |
| `tags` | map(string) | `{}` | Additional resource tags |

## Outputs

| Name | Description |
|------|-------------|
| `alb_dns_name` | ALB DNS name (CNAME/A-alias target) |
| `ecs_cluster_arn` | ECS cluster ARN |
| `rds_endpoint` | RDS endpoint (host:port) |
| `cp_task_role_arn` | Control-plane task role ARN |
| `app_task_role_arn` | App runner task role ARN |
| `app_log_group_name` | CloudWatch log group for app tasks |
| `cp_log_group_name` | CloudWatch log group for control-plane |
| `db_dsn_secret_arn` | Secrets Manager ARN for the DB DSN secret |
