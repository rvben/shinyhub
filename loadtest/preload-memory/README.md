# Pre-fork memory decision harness

This Linux-only benchmark compares today's independent `exec` replicas with a
single imported-and-frozen parent that forks identical Shiny replicas. It drives
real WebSocket browser sessions against every port, waits for allocator decay,
and records RSS, PSS, private memory, and swap PSS from `smaps_rollup` before and
after load.

Run it with the target app's virtual-environment Python so importing `app.py`
uses the exact production dependency set:

```bash
loadtest/render/app/.venv/bin/python \
  loadtest/preload-memory/benchmark.py benchmark \
  --app-file loadtest/render/app/app.py \
  --replicas 3 --sessions-per-replica 2 --duration 30 \
  --output loadtest/results/preload-memory.json
```

Do not run a parent from a long-lived server that already created threads,
event loops, sockets, or clients. The harness forks in a fresh supervisor and
refuses the fork if importing the application starts a thread.

The default decision gate requires at least 20% lower post-render PSS and no
more than 10% p99 latency regression. Passing is evidence for a lifecycle RFC,
not authorization to add a `fork` switch: the RFC must still specify inherited
descriptor cleanup, database/client reconnection, signals, log ownership,
health checks, rolling deploys, crash isolation, and unsupported app behavior.
