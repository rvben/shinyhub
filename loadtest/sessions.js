/**
 * loadtest/sessions.js - ShinyHub WebSocket concurrency scenario.
 *
 * Each VU:
 *   (a) HTTP GET app root  - collects sticky-routing cookie (shinyhub_rep_<slug>)
 *   (b) Open WebSocket via k6/ws (blocking callback API)
 *   (c) ESTABLISHED        - first server frame received within LT_FIRST_MSG_TIMEOUT
 *       Metrics: ws_connect_ms (time-to-101 Upgrade), ws_established_ms (time-to-first-frame)
 *   (d) HOLD               - keep socket open LT_HOLD seconds after establishment
 *   (e) Close cleanly.
 *
 * WHY k6/ws (not k6/websockets):
 *   k6/websockets uses an event-driven async model where callbacks fire only
 *   when a blocking call (sleep) returns. This means timing recorded inside
 *   event listeners captures the sleep duration, not the actual connect or
 *   establishment latency. k6/ws uses a blocking ws.connect() that drives
 *   its own event loop inline, so socket.on('open') fires at the exact moment
 *   the 101 Upgrade completes - giving accurate single-digit-ms local timings.
 *
 * Framework caveat: this scenario is tuned for server-sends-first frameworks
 * (R Shiny, Python Shiny). The established gate waits for the first server
 * frame; Streamlit uses a client-first protobuf handshake on /_stcore/stream
 * and the server will not send a frame first. For Streamlit, set
 * LT_WS_PATH=/_stcore/stream and extend LT_FIRST_MSG_TIMEOUT, and instrument
 * the VU with the protobuf preamble before waiting for a server frame.
 *
 * Usage:
 *   k6 run -e LT_HOST=http://127.0.0.1:8080 -e LT_SLUG=myapp loadtest/sessions.js
 *   k6 run -e LT_SESSIONS=200 -e LT_RAMP=30s -e LT_HOLD=20 ... (explicit params)
 *   ASSERT=1 k6 run ... (enables >=99% established-check threshold)
 */

import http from 'k6/http';
import ws from 'k6/ws';
import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { appURL, wsURL, cookieHeaderFromJar } from './lib.js';

// ---- parameters ----------------------------------------------------------------

const HOST              = __ENV.LT_HOST               || 'http://127.0.0.1:8080';
const SLUG              = __ENV.LT_SLUG;
const SESSIONS          = parseInt(__ENV.LT_SESSIONS          || '100', 10);
const RAMP              = __ENV.LT_RAMP                        || '30s';
const HOLD              = parseFloat(__ENV.LT_HOLD             || '30');   // seconds
const WS_PATH           = __ENV.LT_WS_PATH                     || '/websocket/';
const FIRST_MSG_TIMEOUT = parseFloat(__ENV.LT_FIRST_MSG_TIMEOUT || '5');   // seconds
const AUTH_COOKIE       = __ENV.LT_AUTH_COOKIE                 || '';
const ASSERT            = (__ENV.ASSERT === '1');

// ---- metrics -------------------------------------------------------------------

const wsConnected    = new Counter('ws_connected');
const wsEstablished  = new Counter('ws_established');
const wsFailed       = new Counter('ws_failed');
// ws_connect_ms: time from connectStart to 101 Upgrade (open event).
const wsConnectMs    = new Trend('ws_connect_ms', true);
// ws_established_ms: time from connectStart to first server frame received.
const wsEstablishedMs = new Trend('ws_established_ms', true);

// ---- scenario config -----------------------------------------------------------

// Two stages: ramp to SESSIONS, then a short plateau so every VU completes
// at least one iteration before graceful ramp-down. Without the plateau,
// the last batch of VUs added in the final ramp step may not start before
// the stage ends, causing established/SESSIONS to be off by the final step.
export const options = {
  scenarios: {
    sessions: {
      executor:         'ramping-vus',
      startVUs:         0,
      stages:           [
        { duration: RAMP,  target: SESSIONS },
        { duration: '10s', target: SESSIONS },  // plateau: all VUs run once
      ],
      gracefulRampDown: '10s',
    },
  },
  thresholds: ASSERT
    ? { 'checks{tag:established}': ['rate>0.99'] }
    : {},
};

// ---- setup: log effective params -----------------------------------------------

export function setup() {
  console.log(
    `[sessions] host=${HOST} slug=${SLUG} sessions=${SESSIONS} ` +
    `ramp=${RAMP} hold=${HOLD}s ws_path=${WS_PATH} ` +
    `first_msg_timeout=${FIRST_MSG_TIMEOUT}s assert=${ASSERT}`
  );
}

// ---- main ----------------------------------------------------------------------

export default function () {
  if (!SLUG) {
    console.error('LT_SLUG is required');
    return;
  }

  // (a) HTTP GET - collect sticky-routing cookie.
  const rootURL = appURL(HOST, SLUG, '/');
  const jar     = http.cookieJar();
  const httpRes = http.get(rootURL, { jar, timeout: '10s', redirects: 5 });

  check(httpRes, { 'http root 200': (r) => r.status === 200 });

  // Build merged Cookie header for the WS upgrade.
  const cookieHeader = cookieHeaderFromJar(jar, rootURL, AUTH_COOKIE);

  const wsParams = {
    headers: cookieHeader ? { Cookie: cookieHeader } : {},
  };

  // (b-e) Open WebSocket via blocking k6/ws API.
  // ws.connect() drives the socket's event loop inline; callbacks fire at
  // the exact moment the underlying network event occurs, giving accurate
  // latency measurements without polling-induced jitter.
  const target       = wsURL(HOST, SLUG, WS_PATH);
  const connectStart = Date.now();

  let established    = false;

  const res = ws.connect(target, wsParams, function (socket) {
    socket.on('open', () => {
      const connectedAt = Date.now();
      wsConnected.add(1);
      wsConnectMs.add(connectedAt - connectStart);

      // Abort if no server frame arrives within LT_FIRST_MSG_TIMEOUT.
      socket.setTimeout(() => {
        if (!established) {
          wsFailed.add(1);
          socket.close();
        }
      }, FIRST_MSG_TIMEOUT * 1000);
    });

    socket.on('message', () => {
      if (!established) {
        established = true;
        wsEstablished.add(1);
        wsEstablishedMs.add(Date.now() - connectStart);

        // (d) Hold open for LT_HOLD seconds then close cleanly.
        socket.setTimeout(() => socket.close(), HOLD * 1000);
      }
    });

    socket.on('error', () => {
      if (!established) {
        wsFailed.add(1);
      }
    });
  });

  // Count connection-refused / upgrade-rejected as failures.
  if (!res || res.status !== 101) {
    if (!established) {
      wsFailed.add(1);
    }
  }

  // (e) Record establishment result as a tagged check for threshold targeting.
  check(null, {
    established: () => established,
  }, { tag: 'established' });
}

// ---- summary -------------------------------------------------------------------

export function handleSummary(data) {
  const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19) + 'Z';
  const outPath = `loadtest/results/${ts}-sessions.json`;

  const established = data.metrics['ws_established']
    ? data.metrics['ws_established'].values.count : 0;
  const failed      = data.metrics['ws_failed']
    ? data.metrics['ws_failed'].values.count : 0;
  const total       = established + failed;
  const pct         = total > 0 ? ((established / total) * 100).toFixed(1) : '0.0';

  const p95ms = data.metrics['ws_established_ms']
    ? (data.metrics['ws_established_ms'].values['p(95)'] || 0).toFixed(0)
    : 'n/a';

  console.log(
    `SESSIONS: established ${established}/${total} (${pct}%), established p95=${p95ms}ms`
  );

  return {
    [outPath]: JSON.stringify(data, null, 2),
    stdout:    '\n',
  };
}
