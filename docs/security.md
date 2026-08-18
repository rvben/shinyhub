# Security

ShinyHub is designed for self-hosted environments where platform operators
control who may deploy code. Its security model combines authentication,
per-application authorization, encrypted secrets, bounded resources, audit
history, and optional process or container isolation.

## Recommended deployment posture

- Terminate TLS at a maintained reverse proxy or load balancer.
- Bind the ShinyHub listener to a private interface or loopback.
- Use separate control-plane and application origins.
- Keep applications private unless anonymous access is intentional.
- Use Docker, remote workers, Fargate, or private Scaleway Serverless Containers
  when application authors should not share the control-plane host boundary.
- Apply CPU, memory, session, replica, bundle, and data quotas.
- Store `auth.secret`, OAuth credentials, deploy tokens, and database passwords
  in a secrets manager or owner-readable environment file.
- Enable metrics, structured logs, and retention appropriate to the installation.
- Back up the database, bundles, and persistent app-data directory together.

## Report a vulnerability

Do not open a public issue for a suspected vulnerability. Follow the private
reporting instructions and supported-version policy in
[`SECURITY.md`](https://github.com/rvben/shinyhub/blob/main/SECURITY.md).

## Related guides

- [Isolation](isolation.md)
- [Identity forwarding](identity.md)
- [Native OIDC](native-oidc.md)
- [Secret rotation](secret-rotation.md)
- [Reverse proxy configuration](reverse-proxy/deploying-behind-a-proxy.md)
- [Scaleway Serverless Containers](deployment/scaleway-serverless.md)
