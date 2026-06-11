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
  description = "Docker image for the ShinyHub control-plane container."
  type        = string
  default     = "ghcr.io/rvben/shinyhub:latest"
}

variable "runner_image_python" {
  description = "Fargate runner image for Python Shiny apps."
  type        = string
  default     = "ghcr.io/astral-sh/uv:python3.12-bookworm-slim"
}

variable "runner_image_r" {
  description = "Fargate runner image for R Shiny apps."
  type        = string
  default     = "rocker/r-base"
}

# --- TLS ---

variable "certificate_arn" {
  description = "ACM certificate ARN. When set, the ALB adds an HTTPS listener on port 443. When empty, HTTP only (evaluation/internal use)."
  type        = string
  default     = ""
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
