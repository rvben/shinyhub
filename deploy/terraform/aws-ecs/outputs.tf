output "alb_dns_name" {
  description = "DNS name of the Application Load Balancer. Point your CNAME / A-alias record here."
  value       = aws_lb.this.dns_name
}

output "ecs_cluster_arn" {
  description = "ARN of the ECS cluster that runs the control plane and app tasks."
  value       = aws_ecs_cluster.this.arn
}

output "rds_endpoint" {
  description = "RDS instance endpoint (host:port). Used to construct SHINYHUB_DB_DSN."
  value       = "${aws_db_instance.this.address}:${aws_db_instance.this.port}"
}

output "cp_task_role_arn" {
  description = "ARN of the control-plane ECS task role. Attach additional policies here when the runtime needs extra AWS permissions."
  value       = aws_iam_role.cp_task.arn
}

output "app_task_role_arn" {
  description = "ARN of the app runner task role. Attach additional policies here for apps that call AWS APIs (S3, DynamoDB, etc.)."
  value       = aws_iam_role.app_task.arn
}

output "app_log_group_name" {
  description = "CloudWatch log group name for app runner task logs."
  value       = aws_cloudwatch_log_group.app.name
}

output "cp_log_group_name" {
  description = "CloudWatch log group name for control-plane task logs."
  value       = aws_cloudwatch_log_group.cp.name
}

output "db_dsn_secret_arn" {
  description = "ARN of the Secrets Manager secret holding the full Postgres DSN (SHINYHUB_DB_DSN)."
  value       = aws_secretsmanager_secret.db_dsn.arn
  sensitive   = true
}
