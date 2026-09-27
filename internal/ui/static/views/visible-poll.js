// A background poll that keeps firing on a fixed interval while its tab is in
// the background wastes a network round trip every tick: nobody is looking at
// the result, and the next poll overwrites it before a visitor could ever see
// it. It also keeps a hibernating app's hibernation timer from ever reaching
// zero, since a "someone is watching" poll looks identical to real traffic no
// matter which tab it came from.
//
// startVisiblePoll arms an interval only while the document is visible, drops
// it the instant the tab is hidden, and - because whatever the visitor left on
// screen can be arbitrarily stale by the time they return - runs one immediate
// tick the moment the document becomes visible again rather than waiting out a
// full interval. It never ticks on its own on construction or on stop; the
// caller keeps doing its own initial load exactly as before.
//
// doc may be omitted (or given as a stub with no addEventListener/hidden,
// which is what a plain Node test environment with no DOM looks like): with
// no way to observe visibility the poll simply never pauses, which is the
// same behavior a bare setInterval already had.
export function startVisiblePoll(doc, intervalMs, tick) {
  let timer = null;
  let stopped = false;

  function hidden() {
    return !!(doc && doc.hidden);
  }

  function arm() {
    if (stopped || timer !== null || hidden()) return;
    timer = setInterval(tick, intervalMs);
  }

  function disarm() {
    if (timer !== null) {
      clearInterval(timer);
      timer = null;
    }
  }

  function onVisibilityChange() {
    if (stopped) return;
    if (hidden()) {
      disarm();
    } else {
      tick();
      arm();
    }
  }

  const canObserve = !!(doc && typeof doc.addEventListener === 'function');
  if (canObserve) doc.addEventListener('visibilitychange', onVisibilityChange);
  arm();

  return function stop() {
    if (stopped) return;
    stopped = true;
    disarm();
    if (canObserve) doc.removeEventListener('visibilitychange', onVisibilityChange);
  };
}
