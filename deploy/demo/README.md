# Public demo deployment

This stack runs the curated public instance at `demo.shinyhub.dev`. Application
traffic uses the separate `apps.demo.shinyhub.dev` origin so a compromised app
cannot inherit the control plane's browser origin.

The demo is intentionally not a public sandbox. Anonymous visitors may launch
the apps declared in `fleet.toml`; GitHub OAuth users receive the read-only
`viewer` role. Only the environment deploy token has administrative access.

## Host prerequisites

- A small Linux VM with Docker Engine and Compose v2
- TCP 80 and 443 open; SSH restricted to operator addresses
- DNS-only `A`/`AAAA` records for `demo` and `apps.demo` pointing to the VM
- A GitHub OAuth app with callback URL
  `https://demo.shinyhub.dev/api/auth/github/callback`

Start with at least 2 vCPU, 4 GiB RAM, and 30 GiB disk. The committed runtime,
session, storage, and hibernation caps keep this suitable for a small host; add
monitoring before increasing any limit.

## First deployment

```bash
cd deploy/demo
cp .env.example .env
$EDITOR .env
docker compose config --quiet
docker compose pull
docker compose up -d
../../scripts/demo-smoke.sh
../../scripts/demo-deploy.sh
```

The deploy script reconciles `fleet.toml` through the local loopback endpoint.
It does not place the deploy token on the command line or write credentials to
disk outside `.env`.

## Upgrade and rollback

Pin a new `SHINYHUB_IMAGE` in `.env`, pull it, create a backup, then recreate
the service:

```bash
docker compose pull shinyhub
docker compose exec shinyhub shinyhub backup --out /tmp/pre-upgrade.tar.gz
docker compose up -d shinyhub
../../scripts/demo-smoke.sh
```

Before the first real upgrade, configure an off-host destination for backups;
a backup that only exists on the demo VM is not disaster recovery.

## Security notes

- Keep Cloudflare records DNS-only unless the VM firewall is restricted to
  Cloudflare's published proxy ranges. Otherwise forwarded client IP headers
  can be spoofed.
- Do not add user uploads or arbitrary app deployment to this instance. The
  Docker socket deliberately gives ShinyHub authority over the host daemon;
  only repository-reviewed bundles belong here.
- Rotate the OAuth secret, auth secret, and deploy token on suspected exposure.
- `apps.demo.shinyhub.dev` must never be pointed at a different application;
  both hostnames are deliberately dispatched by ShinyHub.
