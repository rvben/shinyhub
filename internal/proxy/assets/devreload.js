/* Local development only: refresh after a readiness-checked activation. */
(() => {
  'use strict';
  const script = document.currentScript;
  if (!script) return;
  const url = script.dataset.url;
  const revision = script.dataset.revision;
  if (!url || !revision) return;
  let stopped = false;
  let lifecycle = 0;
  let timer;
  async function poll() {
    const started = lifecycle;
    try {
      const response = await fetch(url, {
        cache: 'no-store',
        credentials: 'same-origin',
        signal: AbortSignal.timeout(3000),
      });
      if (response.ok) {
        const current = await response.text();
        if (!stopped && started === lifecycle && current && current !== revision) {
          stopped = true;
          window.location.reload();
          return;
        }
      }
    } catch (_) {
      // Startup, shutdown and temporary network failures must never refresh.
    }
    if (!stopped && started === lifecycle) timer = setTimeout(poll, 750);
  }
  window.addEventListener('pagehide', () => {
    stopped = true;
    lifecycle++;
    clearTimeout(timer);
  });
  // A document restored from the back/forward cache still has its old revision.
  window.addEventListener('pageshow', event => {
    if (event.persisted) {
      stopped = false;
      poll();
    }
  });
  timer = setTimeout(poll, 750);
})();
