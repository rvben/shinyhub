# Cloudflare public demo

This deployment runs `demo.shinyhub.dev` and `apps.demo.shinyhub.dev` through
one Cloudflare Worker and one stable Cloudflare Container instance. The
container sleeps after ten idle minutes and reconstructs its curated fleet from
the image whenever Cloudflare replaces its ephemeral filesystem.

The Worker keeps the container cold start out of the first-page critical path.
When the container is asleep, `/` returns a small self-contained boot page from
the edge immediately and starts the named container in the background. The page
waits on `/__demo/ready`, which gates on ShinyHub's real `/healthz`, then reloads
into the normal UI. Warm requests continue to proxy directly without showing the
boot page.

Cloudflare Containers require the Workers Paid plan ($5 USD/month minimum).
The plan's included container allowance covers an idle or lightly used demo;
usage beyond that allowance is metered.

Anonymous visitors can run public apps. The control-plane tour offers one-click
entry through the Worker, which exchanges the fixed read-only demo credential
for ShinyHub's normal HttpOnly session cookie. The password form remains a
collapsed fallback; neither path has mutation permissions. The demo account is
bootstrapped with a display name and representative group memberships so the
Identity application demonstrates the complete signed-claims contract.

The demo deliberately uses ShinyHub's native runtime inside the outer container.
Cloudflare Containers do not expose a Docker daemon, and the six bundled apps
are repository-reviewed examples rather than visitor-provided code. This is a
product demo, not a multi-tenant sandbox.

## Verify locally

```bash
npm ci
npm run check
docker build -f Dockerfile -t shinyhub-cloudflare-demo ../..
docker run --rm -p 8080:8080 shinyhub-cloudflare-demo
```

## Deploy

```bash
npm ci
npm run check
npx wrangler deploy
../../scripts/demo-smoke.sh
```

The smoke suite verifies the one-click viewer session as well as HTTP and
WebSocket traffic for the bundled applications.

Release deployments use the GitHub `public-demo` environment. Configure the
Cloudflare account ID as the environment variable `CLOUDFLARE_ACCOUNT_ID` and
the deployment token as the environment secret `CLOUDFLARE_API_TOKEN`. The
token should use Cloudflare's **Edit Cloudflare Workers** template, restricted
to the demo account and the `shinyhub.dev` zone. Store only the bare token
value—not the `curl` verification command Cloudflare displays beside it. Keep
these credentials at the environment level rather than repository-wide so only
this deployment job can read them.

Rotate the token without creating a deployment gap: create the replacement,
update the environment secret, manually dispatch **Demo deployment** against
`main`, wait for its smoke test to pass, and only then revoke the superseded
token. The workflow validates the token and account scope before it starts the
container build, so authentication failures stay fast and explicit.

The Worker owns both custom domains and routes them to the named `public-demo`
container. `standard-1` supplies 0.5 vCPU, 4 GiB memory, and 8 GB ephemeral disk;
the ten-minute sleep timer limits idle spend.
