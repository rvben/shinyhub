/**
 * loadtest/coldstart.js - ShinyHub cold-start / wake-latency scenario.
 *
 * Measures what a real user experiences when hitting a hibernated app:
 *
 *   Stage 1 - page ready (http):
 *     t0: GET app root (triggers hibernate wake; may return loading page)
 *     poll: GET app root every 500ms until 200 with body NOT containing
 *           LOADING_MARKER, or LT_COLDSTART_TIMEOUT
 *     -> records Trend `coldstart_http_ms` (t0 to first real-content 200)
 *
 *   Stage 2 - session ready (websocket):
 *     immediately open one WS (same cookie-jar + LT_WS_PATH as sessions.js)
 *     wait for first server frame within LT_FIRST_MSG_TIMEOUT
 *     -> records Trend `coldstart_total_ms` (t0 through WS-established)
 *
 * WHY NOT poll /.shinyhub/ready:
 *   The ready probe returns {"ready":true} only after a completed WebSocket
 *   handshake (serveReadyProbe -> IsWSReady in internal/proxy/proxy.go). A
 *   freshly woken app serving HTTP but not yet having a WS handshake will
 *   return 503 {"ready":false} indefinitely if nothing opens a WS - so
 *   polling the ready probe without also opening a WS will always time out.
 *   The ready probe IS used once at the start as a fast existence check:
 *   it correctly returns 404 for unknown slugs regardless of WS state,
 *   giving a clear "wrong slug" error before the cold-start poll begins.
 *
 * Warm-app interpretation:
 *   Against a running app (min_warm_replicas>=1) stage 1 completes in one
 *   round trip (<100ms); stage 2 is one WS connect. Both numbers together
 *   give an honest warm-floor baseline to compare against cold-start.
 *
 * ASSERT=1 threshold targets coldstart_total_ms (the evaluation claim is
 * time-to-usable-session, not time-to-first-byte).
 *
 * Usage:
 *   k6 run -e LT_HOST=http://127.0.0.1:8080 -e LT_SLUG=myapp loadtest/coldstart.js
 *   ASSERT=1 k6 run ... (enables p95<15s threshold on total)
 */

import http from 'k6/http';
import ws from 'k6/ws';
import { sleep, check, fail } from 'k6';
import { Trend } from 'k6/metrics';
import { appURL, wsURL, cookieHeaderFromJar, LOADING_MARKER } from './lib.js';

// ---- parameters ----------------------------------------------------------------

const HOST              = __ENV.LT_HOST               || 'http://127.0.0.1:8080';
const SLUG              = __ENV.LT_SLUG;
const TIMEOUT           = parseInt(__ENV.LT_COLDSTART_TIMEOUT  || '120', 10); // seconds
const WS_PATH           = __ENV.LT_WS_PATH                      || '/websocket/';
const FIRST_MSG_TIMEOUT = parseFloat(__ENV.LT_FIRST_MSG_TIMEOUT  || '5');     // seconds
const AUTH_COOKIE       = __ENV.LT_AUTH_COOKIE                   || '';
const ASSERT            = (__ENV.ASSERT === '1');

// ---- metrics -------------------------------------------------------------------

// coldstart_http_ms: t0 to first real-content HTTP 200 (app rendered, no loading page).
const coldstartHttpMs  = new Trend('coldstart_http_ms',  true);
// coldstart_total_ms: t0 through WebSocket-established (first server frame received).
const coldstartTotalMs = new Trend('coldstart_total_ms', true);

// ---- scenario config -----------------------------------------------------------

export const options = {
  vus: 1,
  iterations: 1,
  thresholds: ASSERT
    ? { coldstart_total_ms: ['p(95)<15000'] }
    : {},
};

// ---- setup: log effective params -----------------------------------------------

export function setup() {
  console.log(
    `[coldstart] host=${HOST} slug=${SLUG} timeout=${TIMEOUT}s ` +
    `ws_path=${WS_PATH} first_msg_timeout=${FIRST_MSG_TIMEOUT}s assert=${ASSERT}`
  );
}

// ---- main ----------------------------------------------------------------------

export default function () {
  if (!SLUG) {
    fail('LT_SLUG is required');
  }

  const rootURL  = appURL(HOST, SLUG, '/');
  const readyURL = appURL(HOST, SLUG, '/.shinyhub/ready');

  // Existence check: probe the ready endpoint once. It returns 404 for unknown
  // slugs regardless of WS state, 200 or 503 for known ones. This catches a
  // wrong LT_SLUG immediately before the cold-start timer starts.
  const existCheck = http.get(readyURL, { timeout: '5s', redirects: 0 });
  if (existCheck.status === 404) {
    fail(`Unknown slug "${SLUG}" - /.shinyhub/ready returned 404. Check LT_SLUG and LT_HOST.`);
  }

  // ---- Stage 1: page ready (HTTP) ------------------------------------------------

  const jar    = http.cookieJar();
  const t0     = Date.now();
  const params = { jar, timeout: '10s', redirects: 5 };

  // Initial GET - triggers hibernate wake; response may be the loading page.
  const initRes = http.get(rootURL, params);
  check(initRes, { 'initial GET 200': (r) => r.status === 200 });

  // Poll until the response body is real app content (not the loading page).
  // The loading page carries id="shinyhub-box" (internal/proxy/proxy.go
  // loadingPage const); LOADING_MARKER is pinned by the Go contract test in
  // internal/proxy/loading_contract_test.go so a loading-page redesign that
  // removes the element fails the build.
  const deadline = t0 + TIMEOUT * 1000;
  let httpReadyAt = 0;

  // Check the initial response first - if the app was already warm it may
  // have returned real content immediately.
  if (initRes.status === 200 && initRes.body && !initRes.body.includes(LOADING_MARKER)) {
    httpReadyAt = Date.now();
  }

  while (httpReadyAt === 0 && Date.now() < deadline) {
    sleep(0.5);
    const r = http.get(rootURL, params);
    if (r.status === 200 && r.body && !r.body.includes(LOADING_MARKER)) {
      httpReadyAt = Date.now();
      check(r, { 'page ready: not loading page': () => true });
      break;
    }
  }

  if (httpReadyAt === 0) {
    fail(`App "${SLUG}" page did not become ready within ${TIMEOUT}s (cold-start timeout - still serving loading page)`);
  }

  const httpElapsed = httpReadyAt - t0;
  coldstartHttpMs.add(httpElapsed);

  // ---- Stage 2: session ready (WebSocket) ----------------------------------------

  // Open one WS using the cookie jar collected during the HTTP poll (carries
  // the sticky-routing cookie shinyhub_rep_<slug>), matching the session flow.
  const cookieHeader = cookieHeaderFromJar(jar, rootURL, AUTH_COOKIE);
  const wsParams     = {
    headers: cookieHeader ? { Cookie: cookieHeader } : {},
  };
  const target = wsURL(HOST, SLUG, WS_PATH);

  let wsEstablished = false;

  const res = ws.connect(target, wsParams, function (socket) {
    socket.on('open', () => {
      // Abort if no server frame arrives within LT_FIRST_MSG_TIMEOUT.
      socket.setTimeout(() => {
        if (!wsEstablished) {
          socket.close();
        }
      }, FIRST_MSG_TIMEOUT * 1000);
    });

    socket.on('message', () => {
      if (!wsEstablished) {
        wsEstablished = true;
        coldstartTotalMs.add(Date.now() - t0);
        socket.close(); // one frame is enough; don't hold open during cold-start
      }
    });

    socket.on('error', () => {
      socket.close();
    });
  });

  check(null, {
    'WS established (first server frame)': () => wsEstablished,
  });

  if (!wsEstablished) {
    // Still record the http metric so the headline is partial rather than n/a.
    fail(`App "${SLUG}" WS did not establish within ${FIRST_MSG_TIMEOUT}s after page was ready`);
  }
}

// ---- summary -------------------------------------------------------------------

export function handleSummary(data) {
  const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19) + 'Z';
  const outPath = `loadtest/results/${ts}-coldstart.json`;

  function secs(metricName) {
    const m = data.metrics[metricName];
    return m ? (m.values.avg / 1000).toFixed(2) : 'n/a';
  }

  const httpS  = secs('coldstart_http_ms');
  const totalS = secs('coldstart_total_ms');

  console.log(
    `COLD START: http=${httpS}s, session=${totalS}s (slug=${SLUG || 'unknown'}, host=${HOST})`
  );

  return {
    [outPath]: JSON.stringify(data, null, 2),
    stdout:    '\n',
  };
}
