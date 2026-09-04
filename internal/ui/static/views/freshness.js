// Freshness bookkeeping for a cached server payload.
//
// There are two independent reasons to stop trusting a cached copy, and they
// call for different behaviour:
//
//   stale: something was changed (a successful mutating request). The copy is
//          wrong, not merely old, so whatever renders next has to wait for a
//          new one.
//   old:   only time has passed. The copy is still the best answer available,
//          so render it and let a refresh follow behind.
//
// Both are decided against the moment the request that produced the copy was
// SENT, not the moment its answer arrived. That is the whole point of counting
// mutations rather than raising a flag: a change that lands while a request is
// in flight cannot be included in the answer that request brings back, and must
// not be marked as covered by it.
export function createFreshness({ maxAgeMs, now = () => Date.now() } = {}) {
  let mutations = 0;
  // The stamp of the copy currently held, or null when nothing is held.
  let held = null;

  return {
    // Record that something was changed on the server.
    mutated() {
      mutations++;
    },
    // Take a stamp before sending a request. Pass it to adopt() if that
    // request's answer becomes the held copy.
    begin() {
      return { mutations, at: now() };
    },
    adopt(stamp) {
      held = stamp;
    },
    // Nothing is held: there is no copy to reason about (before the first fetch,
    // and after the view is torn down).
    forget() {
      held = null;
    },
    isStale() {
      return held !== null && held.mutations !== mutations;
    },
    isOld() {
      return held !== null && now() - held.at >= maxAgeMs;
    },
    // Held, unchanged and recent: usable with no request at all.
    isFresh() {
      return held !== null && !this.isStale() && !this.isOld();
    },
  };
}
