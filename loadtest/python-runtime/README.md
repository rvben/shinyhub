# Python runtime comparison

Compare standard CPython runtimes with real Shiny WebSocket sessions through
ShinyHub's native launcher and production proxy. This exercises fixed Python
work on each calculation rather than a busy loop that runs for a fixed duration.

```bash
make benchmark-python-runtime
```

Requires current uv, Go, Node and Chrome. Install the render driver's pinned
Playwright package first if absent:

```bash
cd loadtest/render/driver
npm ci
```

The default compares the stable 3.14 baseline and the 3.15 preview, with three
rounds, three simultaneous sessions per replica, and ten recalculations per
session. For exact builds:

```bash
node loadtest/python-runtime/benchmark.mjs \
  --binary tmp/python-compat/shinyhub \
  --python /path/to/python3.14 \
  --python /path/to/python3.15 \
  --rounds 5 --sessions 3 --samples 30
```

The harness resolves one universal dependency lock, prepares each interpreter's
environment before timing, and alternates runtime order between rounds. It
checks the deployed interpreter version and verifies identical calculation
results. Reports record the Python, Shiny and Pydantic versions, dependency-lock
and server-binary hashes, browser version, GIL/JIT state, individual samples and
summary statistics:

- Process startup to HTTP readiness, excluding dependency preparation.
- Browser navigation to first reactive output.
- Click-to-reactive-output latency with simultaneous sessions.
- Server time for the same fixed Python calculation.
- Interpreter RSS; Linux additionally reports PSS, private memory and swap PSS.

Artifacts and private logs live under ignored `tmp/python-runtime/`, in a new
directory per run. A failed run writes `status: failed` and returns nonzero;
missing measurements are never reported as zero or a pass. Source builds are
disabled by default. `--allow-source-builds` permits development comparisons
when a dependency lacks a wheel; such a run is not evidence for a production
default upgrade. Cached source-built wheels can mask availability, so verify
wheel-only provisioning with a clean uv cache as well.

Use the same hardware, avoid concurrent test/build workloads, and run enough
samples for the latency distribution to settle. Three rounds are a smoke test;
tail estimates from a small sample are noisy. The fixture is deliberately small
and does not substitute for representative apps, deployment providers, or a
Linux fleet memory test. JIT, lazy imports and free-threading are not enabled
automatically. Test those variants separately and compare the entire session,
including first-use work and import-time registration.

Run the compatibility checks independently of performance:

```bash
make test-py-identity test-py-bookmarks test-py-agent test-py-runtime PYTHON_VERSION=3.15
make test-browser-agent-shiny-e2e PYTHON_VERSION=3.15
make test-browser-lifecycle-e2e PYTHON_VERSION=3.15
```

The shipped production image uses standard Python 3.14. Before advancing the
default again, require a final interpreter, locked dependencies that install
without source builds on every target architecture, passing compatibility
checks, and representative measurements showing no material regression.
Free-threaded builds remain outside the production
compatibility promise documented in `docs/environment.md`.
