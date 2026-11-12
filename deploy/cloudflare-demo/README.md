# Cloudflare public demo

This deployment runs `demo.shinyhub.dev` and `apps.demo.shinyhub.dev` through
one Cloudflare Worker and one stable Cloudflare Container instance. The
container sleeps after ten idle minutes and reconstructs its curated fleet from
the image whenever Cloudflare replaces its ephemeral filesystem.

The Worker keeps the container cold start out of the first-page critical path.
When a visitor opens the demo while the container is asleep, `/` returns a small
self-contained boot page from the edge immediately and starts the named
container in the background. The page waits on `/__demo/ready`, which reports
the container's state while it is asleep and gates on ShinyHub's real `/healthz`
once it is up, then reloads into the normal UI. A start does not always take,
and that probe deliberately starts nothing, so when it reports the container
down the page reopens the demo rather than waiting on it: reopening is a
navigation, and a navigation is the only request that may start a container.
Warm requests continue to proxy directly without showing the boot page.

The SDK class supplies the bundled `entrypoint.sh` as its startup command. This
allows a bootstrap fix to reuse a pinned release image. Caddy opens the public
port only after every bundled app serves its actual page, so `/healthz` cannot
release the wake page while frameworks still return startup responses. Record
the Worker source and entrypoint hash alongside the image digest for a rollout.
Command changes apply on the next cold start; rolling back the Worker restores
the previous startup configuration.

Port readiness waits up to 90 seconds by default, including automatic starts
through the SDK's request proxy. This accommodates fleet bootstrap before Caddy
opens port 8080; request cancellation and explicit timeout overrides still apply.
The fleet readiness gate and startup failure alerts remain in place.

For Worker-only fixes, use `wrangler deploy --containers-rollout none` to retain
the deployed container image and allocation settings. Verify a genuine cold
start after the container sleeps, then check `/healthz` and all seven public
apps. `/__demo/status` reads lifecycle state without waking the container or
extending its idle deadline; use it when waiting for a natural sleep cycle.

The WebSocket relay coordinates both close handshakes with
the Durable Object inbound WebSocket API and a half-open outbound socket,
forwards text and binary frames in order, and
renews activity for messages. Active sockets defer idle shutdown; after the last
socket closes, the normal ten-minute idle window applies. A missing close
acknowledgement has a five-second deadline and emits `demo_websocket_failed`.
HTTP continues to use the container SDK. This is a focused workaround for the
SDK relay; retain the runtime tests when changing the SDK or compatibility date.
The inbound socket uses `ctx.acceptWebSocket` to avoid workerd's uncaught
`Network connection lost` error on clean client disconnects (workerd issue 5290).
`web_socket_manual_reply_to_close` preserves coordinated close handling for this
API; the backend uses `accept({ allowHalfOpen: true })`. Accepted outbound sockets
keep the object awake while their relay exists.
The outer Worker passes upgrades through unchanged. A second terminating relay
can turn a normal DO close into an abnormal cross-isolate disconnect.

The WebSocket smoke checks require both a successful upgrade and a clean close.
For deployments, `scripts/demo-smoke-verified.mjs` opens a temporary Cloudflare
tail before the suite, repeats marked passive status requests until registration
has propagated, and requires normal close evidence from both proxy ends for all
three apps. A
correlated exception, error log, failed invocation, missing evidence, or lost tail
fails verification even if the client reported success. The live tail does not
reliably include Durable Object platform exceptions: after deployment, also check
persisted Cloudflare Workers Logs for the deployed version and marked smoke URLs.
A passing client or tail check alone is insufficient.
Successful streamed HTTP responses may have a `canceled` invocation outcome;
these require the client HTTP checks to pass and never substitute for WebSocket
close evidence. The smoke script marks its retryable wake, readiness, and version
probes. A canceled probe without a completed response is tolerated only after
telemetry confirms a later successful probe of the same kind. The same holds for
a probe whose record carries the platform's "new version rollout" container
error: a deployment replaces the container under whichever probe is in flight,
so that probe counts as interrupted, including its own response, until a later
one of its kind succeeds. Only that exact message, on a marked probe, is treated
this way; every other exception and error log still fails the run. Event timestamps preserve this ordering even when
tail records arrive late. Synthetic WebSocket clients send a correlation header because
query values are redacted in tail events. Marked passive status probes publish
the DO's bounded synthetic close records through the tail, so verification
checks the actual container relay rather than relying on missing DO tail logs.
Cloudflare may report `responseStreamDisconnected` or `canceled` for an otherwise successful
transparent upgrade; this is accepted only for marked WebSocket paths, with
normal close evidence from every actual container relay. Exceptions and error
logs remain fatal regardless of close evidence. Install the demo Worker's npm dependencies
before running the verifier (it uses `ws` for bounded connection cleanup).
The deployment token needs Worker tail access (the existing Workers Scripts
permission). The session
is deleted in `finally`; raw tail records, cookies and unrelated visitor details
are never printed or saved. A synthetic UUID correlates requests without using
visitor identifiers. The ordinary smoke script remains usable without Cloudflare
credentials for client-only checks.

Memory and disk bill for the whole time the container is awake, so the Worker
decides at the edge what is allowed to reach it (`src/edge-policy.ts`). It
serves `robots.txt` itself, and on the app origin it answers with a static 404
whatever the server would 404 anyway, mirroring `internal/apporigin`. Before
that gate existed, sparse automated traffic to control-plane paths woke the demo
around the clock and was the entire metered charge on the bill.

The control host uses an allowlist generated from the registered API methods,
dashboard route contract, embedded static files, and curated fleet. Unknown
paths, asset names, app names, and project names receive an edge 404 in every
lifecycle state. Scanner paths such as `/.env`, `/.git/config`, `/wp-login.php`,
`/phpmyadmin/`, `/cgi-bin/`, `/debug/pprof/`, `/metrics`, and `/internal/` never
resolve a container handle. Malformed escapes, encoded separators, repeated
slashes, traversal segments, and double-encoded paths fail closed. Real API
routes still enforce authentication and authorization in ShinyHub.

Regenerate `src/route-manifest.json` from the repository root after changing
routes, static assets, or `fleet.toml`:

```sh
GOWORK=off go run ./deploy/cloudflare-demo/generate-routes
GOWORK=off go run ./deploy/cloudflare-demo/generate-routes --check
```

The ordinary Go test suite checks for manifest drift. Versioned and unversioned
asset URLs are both admitted, so a Worker update can retain the current
container image. When retaining an older image, also audit its asset references
and routes against the manifest before deploying; removed assets or routes may
require a coordinated image rollout.

The demo configures no external authentication providers. Requests under
`/api/auth/github`, `/api/auth/google`, `/api/auth/oidc`, and
`/api/auth/forward-auth` receive a 404 at
the edge, including escaped provider names and callbacks. These requests never
touch the container, whether it is asleep, starting, or healthy, so they cannot
wake it or extend its idle deadline. Local viewer sessions still work normally.
This blocks unused routes, not all bots: a client that imitates a browser on an
entry path or submits the start form can still cause a wake.

While the container is asleep, exactly two requests may start it: a browser
navigating to `/` or `/login` on the demo host, and the start page's button. The
first is recognised by `Sec-Fetch-Dest: document`, which browsers generate and a
page cannot set. Ordinary HTTP crawlers do not send it automatically, although
an HTTP client can forge it and a headless browser can supply it. The second
allows a POST to `/__demo/start`, so visitors whose browser tells the edge
nothing can still start the demo deliberately. The one thing it will not
believe is a browser saying that post came from somewhere else: any page on the
web can submit a form here, and `Sec-Fetch-Site` is how a browser reports that
one did. Those get the start page, so a visitor whose click was borrowed still
lands on the demo and can start it deliberately. Only the post is judged on
where it came from; a shared link is cross-site by definition, and following one
is how most visitors arrive.

Everything else on an entry path is answered with the start page, a 200 rendered
at the edge that costs nothing and says what is true: the demo is asleep, and
here is the button that starts it. Nothing on an entry path is refused, so a
shared link previews as the demo rather than as an error, which is why the page
carries Open Graph tags. Only admitted dashboard and app navigations off the
entry paths can redirect to the entry page. API requests, static asset requests,
favicons, and health probes receive a 503 while asleep, even with browser
navigation headers, and never redirect into a wake. Unknown routes receive a
404 before the lifecycle check. App-origin navigations redirect to the control
host, carrying the original app link through the viewer session flow.

A deep link has to survive the wake, because the demo's apps are private and a
visitor arriving cold has no session yet. The path they asked for therefore
rides the whole cold path as a `demo_next` query parameter: the redirect to the
entry page, the start page's form, the wake page, and finally the one-click
entry POST, which is where the session that link needed finally exists and the
visitor is sent on to the page instead of the dashboard. Every one of those URLs
is built by `demoURL`. The `Location` that entry POST finally answers with is
the one that is not, because there it is the destination itself. Both end at
`safeDestination`, which resolves the value against the demo host and then reads
the string it is about to return rather than the one it was handed: a
destination can resolve onto this host and still come back as `//evil.example`,
because `.` and `..` segments are removed after the origin is settled, and that
value is a path here and another origin to every browser. So a `demo_next` a
stranger writes can only ever move someone around the demo.

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

The agent capabilities app uses a scripted assistant. Its public entrypoint
refuses `OPENAI_API_KEY`, `AGENT_DEMO_AGUI_URL`, and `AGENT_DEMO_AGUI_TOKEN`, and
forces scripted mode before loading the app. The shared chat handler also
honors that mode, so this demo cannot make model or external agent requests.
The private agent demo may continue using its own OpenAI key.

The demo deliberately uses ShinyHub's native runtime inside the outer container.
Cloudflare Containers do not expose a Docker daemon, and the seven bundled apps
are repository-reviewed examples rather than visitor-provided code. This is a
product demo, not a multi-tenant sandbox.

## Startup monitoring

Use `GET https://demo.shinyhub.dev/__demo/status` for passive monitoring. It
returns HTTP 200 with `{"state":"asleep"}`, `{"state":"starting"}`, or
`{"state":"ready"}` and disables caching. Sleeping is normal for this demo.
This endpoint reads the SDK's observed lifecycle state from the Durable Object;
it never starts the container, forwards a request, or refreshes its idle timer.
It also bypasses the Worker's warm-state memo. It is an observation, not an
application request or a guarantee that a process cannot fail just afterward.
The visitor wake page continues to use `/__demo/ready`, whose health request
may refresh activity while a visitor is waiting. Do not poll that endpoint for
background monitoring.

The existing Cloudflare observability configuration captures JSON startup
events from stdout/stderr and the Worker:

- `demo_boot_started`: the entrypoint began.
- `demo_boot_phase_completed`: initialization, control-plane ownership, viewer
  bootstrap, or fleet apply completed. `phase` names the completed phase and
  `elapsed_ms` measures from entrypoint start; subtract consecutive values to
  locate slow bootstrap steps. It contains no command output or credentials.
- `demo_fleet_ready`: every application passed the startup gate. `elapsed_ms`
  measures from entrypoint start, including bootstrap and fleet reconciliation;
  `fleet_wait_ms` measures only the final readiness gate. `application_count`
  records the verified fleet size. These timings exclude Cloudflare allocation
  before the entrypoint begins and do not measure a visitor's network latency.
- `demo_boot_failed`: the entrypoint failed before readiness, with `phase`,
  `reason`, `exit_code`, and `elapsed_ms`. A readiness failure also emits
  `demo_fleet_readiness_failed`, with `reason` and the pending application slugs.
  Count `demo_boot_failed` for boot failures; the fleet event supplies detail.
- `demo_wake_failed`: the SDK failed to start the container, including failures
  that happen before the entrypoint can run. It records the request attempt's
  elapsed time and a bounded reason without request URLs, cookies, or tokens.
- `demo_wake_requested`: the edge admitted a start attempt. `cold_start` says
  whether the observed container was asleep rather than already booting;
  `trigger` separates navigation from a start-form submission. `entry`,
  `fetch_site`, and `browser_navigation` are bounded categories, not proof of a
  human visitor. No IP address, URL, cookie, or user-agent value is logged.
- `demo_wake_completed`: the start routine resolved, with `elapsed_ms`. A random
  `wake_id` links the request to completion or `demo_wake_failed`. These are
  request attempts, not unique container boots: concurrent requests can observe
  the same sleeping container. Use `demo_boot_started` to count process boots.
  In Workers Logs, filter `event = "demo_wake_requested" AND cold_start = true`
  to inspect cold admission, then compare navigation and start-form counts
  before adding a challenge. Passive probes and warm visits emit no wake events.
- `demo_upstream_failed`: a proxied application or viewer-session request
  returned HTTP 5xx. It records only the operation and status, without request
  paths, query strings, cookies, or response bodies.

Request recovery is bounded: bodyless GET/HEAD proxy requests can retry once
on HTTP 500/502/503/504 or a recognized transient container exception, with a
250 ms delay and a five-second recovery deadline. A fresh Durable Object handle
is used on retry; caller cancellation is preserved. Successful streams outlive
the recovery deadline. Writes, session POSTs and WebSocket upgrades are never
replayed. An exhausted transport failure returns an uncached 503 with
`Retry-After: 2`; actual upstream failures still emit `demo_upstream_failed`.
`demo_request_recovered` records successful recovery without request data.
State lookups have a five-second total deadline and one retry for an evicted or
disconnected object, without waking the container; exhaustion logs
`demo_state_failed` and returns a retryable response.

Install two custom alerts, **in this order**, after deploying the Worker:

1. **ShinyHub demo — Container lifecycle errors**, using
   `worker-lifecycle-alert.sql`. Alarm failures remain visible here, except
   the reset every deployment causes (below).
2. Update **ShinyHub demo — Worker errors** with `worker-errors-alert.sql`.
   This covers request/RPC errors and unknown invocation types, excluding alarms
   already covered by the lifecycle alert. Never apply this split without the
   lifecycle alert enabled.

For both, keep threshold `>= 1`, evaluation/execution windows `10m`, automatic
time range, repeat interval `1h`, and the existing email destination. The value
counts error **records**, not failed requests or affected users. Cloudflare's
adaptive alert dataset rejects DISTINCT aggregates; do not add a distinct count
without validating support in the live query preview. Use `traceId`/`requestId`
in the log drilldown or the local diagnostic summary to group related records.
Clearing means a subsequent query window was clear, not that recovery took ten
minutes.

The Worker errors query excludes automatic `cf-worker-event` HTTP 503 summaries:
sleeping and readiness polling deliberately return 503. Real upstream 503s remain
covered by `demo_upstream_failed`; exception records with status 503 stay visible.
Unknown invocation types/identifiers retain coverage.

Every deployment resets the Durable Object under its running alarm, which
produces two alarm records a few minutes later: the exception `Durable Object
reset because its code was updated.` and the alarm's `cf-worker-event` summary.
The lifecycle query excludes that exception by its exact message, and excludes
alarm summaries altogether, since the dataset exposes nothing that tells the
summary of a reset from the summary of another failure. Any other alarm
exception or error log still counts. The cost: an alarm that fails with a
summary and no exception or log record of its own is not counted. Keep the
query as shallow as it is: the monitor editor answers a more deeply nested
condition with `recursion limit exceeded`, so validate any edit in its query
preview before saving.

Do not raise the threshold or suppress all deployment/WebSocket errors to make
the mail quiet; exclude one identified signature at a time, with a test case in
`test_worker_errors_alert.py` for it and for a neighbouring failure that must
still count.

For a deduplicated diagnostic summary, export the matching Worker log records as
JSON/JSONL and run `node scripts/demo-error-summary.mjs < logs.json` from the repo
root. The output reports records and correlated failure groups separately,
without printing URLs, cookies, exception messages, or correlation IDs. Records
without identifiers are counted individually; failure groups are not users.

To run deployment verification manually (with the deployment credentials in the
environment):

```bash
node scripts/demo-smoke-verified.mjs  # from the repository root
```

The local runtime regression test checks text/binary forwarding, client/backend
initiated closes, immediate and simultaneous closes, and both relay close codes.
It also fails on uncaught workerd diagnostics even when client assertions pass.
No production change is needed to run it:

```bash
npm run test:runtime  # from deploy/cloudflare-demo
```

Search Cloudflare Logs for these event names to inspect successful boot
durations and failed attempts. Events are emitted at startup transitions, not
on each readiness poll; monitoring adds no scheduled wake or background probe.

## Verify locally

```bash
npm ci
npm run check
docker build --platform linux/amd64 --provenance=false -f Dockerfile -t shinyhub-cloudflare-demo ../..
docker run --rm -p 8080:8080 shinyhub-cloudflare-demo
```

Cloudflare requires `linux/amd64`. The Go builder runs on the build machine's
native architecture and cross-compiles the static binary for the selected target;
the final Python/R/Caddy image uses that target architecture.

## Deploy

```bash
npm ci
npm run check
npx wrangler deploy
../../scripts/demo-smoke.sh
```

The smoke suite verifies the one-click viewer session as well as HTTP and
WebSocket traffic for the bundled applications.

## Isolated container preview

`wrangler.container-baseline.jsonc` and `wrangler.container-preview.jsonc` are
separate benchmark Workers with separate Durable Object namespaces. They reuse
the demo Dockerfile and `standard-1` size; the baseline uses the current `default`
scheduling policy, while the preview uses `durable_object` and chooses its image
and instance size at startup. Both supply the repository's `entrypoint.sh` as
their startup command so the bootstrap script is identical even when reusing
a previously prepared image. Neither configuration claims the public demo's
domains. The production deployment and release workflow continue using
`wrangler.jsonc`.

Validate the preview locally:

```bash
npm run check
npm test
WRANGLER_SEND_METRICS=false npx wrangler deploy --dry-run --containers-rollout none \
  --config wrangler.container-preview.jsonc
WRANGLER_SEND_METRICS=false npx wrangler deploy --dry-run --containers-rollout none \
  --config wrangler.container-baseline.jsonc
```

Dry runs bundle and validate the Worker configuration; they do not build images
or prove Cloudflare allocation/snapshot support. Deploying either configuration
and setting its `CONTAINER_BENCHMARK_TOKEN` secret require explicit approval.
There is no default token: all endpoints authenticate before obtaining a DO stub.
Only authenticated `POST /start` allocates an instance; `GET /state`, `GET /ready`,
and proxy requests cannot wake an asleep container. The controller restores the
ten-minute inactivity timeout after a DO restart, serializes mutations, and
deduplicates concurrent starts. Temporary allocation failures retry for at most
30 seconds and clear the failed instance before retrying. Allocation and health
readiness each have a 90-second limit. Startup monitoring reports asynchronous
container errors. Native cleanup can still remain pending after those limits;
they are not an end-to-end response deadline. Redeploying the isolated Worker
cleared a stalled allocation during live checks. A pending monitor can keep the
DO resident for up to 15 minutes and delay the native inactivity timer. A
persistent ten-minute alarm also stops the isolated container after its last
admitted start, snapshot, or completed proxy request. Pending upstream requests,
open WebSockets (including quiet ones), and streaming response bodies keep the
container alive. The idle window begins when the last connection completes,
cancels, or disconnects. WebSockets bridge text/binary messages and close/error
events in both directions; response cancellation propagates upstream.
Both isolated configs enable `enable_request_signal`: client disconnects release
activity even if response-stream cancellation or native cleanup remains pending.
HTTP bodies use Cloudflare's native stream pipe, as in its Containers SDK.
State/readiness probes do not extend the deadline. A DO reset drops these
non-hibernating connections and grants a fresh ten-minute window if they were
open; other constructor recovery preserves the saved deadline. Explicit stop
invalidates old connections so late disconnects cannot postpone a new instance's
shutdown. Native timeout restoration runs outside the mutation queue so a
pending allocation cannot block manual stop. The protected state endpoint
includes a non-secret phase and active connection count. This remains an isolated
prototype; validate allocation for each new image digest before adoption.
`standard-2` can be selected through the preview's
`CONTAINER_INSTANCE` variable for a separate sizing experiment.

After the two isolated Workers have been approved and deployed, put the benchmark
token in `CONTAINER_BENCHMARK_TOKEN` in the local environment and run:

```bash
npm run benchmark:containers -- --url https://shinyhub-container-baseline.SUBDOMAIN.workers.dev --mode fresh --runs 5
npm run benchmark:containers -- --url https://shinyhub-container-preview.SUBDOMAIN.workers.dev --mode fresh --runs 5
npm run benchmark:containers -- --url https://shinyhub-container-preview.SUBDOMAIN.workers.dev --mode snapshot --runs 5
```

The runner measures ShinyHub `/healthz` readiness and total request time, reports
the median and individual samples as JSON, rejects a fallback boot as a snapshot
measurement, and attempts to stop the instance at the end even after a failure.
Verify the protected `/state` reports `running: false`; platform failures can
prevent cleanup from completing. It uses a generic user-agent. Failed batches
retain completed samples and report the error instead of a successful median.
These are repeated stopped-instance starts, not guarantees
of new VM placement on every sample. Compare the policies under the same image,
size, location, and workload. Save measurements locally before considering a
production migration.

For prebuilt images, use the same concrete `linux/amd64` manifest digest in both
configurations. `--provenance=false` omits the extra provenance manifest. Image
preparation is a separate Cloudflare operation and can remain pending even for
a concrete manifest. Reusing an already prepared image with the same bundled
entrypoint in both Workers isolates bootstrap changes from image preparation.
Set the baseline's `image` and preview's `images.demo.image` to the managed
registry reference, removing the Dockerfile/build-context fields. Do not use
`--containers-rollout none` for a live preview deployment: Wrangler must include
its named-image bindings. That option remains useful for local dry runs.

An image preparation result of `ready` is not a successful allocation check.
Before promoting a digest, test repeated stopped-instance starts and at least
one new Durable Object identity, then verify all seven HTTP applications and
three WebSocket upgrades on both a fresh boot and a snapshot restore. Compare
the exact pinned image, instance size, and startup command; do not infer an
image defect from a single pending allocation. Record preparation status,
startup phases, and platform instance state before changing configuration.
If allocation or native cleanup remains pending, recover the isolated Worker,
verify it is stopped, and retain the previous production image. A later
successful test does not establish the cause of an earlier platform stall.

Authenticated `POST /snapshot` captures a ready preview's filesystem;
`DELETE /snapshot` forgets the saved handle, and `POST /stop` destroys the running
instance. Forgetting a handle does not delete Cloudflare's retained snapshot.
Snapshot handles are associated with the image digest, expire conservatively
after 29 idle days, and refresh their retention timestamp after successful
restoration. An incompatible checkpoint is discarded. A failed restore gets
one bounded fallback to the current image. Image changes never silently replace
a running instance: stop/start it before capturing another checkpoint.

The preview retains randomly generated demo authentication and bootstrap
credentials in private Durable Object storage and supplies them to both image
and snapshot starts. The database contains encrypted records, so restoration must
reuse its authentication key. Retaining the bootstrap token also keeps the demo's
credential configuration stable through restoration. Credentials
are never returned by the benchmark endpoints. The baseline keeps the existing
image-generated credentials because it always starts a fresh filesystem.
The image entrypoint waits for control-plane ownership (`/activez`) before
bootstrap mutations, including after a restored ownership lease expires.

Snapshots capture files, not memory or running processes. The existing entrypoint
runs again after restoration, including fleet reconciliation. This experiment
does not promise faster application startup or durable application state. Before
production adoption, verify SQLite/WAL recovery, stale process/session handling,
repeatable fleet reconciliation, idle shutdown, and HTTP/WebSocket smoke tests
on Cloudflare. Snapshot creation is manual; no public visitor can trigger it.

The protected `/control/<path>` and `/apps/<path>` proxy routes forward into the
isolated container with its canonical demo hosts and preserve WebSocket upgrades.
They are for HTTP/WebSocket checks, not a browser preview: generated links and
redirects still use the production hostnames. Disable redirect following when
testing them, and supply the benchmark bearer token with each request. A browser
staging rollout needs separate control/app domains and matching server URLs.

For billing verification, query `containersUsageAdaptiveGroups`, not only
workload metrics. Its allocated memory/disk and CPU time include the micro VM
and correspond to dashboard usage estimates. Keep each policy's application ID
and time window separate, and normalize UUID formatting when comparing the
application API's compact identifiers with the metrics API's hyphenated ones.
Gross resource estimates do not include plan allowances, Workers/DO compute,
network egress, logs, or retained snapshot storage. Reading `/state` after the
idle window verifies shutdown without allocating another instance.

Rapid snapshot replays can capture an unexpired database ownership lease and
wait for handoff, while a wake after ten idle minutes uses an expired lease.
Measure both paths, including the time until every bundled app responds. Native
`/healthz` readiness alone is not the full application experience.

Cloudflare references: [scheduling policies](https://developers.cloudflare.com/containers/configuration/scheduling-policy/),
[direct API](https://developers.cloudflare.com/containers/api/durable-object-container/),
and [filesystem snapshots](https://developers.cloudflare.com/containers/guides/snapshots/),
[usage metrics](https://developers.cloudflare.com/analytics/graphql-api/tutorials/querying-container-metrics/),
and [pricing](https://developers.cloudflare.com/containers/platform/pricing/).

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


## Browser staging canary

The canary uses the production edge admission, wake page, and one-click viewer
login with its own Worker and Durable Object namespace:

- Control: https://staging.demo.shinyhub.dev
- Applications: https://apps.staging.demo.shinyhub.dev
- Worker: `shinyhub-demo-canary`; config: `wrangler.canary.jsonc`.

Every image/snapshot start receives the staging control and app origins.
Encryption/bootstrap keys stay stable in private DO storage. Snapshot, stop,
and fault-injection routes require the separate `CONTAINER_CANARY_TOKEN` bearer
secret before obtaining a container handle.

Resolve the release candidate to a concrete managed `linux/amd64` digest.
In a private config copy, replace `containers[0].images.demo` with an `image`
reference pinned by digest, removing Dockerfile/build-context fields. Check that
both custom domains are unused or already belong to this canary. Deploy the
private pinned config and set the operator secret through Wrangler standard
input. Store the token outside the repo with mode 0600; never put it in URLs.

Validate locally before the approved canary deployment:

```bash
npm run check
npm test
python3 -m unittest test_startup_readiness.py
go test .
npx wrangler deploy --dry-run --containers-rollout none --config wrangler.canary.jsonc
npx wrangler deploy --config /private/path/to/pinned-canary-config.json
npx wrangler secret put CONTAINER_CANARY_TOKEN --config /private/path/to/pinned-canary-config.json
```

Operator routes on the control origin:

| Method | Path | Result |
| --- | --- | --- |
| GET | `/__canary/state` | Running/readiness, phase, and active connection count |
| POST | `/__canary/start` | Explicit image or snapshot start |
| POST | `/__canary/stop` | Stop the instance |
| POST | `/__canary/snapshot` | Save a checkpoint; return only its size |
| DELETE | `/__canary/snapshot` | Forget the saved handle |
| POST | `/__canary/terminate` | Fixed SIGKILL fault injection in the canary |

Verify fresh browser wake, deep-link redirects, viewer login, control/app cookie
isolation, all seven app pages, and three WebSocket upgrades. The scripted agent
app's `POST /app/agent-capabilities-demo/chat` exercises real SSE delivery without
a paid model. Hold quiet WebSockets beyond ten minutes, close them, and verify
shutdown after the last connection's ten-minute idle window. State/readiness
probes must not allocate or extend idle. Check rapid and after-idle snapshot
restores, all apps, and a previously issued viewer cookie. Use authenticated
`terminate` to verify recovery through a later browser navigation; readiness
polling alone must never allocate a replacement.

Measure idle time from the last admitted request as well as the last open
connection: warm page and asset requests extend activity even when the active
connection count returns to zero. Keep other staging browsers closed and inspect
Worker logs for interfering requests. An isolated lifecycle check may briefly
gate public staging traffic before any DO access; restore the normal browser
entry after verification. Native browser readiness uses passive HTTP health
state and never goes through the activity-tracking proxy.

The bootstrap keeps Caddy's external port closed until every application in the
bundled fleet returns HTTP 200 through ShinyHub's app-origin proxy. It checks the
whole fleet in parallel, rejects redirects, errors, and HTTP 200 wait pages,
and fails startup after 60 seconds if an application remains unavailable. This
runs on fresh boots and
snapshot restores before `/healthz` can release the browser wake page. Verify
all app pages immediately after browser readiness, without retrying startup
errors, as well as WebSockets and SSE; an HTTP gate alone does not test those
connections.

### Production promotion and rollback

Keep the production configuration and release workflow on the existing Worker
until the canary passes. Before promotion, save its current Worker version,
application configuration/image digest, and domain mapping privately. Compare
the tested image's ShinyHub version with the production release: an older
experimental image is not a release candidate merely because lifecycle checks
pass.

Prepare a new native DO class/namespace with production origins. Do not change
scheduling policy underneath the existing default-policy class. Build the
approved release image, repeat the new-digest allocation gate, and pin the
validated digest. Production needs its own stable DO credentials. Snapshot only
after fleet reconciliation; checkpoints are image-specific performance aids,
not user-data backups.

After explicit production approval, promote the two browser domains while
retaining the original default-policy Worker/class/namespace and pinned image.
If startup, login, any app, connections, or idle shutdown regress, restore both
domains to the saved Worker/version and run the original viewer/app smoke suite,
then stop the new native instance. Native allocation failures can require a
Worker redeploy before cleanup finishes. Roll back routing and its compatible
image together; never restore a checkpoint under a different image/key.
Changing only `scheduling_policy` is not a complete rollback.

The container relay registers its completion with `waitUntil()` to retain
close-handshake work after a client disconnects. The five-second acknowledgement
deadline bounds cleanup. The workerd regression test crosses a real Durable
Object boundary to a TCP WebSocket backend and includes an immediate client
close, text and binary frames, and both close directions.
