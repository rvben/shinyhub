import orbitHubLockupDarkSource from "../../../internal/ui/static/brand/orbit-hub-lockup-dark.svg";
import { DEMO_START_PATH, demoURL, ENTRY_URL } from "./edge-policy.ts";

export const DEMO_READY_PATH = "/__demo/ready";

const orbitHubLockupDark = orbitHubLockupDarkSource.replace(/^<\?xml[^>]+>\s*/, "");

// Both pages interpolate a destination a stranger can write into an attribute.
// The value is already known to be a same-origin path, and the URL parser
// percent-encodes the angle brackets, but neither fact is visible where the
// interpolation happens, so the escape is done where it can be read.
function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

const wakeStyles = String.raw`
:root {
  color-scheme: dark;
  --canvas: #030510;
  --surface: #0e1426;
  --line: #1e2a4a;
  --text: #e8eeff;
  --text-soft: #a8b4d4;
  --text-muted: #6b7aa3;
  --signal: #38bdf8;
  --sparkle: #bae6fd;
  --success: #4ade80;
  font-family: Manrope, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}

* { box-sizing: border-box; }

html, body { min-height: 100%; }

body {
  min-height: 100vh;
  margin: 0;
  display: grid;
  place-items: center;
  overflow: hidden;
  background:
    radial-gradient(50rem 32rem at 82% -8%, rgba(56, 189, 248, 0.14), transparent 62%),
    radial-gradient(42rem 32rem at 8% 108%, rgba(96, 165, 250, 0.10), transparent 64%),
    var(--canvas);
  color: var(--text);
  -webkit-font-smoothing: antialiased;
}

main {
  width: min(38rem, calc(100vw - 3rem));
  padding: 4rem 0;
}

.brand {
  display: block;
  width: min(11.5rem, 58vw);
}

.brand svg { display: block; width: 100%; height: auto; }

h1 {
  max-width: 12ch;
  margin: 3rem 0 0.9rem;
  font-size: 2.6rem;
  font-weight: 200;
  line-height: 0.98;
  letter-spacing: -0.04em;
  text-wrap: balance;
}

.intro {
  max-width: 42ch;
  margin: 0;
  color: var(--text-soft);
  font-size: 0.875rem;
  line-height: 1.65;
  text-wrap: pretty;
}

.progress {
  position: relative;
  width: min(24rem, 100%);
  height: 2px;
  margin: 2.75rem 0 1.15rem;
  overflow: hidden;
  background: var(--line);
}

.progress::after {
  content: "";
  position: absolute;
  inset: 0;
  width: 38%;
  background: linear-gradient(90deg, transparent, var(--signal), var(--sparkle));
  animation: wake-progress 1.6s cubic-bezier(.22, 1, .36, 1) infinite;
}

[data-ready="true"] .progress::after {
  width: 100%;
  background: var(--success);
  animation: none;
}

.status {
  min-height: 3.4rem;
}

.status strong {
  display: block;
  font-size: 0.75rem;
  font-weight: 600;
  letter-spacing: 0.01em;
}

.status span {
  display: block;
  max-width: 54ch;
  margin-top: 0.3rem;
  color: var(--text-muted);
  font-size: 0.75rem;
  line-height: 1.5;
}

button {
  min-height: 44px;
  margin-top: 1.25rem;
  padding: 0.65rem 1rem;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--surface);
  color: var(--text);
  cursor: pointer;
  font: 600 0.75rem/1.2 inherit;
}

button:hover { border-color: var(--signal); }
button:focus-visible { outline: 3px solid rgba(56, 189, 248, 0.34); outline-offset: 3px; }
button[hidden] { display: none; }

::selection { background: var(--signal); color: var(--canvas); }

@keyframes wake-progress {
  from { transform: translateX(-105%); }
  to { transform: translateX(265%); }
}

@media (max-width: 520px) {
  main { width: min(100% - 2rem, 38rem); padding: 2rem 0; }
  h1 { margin-top: 2.25rem; }
}

@media (prefers-reduced-motion: reduce) {
  .progress::after { width: 60%; animation: none; }
}
`;

const startStyles = String.raw`
body { overflow: auto; }

.start-form { margin: 2.5rem 0 0; }

.start-form button {
  margin-top: 0;
  padding: 0.85rem 1.4rem;
  border-color: var(--signal);
  background: var(--signal);
  color: var(--canvas);
  font-size: 0.8125rem;
}

.start-form button:hover { border-color: var(--sparkle); background: var(--sparkle); }

.notes {
  display: flex;
  flex-wrap: wrap;
  gap: 0.4rem 1.25rem;
  margin: 1.6rem 0 0;
  padding: 0;
  list-style: none;
  color: var(--text-muted);
  font-size: 0.75rem;
  line-height: 1.5;
}
`;

const wakeScript = String.raw`
(() => {
  const status = document.querySelector('#wake-status');
  const detail = document.querySelector('#wake-detail');
  const retry = document.querySelector('#wake-retry');
  let failures = 0;
  let down = 0;
  let settled = false;

  // Reopening the demo is a real navigation, because a navigation is the only
  // request that may start a container. The probe below deliberately starts
  // nothing, so a container that crashed before it ever served anything cannot
  // be polled back to life however long the page waits. This is the same move
  // the page makes when it has no script at all.
  const reopen = () => {
    settled = true;
    window.location.replace(document.documentElement.dataset.landing || '/');
  };

  const check = async () => {
    if (settled) return;

    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 25_000);
    try {
      const response = await fetch('${DEMO_READY_PATH}', {
        cache: 'no-store',
        credentials: 'same-origin',
        headers: { accept: 'application/json' },
        signal: controller.signal,
      });
      if (response.ok) {
        settled = true;
        document.documentElement.dataset.ready = 'true';
        status.textContent = 'Demo ready';
        detail.textContent = 'Opening ShinyHub…';
        // Built on the control host by the Worker, so the page a visitor asked
        // for survives the wake without this script having to trust the URL it
        // was loaded from.
        window.location.replace(document.documentElement.dataset.landing || '/');
        return;
      }
      // The demo reports itself down rather than merely not ready yet. A start
      // takes a moment to show, so the container is given a few probes to say
      // otherwise before the visitor is handed back to the gate that can start
      // it again.
      down = response.headers.get('x-shinyhub-demo-state') === 'asleep' ? down + 1 : 0;
      if (down >= 3) {
        reopen();
        return;
      }
    } catch (_) {
      // A cold container can outlive one probe. The next probe joins the same wake.
    } finally {
      clearTimeout(timeout);
    }

    failures += 1;
    if (failures >= 3) {
      status.textContent = 'Still starting';
      detail.textContent = 'The first start is taking longer than usual. You can retry without losing your place.';
      retry.hidden = false;
    }
    setTimeout(check, 1_500);
  };

  retry.addEventListener('click', reopen);

  check();
})();
`;

const START_TITLE = "ShinyHub Live Demo";
const START_DESCRIPTION = "A real ShinyHub control plane running Python, R, Dash and Streamlit apps. "
  + "The demo environment sleeps when nobody is using it; press start and it opens in about a minute.";

// The page everything that is not a browser navigating gets when the container
// is asleep: a link unfurler, an uptime monitor, a crawler, a fetch from a
// script. It is rendered here at the edge, so answering with it costs nothing,
// and it carries the Open Graph tags that make a shared demo link preview as
// what it is instead of as an error. The button is the way through: bots read
// pages, they do not submit forms, so a post from here is the one request the
// gate can believe without a header.
export function demoStartResponse(destination: string | null, method: string): Response {
  const nonce = crypto.randomUUID().replaceAll("-", "");
  const action = escapeHtml(demoURL(DEMO_START_PATH, destination));
  const html = String.raw`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="theme-color" content="#030510">
  <meta name="description" content="${escapeHtml(START_DESCRIPTION)}">
  <meta property="og:type" content="website">
  <meta property="og:site_name" content="ShinyHub">
  <meta property="og:url" content="${escapeHtml(ENTRY_URL)}">
  <meta property="og:title" content="${escapeHtml(START_TITLE)}">
  <meta property="og:description" content="${escapeHtml(START_DESCRIPTION)}">
  <meta name="twitter:card" content="summary">
  <meta name="twitter:title" content="${escapeHtml(START_TITLE)}">
  <meta name="twitter:description" content="${escapeHtml(START_DESCRIPTION)}">
  <title>${escapeHtml(START_TITLE)}</title>
  <style nonce="${nonce}">${wakeStyles}${startStyles}</style>
</head>
<body>
  <main>
    <div class="brand">${orbitHubLockupDark}</div>
    <h1>The live demo is resting</h1>
    <p class="intro">ShinyHub runs this demo on compute that stops when nobody is using it, so an idle demo costs nothing to keep online. Starting it takes about a minute, and it will bring you straight in.</p>
    <form class="start-form" method="post" action="${action}">
      <button type="submit">Start the live demo</button>
    </form>
    <ul class="notes">
      <li>No account created</li>
      <li>Read-only access</li>
      <li>Resets automatically</li>
    </ul>
  </main>
</body>
</html>`;

  return new Response(method === "HEAD" ? null : html, {
    status: 200,
    headers: {
      "cache-control": "no-store",
      // No script at all, so nothing here needs to run. The form is the whole
      // page, which is why form-action is the one directive opened up.
      "content-security-policy": `default-src 'none'; style-src 'nonce-${nonce}'; script-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'`,
      "content-type": "text/html; charset=utf-8",
      "cross-origin-opener-policy": "same-origin",
      "permissions-policy": "camera=(), microphone=(), geolocation=()",
      "referrer-policy": "strict-origin-when-cross-origin",
      "strict-transport-security": "max-age=31536000; includeSubDomains",
      "x-content-type-options": "nosniff",
      // robots.txt already disallows the whole demo. This repeats it for the
      // crawlers that read the tag and ignore the file.
      "x-robots-tag": "noindex",
      "x-shinyhub-demo-state": "asleep",
    },
  });
}

// The page a visitor waits on while the container boots. destination is the path
// they originally asked for, which the wake outlives: it is folded into the URL
// this page navigates to once the demo answers, so the demo login can send them
// on to it rather than dropping them on the dashboard.
export function demoWakeResponse(destination: string | null): Response {
  const nonce = crypto.randomUUID().replaceAll("-", "");
  const landing = escapeHtml(demoURL("/", destination));
  const html = String.raw`<!doctype html>
<html lang="en" data-landing="${landing}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="theme-color" content="#030510">
  <noscript><meta http-equiv="refresh" content="4"></noscript>
  <title>Starting ShinyHub Demo</title>
  <style nonce="${nonce}">${wakeStyles}</style>
</head>
<body>
  <main>
    <div class="brand">${orbitHubLockupDark}</div>
    <h1>Waking the live demo</h1>
    <p class="intro">The front door is ready. ShinyHub is starting the demo environment now, then it will bring you straight in.</p>
    <div class="progress" aria-hidden="true"></div>
    <div class="status" role="status" aria-live="polite">
      <strong id="wake-status">Starting the live demo</strong>
      <span id="wake-detail">This page is already running at the edge. The demo compute is waking in the background.</span>
    </div>
    <button id="wake-retry" type="button" hidden>Try again</button>
  </main>
  <script nonce="${nonce}">${wakeScript}</script>
</body>
</html>`;

  return new Response(html, {
    status: 200,
    headers: {
      "cache-control": "no-store",
      "content-security-policy": `default-src 'none'; connect-src 'self'; style-src 'nonce-${nonce}'; script-src 'nonce-${nonce}'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'`,
      "content-type": "text/html; charset=utf-8",
      "cross-origin-opener-policy": "same-origin",
      "permissions-policy": "camera=(), microphone=(), geolocation=()",
      "referrer-policy": "strict-origin-when-cross-origin",
      "strict-transport-security": "max-age=31536000; includeSubDomains",
      "x-content-type-options": "nosniff",
      "x-robots-tag": "noindex",
      "x-shinyhub-demo-state": "waking",
    },
  });
}
