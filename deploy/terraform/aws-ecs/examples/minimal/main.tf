# Minimal ShinyHub ECS/Fargate example.
#
# This example exercises terraform validate on the module with placeholder-but-
# type-valid values. It does NOT represent a working deployment: the VPC/subnet/
# secret IDs are fictional and the plan/apply steps require real AWS credentials.
# See deploy/terraform/aws-ecs/README.md for apply prerequisites.

module "shinyhub" {
  source = "../.."

  # Networking: use your existing VPC.
  vpc_id             = "vpc-12345678"
  public_subnet_ids  = ["subnet-aaaa0001", "subnet-aaaa0002"]
  private_subnet_ids = ["subnet-bbbb0001", "subnet-bbbb0002"]

  # Secrets: ARN of a pre-existing Secrets Manager secret holding the auth secret.
  auth_secret_arn = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:shinyhub/auth-secret-AbCdEf"

  # Resolve and review real multi-architecture digests before deployment.
  image               = "ghcr.io/rvben/shinyhub@sha256:0000000000000000000000000000000000000000000000000000000000000000"
  runner_image_python = "ghcr.io/astral-sh/uv@sha256:1111111111111111111111111111111111111111111111111111111111111111"
  runner_image_r      = "rocker/r-base@sha256:2222222222222222222222222222222222222222222222222222222222222222"

  # Trusted proxy CIDRs: ALB subnet CIDRs so the server sees real client IPs
  # rather than ALB node IPs in rate-limiting and audit logs.
  trusted_proxy_cidrs = ["10.0.1.0/24", "10.0.2.0/24"]

  # HTTPS is mandatory; HTTP redirects to this listener.
  certificate_arn = "arn:aws:acm:eu-west-1:123456789012:certificate/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
  base_url        = "https://hub.example.com"
  app_origin      = "https://apps.example.com"

  name_prefix = "shinyhub-prod"
  tags = {
    Environment = "production"
    Team        = "platform"
  }
}

output "alb_dns_name" {
  value = module.shinyhub.alb_dns_name
}

output "ecs_cluster_arn" {
  value = module.shinyhub.ecs_cluster_arn
}
