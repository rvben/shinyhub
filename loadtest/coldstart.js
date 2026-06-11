/**
 * loadtest/coldstart.js - ShinyHub cold-start / wake-latency scenario.
 *
 * Measures the real time-to-app for a hibernated ShinyHub app:
 *   t0: GET app root (may trigger hibernate wake)
 *   poll: GET /.shinyhub/ready every 500 ms until 200 + {"ready":true}
 *         or until LT_COLDSTART_TIMEOUT
 *   final: GET app root, assert 200 and body does NOT contain LOADING_MARKER
 *   metric: coldstart_time (Trend, ms) = elapsed t0 -> final OK
 *
 * Run against a warm app (min_warm_replicas>=1) it measures one-round-trip
 * latency instead - a different but equally honest number.
 *
 * Usage:
 *   k6 run -e LT_HOST=http://127.0.0.1:8080 -e LT_SLUG=myapp loadtest/coldstart.js
 *   ASSERT=1 k6 run ... (enables p95<15s threshold)
 */

import http from 'k6/http';
import { sleep, check, fail } from 'k6';
import { Trend } from 'k6/metrics';
import { appURL, LOADING_MARKER } from './lib.js';

// ---- parameters ----------------------------------------------------------------

const HOST    = __ENV.LT_HOST    || 'http://127.0.0.1:8080';
const SLUG    = __ENV.LT_SLUG;
const TIMEOUT = parseInt(__ENV.LT_COLDSTART_TIMEOUT || '120', 10); // seconds
const ASSERT  = (__ENV.ASSERT === '1');

// ---- metrics -------------------------------------------------------------------

const coldstartTime = new Trend('coldstart_time', true); // ms

// ---- scenario config -----------------------------------------------------------

export const options = {
  vus: 1,
  iterations: 1,
  thresholds: ASSERT
    ? { coldstart_time: ['p(95)<15000'] }
    : {},
};

// ---- main ----------------------------------------------------------------------

export default function () {
  if (!SLUG) {
    fail('LT_SLUG is required');
  }

  const rootURL  = appURL(HOST, SLUG, '/');
  const readyURL = appURL(HOST, SLUG, '/.shinyhub/ready');
  const params   = { timeout: '10s', redirects: 5 };

  const t0 = Date.now();

  // Initial GET - may trigger a wake, collects any sticky-session cookie.
  const initRes = http.get(rootURL, params);
  check(initRes, { 'initial GET 200': (r) => r.status === 200 });

  // Poll ready endpoint until ready or timeout.
  const deadline = t0 + TIMEOUT * 1000;
  let ready = false;

  while (Date.now() < deadline) {
    sleep(0.5);

    const r = http.get(readyURL, { timeout: '5s', redirects: 0 });

    if (r.status === 404) {
      fail(`Unknown slug "${SLUG}" - /.shinyhub/ready returned 404. Check LT_SLUG and LT_HOST.`);
    }

    if (r.status === 200) {
      let body;
      try {
        body = r.json();
      } catch (_) {
        body = {};
      }
      if (body && body.ready === true) {
        ready = true;
        break;
      }
    }
    // 503 + Retry-After:1 means not ready yet; any other status also loops.
  }

  if (!ready) {
    fail(`App "${SLUG}" did not become ready within ${TIMEOUT}s (cold-start timeout)`);
  }

  // Final GET - must be real app content (not the loading page).
  const finalRes = http.get(rootURL, params);
  const elapsed  = Date.now() - t0;

  const notLoadingPage = !finalRes.body || !finalRes.body.includes(LOADING_MARKER);
  check(finalRes, {
    'final GET 200':              (r) => r.status === 200,
    'final GET not loading page': ()  => notLoadingPage,
  });

  coldstartTime.add(elapsed);
}

// ---- summary -------------------------------------------------------------------

export function handleSummary(data) {
  const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19) + 'Z';
  const outPath = `loadtest/results/${ts}-coldstart.json`;

  const trend = data.metrics['coldstart_time'];
  const secs  = trend ? (trend.values.avg / 1000).toFixed(2) : 'n/a';

  console.log(`COLD START: ${secs}s (slug=${SLUG || 'unknown'}, host=${HOST})`);

  return {
    [outPath]: JSON.stringify(data, null, 2),
    stdout:    '\n', // k6 default summary still printed
  };
}
