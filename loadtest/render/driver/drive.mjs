/**
 * Headless chromium driver for the render-saturation rig.
 *
 * Runs one browser with N contexts, each loading the synthetic Shiny app and
 * then interacting on a fixed cadence. Contexts rather than browsers keep the
 * host cost manageable at high N while still giving each session its own
 * cookie jar, so ShinyHub sees N distinct clients.
 *
 * The driver uses a real browser deliberately. A scripted WebSocket client
 * holds an idle socket and implements no heartbeat, so it cannot observe the
 * teardown this rig measures.
 */
import { chromium } from 'playwright';
import { writeFileSync, mkdirSync } from 'node:fs';
import { dirname } from 'node:path';
import { summarize } from './detect.mjs';

const URL_BASE = process.env.RIG_URL;
const SLUG = process.env.RIG_SLUG || 'rig';
const SESSIONS = Number(process.env.RIG_SESSIONS || '5');
const CADENCE_MS = Number(process.env.RIG_CADENCE_MS || '2000');
const DURATION_S = Number(process.env.RIG_DURATION_S || '120');
const AUTH_COOKIE = process.env.RIG_AUTH_COOKIE || '';
// loadtest/render/driver -> ../../results resolves to loadtest/results, the
// gitignored evidence directory shared by the whole loadtest suite. The
// timestamp keeps two default-config runs from overwriting each other; ISO
// 8601 with colons and the decimal point stripped sorts lexicographically by
// run time and stays filesystem-safe on every OS this driver targets.
const RUN_TIMESTAMP = new Date().toISOString().replace(/[:.]/g, '-');
const OUT =
  process.env.RIG_OUT ||
  `../../results/render-${SESSIONS}s-${CADENCE_MS}ms-${RUN_TIMESTAMP}.json`;
// The Playwright CDN that serves the pinned chromium build is not reliably
// reachable from every network, and even where it is, real Chrome is a
// higher-fidelity target since it is what users actually run. Set
// RIG_BROWSER_CHANNEL (e.g. "chrome") to drive an already-installed system
// browser instead of downloading the bundled chromium.
const BROWSER_CHANNEL = process.env.RIG_BROWSER_CHANNEL || '';

if (!URL_BASE) {
  console.error('RIG_URL is required, e.g. RIG_URL=$(./rig.sh url)');
  process.exit(2);
}

const appUrl = `${URL_BASE}/app/${SLUG}/`;

// Shiny's live-session websocket path always contains this fragment. A page
// can open other sockets (browser tooling, third-party scripts) whose close
// has nothing to do with the app's connection; without this filter, one of
// those closing would be recorded as a fabricated session disconnect.
const SHINY_WEBSOCKET_PATH = '/websocket/';

/** Drive one session for the run duration, returning its record. */
async function runSession(browser, index, deadline) {
  const record = {
    index,
    established: false,
    disconnected: false,
    disconnectAtMs: null,
    firstRenderMs: null,
    actionsAttempted: 0,
    actionsSucceeded: 0,
    error: null,
  };

  const context = await browser.newContext();
  if (AUTH_COOKIE) {
    const [name, ...rest] = AUTH_COOKIE.split('=');
    const u = new global.URL(URL_BASE);
    await context.addCookies([
      { name, value: rest.join('='), domain: u.hostname, path: '/' },
    ]);
  }
  const page = await context.newPage();

  // Two independent disconnect signals. The overlay is what a user sees; the
  // socket close is what actually happened. Either one counts. Only Shiny's
  // own websocket is a valid disconnect signal; see SHINY_WEBSOCKET_PATH.
  page.on('websocket', (ws) => {
    if (!ws.url().includes(SHINY_WEBSOCKET_PATH)) return;
    ws.on('close', () => {
      if (record.established && !record.disconnected) {
        record.disconnected = true;
        record.disconnectAtMs = Date.now() - startedAt;
      }
    });
  });

  const startedAt = Date.now();
  try {
    await page.goto(appUrl, { waitUntil: 'domcontentloaded', timeout: 120000 });
    await page.waitForFunction(
      () => document.body && document.body.innerText.includes('RIG_READY'),
      { timeout: 120000 },
    );
    record.established = true;
    record.firstRenderMs = Date.now() - startedAt;

    while (Date.now() < deadline && !record.disconnected) {
      record.actionsAttempted++;
      try {
        const target = record.actionsAttempted % 2 === 0 ? 'Tab B' : 'Tab A';
        await page.click('#apply', { timeout: CADENCE_MS * 3 });
        await page.click(`text=${target}`, { timeout: CADENCE_MS * 3 });
        if (await page.locator('#shiny-disconnected-overlay').count() > 0) {
          record.disconnected = true;
          record.disconnectAtMs = Date.now() - startedAt;
          break;
        }
        record.actionsSucceeded++;
      } catch {
        // A failed action is a data point, not a driver error. Keep going so
        // the run reports how many actions were lost rather than aborting.
      }
      await page.waitForTimeout(CADENCE_MS);
    }
  } catch (err) {
    record.error = String(err && err.message ? err.message : err);
  } finally {
    await context.close().catch(() => {});
  }
  return record;
}

const browser = await chromium.launch(
  BROWSER_CHANNEL ? { headless: true, channel: BROWSER_CHANNEL } : { headless: true },
);
const deadline = Date.now() + DURATION_S * 1000;
const sessions = await Promise.all(
  Array.from({ length: SESSIONS }, (_, i) => runSession(browser, i, deadline)),
);
await browser.close();

const established = sessions.filter((s) => s.established);
const result = {
  config: {
    appUrl,
    sessions: SESSIONS,
    cadenceMs: CADENCE_MS,
    durationS: DURATION_S,
    browserChannel: BROWSER_CHANNEL || 'bundled',
  },
  establishedCount: established.length,
  summary: summarize(established),
  sessions,
};

mkdirSync(dirname(OUT), { recursive: true });
writeFileSync(OUT, JSON.stringify(result, null, 2));
console.log(JSON.stringify(result.summary, null, 2));
console.log(`established ${established.length}/${SESSIONS}, wrote ${OUT}`);

// No established session means the run proves nothing. Fail loudly rather
// than emitting a clean-looking summary over an empty fleet.
if (established.length === 0) {
  console.error('no session established; the run is void');
  process.exit(1);
}
