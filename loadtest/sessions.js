/**
 * loadtest/sessions.js - ShinyHub WebSocket concurrency scenario.
 *
 * Each VU:
 *   (a) HTTP GET app root  - collects sticky-routing cookie (shinyhub_rep_<slug>)
 *   (b) Open WebSocket     - forwards merged Cookie header (jar + LT_AUTH_COOKIE)
 *   (c) ESTABLISHED        - wait up to LT_FIRST_MSG_TIMEOUT for first server frame
 *   (d) HOLD               - keep socket open for LT_HOLD seconds after establishment
 *   (e) Close cleanly.
 *
 * Framework caveat: this scenario is tuned for server-sends-first frameworks
 * (R Shiny, Python Shiny). Streamlit uses a client-first protobuf handshake on
 * /_stcore/stream - the established gate (waiting for the first server frame)
 * will time out for Streamlit unless the client sends the initial handshake.
 * For Streamlit load-testing, set LT_WS_PATH=/_stcore/stream and extend
 * LT_FIRST_MSG_TIMEOUT, or instrument the VU with the protobuf preamble.
 *
 * Usage:
 *   k6 run -e LT_HOST=http://127.0.0.1:8080 -e LT_SLUG=myapp loadtest/sessions.js
 *   k6 run -e LT_SESSIONS=200 -e LT_RAMP=30s -e LT_HOLD=20s ... (explicit params)
 *   ASSERT=1 k6 run ... (enables >=99% established-check threshold)
 */

import http from 'k6/http';
import { WebSocket } from 'k6/websockets';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { appURL, wsURL, cookieHeaderFromJar, LOADING_MARKER } from './lib.js';

// ---- parameters ----------------------------------------------------------------

const HOST              = __ENV.LT_HOST              || 'http://127.0.0.1:8080';
const SLUG              = __ENV.LT_SLUG;
const SESSIONS          = parseInt(__ENV.LT_SESSIONS          || '100', 10);
const RAMP              = __ENV.LT_RAMP                        || '30s';
const HOLD              = parseFloat(__ENV.LT_HOLD             || '30');  // seconds
const WS_PATH           = __ENV.LT_WS_PATH                     || '/websocket/';
const FIRST_MSG_TIMEOUT = parseFloat(__ENV.LT_FIRST_MSG_TIMEOUT || '5');  // seconds
const AUTH_COOKIE       = __ENV.LT_AUTH_COOKIE                 || '';
const ASSERT            = (__ENV.ASSERT === '1');

// ---- metrics -------------------------------------------------------------------

const wsConnected    = new Counter('ws_connected');
const wsEstablished  = new Counter('ws_established');
const wsFailed       = new Counter('ws_failed');
const wsConnectMs    = new Trend('ws_connect_ms', true);

// ---- scenario config -----------------------------------------------------------

export const options = {
  scenarios: {
    sessions: {
      executor:          'ramping-vus',
      startVUs:          0,
      stages:            [{ duration: RAMP, target: SESSIONS }],
      gracefulRampDown:  '10s',
    },
  },
  thresholds: ASSERT
    ? { 'checks{tag:established}': ['rate>0.99'] }
    : {},
};

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

  // (b) Open WebSocket.
  const target  = wsURL(HOST, SLUG, WS_PATH);
  const connectStart = Date.now();

  // Shared state between the WS callbacks and the outer scope.
  let established  = false;
  let connectedAt  = 0;
  let establishErr = null;
  let holdUntil    = 0;
  let closeOnce    = false;

  const ws = new WebSocket(target, null, wsParams);

  ws.addEventListener('open', () => {
    connectedAt = Date.now();
    wsConnected.add(1);
    wsConnectMs.add(connectedAt - connectStart);
    // Start the establishment timeout ticker.
    holdUntil = connectedAt + FIRST_MSG_TIMEOUT * 1000;
  });

  ws.addEventListener('message', () => {
    if (!established) {
      established = true;
      wsEstablished.add(1);
      // (d) Hold open for LT_HOLD seconds after establishment.
      holdUntil = Date.now() + HOLD * 1000;
    }
  });

  ws.addEventListener('error', (e) => {
    if (!established) {
      establishErr = e.type || 'error';
      wsFailed.add(1);
    }
  });

  ws.addEventListener('close', () => {
    // Closed by us or server; nothing to do.
  });

  // Drive the event loop: poll until we should close.
  const pollInterval = 0.1; // seconds
  const maxWait = (FIRST_MSG_TIMEOUT + HOLD + 5) * 1000; // hard outer bound

  const loopStart = Date.now();
  while (Date.now() - loopStart < maxWait) {
    sleep(pollInterval);

    if (establishErr !== null && !established) {
      // Connection failed; bail.
      break;
    }

    if (connectedAt > 0 && !established && Date.now() >= holdUntil) {
      // First-message timeout expired without establishment.
      wsFailed.add(1);
      break;
    }

    if (established && Date.now() >= holdUntil) {
      // Hold period elapsed; close cleanly.
      if (!closeOnce) {
        closeOnce = true;
        ws.close();
      }
      break;
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

  const connected   = data.metrics['ws_connected']   ? data.metrics['ws_connected'].values.count   : 0;
  const established = data.metrics['ws_established']  ? data.metrics['ws_established'].values.count  : 0;
  const failed      = data.metrics['ws_failed']       ? data.metrics['ws_failed'].values.count       : 0;
  const total       = established + failed;
  const pct         = total > 0 ? ((established / total) * 100).toFixed(1) : '0.0';

  const p95ms = data.metrics['ws_connect_ms']
    ? (data.metrics['ws_connect_ms'].values['p(95)'] || 0).toFixed(0)
    : 'n/a';

  console.log(
    `SESSIONS: established ${established}/${total} (${pct}%), connect p95=${p95ms}ms`
  );

  return {
    [outPath]: JSON.stringify(data, null, 2),
    stdout:    '\n',
  };
}
