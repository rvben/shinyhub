locals {
  name = var.name_prefix
  tags = merge(var.tags, { ManagedBy = "terraform", Product = "shinyhub" })

  # SHINYHUB_TRUSTED_PROXIES accepts a comma-separated CIDR list
  # (internal/config applyEnv splits on comma).
  trusted_proxies = join(",", var.trusted_proxy_cidrs)
}

# ---------------------------------------------------------------------------
# Random password for the RDS master user
# ---------------------------------------------------------------------------

resource "random_password" "db" {
  length  = 32
  special = false
}

# ---------------------------------------------------------------------------
# Secrets Manager: RDS password
# ---------------------------------------------------------------------------

resource "aws_secretsmanager_secret" "db_password" {
  name                    = "${local.name}/db-password"
  description             = "ShinyHub RDS master password"
  recovery_window_in_days = 0
  tags                    = local.tags
}

resource "aws_secretsmanager_secret_version" "db_password" {
  secret_id     = aws_secretsmanager_secret.db_password.id
  secret_string = random_password.db.result
}

# ---------------------------------------------------------------------------
# CloudWatch log groups
# ---------------------------------------------------------------------------

resource "aws_cloudwatch_log_group" "cp" {
  name              = "/ecs/${local.name}/control-plane"
  retention_in_days = 30
  tags              = local.tags
}

resource "aws_cloudwatch_log_group" "app" {
  name              = "/ecs/${local.name}/apps"
  retention_in_days = 14
  tags              = local.tags
}

# ---------------------------------------------------------------------------
# Security groups
# Cross-SG references (ALB<->CP, CP<->app, CP<->RDS) use separate
# aws_security_group_rule resources to avoid Terraform dependency cycles.
# ---------------------------------------------------------------------------

# ALB security group: accepts HTTP (and HTTPS when certificate_arn is set).
# The egress rule to the CP is separated out below.
resource "aws_security_group" "alb" {
  name        = "${local.name}-alb"
  description = "ShinyHub ALB: allow inbound HTTP/HTTPS from internet"
  vpc_id      = var.vpc_id
  tags        = merge(local.tags, { Name = "${local.name}-alb" })
}

resource "aws_security_group_rule" "alb_ingress_http" {
  security_group_id = aws_security_group.alb.id
  type              = "ingress"
  description       = "HTTP from internet"
  from_port         = 80
  to_port           = 80
  protocol          = "tcp"
  cidr_blocks       = ["0.0.0.0/0"]
}

resource "aws_security_group_rule" "alb_ingress_https" {
  count             = var.certificate_arn != "" ? 1 : 0
  security_group_id = aws_security_group.alb.id
  type              = "ingress"
  description       = "HTTPS from internet"
  from_port         = 443
  to_port           = 443
  protocol          = "tcp"
  cidr_blocks       = ["0.0.0.0/0"]
}

# ALB egress to CP on 8080 (cross-SG reference; separate resource to avoid cycle).
resource "aws_security_group_rule" "alb_egress_cp" {
  security_group_id        = aws_security_group.alb.id
  type                     = "egress"
  description              = "Forward to control plane"
  from_port                = 8080
  to_port                  = 8080
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.cp.id
}

# Control-plane security group.
resource "aws_security_group" "cp" {
  name        = "${local.name}-cp"
  description = "ShinyHub control plane: accept ALB, forward to RDS and app tasks"
  vpc_id      = var.vpc_id
  tags        = merge(local.tags, { Name = "${local.name}-cp" })
}

# CP ingress from ALB on 8080.
resource "aws_security_group_rule" "cp_ingress_alb" {
  security_group_id        = aws_security_group.cp.id
  type                     = "ingress"
  description              = "From ALB"
  from_port                = 8080
  to_port                  = 8080
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.alb.id
}

# CP egress: all outbound (AWS APIs, ECR, Secrets Manager, bundle fetch).
resource "aws_security_group_rule" "cp_egress_all" {
  security_group_id = aws_security_group.cp.id
  type              = "egress"
  description       = "All outbound (AWS APIs, ECR, bundle fetch)"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"]
}

# App-task security group.
resource "aws_security_group" "app" {
  name        = "${local.name}-app"
  description = "ShinyHub app tasks: accept traffic from control plane"
  vpc_id      = var.vpc_id
  tags        = merge(local.tags, { Name = "${local.name}-app" })
}

# App ingress from CP (all TCP so the proxy can reach any app port).
resource "aws_security_group_rule" "app_ingress_cp" {
  security_group_id        = aws_security_group.app.id
  type                     = "ingress"
  description              = "All TCP from control plane"
  from_port                = 0
  to_port                  = 65535
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.cp.id
}

# App self-ingress for inter-task traffic.
resource "aws_security_group_rule" "app_ingress_self" {
  security_group_id = aws_security_group.app.id
  type              = "ingress"
  description       = "Self"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  self              = true
}

# App egress: all outbound.
resource "aws_security_group_rule" "app_egress_all" {
  security_group_id = aws_security_group.app.id
  type              = "egress"
  description       = "All outbound"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"]
}

# RDS security group: accepts only from the control-plane SG on 5432.
resource "aws_security_group" "rds" {
  name        = "${local.name}-rds"
  description = "ShinyHub RDS: accept Postgres from control plane only"
  vpc_id      = var.vpc_id
  tags        = merge(local.tags, { Name = "${local.name}-rds" })
}

resource "aws_security_group_rule" "rds_ingress_cp" {
  security_group_id        = aws_security_group.rds.id
  type                     = "ingress"
  description              = "Postgres from control plane"
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.cp.id
}

# ---------------------------------------------------------------------------
# RDS (PostgreSQL 16, single-AZ, encrypted)
# ---------------------------------------------------------------------------

resource "aws_db_subnet_group" "this" {
  name       = local.name
  subnet_ids = var.private_subnet_ids
  tags       = local.tags
}

resource "aws_db_instance" "this" {
  identifier           = local.name
  engine               = "postgres"
  engine_version       = "16"
  instance_class       = var.db_instance_class
  allocated_storage    = 20
  storage_type         = "gp3"
  storage_encrypted    = true
  db_name              = var.db_name
  username             = var.db_username
  password             = random_password.db.result
  db_subnet_group_name = aws_db_subnet_group.this.name

  vpc_security_group_ids = [aws_security_group.rds.id]

  multi_az            = false
  publicly_accessible = false
  skip_final_snapshot = true
  deletion_protection = false

  tags = local.tags
}

# ---------------------------------------------------------------------------
# ECS cluster
# ---------------------------------------------------------------------------

resource "aws_ecs_cluster" "this" {
  name = local.name
  tags = local.tags
}

resource "aws_ecs_cluster_capacity_providers" "this" {
  cluster_name       = aws_ecs_cluster.this.name
  capacity_providers = ["FARGATE"]

  default_capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 1
  }
}

# ---------------------------------------------------------------------------
# IAM: task execution role
# Allows ECS to pull images from ECR and read secrets from Secrets Manager.
# ---------------------------------------------------------------------------

resource "aws_iam_role" "execution" {
  name = "${local.name}-execution"
  tags = local.tags

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "execution_ecr" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "execution_secrets" {
  name = "secrets"
  role = aws_iam_role.execution.id

  # The execution role needs GetSecretValue for:
  #   - var.auth_secret_arn: the operator's pre-existing SHINYHUB_AUTH_SECRET
  #   - aws_secretsmanager_secret.db_password: the module-created RDS password
  # Both are read by ECS at task launch to inject into the container via the
  # task definition secrets block. The execution role is also assumed by app
  # runner tasks (var.app_execution_role_arn not yet separate; runner tasks
  # that use fargate_secrets_name_prefix need GetSecretValue on the per-app
  # secrets -- those are covered by the task role policy below when the same
  # execution role is reused, or the operator creates a separate runner
  # execution role with broader scope).
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid    = "ReadShinyHubSecrets"
      Effect = "Allow"
      Action = "secretsmanager:GetSecretValue"
      Resource = [
        var.auth_secret_arn,
        aws_secretsmanager_secret.db_password.arn,
      ]
    }]
  })
}

# ---------------------------------------------------------------------------
# IAM: control-plane task role
# Grants the runtime's ECS + EC2 + Secrets Manager calls. Each statement is
# annotated with the source code path that drives the permission requirement.
# ---------------------------------------------------------------------------

resource "aws_iam_role" "cp_task" {
  name = "${local.name}-cp-task"
  tags = local.tags

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "cp_task" {
  name = "fargate-runtime"
  role = aws_iam_role.cp_task.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      # internal/fargate/fargate.go: ECSClient.RunTask
      # Called by Runtime.Start and Runtime.RunOnce to launch a Fargate task.
      {
        Sid      = "RunTask"
        Effect   = "Allow"
        Action   = ["ecs:RunTask"]
        Resource = "*"
        Condition = {
          ArnEquals = {
            "ecs:cluster" = aws_ecs_cluster.this.arn
          }
        }
      },
      # internal/fargate/fargate.go: ECSClient.StopTask
      # Called by Runtime.stop (via Signal, RunOnce cancel, waitForIP cleanup).
      {
        Sid      = "StopTask"
        Effect   = "Allow"
        Action   = ["ecs:StopTask"]
        Resource = "*"
        Condition = {
          ArnEquals = {
            "ecs:cluster" = aws_ecs_cluster.this.arn
          }
        }
      },
      # internal/fargate/fargate.go: ECSClient.DescribeTasks
      # Called by waitForIP, describeTask, Inventory, ListManagedTasks.
      {
        Sid    = "DescribeTasks"
        Effect = "Allow"
        Action = [
          "ecs:DescribeTasks",
          "ecs:ListTasks",
        ]
        Resource = "*"
        Condition = {
          ArnEquals = {
            "ecs:cluster" = aws_ecs_cluster.this.arn
          }
        }
      },
      # internal/fargate/fargate.go: ECSClient.DescribeTaskDefinition
      # Called by describeBaseTaskDef when resolveTaskDef clones a per-app revision.
      {
        Sid      = "DescribeTaskDefinition"
        Effect   = "Allow"
        Action   = ["ecs:DescribeTaskDefinition"]
        Resource = "*"
      },
      # internal/fargate/fargate.go: ECSClient.RegisterTaskDefinition
      # Called by resolveTaskDef to create a per-app revision with a secrets block.
      {
        Sid      = "RegisterTaskDefinition"
        Effect   = "Allow"
        Action   = ["ecs:RegisterTaskDefinition"]
        Resource = "*"
      },
      # internal/fargate/fargate.go: ECSClient.ListTaskDefinitions + DeregisterTaskDefinition
      # Called by CleanupApp on app delete to remove per-app task-def revisions.
      {
        Sid    = "ManageTaskDefinitions"
        Effect = "Allow"
        Action = [
          "ecs:ListTaskDefinitions",
          "ecs:DeregisterTaskDefinition",
        ]
        Resource = "*"
      },
      # ecs:PassRole is required for RunTask to pass both the execution role and
      # the app task role to ECS. Without this, RunTask returns AccessDenied even
      # when the caller has ecs:RunTask.
      {
        Sid    = "PassRoleToTasks"
        Effect = "Allow"
        Action = ["iam:PassRole"]
        Resource = [
          aws_iam_role.execution.arn,
          aws_iam_role.app_task.arn,
        ]
      },
      # internal/fargate/fargate.go: EC2Client.DescribeNetworkInterfaces
      # Called by routeIP when RouteViaPublicIP is set to resolve task public IPs.
      # Included here so the role is complete even for dev/test RouteViaPublicIP mode.
      {
        Sid      = "DescribeENI"
        Effect   = "Allow"
        Action   = ["ec2:DescribeNetworkInterfaces"]
        Resource = "*"
      },
      # internal/fargate/secrets.go: secretsManagerAPI.CreateSecret + PutSecretValue
      # Called by SecretsStore.Put when routing app secret env via Secrets Manager.
      # Scoped to the configured name prefix so a compromised CP cannot touch other secrets.
      {
        Sid    = "ManageAppSecrets"
        Effect = "Allow"
        Action = [
          "secretsmanager:CreateSecret",
          "secretsmanager:PutSecretValue",
          "secretsmanager:DeleteSecret",
          "secretsmanager:ListSecrets",
        ]
        Resource = var.fargate_secrets_name_prefix != "" ? (
          "arn:aws:secretsmanager:*:*:secret:${var.fargate_secrets_name_prefix}/*"
        ) : "arn:aws:secretsmanager:*:*:secret:shinyhub/*"
      },
    ]
  })
}

# ---------------------------------------------------------------------------
# IAM: app runner task role
# Minimal role for runner tasks. Extend as needed for S3, KMS, etc.
# ---------------------------------------------------------------------------

resource "aws_iam_role" "app_task" {
  name = "${local.name}-app-task"
  tags = local.tags

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ecs-tasks.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

# NOTE: the app task role deliberately has NO Secrets Manager access. ECS injects
# per-app secret env vars at container start via the EXECUTION role (see
# aws_iam_role_policy.execution_secrets), not the task role. The task role is the
# identity available to (untrusted) app code AFTER launch; granting it
# GetSecretValue on the shared "${prefix}/app-*" wildcard would let any app task
# read every other tenant's secrets - cross-tenant exfiltration. This matches the
# least-privilege model documented in SECURITY.md. An operator whose app makes
# runtime Secrets Manager SDK calls should add a grant scoped to that app's own
# secret ARN only, on a per-app role - never the shared wildcard here.

# ---------------------------------------------------------------------------
# Control-plane ECS task definition
# ---------------------------------------------------------------------------

# SHINYHUB_DB_DSN is assembled from the RDS endpoint. The format for Postgres:
# postgres://<user>:<password>@<host>:5432/<db>
# The password comes from the Secrets Manager secret, injected via the secrets
# block at ECS launch time. We cannot embed it inline in the env block, so we
# use a data source pattern: the task env carries all the non-secret parts and
# the secret is injected as a separate env-var sourced by ARN. ShinyHub's
# SHINYHUB_DB_DSN overrides the database block in shinyhub.yaml.
# We also need the database driver set to postgres.
# Workaround: pass the full DSN without the password as SHINYHUB_DB_DSN_PREFIX
# and the password as a secret, then use a container command to assemble them --
# but ShinyHub is distroless. Simpler: store the full DSN (with embedded password)
# in a dedicated Secrets Manager secret so the execution role can inject it.

resource "aws_secretsmanager_secret" "db_dsn" {
  name                    = "${local.name}/db-dsn"
  description             = "ShinyHub SHINYHUB_DB_DSN (full Postgres connection string)"
  recovery_window_in_days = 0
  tags                    = local.tags
}

resource "aws_secretsmanager_secret_version" "db_dsn" {
  secret_id     = aws_secretsmanager_secret.db_dsn.id
  secret_string = "postgres://${var.db_username}:${random_password.db.result}@${aws_db_instance.this.address}:5432/${var.db_name}?sslmode=require"
}

resource "aws_iam_role_policy" "execution_db_dsn" {
  name = "db-dsn"
  role = aws_iam_role.execution.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "ReadDBDSN"
      Effect   = "Allow"
      Action   = "secretsmanager:GetSecretValue"
      Resource = aws_secretsmanager_secret.db_dsn.arn
    }]
  })
}

resource "aws_ecs_task_definition" "cp" {
  family                   = "${local.name}-cp"
  cpu                      = tostring(var.cp_cpu)
  memory                   = tostring(var.cp_memory)
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.cp_task.arn
  tags                     = local.tags

  container_definitions = jsonencode([{
    name      = "shinyhub"
    image     = var.image
    essential = true

    portMappings = [{
      containerPort = 8080
      protocol      = "tcp"
    }]

    # Secrets injected by ECS at launch (values never appear in DescribeTasks).
    # The execution role's GetSecretValue policy covers these ARNs.
    secrets = [
      {
        name      = "SHINYHUB_AUTH_SECRET"
        valueFrom = var.auth_secret_arn
      },
      {
        name      = "SHINYHUB_DB_DSN"
        valueFrom = aws_secretsmanager_secret.db_dsn.arn
      },
    ]

    # Environment: all non-secret config expressed as SHINYHUB_* env vars.
    # Every required FargateRuntimeConfig field has a corresponding env var;
    # see internal/config/config.go applyEnv for the full list.
    environment = [
      # Database driver override (the env var sets the DSN; we must also tell
      # ShinyHub to use postgres).
      { name = "SHINYHUB_RUNTIME_MODE", value = "fargate" },

      # Trusted proxies: ALB subnet CIDRs. Without this, ALB node IPs appear
      # as client addresses, breaking per-IP rate limiting and audit logs.
      # (internal/config applyEnv SHINYHUB_TRUSTED_PROXIES)
      { name = "SHINYHUB_TRUSTED_PROXIES", value = local.trusted_proxies },

      # --- FargateRuntimeConfig required fields ---
      # (internal/config validateFargate enforces these)

      # runtime.fargate.cluster -> SHINYHUB_RUNTIME_FARGATE_CLUSTER
      { name = "SHINYHUB_RUNTIME_FARGATE_CLUSTER", value = aws_ecs_cluster.this.name },

      # runtime.fargate.subnets -> SHINYHUB_RUNTIME_FARGATE_SUBNETS (CSV)
      # App tasks run in the same private subnets as the control plane.
      { name = "SHINYHUB_RUNTIME_FARGATE_SUBNETS", value = join(",", var.private_subnet_ids) },

      # runtime.fargate.security_groups -> SHINYHUB_RUNTIME_FARGATE_SECURITY_GROUPS (CSV)
      # App tasks get the app-task SG so the control plane can reach them.
      { name = "SHINYHUB_RUNTIME_FARGATE_SECURITY_GROUPS", value = aws_security_group.app.id },

      # runtime.fargate.task_cpu_units -> SHINYHUB_RUNTIME_FARGATE_TASK_CPU_UNITS
      { name = "SHINYHUB_RUNTIME_FARGATE_TASK_CPU_UNITS", value = tostring(var.app_cpu_units) },

      # runtime.fargate.task_memory_mb -> SHINYHUB_RUNTIME_FARGATE_TASK_MEMORY_MB
      { name = "SHINYHUB_RUNTIME_FARGATE_TASK_MEMORY_MB", value = tostring(var.app_memory_mb) },

      # runtime.fargate.control_plane_url -> SHINYHUB_RUNTIME_FARGATE_CONTROL_PLANE_URL
      # Runners fetch their bundle from this URL over the VPC-internal network.
      # The ALB DNS name is reachable from inside the VPC.
      { name = "SHINYHUB_RUNTIME_FARGATE_CONTROL_PLANE_URL", value = "http://${aws_lb.this.dns_name}" },

      # --- Runtime fields supplied via ARNs passed as env (CP env protocol) ---
      # docs/fargate-runner-contract.md: the module supplies these ARNs as CP env
      # so the runtime can pass them when registering per-app task definitions.

      # Execution role ARN for app runner tasks (needed for ECR pull + secrets).
      { name = "SHINYHUB_RUNTIME_FARGATE_APP_EXECUTION_ROLE_ARN", value = aws_iam_role.execution.arn },

      # Task role ARN for app runner tasks.
      { name = "SHINYHUB_RUNTIME_FARGATE_APP_TASK_ROLE_ARN", value = aws_iam_role.app_task.arn },

      # CloudWatch log group for app runner tasks.
      { name = "SHINYHUB_RUNTIME_FARGATE_APP_LOG_GROUP", value = aws_cloudwatch_log_group.app.name },

      # AWS region (SDK default chain covers this, but explicit is clearer).
      { name = "SHINYHUB_RUNTIME_FARGATE_REGION", value = data.aws_region.current.name },

      # --- Optional fargate fields ---

      # Secrets name prefix for per-app secret env routing.
      { name = "SHINYHUB_RUNTIME_FARGATE_SECRETS_NAME_PREFIX", value = var.fargate_secrets_name_prefix },

      # Runner images for the Python and R backends.
      { name = "SHINYHUB_RUNTIME_DOCKER_IMAGE_PYTHON", value = var.runner_image_python },
      { name = "SHINYHUB_RUNTIME_DOCKER_IMAGE_R", value = var.runner_image_r },
    ]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.cp.name
        "awslogs-region"        = data.aws_region.current.name
        "awslogs-stream-prefix" = "cp"
      }
    }
  }])
}

# ---------------------------------------------------------------------------
# ALB
# ---------------------------------------------------------------------------

resource "aws_lb" "this" {
  name               = local.name
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = var.public_subnet_ids
  tags               = local.tags
}

resource "aws_lb_target_group" "cp" {
  name        = "${local.name}-cp"
  port        = 8080
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "ip"
  tags        = local.tags

  health_check {
    path                = "/healthz"
    protocol            = "HTTP"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    interval            = 30
    timeout             = 5
    matcher             = "200"
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"
  tags              = local.tags

  # When certificate_arn is set, redirect HTTP to HTTPS; otherwise forward.
  dynamic "default_action" {
    for_each = var.certificate_arn != "" ? [1] : []
    content {
      type = "redirect"
      redirect {
        port        = "443"
        protocol    = "HTTPS"
        status_code = "HTTP_301"
      }
    }
  }

  dynamic "default_action" {
    for_each = var.certificate_arn == "" ? [1] : []
    content {
      type             = "forward"
      target_group_arn = aws_lb_target_group.cp.arn
    }
  }
}

resource "aws_lb_listener" "https" {
  count             = var.certificate_arn != "" ? 1 : 0
  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.certificate_arn
  tags              = local.tags

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.cp.arn
  }
}

# ---------------------------------------------------------------------------
# ECS service (control plane)
# ---------------------------------------------------------------------------

resource "aws_ecs_service" "cp" {
  name            = "${local.name}-cp"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.cp.arn
  desired_count   = var.cp_desired_count
  launch_type     = "FARGATE"
  tags            = local.tags

  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [aws_security_group.cp.id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.cp.arn
    container_name   = "shinyhub"
    container_port   = 8080
  }

  lifecycle {
    ignore_changes = [task_definition, desired_count]
  }

  depends_on = [
    aws_lb_listener.http,
    aws_iam_role_policy_attachment.execution_ecr,
    aws_iam_role_policy.execution_secrets,
    aws_iam_role_policy.execution_db_dsn,
    aws_iam_role_policy.cp_task,
  ]
}

# ---------------------------------------------------------------------------
# Data sources
# ---------------------------------------------------------------------------

data "aws_region" "current" {}
