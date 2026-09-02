import orbitHubLockupDarkSource from "../../../internal/ui/static/brand/orbit-hub-lockup-dark.svg";

export const DEMO_READY_PATH = "/__demo/ready";

const orbitHubLockupDark = orbitHubLockupDarkSource.replace(/^<\?xml[^>]+>\s*/, "");

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

const wakeScript = String.raw`
(() => {
  const status = document.querySelector('#wake-status');
  const detail = document.querySelector('#wake-detail');
  const retry = document.querySelector('#wake-retry');
  let failures = 0;
  let settled = false;

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
        window.location.replace('/');
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

  retry.addEventListener('click', () => {
    failures = 0;
    retry.hidden = true;
    status.textContent = 'Starting the live demo';
    detail.textContent = 'This page is already running at the edge. The demo compute is waking in the background.';
    check();
  });

  check();
})();
`;

export function demoWakeResponse(): Response {
  const nonce = crypto.randomUUID().replaceAll("-", "");
  const html = String.raw`<!doctype html>
<html lang="en">
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
