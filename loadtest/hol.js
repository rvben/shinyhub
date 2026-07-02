/**
 * loadtest/hol.js - ShinyHub head-of-line (HOL) blocking elimination scenario.
 *
 * Acceptance criterion G2: under per_session isolation, a single CPU-heavy
 * session must NOT block the other N-1 sessions. This scenario drives
 * LT_SESSIONS concurrent WebSocket connections with one designated "heavy"
 * VU that holds its connection open (simulating a long-running computation),
 * and measures time-to-first-frame for the remaining "light" VUs.
 *
 * The scenario runs two sequential phases - first against the multiplex slug,
 * then against the per_session slug - so a single k6 invocation records both
 * baselines for direct comparison.
 *
 * HOL mechanism recap:
 *   - multiplex: all sessions share one R/Python worker process. A CPU-heavy
 *     computation monopolises the event loop; other sessions starve until it
 *     yields. At N=50 with one heavy session, observed p50 degrades from
 *     0.8 s to ~12 s.
 *   - per_session: each session gets its own worker process. The heavy session
 *     runs in an isolated process and cannot block others. The light sessions'
 *     p95 should stay close to the single-session baseline (~800 ms).
 *
 * The "heavy" behavior comes from the app, not k6. To trigger a meaningful HOL
 * effect you need a Shiny app that performs CPU-intensive work on connection
 * (e.g. a slow model fit, a large matrix operation). k6 simply holds the heavy
 * VU's WebSocket open while measuring latency for the other VUs.
 *
 * Thresholds (ASSERT=1):
 *   hol_light_ms{mode:iso} p(95) < 3000 ms (3 s)
 *
 * This threshold captures the acceptance gate: under per_session isolation,
 * non-heavy sessions must complete their WS handshake within 3 s (3x the
 * ~800 ms single-session baseline), well below the ~12 s multiplex-degraded
 * figure. The multiplex run is intentionally left without a threshold - it is
 * expected to degrade and its numbers serve as the "before" baseline.
 *
 * WHY k6/ws (not k6/websockets): same reason as sessions.js - the blocking
 * callback API fires socket.on('open') and socket.on('message') at the exact
 * network event, giving accurate sub-millisecond local timings. The async
 * k6/websockets module fires callbacks only after a sleep() returns, making
 * connect and establishment latencies unreliable.
 *
 * Usage (two-slug, two-phase run):
 *   k6 run \
 *     -e LT_HOST=http://127.0.0.1:8080 \
 *     -e LT_SLUG_MUX=demo-mux \
 *     -e LT_SLUG_ISO=demo-iso \
 *     loadtest/hol.js
 *
 * Single-mode runs (omit the other slug):
 *   k6 run -e LT_SLUG_ISO=demo-iso loadtest/hol.js          # per_session only
 *   k6 run -e LT_SLUG_MUX=demo-mux loadtest/hol.js          # multiplex only
 *
 * With thresholds enabled:
 *   ASSERT=1 k6 run -e LT_SLUG_MUX=demo-mux -e LT_SLUG_ISO=demo-iso loadtest/hol.js
 *
 * Or via make:
 *   make load-test-isolation LT_SLUG_MUX=demo-mux LT_SLUG_ISO=demo-iso ASSERT=1
 */

import http from 'k6/http';
import ws from 'k6/ws';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { appURL, wsURL, cookieHeaderFromJar } from './lib.js';

// ---- parameters ----------------------------------------------------------------

const HOST              = __ENV.LT_HOST               || 'http://127.0.0.1:8080';
const SLUG_MUX          = __ENV.LT_SLUG_MUX           || '';
const SLUG_ISO          = __ENV.LT_SLUG_ISO           || '';
const SESSIONS          = parseInt(__ENV.LT_SESSIONS          || '50', 10);  // total VUs per phase
const RAMP              = __ENV.LT_RAMP               || '30s';
const HOLD              = parseFloat(__ENV.LT_HOLD    || '60');   // seconds heavy VU stays connected
const WS_PATH           = __ENV.LT_WS_PATH            || '/websocket/';
const FIRST_MSG_TIMEOUT = parseFloat(__ENV.LT_FIRST_MSG_TIMEOUT || '5');    // seconds
const AUTH_COOKIE       = __ENV.LT_AUTH_COOKIE        || '';
const ASSERT            = (__ENV.ASSERT === '1');

// Number of light VUs = total sessions minus the one heavy VU.
const LIGHT_VUS = Math.max(1, SESSIONS - 1);

// Parse RAMP duration string ("30s", "1m") into seconds for offset arithmetic.
function parseDurationSecs(d) {
  const m = String(d).match(/^(\d+(?:\.\d+)?)(ms|s|m)?$/);
  if (!m) return 30;
  const n = parseFloat(m[1]);
  switch (m[2]) {
    case 'ms': return n / 1000;
    case 'm':  return n * 60;
    default:   return n; // 's' or bare number
  }
}

const RAMP_SECS = parseDurationSecs(RAMP);

// Each phase covers: heavy lead (5s) + ramp + plateau (10s) + graceful ramp-down (10s) + buffer (5s).
// The heavy VU starts at phase start and must outlast the light VUs.
const HEAVY_LEAD_S = 5;           // seconds heavy VU runs before light VUs start ramping
const PLATEAU_S    = 10;          // k6 plateau so all VUs complete at least one iteration
const GRACE_S      = 10;          // gracefulRampDown
const PHASE_S      = Math.ceil(HEAVY_LEAD_S + RAMP_SECS + PLATEAU_S + GRACE_S + 5);
const INTER_PHASE_S = 10;         // gap between phases so connections drain

// ---- metrics -------------------------------------------------------------------

// hol_light_ms: time from connectStart to first server frame (WS-established)
// for LIGHT VUs only. Tagged {mode:mux} or {mode:iso} so thresholds can
// target the per_session phase independently.
const holLightMs    = new Trend('hol_light_ms',    true);

// hol_light_established, hol_light_failed: counters for light VUs only.
const holEstablished = new Counter('hol_light_established');
const holFailed      = new Counter('hol_light_failed');

// ---- scenario config -----------------------------------------------------------

// Build the scenarios object dynamically: each slug that is set contributes a
// "heavy" scenario (1 VU that holds the connection open to create HOL pressure)
// and a "light" scenario (LIGHT_VUS VUs that ramp up and measure latency).
// Phases run sequentially so cross-phase interference is avoided.

const scenarios = {};

let phaseOffset = 0; // absolute t=0 offset for the next phase, in seconds

if (SLUG_MUX) {
  scenarios.mux_heavy = {
    executor:  'constant-vus',
    vus:       1,
    duration:  PHASE_S + 's',
    startTime: phaseOffset + 's',
    env: { HOL_SLUG: SLUG_MUX, HOL_ROLE: 'heavy', HOL_MODE: 'mux' },
  };
  scenarios.mux_light = {
    executor:         'ramping-vus',
    startVUs:         0,
    stages: [
      { duration: RAMP,   target: LIGHT_VUS },
      { duration: PLATEAU_S + 's', target: LIGHT_VUS },
    ],
    gracefulRampDown: GRACE_S + 's',
    startTime:        (phaseOffset + HEAVY_LEAD_S) + 's',
    env: { HOL_SLUG: SLUG_MUX, HOL_ROLE: 'light', HOL_MODE: 'mux' },
  };
  phaseOffset += PHASE_S + INTER_PHASE_S;
}

if (SLUG_ISO) {
  scenarios.iso_heavy = {
    executor:  'constant-vus',
    vus:       1,
    duration:  PHASE_S + 's',
    startTime: phaseOffset + 's',
    env: { HOL_SLUG: SLUG_ISO, HOL_ROLE: 'heavy', HOL_MODE: 'iso' },
  };
  scenarios.iso_light = {
    executor:         'ramping-vus',
    startVUs:         0,
    stages: [
      { duration: RAMP,   target: LIGHT_VUS },
      { duration: PLATEAU_S + 's', target: LIGHT_VUS },
    ],
    gracefulRampDown: GRACE_S + 's',
    startTime:        (phaseOffset + HEAVY_LEAD_S) + 's',
    env: { HOL_SLUG: SLUG_ISO, HOL_ROLE: 'light', HOL_MODE: 'iso' },
  };
}

if (Object.keys(scenarios).length === 0) {
  // k6 requires at least one scenario; emit a dummy that fails fast so the
  // user sees the missing-slug error rather than a confusing k6 parse error.
  scenarios.error = {
    executor:   'shared-iterations',
    vus:        1,
    iterations: 1,
    env: { HOL_SLUG: '', HOL_ROLE: 'error', HOL_MODE: '' },
  };
}

export const options = {
  scenarios,
  thresholds: ASSERT ? {
    // per_session light sessions must complete WS handshake in < 3 s (p95).
    // 3 s = 3x the ~800 ms single-session baseline and well below the
    // ~12 s multiplex-degraded figure at N=50 with one heavy session.
    // The multiplex run carries no threshold: it is the "before" evidence.
    'hol_light_ms{mode:iso}': ['p(95)<3000'],
  } : {},
};

// ---- setup: log effective params -----------------------------------------------

export function setup() {
  console.log(
    `[hol] host=${HOST} sessions=${SESSIONS} (1 heavy + ${LIGHT_VUS} light) ` +
    `ramp=${RAMP} hold=${HOLD}s ws_path=${WS_PATH} ` +
    `first_msg_timeout=${FIRST_MSG_TIMEOUT}s assert=${ASSERT}`
  );
  if (SLUG_MUX) console.log(`[hol] MUX phase: slug=${SLUG_MUX} (expected to degrade under HOL)`);
  if (SLUG_ISO) console.log(`[hol] ISO phase: slug=${SLUG_ISO} (expected to be flat; threshold p95<3s)`);
  if (!SLUG_MUX && !SLUG_ISO) {
    console.error('[hol] ERROR: set LT_SLUG_MUX and/or LT_SLUG_ISO');
  }
}

// ---- main ----------------------------------------------------------------------

export default function () {
  const slug = __ENV.HOL_SLUG;
  const role = __ENV.HOL_ROLE;
  const mode = __ENV.HOL_MODE;

  if (!slug) {
    console.error('HOL_SLUG not set - set LT_SLUG_MUX and/or LT_SLUG_ISO');
    sleep(1);
    return;
  }

  if (role === 'error') {
    console.error('No slugs configured. Set LT_SLUG_MUX and/or LT_SLUG_ISO.');
    sleep(1);
    return;
  }

  const rootURL = appURL(HOST, slug, '/');
  const jar     = http.cookieJar();

  // (a) HTTP GET - collect sticky-routing cookie, wake app if hibernated.
  const httpRes = http.get(rootURL, { jar, timeout: '15s', redirects: 5 });

  check(httpRes, { 'http root 200': (r) => r.status === 200 });

  // Failure backoff: prevents tight reconnect loops from exhausting ephemeral
  // ports or DDoSing the target if it is down.
  if (httpRes.status !== 200) {
    if (role === 'light') holFailed.add(1, { mode });
    sleep(1);
    return;
  }

  // Build merged Cookie header for the WS upgrade (sticky-routing + auth cookie).
  const cookieHeader = cookieHeaderFromJar(jar, rootURL, AUTH_COOKIE);
  const wsParams     = { headers: cookieHeader ? { Cookie: cookieHeader } : {} };
  const target       = wsURL(HOST, slug, WS_PATH);
  const connectStart = Date.now();
  let established    = false;

  // (b) Open WebSocket via blocking k6/ws API.
  // ws.connect() drives the event loop inline; callbacks fire at the exact
  // network event, giving accurate latency without polling-induced jitter.
  const res = ws.connect(target, wsParams, function (socket) {
    socket.on('open', () => {
      // Abort if no server frame arrives within LT_FIRST_MSG_TIMEOUT.
      socket.setTimeout(() => {
        if (!established) {
          if (role === 'light') holFailed.add(1, { mode });
          socket.close();
        }
      }, FIRST_MSG_TIMEOUT * 1000);
    });

    socket.on('message', () => {
      if (!established) {
        established = true;
        const elapsed = Date.now() - connectStart;

        if (role === 'heavy') {
          // Heavy VU: hold the connection open for LT_HOLD seconds to create
          // HOL pressure. No latency metric recorded for the heavy VU.
          socket.setTimeout(() => socket.close(), HOLD * 1000);
        } else {
          // Light VU: record time-to-first-frame and close.
          holLightMs.add(elapsed, { mode });
          holEstablished.add(1, { mode });
          socket.setTimeout(() => socket.close(), 0);
        }
      }
    });

    socket.on('error', () => {
      if (!established && role === 'light') {
        holFailed.add(1, { mode });
      }
      socket.close();
    });
  });

  // Count connection-refused / upgrade-rejected as failures.
  if (!res || res.status !== 101) {
    if (!established && role === 'light') {
      holFailed.add(1, { mode });
    }
  }

  // Tagged check for threshold targeting (light VUs only).
  if (role === 'light') {
    check(null, { established: () => established }, { tag: 'established' });
  }

  // WS-level failure backoff (same rationale as sessions.js).
  if (!established) {
    sleep(1);
  }
}

// ---- summary -------------------------------------------------------------------

export function handleSummary(data) {
  const ts      = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19) + 'Z';
  const outPath = `loadtest/results/${ts}-hol.json`;

  function p(metricName, pct, tag) {
    const key = tag ? `${metricName}{${tag}}` : metricName;
    const m   = data.metrics[key] || data.metrics[metricName];
    if (!m) return 'n/a';
    const v = m.values[`p(${pct})`];
    return v != null ? (v / 1000).toFixed(2) + 's' : 'n/a';
  }

  function cnt(metricName, tag) {
    const key = tag ? `${metricName}{${tag}}` : metricName;
    const m   = data.metrics[key] || data.metrics[metricName];
    return m ? (m.values.count || 0) : 0;
  }

  const muxOk   = cnt('hol_light_established', 'mode:mux');
  const muxFail = cnt('hol_light_failed',       'mode:mux');
  const isoOk   = cnt('hol_light_established', 'mode:iso');
  const isoFail = cnt('hol_light_failed',       'mode:iso');

  const lines = ['HOL ELIMINATION RESULTS:'];

  if (SLUG_MUX) {
    const total = muxOk + muxFail;
    const pct   = total > 0 ? ((muxOk / total) * 100).toFixed(1) : '0.0';
    lines.push(
      `  multiplex  (${SLUG_MUX}): established ${muxOk}/${total} (${pct}%)` +
      `  light_ms p50=${p('hol_light_ms', 50, 'mode:mux')} p95=${p('hol_light_ms', 95, 'mode:mux')}`
    );
  }

  if (SLUG_ISO) {
    const total = isoOk + isoFail;
    const pct   = total > 0 ? ((isoOk / total) * 100).toFixed(1) : '0.0';
    lines.push(
      `  per_session (${SLUG_ISO}): established ${isoOk}/${total} (${pct}%)` +
      `  light_ms p50=${p('hol_light_ms', 50, 'mode:iso')} p95=${p('hol_light_ms', 95, 'mode:iso')}`
    );
  }

  if (ASSERT && SLUG_ISO) {
    lines.push(`  threshold (ASSERT=1): hol_light_ms{mode:iso} p(95) < 3 s`);
  }

  lines.forEach((l) => console.log(l));

  return {
    [outPath]: JSON.stringify(data, null, 2),
    stdout:    '\n',
  };
}
