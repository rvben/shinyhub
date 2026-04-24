# Output caching across sessions

`@reactive.calc` caches *within* a session. The moment a second user
connects, the calc reruns for that user. For apps where many users
trigger the same expensive computation (dashboards, demos, public
tools), this is pure duplicate work.

Wrapping a pure helper in `functools.cache` or `functools.lru_cache`
at module scope gives you a **per-process, cross-session** cache. One
user's miss pays the cost once; every other user on the same replica
gets the result in under a millisecond.

On a CPU-bound synthetic app (one replica, small input domain, 45 ms
compute per call), adding `@cache` to a module-scope helper produced
a 37–64× drop in median response time and +21% throughput at the
saturation point where the uncached version starts dropping requests:

| app      | concurrency | interactions / 30 s | p50 ms | p95 ms |
|----------|------------:|--------------------:|-------:|-------:|
| uncached | 10 | 1 185 | 51.2 |  234.5 |
| cached   | 10 | 1 193 |  0.8 |  158.8 |
| uncached | 30 | 2 755 | 101.0 | 274.3 |
| cached   | 30 | 3 329 |   2.7 | 174.6 |

Your mileage will vary with compute cost, input-domain size, and
cache hit rate — but the shape of the win is reliable: the median
collapses to a dict lookup, and throughput stops falling off at
saturation.

## Recipe

```python
from functools import lru_cache

from shiny import App, render, ui

@lru_cache(maxsize=128)
def expensive_plot(xvar: str, yvar: str, filter_tuple: tuple[str, ...]):
    # Any hashable-keyed function is a candidate: SQL results,
    # matplotlib figures, sklearn predictions, pandas aggregations.
    df = load_frame()
    df = df[df["category"].isin(filter_tuple)]
    return make_fig(df, xvar, yvar)


app_ui = ui.page_fluid(
    ui.input_select("xvar", "X", {"a": "A", "b": "B"}),
    ui.input_select("yvar", "Y", {"x": "X", "y": "Y"}),
    ui.input_checkbox_group("filters", "Categories", ["north", "south"]),
    ui.output_plot("chart"),
)

def server(input, output, session):
    @render.plot
    def chart():
        return expensive_plot(
            input.xvar(),
            input.yvar(),
            tuple(sorted(input.filters())),   # tuple so it's hashable
        )

app = App(app_ui, server)
```

Three properties make this work:

1. **`expensive_plot` is defined at module scope**, not inside
   `server()`. That's what makes the cache survive across sessions —
   one dict per process, not one per session.
2. **All arguments are hashable.** Lists and dicts aren't; convert to
   `tuple(sorted(...))` or `frozenset(...)` before calling.
3. **The function is a pure function of its arguments.** No
   `datetime.now`, no global-state reads, no reads from a DB that
   changes over time. If the result should refresh when data changes,
   output caching is the wrong tool — use `@reactive.poll` or
   `@reactive.file_reader` instead.

## When to bound the cache

`@cache` is unbounded. Prefer `@lru_cache(maxsize=N)` unless the key
space is genuinely small and each entry is small:

| Key space | Entry size | Recommendation |
|-----------|-----------|----------------|
| < 100 | < 1 MB | `@cache` is fine |
| small but entries are big (DataFrames, figures) | any | `@lru_cache(maxsize=32)` — bound for memory |
| large (date ranges, free-text) | any | `@lru_cache(maxsize=256)` + measure |

Size the bound by **N × peak entry size** — that's the steady-state
memory added per replica. ShinyHub's per-app memory limit
(`memory_limit_mb`) still applies, so an unbounded cache that OOMs
will trigger an OOM-kill and restart.

## When *not* to use it

- **User-specific results.** Caching `build_report(user_id, …)` shares
  across sessions but by-user keying works; caching
  `build_report(filters)` when filters silently include user data
  leaks between users. If the key captures the user explicitly, you're
  fine.
- **Time-sensitive data.** Prices, live metrics, "rows added in the
  last minute" — either don't cache or use `@reactive.poll` with a
  TTL-keyed helper (`def fetch(bucket=int(time.time() // 60))`).
- **Cheap computations.** If the work is under ~5 ms, the caching
  overhead and cognitive cost aren't worth it.

## Interaction with replicas

The cache is **per-process**, so each replica has its own dict.
Increasing `replicas` doesn't dilute the hit rate once warm (random
input selection still covers the small key space quickly) — but it
does multiply total memory overhead by the replica count. Don't
forget that when sizing `memory_limit_mb`.

Sticky cookies don't help here: the cache is content-keyed, not
session-keyed. Two different users on two different replicas that
happen to request the same plot will each cause one miss on their
own replica and then hit for the rest of the session.

## Interaction with schedules

If a [scheduled job](../schedules.md) produces a file that the app
reads, output-caching the file read is safe — but key it by the
file's mtime so fresh writes invalidate the cache:

```python
from functools import lru_cache
from pathlib import Path

@lru_cache(maxsize=1)
def _load(mtime_ns: int):
    return pd.read_parquet("data/shared/fetch/latest.parquet")

def load_latest():
    p = Path("data/shared/fetch/latest.parquet")
    return _load(p.stat().st_mtime_ns)
```

`_load` is keyed by the mtime, so replacing the parquet evicts the
old entry automatically on the next call.

## Verifying the cache is working

Print the hit ratio on demand (only valid for `lru_cache`, not
`cache`):

```python
@lru_cache(maxsize=128)
def expensive_plot(...): ...

# Log or expose via a debug route.
info = expensive_plot.cache_info()
# CacheInfo(hits=420, misses=36, maxsize=128, currsize=36)
```

A hit ratio above ~90% under steady load confirms the cache is doing
its job. A ratio near 0 means your key space is too large or the
function isn't actually being called through the cached entry point —
double-check the module-scope definition.
