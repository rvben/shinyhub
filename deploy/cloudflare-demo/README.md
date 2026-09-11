# Cloudflare public demo

This deployment runs `demo.shinyhub.dev` and `apps.demo.shinyhub.dev` through
one Cloudflare Worker and one stable Cloudflare Container instance. The
container sleeps after ten idle minutes and reconstructs its curated fleet from
the image whenever Cloudflare replaces its ephemeral filesystem.

The Worker keeps the container cold start out of the first-page critical path.
When a visitor opens the demo while the container is asleep, `/` returns a small
self-contained boot page from the edge immediately and starts the named
container in the background. The page
waits on `/__demo/ready`, which reports the container's state while it is asleep
and gates on ShinyHub's real `/healthz` once it is up, then reloads into the
normal UI. A start does not always take, and that probe deliberately starts
nothing, so when it reports the container down the page reopens the demo rather
than waiting on it: reopening is a navigation, and a navigation is the only
request that may start a container. Warm requests continue to proxy directly
without showing the boot page.

Memory and disk bill for the whole time the container is awake, so the Worker
decides at the edge what is allowed to reach it (`src/edge-policy.ts`). It
serves `robots.txt` itself, and on the app origin it answers with a static 404
whatever the server would 404 anyway, mirroring `internal/apporigin`. Before
that gate existed, sparse automated traffic to control-plane paths woke the demo
around the clock and was the entire metered charge on the bill.

While the container is asleep, exactly two requests may start it: a browser
navigating to `/` or `/login` on the demo host, and the start page's button. The
first is recognised by `Sec-Fetch-Dest: document`, which browsers generate and a
page cannot set, so unlike `Accept: text/html` it is not something a crawler
produces merely by asking for HTML. The second rests on a plainer fact: bots
fetch and parse, they do not submit forms, so a POST to `/__demo/start` is the
one request the gate can believe with no headers at all. That is what lets a
visitor through whose browser tells the edge nothing.

Everything else on an entry path is answered with the start page, a 200 rendered
at the edge that costs nothing and says what is true: the demo is asleep, and
here is the button that starts it. Nothing on an entry path is refused, so a
shared link previews as the demo rather than as an error, which is why the page
carries Open Graph tags. Requests off the entry paths are sent to the entry page
if they look like a page load at all, including a link into an app on the app
origin, whose page could not be served before the container is up anyway; the
redirect names that page absolutely, because the app origin does not serve it.
Only what is neither is refused with a 503 that never touches the container.

A deep link has to survive the wake, because the demo's apps are private and a
visitor arriving cold has no session yet. The path they asked for therefore
rides the whole cold path as a `demo_next` query parameter: the redirect to the
entry page, the start page's form, the wake page, and finally the one-click
entry POST, which is where the session that link needed finally exists and the
visitor is sent on to the page instead of the dashboard. Every one of those URLs
is built by `demoURL`, which resolves against the demo host and drops any
destination that does not land back on it, so a `demo_next` a stranger writes
can only ever move someone around the demo.

Reading container state is a round trip to the Durable Object, so the Worker
holds its own observation for a few seconds and forwards warm traffic without
asking again. The container sleeps only after ten idle minutes, so one seen
healthy that recently is still up; the memo can at worst forward to a container
that has since crashed, which starts it again, and it can never refuse a visitor
the gate would have admitted.

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
