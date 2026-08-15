# --- Networking (required; module does not create a VPC) ---

variable "vpc_id" {
  description = "ID of an existing VPC. The module does not create a VPC."
  type        = string
}

variable "public_subnet_ids" {
  description = "Subnet IDs for the ALB listeners (must be public)."
  type        = list(string)
}

variable "private_subnet_ids" {
  description = "Subnet IDs for the ECS control-plane service and RDS instance (private, no direct internet access)."
  type        = list(string)
}

# --- Secrets (required) ---

variable "auth_secret_arn" {
  description = "ARN of an existing Secrets Manager secret whose value is SHINYHUB_AUTH_SECRET (>=32 chars, random hex). The module never reads the value -- it is injected into the task via a secrets block."
  type        = string
}

variable "trusted_proxy_cidrs" {
  description = <<-EOT
    Comma-separated CIDR list injected as SHINYHUB_TRUSTED_PROXIES. REQUIRED.
    Behind an ALB every TCP connection peers from an ALB node, so the server
    would otherwise log ALB IPs as client addresses and the per-IP login
    rate-limit would apply to the ALB rather than end users. Set this to the
    ALB's subnet CIDRs (e.g. ["10.0.1.0/24","10.0.2.0/24"]). The value is
    joined with commas and passed as a single env-var string, which
    SHINYHUB_TRUSTED_PROXIES accepts (see internal/config applyEnv).
  EOT
  type        = list(string)
  # No default: the operator MUST supply the correct ALB subnet CIDRs.
}

# --- Image ---

variable "image" {
  description = "Digest-pinned Docker image for the ShinyHub control-plane container."
  type        = string

  validation {
    condition     = can(regex("@sha256:[0-9a-f]{64}$", var.image))
    error_message = "image must be pinned by sha256 digest."
  }
}

variable "runner_image_python" {
  description = "Digest-pinned Fargate runner image for Python Shiny apps."
  type        = string

  validation {
    condition     = can(regex("@sha256:[0-9a-f]{64}$", var.runner_image_python))
    error_message = "runner_image_python must be pinned by sha256 digest."
  }
}

variable "runner_image_r" {
  description = "Digest-pinned Fargate runner image for R Shiny apps."
  type        = string

  validation {
    condition     = can(regex("@sha256:[0-9a-f]{64}$", var.runner_image_r))
    error_message = "runner_image_r must be pinned by sha256 digest."
  }
}

# --- TLS ---

variable "certificate_arn" {
  description = "ACM certificate ARN for the required HTTPS listener. HTTP is redirect-only."
  type        = string

  validation {
    condition     = can(regex("^arn:[^:]+:acm:[^:]+:[0-9]{12}:certificate/", var.certificate_arn))
    error_message = "certificate_arn must be a non-empty ACM certificate ARN."
  }
}

variable "base_url" {
  description = "Public HTTPS control-plane origin covered by certificate_arn (for example https://hub.example.com)."
  type        = string

  validation {
    condition     = can(regex("^https://[^/]+$", var.base_url))
    error_message = "base_url must be a bare HTTPS origin."
  }
}

variable "app_origin" {
  description = "Separate public HTTPS origin for proxied apps (for example https://apps.example.com). Route it to the same ALB."
  type        = string

  validation {
    condition     = can(regex("^https://[^/]+$", var.app_origin))
    error_message = "app_origin must be a bare HTTPS origin."
  }
}

# --- ECS sizing ---

variable "cp_cpu" {
  description = "CPU units for the control-plane Fargate task (1 vCPU = 1024)."
  type        = number
  default     = 512
}

variable "cp_memory" {
  description = "Memory in MiB for the control-plane Fargate task."
  type        = number
  default     = 1024
}

variable "cp_desired_count" {
  description = "Desired replica count for the control-plane ECS service. Default 1; HA multi-CP requires the lease config (documented as a future feature)."
  type        = number
  default     = 1
}

variable "app_cpu_units" {
  description = "Default CPU units for runner app tasks (1024 = 1 vCPU). Injected as SHINYHUB_RUNTIME_FARGATE_TASK_CPU_UNITS."
  type        = number
  default     = 512
}

variable "app_memory_mb" {
  description = "Default memory in MiB for runner app tasks. Must satisfy the Fargate CPU/memory matrix. Injected as SHINYHUB_RUNTIME_FARGATE_TASK_MEMORY_MB."
  type        = number
  default     = 1024
}

# --- RDS ---

variable "db_instance_class" {
  description = "RDS instance class for the PostgreSQL database."
  type        = string
  default     = "db.t4g.micro"
}

variable "db_name" {
  description = "PostgreSQL database name."
  type        = string
  default     = "shinyhub"
}

variable "db_username" {
  description = "PostgreSQL master username."
  type        = string
  default     = "shinyhub"
}

variable "db_multi_az" {
  description = "Create a Multi-AZ RDS deployment. Keep enabled for production resilience."
  type        = bool
  default     = true
}

variable "db_backup_retention_days" {
  description = "RDS automated backup retention in days."
  type        = number
  default     = 7

  validation {
    condition     = var.db_backup_retention_days >= 7 && var.db_backup_retention_days <= 35
    error_message = "db_backup_retention_days must be between 7 and 35."
  }
}

variable "db_deletion_protection" {
  description = "Protect the RDS instance from accidental deletion."
  type        = bool
  default     = true
}

# --- Fargate secrets routing (optional) ---

variable "fargate_secrets_name_prefix" {
  description = "Secrets Manager name prefix for per-app secret env routing (e.g. shinyhub/prod). Injected as SHINYHUB_RUNTIME_FARGATE_SECRETS_NAME_PREFIX. Leave empty to disable secret routing (app secret env stays as plaintext task overrides)."
  type        = string
  default     = ""
}

# --- Misc ---

variable "name_prefix" {
  description = "Short identifier prepended to all resource names and tags (e.g. 'shinyhub-prod')."
  type        = string
  default     = "shinyhub"
}

variable "tags" {
  description = "Additional resource tags applied to all taggable resources."
  type        = map(string)
  default     = {}
}
