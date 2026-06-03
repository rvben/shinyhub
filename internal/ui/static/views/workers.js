// workers.js - the admin Workers page (read-only fleet view).
//
// mountWorkers is the thin view shim (mirrors audit-log.js): it reveals the
// pre-rendered section, triggers the data load through ctx, and updates the
// active nav. The fetch + DOM rendering live in app.js (which holds the api
// helper and auth handling); the pure per-row display mapping lives here so it
// is unit-testable under jsdom without importing app.js.

/**
 * workerDisplay maps one /api/workers row to its presentation fields. Pure (no
 * DOM, no time formatting) so it is exhaustively unit-testable. A revoked worker
 * always reads "revoked" regardless of its raw up/down status, since revocation
 * is a deliberate, sticky admin action distinct from a transient heartbeat loss.
 *
 * statusClass reuses the shared badge-<class> CSS (running = healthy/up,
 * lost = down/revoked, stopped = unknown).
 *
 * @param {{node_id?:string, name?:string, tier?:string, status?:string, version?:string, revoked?:boolean}} w
 * @returns {{node:string, tier:string, statusText:string, statusClass:string, version:string}}
 */
export function workerDisplay(w) {
  const worker = w && typeof w === 'object' ? w : {};
  const revoked = !!worker.revoked;
  const raw = worker.status || 'unknown';
  let statusText, statusClass;
  if (revoked) {
    statusText = 'revoked';
    statusClass = 'lost';
  } else if (raw === 'up') {
    statusText = 'up';
    statusClass = 'running';
  } else if (raw === 'down') {
    statusText = 'down';
    statusClass = 'lost';
  } else if (raw === 'joining') {
    // Transitional: registered, awaiting its first heartbeat. Show it neutrally
    // (not the red "lost" treatment) so a worker mid-join does not read as an error.
    statusText = 'joining';
    statusClass = 'stopped';
  } else {
    statusText = raw;
    statusClass = 'stopped';
  }
  const nodeID = worker.node_id || 'unknown';
  const node = worker.name ? `${worker.name} (${nodeID})` : nodeID;
  return {
    node,
    tier: worker.tier || '-',
    statusText,
    statusClass,
    version: worker.version || '-',
  };
}

export function mountWorkers(ctx) {
  const view = document.getElementById('workers-view');
  view.hidden = false;
  ctx.loadWorkers();
  ctx.updateActiveNav(location.pathname);
  return {
    title: 'Workers',
    unmount() {
      view.hidden = true;
    },
  };
}
