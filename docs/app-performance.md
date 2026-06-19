# App startup performance

A common report is: "the app opens, but the charts and tables take a couple of
seconds to appear." This page explains where that delay comes from, shows that
it is the app's own initial render (not the ShinyHub proxy), and gives the
patterns that remove it.

The short version: **move work that does not depend on the user out of the
server function so it runs once at process startup instead of on every visit.**

---

## Where the delay comes from

When a browser opens an already-running app, two things happen in sequence:

1. **The HTML shell loads.** ShinyHub proxies the app's root page. This is fast:
   the proxy hop is a single in-memory replica pick plus a reverse-proxy
   forward, on the order of a millisecond for a warm app, and is not where the
   delay is.
2. **Shiny renders the data.** The browser opens a WebSocket, Shiny starts a new
   **session**, runs the `server` function, builds the reactive graph, and
   computes every output for the first time. The results stream back over the
   WebSocket and paint.

The "couple of seconds" lives entirely in step 2. ShinyHub keeps the app process
warm between sessions, but a fresh WebSocket connection still re-runs the
`server` function every time. If that function loads a dataset or runs a heavy
aggregation, every visitor pays for it on every visit.

It also multiplies under load: ten people opening the app at once run that heavy
build ten times concurrently, spiking CPU and memory and pushing you toward more
replicas than the app actually needs. Fixing it is both a latency win and a
capacity win.

---

## The fix

### Python Shiny

Anything that does not depend on `input` or the user belongs at **module scope**,
where it runs once when the process boots and is shared by all sessions:

```python
# Bad: runs on every WebSocket connect (every visitor, every visit)
def server(input, output, session):
    df = load_and_aggregate()            # ~2s paid per session
    @render.plot
    def chart():
        return make_plot(df)

# Good: runs once at startup, shared across all sessions
DF = load_and_aggregate()                # ~2s paid once per process
def server(input, output, session):
    @render.plot
    def chart():
        return make_plot(DF)             # already in memory
```

For work that **does** depend on `input`, cache it instead of recomputing on
every reactive invalidation:

- `@reactive.calc` memoizes a computation within a session and only recomputes
  when its dependencies change.
- A module-level `dict` or `functools.lru_cache` caches across sessions (use for
  expensive, input-keyed results that are safe to share).

Tip: prefer `@render.data_frame` over `@render.table`. The latter renders through
pandas' Styler, which pulls in `jinja2` and is heavier; `@render.data_frame` is
lighter and faster.

### R Shiny

The same rule applies. Code at the top of `app.R` (before `ui`/`server`) or in
`global.R` runs **once** when the R process starts and is visible to every
session. Code inside `server <- function(input, output, session) { ... }` runs
**per session**.

```r
# global.R (or top of app.R): runs once at startup, shared by all sessions
df <- load_and_aggregate()

server <- function(input, output, session) {
  output$chart <- renderPlot({
    make_plot(df)            # reuses the startup-scope data
  })
}
```

For input-dependent work, use `bindCache()` (cache a render/reactive keyed by its
inputs) and `bindEvent()` to control when it invalidates.

### Render the layout first

If some heavy output is unavoidable, render the page skeleton immediately and let
that output fill in afterward (a placeholder, a deferred reactive, or an
`actionButton`-gated compute), so the user sees structure instantly instead of a
blank panel.

---

## A note on the warm process lifetime

Startup-scope work is paid once **per process lifetime**, not once forever. An
app that has been idle past its hibernation timeout (default 30 minutes) is
stopped, and the next access cold-boots it, re-running module-scope code. Two
knobs keep a frequently-used app's startup data resident:

- **Raise or disable the hibernation timeout** (trades memory for instant access):

  ```bash
  shinyhub apps set <slug> --hibernate-timeout 0    # 0 disables; -1 resets to the global default
  ```

  Or pin it in the bundle's `shinyhub.toml` so it travels with the app:

  ```toml
  [app]
  hibernate_timeout_minutes = 0
  ```

- **Keep a pre-warm floor of replicas** so at least one process stays up, even
  when idle (see [Scaling apps](scaling.md)):

  ```bash
  shinyhub apps set <slug> --min-warm-replicas 1
  ```

These reduce how often the startup cost is paid; they do not change the
per-session win above, which applies whenever the process is warm.

---

## Measuring your own app

You do not have to guess which output is slow.

- **Log timestamps from the app.** Print to stderr around expensive work; the
  output appears in the app's Logs tab in the dashboard:

  ```python
  import sys, time
  t0 = time.time()
  df = load_and_aggregate()
  print(f"build took {time.time() - t0:.3f}s", file=sys.stderr, flush=True)
  ```

  If you see this line on **every** page open, the work is in the wrong scope.
  If you see it once at startup and never again, it is where it belongs.

- **Trace it.** Enable `tracing.auto_instrument_apps` (see [Tracing](tracing.md))
  to get per-request spans propagated into the app, so you can attribute the time
  precisely rather than guessing.

---

## What ShinyHub does and does not do here

ShinyHub keeps your app process warm between sessions and proxies requests in
~1 ms, so the platform is not the bottleneck for an already-running app. It
cannot make your app's server-side compute faster, because that is your code.
The patterns above are where the seconds are won.
