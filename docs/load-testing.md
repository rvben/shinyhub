# Load Testing ShinyHub

ShinyHub ships a k6-based load-test harness under `loadtest/`. Two scenarios
provide the evidence for the platform's key performance claims:

- **cold-start**: real wake latency from a hibernated app (< 15 s p95 claim)
- **sessions**: concurrent WebSocket sessions at scale (1 000-session claim)

All load tests are make targets; no pipeline-specific tooling is needed. CI
runs the same `make load-test` invocation that you run locally.

---

## Prerequisites

Install [k6](https://k6.io/docs/get-started/installation/) (v2.0+, confirmed
with v2.0.0):

```sh
brew install k6           # macOS
# or: https://k6.io/docs/get-started/installation/
```

A running ShinyHub server and a deployed app are required. Quick local setup:

```sh
make dev                  # starts server on :8080
shinyhub deploy /tmp/demo-shiny-app --slug demo
```

---

## Running the tests

### Sessions scenario (default)

Ramps to `LT_SESSIONS` concurrent WebSocket connections, holds each for
`LT_HOLD` seconds, then reports established counts and connect latencies.

```sh
make load-test LT_SLUG=demo
```

With explicit parameters:

```sh
make load-test LT_SLUG=demo LT_SESSIONS=200 LT_RAMP=30s LT_HOLD=20s
```

Reproduce the 1 000-session evaluation claim (needs adequate hardware):

```sh
make load-test LT_SLUG=demo LT_SESSIONS=1000 LT_RAMP=60s LT_HOLD=30s
```

### Cold-start scenario

Triggers hibernation wake and measures elapsed time until the app serves real
content (not the loading page), using the `/.shinyhub/ready` probe as the
primary signal.

```sh
make load-test LT_SCENARIO=cold-start LT_SLUG=demo
```

First hibernate the app to make the cold-start meaningful:

```sh
shinyhub apps set --slug demo --hibernate-timeout 1  # 1-minute idle timeout
# wait for the app to hibernate, then:
make load-test LT_SCENARIO=cold-start LT_SLUG=demo
```

### Both scenarios

```sh
make load-test LT_SCENARIO=both LT_SLUG=demo
```

### CI usage with ASSERT=1

Pass `ASSERT=1` to turn on k6 thresholds. The run exits non-zero if the
thresholds are not met, gating CI on the performance claims.

```sh
# cold-start p95 < 15 s
make load-test LT_SCENARIO=cold-start LT_SLUG=demo ASSERT=1

# session established rate >= 99%
make load-test LT_SLUG=demo LT_SESSIONS=100 ASSERT=1

# both thresholds in one run
make load-test LT_SCENARIO=both LT_SLUG=demo ASSERT=1
```

---

## Parameter reference

All parameters are optional except `LT_SLUG`.

| Variable | Default | Description |
|---|---|---|
| `LT_SLUG` | (required) | App slug to target |
| `LT_HOST` | `http://127.0.0.1:8080` | ShinyHub server base URL |
| `LT_SCENARIO` | `sessions` | Which scenario to run: `sessions`, `cold-start`, or `both` |
| `LT_SESSIONS` | `100` | Target concurrent VUs for the sessions scenario |
| `LT_RAMP` | `30s` | Ramp duration to reach `LT_SESSIONS` VUs |
| `LT_HOLD` | `30` | Seconds each VU holds the WebSocket open after establishment |
| `LT_WS_PATH` | `/websocket/` | WebSocket endpoint beneath the app root (see framework table below) |
| `LT_FIRST_MSG_TIMEOUT` | `5` | Seconds to wait for the first server frame before counting as failed |
| `LT_COLDSTART_TIMEOUT` | `120` | Maximum seconds to wait for the ready probe before aborting |
| `LT_AUTH_COOKIE` | (empty) | Full Cookie header value for private apps (e.g. `session=abc123`) |
| `ASSERT` | `0` | Set to `1` to enable k6 thresholds and fail on regression |

---

## WebSocket paths by framework

| Framework | `LT_WS_PATH` | Notes |
|---|---|---|
| R Shiny | `/websocket/` (default) | Server sends an init frame on open; sessions scenario works as-is |
| Python Shiny | `/websocket/` (default) | Same as R Shiny; server-sends-first |
| Streamlit | `/_stcore/stream` | Client-sends-first protobuf handshake required; the established gate (first server frame) will time out unless the VU sends the preamble. Extend `LT_FIRST_MSG_TIMEOUT` or instrument the VU with the protobuf handshake. |

The sessions scenario was designed and evaluated against Python Shiny (server-
sends-first). The framework caveat is also noted in the script header of
`loadtest/sessions.js`.

---

## Private apps

For apps with `access: private` or `access: shared`, obtain a session cookie
by logging in through the UI or via `POST /api/auth/login`, then pass the
full Cookie header value:

```sh
make load-test LT_SLUG=demo \
  LT_AUTH_COOKIE="shinyhub_session=<your-session-token>"
```

The cookie is forwarded on both the initial HTTP GET (which collects the
sticky-routing cookie) and the WebSocket upgrade.

---

## How to read the results

### Headline output

Each scenario prints a single headline line to stdout:

```
COLD START: 3.42s (slug=demo, host=http://127.0.0.1:8080)
SESSIONS: established 198/200 (99.0%), established p95=4ms
```

The cold-start time is the total elapsed from first GET to `/.shinyhub/ready`
returning `{"ready":true}` and the app root returning real content. For a
warm app (min_warm_replicas >= 1) this is one round-trip latency.

The sessions line shows how many VUs received the first server WebSocket frame
within `LT_FIRST_MSG_TIMEOUT`. A mere TCP connection is not counted as
established - the first server message must arrive. This matches the real user
experience: a Shiny session is not active until the server sends its init frame.

Two timing metrics are recorded per VU:
- `ws_connect_ms`: time from `connectStart` to the 101 Upgrade (open event)
- `ws_established_ms`: time from `connectStart` to the first server frame

The headline prints `established p95` from `ws_established_ms` - the more
meaningful number for real users. Both metrics use `k6/ws` (blocking callback
API) which drives the event loop inline, giving accurate sub-millisecond
local timings. The newer `k6/websockets` async module fires callbacks only
after a blocking sleep returns, making connect timings unreliable.

### JSON results

Full k6 metric data is written to `loadtest/results/<ISO-ts>-<scenario>.json`
after each run. The directory is gitignored. Use these files for trend
analysis or to share results without re-running.

### Evaluation claims

| Claim | How to reproduce |
|---|---|
| Initial app load < 15 s (p95) | Hibernate the app, run `make load-test LT_SCENARIO=cold-start LT_SLUG=<slug> ASSERT=1` |
| 1 000 concurrent sessions | `make load-test LT_SLUG=<slug> LT_SESSIONS=1000 LT_RAMP=60s LT_HOLD=30s ASSERT=1` on hardware with sufficient open-file limits |

For the 1 000-session run, ensure the OS open-file limit is high enough:

```sh
ulimit -n 65536
make load-test LT_SLUG=demo LT_SESSIONS=1000 LT_RAMP=60s LT_HOLD=30s
```

---

## Results directory

`loadtest/results/` is gitignored (all files except `.gitignore`). Results
are local to the machine that ran the test. Copy them out before deleting the
working tree if you want to keep them.
