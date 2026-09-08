# Real Shiny browser lifecycle check

This small Python Shiny app complements the synthetic mixed-load and lifecycle
soak fixtures. Its visible output identifies the deployed version, the session,
and the number of button clicks. A calculation verifies that the WebSocket is
still delivering reactive updates; a page that merely remains visible is not a
passing session.

Deploy `app/` to an isolated ShinyHub test instance with a Python runtime and its
normal dependency builder. `requirements.txt` pins the Shiny version. Use a
private test app and a disposable account. Keep instance configuration,
credentials, addresses, screenshots, logs, and measurements under ignored
`loadtest/results/`; none belong in this fixture.

## Acceptance sequence

1. Start signed out and open the app's proxy URL, including a query string if
   the app uses one. Follow its sign-in link. Signing in must return directly
   to the requested app, without a detour through the overview.
2. Confirm `Version: v1`, record the session label, set Samples to 500, and click
   Recalculate twice. Expect Calculation 2, Sum 125250, and Mean 250.5.
3. In a **copy** of the fixture, change `version.txt` to `v2` and deploy it to
   the same app using normal rolling deployment. Keep the original tab open.
   Its next click must show v1, the same session label, and Calculation 3.
4. Open the app in another tab in the same browser profile. Expect v2 and a
   different session label. A calculation must update. Close the v1 tab so
   its draining replica can exit.
5. Explicitly restart the app. With `server.status_overlay` enabled (the server
   default), the disconnected tab must offer recovery. A Shiny reload marker
   receives a three-second grace period. New-tab recovery is available while
   readiness is pending: a new session may be needed to establish readiness.
   Start a new session using the offered
   action; expect v2, a new label, and working calculations. If opening a new
   tab, verify the old tab preserves its results and identifies them as a
   snapshot. A restart does not preserve in-memory session state.
6. Close the app tabs, explicitly sleep the app, and verify it is hibernated.
   Open its URL again. Expect v2, a new session label, and a working calculation.
   Record cold-wake timing separately from warm navigation, including whether
   it measures HTTP response, browser navigation, or actual interactivity.
7. Stop the isolated instance and remove its temporary app data and credentials.

Record the application revision, fixture/runtime versions, browser, configuration,
and results for each step. Browser automation latency is not a precise app
startup measurement. This is a bounded acceptance check, not a claim about all
Shiny versions, R Shiny behavior, arbitrary app workloads, or long-term capacity.

## Automated release check

Run `make test-browser-lifecycle-e2e` with Go, Node 20+, uv, and system Python
3.12 available. The target installs the render driver's lockfile-pinned
Playwright and its Chromium, builds the current application, and runs the
sequence against a fresh loopback server and an extension-free browser context.
On Linux, install browser system dependencies first with
`cd loadtest/render/driver && npm ci && npx --no-install playwright install-deps chromium`.

The test asserts the exact sign-in return URL, reactive calculations, old/new
session identities across rolling deployment, new-tab recovery while readiness is pending, preservation and labeling of the previous results, and a
reactive session after sleep/wake. Missing recovery controls fail the check;
there is no manual-reload fallback that can hide a regression. It runs in CI and
against the exact release tree before artifact publication.

The run has a ten-minute deadline and bounded waits for each browser/API step.
It removes its server state, disposable credentials, and browser context even
on failure. Logs, recovery/snapshot screenshots, and a `result.json` stay in an ignored
`loadtest/results/browser-lifecycle-*` directory. Timings describe complete test
steps, including automation and assertions; they are not startup benchmarks.
To verify an already-built binary, set `SHINYHUB_E2E_BINARY` explicitly. Otherwise
the check always builds the current source. The report records the binary hash
and browser version so results can be tied to the tested executable.
