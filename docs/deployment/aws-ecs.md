# AWS ECS and Fargate

The Terraform module deploys an HTTPS control plane on ECS, PostgreSQL on RDS,
and isolated application tasks on ECS/Fargate.

It creates:

- an Application Load Balancer with HTTP-to-HTTPS redirect;
- an ECS service for the ShinyHub control plane;
- encrypted PostgreSQL 16 RDS storage;
- IAM roles restricted to the Fargate runtime API surface;
- CloudWatch log groups for control-plane and app tasks; and
- security groups for ALB, control-plane, database, and application traffic.

The module intentionally requires an existing VPC, public and private subnets,
an ACM certificate, and a Secrets Manager secret for `auth.secret`.

## Apply

```bash
cd deploy/terraform/aws-ecs/examples/minimal
terraform init
terraform plan
terraform apply
```

Control-plane and runner images must be pinned by digest. Set
`trusted_proxy_cidrs` to the ALB subnet CIDRs so rate limiting and audit events
use the real client address.

Review every input, security boundary, and output in the
[Terraform module README](https://github.com/rvben/shinyhub/blob/main/deploy/terraform/aws-ecs/README.md).
The application task protocol is documented separately in the
[Fargate runner contract](../fargate-runner-contract.md).
