---
description: "Give an app's replicas, workers, and scheduled jobs one shared disk cache for computed results, so an expensive output is computed once instead of once per process."
---

# Result cache

Every app gets a disk directory for caching computed results. All of the app's
processes share it: its replicas, its elastic workers, and its scheduled jobs.
A result one process computes is a cache hit for every other process, and it is
still there after a restart or a wake from hibernation.

This complements the per-process caches described in
[Application performance](app-performance.md) and
[Output caching](recipes/output-caching.md). An in-memory cache is the fastest
option, but each process fills its own copy, and the copy is lost when the
process stops. The result cache is filled once for the whole app.

---

## How the app sees it

ShinyHub sets two environment variables on every process it starts for the app:

| Variable | Meaning |
|----------|---------|
| `SHINYHUB_CACHE_DIR` | Absolute path of the app's cache directory. Under `runtime.mode: docker` this is `/app-cache`, a bind mount of the directory on the host. |
| `SHINYHUB_CACHE_MAX_MB` | The size the app's cache library should stay under, in MiB. The default is `1024`. |

ShinyHub does not enforce the size. The cache library does, from the value it
is given. The cache does not count against `storage.app_quota_mb` and is not
listed by `shinyhub data ls`.

When the cache is off, or not available for the process (see
[Where there is no cache](#where-there-is-no-cache)), neither variable is set.
An app that uses the cache should treat a missing `SHINYHUB_CACHE_DIR` as "cache
in memory", not as an error.

---

## R Shiny: automatic for `bindCache`

R apps need no code change. Before `runApp`, ShinyHub makes the cache directory
Shiny's default cache, using `cachem::cache_disk` bounded by
`SHINYHUB_CACHE_MAX_MB`. Every `bindCache()` in the app then reads and writes
the shared disk cache instead of the process's memory:

```r
server <- function(input, output, session) {
  output$chart <- renderPlot({
    make_plot(summarise_region(input$region))
  }) |> bindCache(input$region)
}
```

With four replicas, the first visitor to pick a region pays for the plot. The
other replicas, and the same replica after a restart, serve it from disk.

The app keeps control:

- **An app's own cache wins.** A `shinyOptions(cache = ...)` in `.Rprofile`,
  `app.R`, or `global.R` replaces ShinyHub's. Use this for a
  `cachem::cache_mem()` you want to keep, or for a cache at a path you manage
  yourself.
- **A cache problem never stops the app.** If the directory is missing or not
  writable, or the cache cannot be opened, the app starts on Shiny's in-memory
  cache and logs a line starting `shinyhub: result cache unavailable` to its
  Logs tab.

Caches you create yourself (`memoise`, `cachem::cache_disk` on a
`reactive()`) can use the same directory through
`Sys.getenv("SHINYHUB_CACHE_DIR")`.

---

## Python: `diskcache`

Python has no framework-level default to hook, so the app opens the cache
itself. [`diskcache`](https://grantjenks.com/docs/diskcache/) is safe to share
between processes. Add it to `requirements.txt` and open it once at module
scope:

```python
import functools
import os

import diskcache

_cache_dir = os.environ.get("SHINYHUB_CACHE_DIR")
if _cache_dir:
    _max_mb = int(os.environ.get("SHINYHUB_CACHE_MAX_MB", "1024"))
    memoize = diskcache.Cache(_cache_dir, size_limit=_max_mb * 1024 * 1024).memoize()
else:
    # No shared cache (turned off, a remote worker, a local run): cache in
    # this process's memory instead.
    memoize = functools.lru_cache(maxsize=128)


@memoize
def summarise_region(region: str, data_mtime: float):
    ...
```

This works the same in Shiny for Python, Dash, and Streamlit. Arguments and
return values must be picklable to go through `diskcache`.

---

## Keep cached results fresh

**The cache key must include every input the result depends on, including the
data files the computation reads.** ShinyHub never invalidates the cache when
data changes. Pushing a file with `shinyhub data push`, or a scheduled job
rewriting a dataset, leaves results computed from the old file in place, and a
key that only names the user's inputs keeps serving them.

Include a cheap fingerprint of the data in the key. A file's modification time
is usually enough, read through a poll so that an open session notices when
the file changes. A plain `file.mtime(path)` in the key is read only when
something else invalidates the output, so a session would keep showing the old
result:

```r
path <- file.path(Sys.getenv("SHINYHUB_APP_DATA"), "sales.parquet")

server <- function(input, output, session) {
  # Checks the file every 10 s and changes only when the file does.
  data_version <- reactivePoll(10000, session,
    checkFunc = function() file.mtime(path),
    valueFunc = function() file.mtime(path))

  output$chart <- renderPlot({
    make_plot(summarise_region(input$region))
  }) |> bindCache(input$region, data_version())
}
```

```python
path = os.path.join(os.environ["SHINYHUB_APP_DATA"], "sales.parquet")


@reactive.poll(lambda: os.path.getmtime(path), interval_secs=10)
def data_version():
    return os.path.getmtime(path)


@render.plot
def chart():
    return make_plot(summarise_region(input.region(), data_version()))
```

A new data file then produces new keys, and the stale entries age out under the
size bound. The key only decides which entry is read; the computation behind a
miss must read the new data too. A dataset the app loaded once at startup is
still the old one in memory, so push with `shinyhub data push --restart`, or
load the data inside the cached computation. When the key cannot express the
dependency (an external database, say), clear the cache by hand after the data
changes (see below).

**Do not cache per-user results under a shared key.** The cache is shared by
every visitor. A result that depends on who is asking, such as rows filtered by
the viewer's entitlements or a per-user greeting, must carry the user in its
key, or one visitor sees another's result. When in doubt, cache only the shared
part of the computation and apply the per-user step afterward.

---

## Lifecycle

The cache belongs to one deployment of the app:

- **Restarts, crashes, hibernation, and scaling keep it.** Every process of the
  deployment uses the same directory.
- **A deploy or a rollback starts empty.** New code never reads results the
  previous code computed, in whatever format that code wrote them. Once the
  replaced deployment has stopped, its cache directory is removed.
- **Scheduled jobs share it.** A job of the app gets the same
  `SHINYHUB_CACHE_DIR` as the deployment it runs, so a job that writes results
  with the same cache library and keys the app reads can warm the cache ahead
  of visitors. An R job is a plain script, not `runApp`, so it opens the cache
  itself (`cachem::cache_disk(Sys.getenv("SHINYHUB_CACHE_DIR"))`).
- **Deleting the app removes it.**

At startup, ShinyHub also removes cache directories no running deployment is
using, for example ones left behind by a server that stopped in the middle of a
deploy.

---

## Clearing the cache

```bash
shinyhub apps stop sales
shinyhub cache clear sales
shinyhub apps start sales
```

`cache clear` (`DELETE /api/apps/{slug}/cache`) deletes everything in the app's
cache. It needs the right to manage the app (its owner, a `manager` member,
or a platform `admin` or `operator`), and it is recorded in the audit log as `cache.clear`.

The app must be stopped, with no scheduled run in progress. A running process
holds its cache open, and deleting the directory underneath it breaks caching
until that process restarts, so the server answers `409 Conflict` instead. Stop
the app, clear, and start it again.

---

## Where there is no cache

The cache is a directory on the ShinyHub host, so only processes on that host
can use it. Apps on [remote workers](recipes/remote-worker.md) and on
[AWS Fargate](deployment/aws-ecs.md) do not get `SHINYHUB_CACHE_DIR` and cache
in memory. For R apps that means Shiny's default per-process cache, with no
code change needed.

---

## Configuration

| Key | Environment variable | Default | Meaning |
|-----|----------------------|---------|---------|
| `storage.app_cache_dir` | `SHINYHUB_APP_CACHE_DIR` | `app-cache` beside `app_data_dir` | Root of every app's cache. Each deployment uses `<app_cache_dir>/<slug>/d<deployment id>`. |
| `storage.app_cache_max_mb` | `SHINYHUB_APP_CACHE_MAX_MB` | `1024` | Size bound passed to each app as `SHINYHUB_CACHE_MAX_MB`. `0` turns the result cache off: no directory is created and no variable is set. |

With the default `app_data_dir` of `./data/app-data`, the cache lives in
`./data/app-cache`. The cache is disposable, so keep it out of backups and on
fast local disk. Nothing breaks if it is lost; results are computed again.

The ShinyHub server must be able to create the directory. When it cannot, apps
start without a cache and the server logs `result cache unavailable` at each
start. The [Docker Compose](deployment/docker-compose.md) stacks create it
under `SHINYHUB_DATA_ROOT` for you.
